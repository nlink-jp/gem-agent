package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// mcpAdvertiser decides which MCP tools the model is shown (ADR-0083).
// Every server's tools are registered; under [mcp].advertise =
// "on-request" only a loaded tool is advertised — loaded by the model
// through mcp_load (a whole server) or find_tools (single tools), or by
// the operator through [mcp].preload, a --allow grant naming the server,
// or /mcp load. A withheld tool (flagged by the librarian) is advertised
// to nobody whatever else says, and the same predicate refuses it at
// dispatch (ADR-0083 §1, §6).
//
// Loads staged by a tool are applied by the agent loop, not by the
// tool: a tool's Run is on its own goroutine and may be abandoned
// (ADR-0065), so a tool stages under its call id and the loop's
// AfterTool hook commits or discards the stage with the call's fate.
type mcpAdvertiser struct {
	mu        sync.Mutex
	onRequest bool
	preload   map[string]bool // server names, as configured or sanitised
	// loadedServers and loadedTools are what the session has loaded;
	// owner maps a registered tool name to its server and present is
	// every server that registered tools — both rebuilt from the
	// inventory after every connect or reconnect.
	loadedServers map[string]bool
	loadedTools   map[string]bool
	withheld      map[string]string // registered name -> the librarian's reason
	owner         map[string]string
	present       map[string]bool
	pending       map[string]*pendingLoad // call id -> staged effect
	// log receives the mcp_advertise records a resumed session replays
	// (ADR-0083 §8); nil logs nothing.
	log agent.SessionLog
}

// pendingLoad is one tool call's staged effect.
type pendingLoad struct {
	servers  []string
	tools    []string
	withheld map[string]string
}

// mcpAdvertiseRecord is the transcript record of a committed load: what
// a resumed session re-advertises. Withheld tools are deliberately not
// in it — flags are not persisted (ADR-0083 §6).
type mcpAdvertiseRecord struct {
	Servers []string `json:"servers,omitempty"`
	Tools   []string `json:"tools,omitempty"`
}

const (
	// MCPLoadName is the built-in that advertises one server's tools.
	MCPLoadName = "mcp_load"
	// mcpAdvertiseKind is the transcript record kind of a committed load.
	mcpAdvertiseKind = "mcp_advertise"
	// catalogSentenceCap bounds the one-line description mcp_load
	// returns per tool, in runes.
	catalogSentenceCap = 160
)

func newMCPAdvertiser(onRequest bool, preload []string, allow []string, log agent.SessionLog) *mcpAdvertiser {
	a := &mcpAdvertiser{onRequest: onRequest, preload: map[string]bool{}, log: log}
	for _, name := range preload {
		if name = strings.TrimSpace(name); name != "" {
			a.preload[name] = true
		}
	}
	for _, name := range serversFromAllow(allow) {
		a.preload[name] = true
	}
	a.reset()
	return a
}

// serversFromAllow reads the servers a --allow grant names: an entry of
// the form mcp__<server>__* is the operator's declaration that this run
// needs that server, so it is advertised from the start — a pipeline
// must not depend on the model remembering to load (ADR-0083 §7). The
// name is the sanitised one the prefix carries; setInventory matches
// it against sanitised server names.
func serversFromAllow(patterns []string) []string {
	var out []string
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		rest, ok := strings.CutPrefix(p, "mcp__")
		if !ok {
			continue
		}
		server, ok := strings.CutSuffix(rest, "__*")
		if !ok || server == "" || strings.Contains(server, "__") {
			continue
		}
		out = append(out, server)
	}
	return out
}

// reset returns to the starting state: preloaded servers only, nothing
// withheld, nothing staged.
func (a *mcpAdvertiser) reset() {
	a.loadedServers = map[string]bool{}
	a.loadedTools = map[string]bool{}
	a.withheld = map[string]string{}
	a.pending = map[string]*pendingLoad{}
	for name := range a.preload {
		a.loadedServers[name] = true
	}
	a.resolvePreload()
}

