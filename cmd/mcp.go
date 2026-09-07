package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/mcpfilter"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// mcpCaller is the slice of *mcp.Client the adapter needs — an interface
// so tests can stub the wire protocol away.
type mcpCaller interface {
	Name() string
	CallTool(ctx context.Context, tool string, args map[string]any) ([]mcp.Content, bool, error)
}

const (
	// Gemini function names allow [a-zA-Z0-9_.-], max 64 chars.
	maxToolNameLen = 64
	maxToolDescLen = 2000
)

// mcpToolName builds the registry name for an MCP tool, Claude Code
// style: mcp__<server>__<tool>, sanitized to Gemini's charset. An
// over-long name is truncated with a deterministic hash suffix: a bare
// cut collided two long remote names into one registry entry, silently
// dropping the second tool (ADR-0021). The hash is stable across runs,
// so an exact [approval.tools] entry can still target it.
func mcpToolName(server, tool string) string {
	name := "mcp__" + sanitizeToolName(server) + "__" + sanitizeToolName(tool)
	if len(name) > maxToolNameLen {
		sum := sha256.Sum256([]byte(name))
		name = name[:maxToolNameLen-8] + "-" + hex.EncodeToString(sum[:4])[:7]
	}
	return name
}

func sanitizeToolName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// registerMCPTools adapts one server's tools into the registry. All MCP
// tools require approval (RFP: external-server tools are gated) — this
// tool cannot know which remote operations mutate what.
func registerMCPTools(registry *tools.Registry, client mcpCaller, list []mcp.Tool) (added []string, errs []string) {
	// The intake spills to the registry's work directory, so a result
	// it saves is one the file tools can read back (ADR-0058).
	intake := newMCPIntake(registry.WorkDir)
	for _, t := range list {
		remoteName := t.Name
		desc := "[MCP:" + client.Name() + "] " + t.Description
		if len(desc) > maxToolDescLen {
			desc = desc[:maxToolDescLen]
		}
		params := t.InputSchema
		if params == nil {
			params = map[string]any{"type": "object"}
		}
		tool := &tools.Tool{
			Name:        mcpToolName(client.Name(), remoteName),
			Description: desc,
			Parameters:  params,
			Mutating:    true,
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				blocks, isErr, err := client.CallTool(ctx, remoteName, args)
				// Provenance is typed, never inferred from text (ADR-0075
				// §1): the server's rejection travels as *mcp.RPCError
				// inside the call error; anything else is a failure of
				// gem-agent's own to complete the call.
				if err != nil {
					re := &tools.RemoteError{Server: client.Name(), Tool: remoteName, Kind: tools.RemoteIncomplete, Text: err.Error()}
					var ce *mcp.CallError
					if errors.As(err, &ce) && ce.Err != nil {
						re.Text, re.Sent = ce.Err.Error(), ce.Sent
					}
					// A rejection is the server refusing THIS call. An
					// RPCError from a call that was never sent is the
					// server refusing to start (initialize) — the call
					// is incomplete, and the cause says why.
					var rpc *mcp.RPCError
					if re.Sent && errors.As(err, &rpc) {
						re.Kind, re.Text = tools.RemoteRejected, rpc.Error()
					}
					return "", re
				}
				text := intake.render(client.Name(), remoteName, blocks)
				if isErr {
					return "", &tools.RemoteError{Server: client.Name(), Tool: remoteName, Kind: tools.RemoteResult, Text: text, Sent: true}
				}
				return text, nil
			},
		}
		if err := registry.Register(tool); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		added = append(added, tool.Name)
	}
	return added, errs
}

// policyScope is the machine-owned file's word, in the shape the filter
// composes (ADR-0077 §2). Decided travels beside the entries because a
// server the panel decided to exclude NOTHING of still shadows
// config.toml — see PolicyMCP.Decided.
func policyScope(pf *config.PolicyFile) mcpfilter.PolicyScope {
	return mcpfilter.PolicyScope{Entries: pf.MCP.Exclude, Decided: pf.MCP.Decided}
}

// mcpToolPrefix is the registry-name prefix of one server's tools.
//
// Two edges, both bounded: a server named "foo__bar" (or anything that
// sanitises to it) shares the prefix of a server named "foo", and a
// server name near the 64-character cap is truncated past its own
// prefix. Neither can hide a live tool — Excluded is consulted only
// after the registry fails to resolve the name — so the cost is a
// transcript record attributed to the wrong server, or missing for a
// very long name (pre-release re-review).
func mcpToolPrefix(server string) string {
	return "mcp__" + sanitizeToolName(server) + "__"
}

