package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// mcpCaller is the slice of *mcp.Client the adapter needs — an interface
// so tests can stub the wire protocol away.
type mcpCaller interface {
	Name() string
	CallTool(ctx context.Context, tool string, args map[string]any) (string, error)
}

const (
	// Gemini function names allow [a-zA-Z0-9_.-], max 64 chars.
	maxToolNameLen = 64
	maxToolDescLen = 2000
)

// mcpToolName builds the registry name for an MCP tool, Claude Code
// style: mcp__<server>__<tool>, sanitized to Gemini's charset.
func mcpToolName(server, tool string) string {
	name := "mcp__" + sanitizeToolName(server) + "__" + sanitizeToolName(tool)
	if len(name) > maxToolNameLen {
		name = name[:maxToolNameLen]
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
				return client.CallTool(ctx, remoteName, args)
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

// connectMCPServers loads .mcp.json from the project and registers every
// reachable server's tools. Failures are warnings — a broken MCP server
// must not block a backup tool.
func connectMCPServers(ctx context.Context, cfg *config.Config, projectDir, version string, registry *tools.Registry, stderr io.Writer) (clients []*mcp.Client, summary []string) {
	if !cfg.MCP.Enabled {
		return nil, nil
	}
	path := filepath.Join(projectDir, ".mcp.json")
	servers, skipped, err := mcp.LoadConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "warning: %v — MCP disabled for this session\n", err)
		return nil, nil
	}
	for _, s := range skipped {
		fmt.Fprintf(stderr, "warning: skipping MCP server %s\n", s)
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	timeout := time.Duration(cfg.MCP.CallTimeoutSec) * time.Second
	for _, name := range names {
		client := mcp.NewStdio(name, servers[name], timeout, version)
		lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		toolList, err := client.ListTools(lctx)
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "warning: MCP server %s unavailable: %v\n", name, err)
			client.Close()
			continue
		}
		added, errs := registerMCPTools(registry, client, toolList)
		for _, e := range errs {
			fmt.Fprintf(stderr, "warning: MCP server %s: %s\n", name, e)
		}
		if len(added) == 0 {
			fmt.Fprintf(stderr, "warning: MCP server %s advertises no usable tools\n", name)
			client.Close()
			continue
		}
		clients = append(clients, client)
		summary = append(summary, fmt.Sprintf("%s (%d tools)", name, len(added)))
	}
	return clients, summary
}