// resolvePreload marks present servers named by a preload under either
// spelling (the configured name, or the sanitised name a --allow pattern
// carries).
func (a *mcpAdvertiser) resolvePreload() {
	for want := range a.preload {
		for server := range a.present {
			if want == server || want == sanitizeToolName(server) {
				a.loadedServers[server] = true
			}
		}
	}
}

// Reset is /clear's: a cleared session starts unloaded, preloads aside,
// and its flags start over — they belong to the session that made them.
func (a *mcpAdvertiser) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reset()
}

// setInventory rebuilds the tool→server map from what the servers
// registered. Loads survive by name across a reconnect (ADR-0083 §8):
// a server or tool that is still there stays loaded; the flags are
// cleared, because the descriptions may have changed and the next
// librarian call judges them again.
func (a *mcpAdvertiser) setInventory(inv mcpInventory) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.owner = map[string]string{}
	a.present = map[string]bool{}
	for server, names := range inv.registered {
		has := false
		for _, n := range names {
			if strings.HasPrefix(n, mcpToolPrefix(server)) {
				a.owner[n] = server
				has = true
			}
		}
		if has {
			a.present[server] = true
		}
	}
	a.withheld = map[string]string{}
	a.resolvePreload()
}

// Advertise is the agent's predicate (ADR-0083 §1): a built-in is always
// shown; a withheld MCP tool never; otherwise an MCP tool when
// everything is advertised, or its server or itself is loaded.
func (a *mcpAdvertiser) Advertise(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	server, isMCP := a.owner[name]
	if !isMCP {
		// Not a registered MCP tool: a built-in, or a name the
		// registry will refuse on its own.
		return !strings.HasPrefix(name, "mcp__") || !a.onRequest
	}
	if _, held := a.withheld[name]; held {
		return false
	}
	if !a.onRequest {
		return true
	}
	return a.loadedServers[server] || a.loadedTools[name]
}

// Stage records a tool call's effect under its id, to be applied by
// Commit when the loop accepts the call's result. An empty id (a tool
// run outside the loop) applies at once.
func (a *mcpAdvertiser) Stage(callID string, servers, toolNames []string, withheld map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := &pendingLoad{servers: servers, tools: toolNames, withheld: withheld}
	if callID == "" {
		a.apply(p)
		return
	}
	a.pending[callID] = p
}

// apply commits one staged effect and writes its record.
func (a *mcpAdvertiser) apply(p *pendingLoad) bool {
	changed := false
	rec := mcpAdvertiseRecord{}
	for _, s := range p.servers {
		if a.present[s] && !a.loadedServers[s] {
			a.loadedServers[s] = true
			changed = true
		}
		if a.present[s] {
			rec.Servers = append(rec.Servers, s)
		}
	}
	for _, n := range p.tools {
		if _, ok := a.owner[n]; ok && !a.loadedTools[n] {
			a.loadedTools[n] = true
			changed = true
		}
		if _, ok := a.owner[n]; ok {
			rec.Tools = append(rec.Tools, n)
		}
	}
	for n, why := range p.withheld {
		if _, ok := a.owner[n]; ok {
			if _, held := a.withheld[n]; !held {
				changed = true
			}
			a.withheld[n] = why
		}
	}
	if a.log != nil && (len(rec.Servers) > 0 || len(rec.Tools) > 0) {
		_ = a.log.Log(mcpAdvertiseKind, rec)
	}
	return changed
}

// AfterTool is the agent's hook (ADR-0083 §3): the call's staged effect
// is committed when the loop accepted the result and discarded when the
// call was abandoned. Returns whether the declarations changed.
func (a *mcpAdvertiser) AfterTool(tc llm.ToolCall, abandoned bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.pending[tc.ID]
	if !ok {
		return false
	}
	delete(a.pending, tc.ID)
	if abandoned {
		return false
	}
	return a.apply(p)
}