// mcpInventory is what the session knows about its MCP servers after a
// connect: every configured server (whether or not it was started, and
// whether or not it was excluded), and the function names of the ones
// that answered tools/list. The settings panel needs both to draw its
// two levels — a server excluded whole has no functions to list, and its
// row still has to be there to turn back on (ADR-0077 §1).
//
// It also keeps what a later reconnect of ONE server needs from the
// files the full connect read: how to start it, which names the
// operator's files mention, and whether every list was read. A panel
// toggle changes one server, and reconnecting all of them for it —
// twenty-five processes killed and spawned on this machine, inside the
// TUI's event loop — was what made an arrow key take seconds and queue
// the keys typed meanwhile (post-release field report).
type mcpInventory struct {
	Servers []string            // sorted; every configured server
	Offered map[string][]string // server -> function names, for those that listed
	Scopes  map[string]string   // server -> "global" | "project"

	configs    map[string]mcp.ServerConfig // how each server is started
	configured map[string]bool             // every name the files mention, skipped ones included
	complete   bool                        // every list this session should have was read
	summary    map[string]string           // server -> its /mcp line, for those with one
}

// summaryLines is the /mcp listing, in server order: one line per
// server that was started or deliberately not, none for one that failed.
func (inv mcpInventory) summaryLines() []string {
	var out []string
	for _, name := range inv.Servers {
		if line, ok := inv.summary[name]; ok {
			out = append(out, line)
		}
	}
	return out
}

// warnUnmatched names every exclude entry that did no work, with its own
// remedy (ADR-0077 §2). Checked against the whole inventory because a
// stale entry is stale whichever server was just touched.
func (inv mcpInventory) warnUnmatched(filter mcpfilter.Filter, stderr io.Writer) {
	for _, note := range filter.Unmatched(inv.configured, inv.Offered, inv.complete) {
		// The fact and the next command on one line. The command
		// differs by cause — a misspelled name is not a line another
		// file has overridden — so each note carries its own.
		fmt.Fprintf(stderr, "warning: [mcp] exclude: %s\n", note)
	}
}

// mcpServer is what the connect path needs of a server process: the
// real *mcp.Client in the runtime, a stub in tests.
type mcpServer interface {
	mcpCaller
	ListTools(ctx context.Context) ([]mcp.Tool, error)
	Close()
}

// excludeMCPServer records a server the session does not start (ADR-0077
// §1): no process, no credentials touched, nothing of it in the
// declarations. Its row still comes from .mcp.json, so it can be turned
// back on without having been running.
func excludeMCPServer(name string, registry *tools.Registry, inv *mcpInventory) {
	// Recorded by prefix: the server never listed, so there are no
	// function names to note one by one, and a call naming one must
	// still read as the operator's doing in the transcript rather than
	// as a tool that never existed (ADR-0077 §5, pre-release review).
	registry.NoteExcludedPrefix(mcpToolPrefix(name))
	delete(inv.Offered, name)
	inv.summary[name] = fmt.Sprintf("%s [%s] (not started — excluded)", name, inv.Scopes[name])
}

// attachMCPServer lists one server's tools and registers what the filter
// keeps. It reports whether the client is worth holding on to: false
// means it was closed here (unavailable, or nothing usable) and the
// caller drops it. The server's slots in the inventory are overwritten,
// so the same call serves the first connect and a later reconnect of
// that one server.
func attachMCPServer(ctx context.Context, client mcpServer, registry *tools.Registry, stderr io.Writer, filter mcpfilter.Filter, inv *mcpInventory) bool {
	name := client.Name()
	scope := inv.Scopes[name]
	delete(inv.summary, name)
	delete(inv.Offered, name)
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	toolList, err := client.ListTools(lctx)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "warning: MCP server %s unavailable: %v\n", name, err)
		client.Close()
		return false
	}
	offered, kept, excluded := splitByFilter(name, toolList, filter)
	for _, n := range excluded {
		// Not registered. The name is kept so the transcript can say
		// the operator removed it (ADR-0077 §5).
		registry.NoteExcluded(n)
	}
	removed := len(excluded)
	inv.Offered[name] = offered

	added, errs := registerMCPTools(registry, client, kept)
	for _, e := range errs {
		fmt.Fprintf(stderr, "warning: MCP server %s: %s\n", name, e)
	}
	switch {
	case len(added) == 0 && removed > 0:
		// Every function excluded is a choice, not a fault: the server
		// keeps running for nothing, which is what §1 says an empty
		// function list means.
		inv.summary[name] = fmt.Sprintf("%s [%s] (0 of %d tools — all excluded)", name, scope, len(toolList))
	case len(added) == 0:
		fmt.Fprintf(stderr, "warning: MCP server %s advertises no usable tools\n", name)
		client.Close()
		return false
	case removed > 0:
		inv.summary[name] = fmt.Sprintf("%s [%s] (%d of %d tools)", name, scope, len(added), len(toolList))
	default:
		inv.summary[name] = fmt.Sprintf("%s [%s] (%d tools)", name, scope, len(added))
	}
	return true
}

// reconnectMCPServer applies the filter to ONE server and leaves every
// other server's process and tools alone (ADR-0077 §3: the panel's edit
// names one server, and that is the whole of what changes). running is
// the server's client if the session has one, nil otherwise; start
// makes a fresh one. The server's registry names are removed first, so
// a running server is re-listed — one tools/list round trip, no
// respawn — and re-registered under the new filter. What comes back is
// the client to keep, or nil when the server is now excluded, gone, or
// useless.
func reconnectMCPServer(ctx context.Context, name string, running mcpServer, start func() mcpServer, registry *tools.Registry, stderr io.Writer, filter mcpfilter.Filter, inv *mcpInventory) mcpServer {
	registry.RemoveByPrefix(mcpToolPrefix(name))
	if filter.Server(name) {
		if running != nil {
			running.Close()
		}
		excludeMCPServer(name, registry, inv)
		inv.warnUnmatched(filter, stderr)
		return nil
	}
	client := running
	if client == nil {
		client = start()
	}
	kept := attachMCPServer(ctx, client, registry, stderr, filter, inv)
	inv.warnUnmatched(filter, stderr)
	if !kept {
		return nil
	}
	return client
}

