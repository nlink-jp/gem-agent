//go:build live

package cmd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/mcpfilter"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// Dumps every connected server's tools/list (raw name, description,
// input schema) to MCP_DUMP_OUT, for the tool-librarian probe.
//
//	MCP_DUMP_OUT=/path/tools.json go test -tags live -run TestDumpMCPDeclarations ./cmd/
func TestDumpMCPDeclarations(t *testing.T) {
	out := os.Getenv("MCP_DUMP_OUT")
	if out == "" {
		t.Skip("MCP_DUMP_OUT not set")
	}
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	filter, err := mcpfilter.Build(cfg.MCP.Exclude, mcpfilter.PolicyScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	clients, summary, _ := connectMCPServers(ctx, cfg, t.TempDir(), "live-test", reg, io.Discard, projectGrant{}, filter)
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	type server struct {
		Name  string `json:"name"`
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	var dump []server
	for _, c := range clients {
		list, err := c.ListTools(ctx)
		if err != nil {
			t.Logf("%s: %v", c.Name(), err)
			continue
		}
		s := server{Name: c.Name()}
		for _, tl := range list {
			s.Tools = append(s.Tools, struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
			}{tl.Name, tl.Description, tl.InputSchema})
		}
		dump = append(dump, s)
	}
	b, _ := json.MarshalIndent(dump, "", " ")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("servers=%d summary=%d bytes=%d", len(dump), len(summary), len(b))
}