// LoadServerNow advertises one server at the operator's request (/mcp
// load, a replay) — between turns, so applied at once. Unknown names
// are refused with the names that exist.
func (a *mcpAdvertiser) LoadServerNow(server string) error { return a.loadServer(server, true) }

// loadServer is LoadServerNow with the record optional: a replay
// re-advertises what the transcript already holds and must not write
// it again.
func (a *mcpAdvertiser) loadServer(server string, record bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.present[server] {
		return a.unknownServer(server)
	}
	saved := a.log
	if !record {
		a.log = nil
	}
	a.apply(&pendingLoad{servers: []string{server}})
	a.log = saved
	// The operator's load also lifts the librarian's flags on that
	// server: /mcp load is the override ADR-0083 §6 names.
	for n, s := range a.owner {
		if s == server {
			delete(a.withheld, n)
		}
	}
	return nil
}

// LoadToolsNow advertises named tools at once (a replay). Names that no
// longer exist are returned, not skipped silently (ADR-0083 §8).
func (a *mcpAdvertiser) LoadToolsNow(names []string) (missing []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var have []string
	for _, n := range names {
		if _, ok := a.owner[n]; ok {
			have = append(have, n)
		} else {
			missing = append(missing, n)
		}
	}
	if len(have) > 0 {
		// Replayed loads write no new record: the transcript already
		// holds the one being replayed.
		saved := a.log
		a.log = nil
		a.apply(&pendingLoad{tools: have})
		a.log = saved
	}
	return missing
}

func (a *mcpAdvertiser) unknownServer(server string) error {
	known := a.servers()
	if len(known) == 0 {
		return errors.New("no MCP server registered tools this session")
	}
	return fmt.Errorf("no MCP server named %q this session; the servers are: %s", server, strings.Join(known, ", "))
}