// splitByFilter divides one server's advertised tools into what the
// session declares and what it does not (ADR-0077 §1). It answers in
// three parts: every function name the server offered (what a stale
// exclusion is checked against), the tools to register, and the REGISTRY
// names of the excluded ones — the filter matches the name the server
// spells, while the transcript and the executor see gem-agent's
// sanitized `mcp__server__tool`, and confusing the two would file the
// exclusion under a name no call ever carries.
func splitByFilter(server string, list []mcp.Tool, filter mcpfilter.Filter) (offered []string, kept []mcp.Tool, excluded []string) {
	offered = make([]string, 0, len(list))
	kept = make([]mcp.Tool, 0, len(list))
	for _, t := range list {
		offered = append(offered, t.Name)
		if filter.Func(server, t.Name) {
			excluded = append(excluded, mcpToolName(server, t.Name))
			continue
		}
		kept = append(kept, t)
	}
	return offered, kept, excluded
}

// connectMCPServers loads the global (~/.config/gem-agent/mcp.json) and
// project (.mcp.json) server lists — the project entry wins a name
// collision — and registers every reachable server's tools. Failures on
// either scope are warnings; a broken file or server degrades the
// session, it must not block the runtime from starting.
// scopes maps each connected server to "global" or "project" — kept
// for consumers that must not treat a project-supplied server like an
// operator-installed one (none today; /learn was, before ADR-0049).
func connectMCPServers(ctx context.Context, cfg *config.Config, projectDir, version string, registry *tools.Registry, stderr io.Writer, grant projectGrant, filter mcpfilter.Filter) (clients []*mcp.Client, summary []string, inv mcpInventory) {
	if !cfg.MCP.Enabled {
		return nil, nil, mcpInventory{Offered: map[string][]string{}, summary: map[string]string{}}
	}

	// complete says every server list this session should have was
	// actually read. A file that failed to parse, or a project file left
	// unread because the project is untrusted, means a name missing from
	// the merged map proves nothing about the operator's exclude line.
	complete := true
	var skippedNames []string
	load := func(path, scope string) map[string]mcp.ServerConfig {
		servers, skipped, err := mcp.LoadConfig(path)
		if err != nil {
			fmt.Fprintf(stderr, "warning: %s MCP config: %v — skipped\n", scope, err)
			complete = false
			return nil
		}
		for _, s := range skipped {
			fmt.Fprintf(stderr, "warning: %s MCP server skipped: %s\n", scope, s)
			// A server the loader skipped (a transport this client does
			// not speak, a missing command) exists in the operator's
			// file. Leaving it out of `configured` made an exclude entry
			// naming it read as a typo (pre-release re-review).
			skippedNames = append(skippedNames, strings.Fields(s)[0])
		}
		return servers
	}

	var global map[string]mcp.ServerConfig
	if gp := mcp.GlobalConfigPath(); gp != "" {
		global = load(gp, "global")
	}
	// A server entry is a child process, so an untrusted project's
	// .mcp.json is not read at all (ADR-0023 §2) — nor one whose content
	// changed since it was trusted (ADR-0074).
	var project map[string]mcp.ServerConfig
	if grant.mcp() {
		project = load(filepath.Join(projectDir, ".mcp.json"), "project")
	} else {
		complete = false
	}

	servers, scopes, overridden := mcp.Merge(global, project)
	inv.Scopes = scopes
	inv.configs = servers
	inv.summary = map[string]string{}
	for _, name := range overridden {
		fmt.Fprintf(stderr, "note: project .mcp.json overrides global MCP server %q\n", name)
	}

	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	inv.Servers = names

	// What the filter is checked against, so a stale entry can be told
	// from one that did its work (ADR-0077 §2): every configured server,
	// and the function names of the ones that actually listed.
	inv.configured = make(map[string]bool, len(names)+len(skippedNames))
	inv.Offered = map[string][]string{}
	inv.complete = complete
	for _, name := range names {
		inv.configured[name] = true
	}
	for _, name := range skippedNames {
		inv.configured[name] = true
	}
	defer func() { inv.warnUnmatched(filter, stderr) }()

	timeout := time.Duration(cfg.MCP.CallTimeoutSec) * time.Second
	for _, name := range names {
		if filter.Server(name) {
			excludeMCPServer(name, registry, &inv)
			continue
		}
		client := mcp.NewStdio(name, servers[name], timeout, version)
		if attachMCPServer(ctx, client, registry, stderr, filter, &inv) {
			clients = append(clients, client)
		}
	}
	return clients, inv.summaryLines(), inv
}