// servers lists the present servers, sorted. Caller holds mu.
func (a *mcpAdvertiser) servers() []string {
	var out []string
	for s := range a.present {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Servers lists the present servers, sorted.
func (a *mcpAdvertiser) Servers() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.servers()
}

// ServerTools lists a server's registered tool names, sorted.
func (a *mcpAdvertiser) ServerTools(server string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.serverTools(server)
}

func (a *mcpAdvertiser) serverTools(server string) []string {
	var names []string
	for n, s := range a.owner {
		if s == server {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// Loaded reports whether the model has the server's tools.
func (a *mcpAdvertiser) Loaded(server string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.onRequest || a.loadedServers[server]
}

// Status describes one server's advertisement for /mcp: "" under
// "all" (nothing to say), otherwise what of it the model can see.
func (a *mcpAdvertiser) Status(server string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.onRequest {
		if n := a.withheldCount(server); n > 0 {
			return fmt.Sprintf("%d withheld", n)
		}
		return ""
	}
	names := a.serverTools(server)
	loaded, held := 0, 0
	for _, n := range names {
		if _, h := a.withheld[n]; h {
			held++
			continue
		}
		if a.loadedServers[server] || a.loadedTools[n] {
			loaded++
		}
	}
	var parts []string
	switch {
	case loaded == 0:
		parts = append(parts, "not loaded")
	case loaded == len(names):
		parts = append(parts, "loaded")
	default:
		parts = append(parts, fmt.Sprintf("%d of %d loaded", loaded, len(names)))
	}
	if held > 0 {
		parts = append(parts, fmt.Sprintf("%d withheld", held))
	}
	return strings.Join(parts, ", ")
}

func (a *mcpAdvertiser) withheldCount(server string) int {
	n := 0
	for name := range a.withheld {
		if a.owner[name] == server {
			n++
		}
	}
	return n
}

// WithheldTool is one librarian flag, for /mcp.
type WithheldTool struct{ Name, Why string }

// Withheld lists the flagged tools, sorted by name.
func (a *mcpAdvertiser) Withheld() []WithheldTool {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []WithheldTool
	for n, why := range a.withheld {
		out = append(out, WithheldTool{n, why})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Counts reports registered, advertised and withheld MCP tool counts
// (agent_info, ADR-0083 §8).
func (a *mcpAdvertiser) Counts() (registered, advertised, withheld int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for n, s := range a.owner {
		registered++
		if _, h := a.withheld[n]; h {
			withheld++
			continue
		}
		if !a.onRequest || a.loadedServers[s] || a.loadedTools[n] {
			advertised++
		}
	}
	return
}

// PromptLines renders the server list the system prompt carries under
// on-request (ADR-0083 §5): one line per present server with its tool
// count. Nothing under "all" — the declarations say it all.
func (a *mcpAdvertiser) PromptLines() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.onRequest {
		return nil
	}
	var out []string
	for _, s := range a.servers() {
		out = append(out, fmt.Sprintf("- %s (%d tools)", s, len(a.serverTools(s))))
	}
	return out
}

// toolLine renders one tool for a load result: its registered name and
// the first sentence of its description.
func toolLine(registry *tools.Registry, server, name string) string {
	desc := ""
	if t, ok := registry.Get(name); ok {
		desc = firstSentence(strings.TrimPrefix(t.Description, "[MCP:"+server+"] "))
	}
	return fmt.Sprintf("  - %s: %s", name, clipRunes(desc, catalogSentenceCap))
}

// registerMCPLoadTool registers mcp_load (ADR-0083 §4). The load is
// staged under the call id and applied by the loop.
func registerMCPLoadTool(registry *tools.Registry, adv *mcpAdvertiser) error {
	return registry.Register(&tools.Tool{
		Name: MCPLoadName,
		Description: "Load one MCP server named in the system prompt's server list: its tools join your tool " +
			"list for the rest of the session and are listed in the result. Call it before using any tool " +
			"of a server whose tools are not in your list. Loading runs nothing on the server.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"server": map[string]any{"type": "string", "description": "the server name exactly as the system prompt lists it"},
			},
			"required": []string{"server"},
		},
		Mutating: false,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			server, _ := args["server"].(string)
			server = strings.TrimSpace(server)
			if server == "" {
				return "", errors.New("server is required")
			}
			adv.mu.Lock()
			present := adv.present[server]
			already := !adv.onRequest || adv.loadedServers[server]
			names := adv.serverTools(server)
			adv.mu.Unlock()
			if !present {
				adv.mu.Lock()
				err := adv.unknownServer(server)
				adv.mu.Unlock()
				return "", err
			}
			adv.Stage(tools.CallID(ctx), []string{server}, nil, nil)
			var b strings.Builder
			if already {
				fmt.Fprintf(&b, "%s is already loaded. Its tools:\n", server)
			} else {
				fmt.Fprintf(&b, "%s loaded. Its tools are in your tool list now:\n", server)
			}
			for _, n := range names {
				b.WriteString(toolLine(registry, server, n) + "\n")
			}
			return b.String(), nil
		},
	})
}

// replayAdvertised re-advertises what a restored transcript loaded
// (ADR-0083 §8): every mcp_advertise record. Names that are gone are
// returned so the resume notes can say so.
func replayAdvertised(path string, adv *mcpAdvertiser) (loaded int, missing []string, err error) {
	err = session.Scan(path, func(kind string, _ time.Time, data json.RawMessage) error {
		if kind != mcpAdvertiseKind {
			return nil
		}
		var rec mcpAdvertiseRecord
		if json.Unmarshal(data, &rec) != nil {
			return nil
		}
		for _, s := range rec.Servers {
			if e := adv.loadServer(s, false); e != nil {
				missing = append(missing, s)
			} else {
				loaded++
			}
		}
		if len(rec.Tools) > 0 {
			gone := adv.LoadToolsNow(rec.Tools)
			missing = append(missing, gone...)
			loaded += len(rec.Tools) - len(gone)
		}
		return nil
	})
	return loaded, missing, err
}
