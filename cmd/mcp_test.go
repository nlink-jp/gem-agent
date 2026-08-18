package cmd

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

type stubCaller struct {
	name  string
	calls []string
}

func (s *stubCaller) Name() string { return s.name }
func (s *stubCaller) CallTool(ctx context.Context, tool string, args map[string]any) (string, error) {
	s.calls = append(s.calls, tool)
	return "remote-result", nil
}

func TestMCPToolName(t *testing.T) {
	if got := mcpToolName("tor-exit", "check_ip"); got != "mcp__tor-exit__check_ip" {
		t.Errorf("got %q", got)
	}
	// Invalid chars sanitized, length capped at Gemini's 64.
	got := mcpToolName("srv name", strings.Repeat("x", 100))
	if strings.Contains(got, " ") || len(got) > 64 {
		t.Errorf("got %q (len %d)", got, len(got))
	}
}

func TestRegisterMCPTools(t *testing.T) {
	reg, err := tools.New(t.TempDir(), func(ctx context.Context, c string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/true")
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubCaller{name: "tor-exit"}
	added, errs := registerMCPTools(reg, stub, []mcp.Tool{
		{Name: "check_ip", Description: "Check an IP.", InputSchema: map[string]any{"type": "object"}},
		{Name: "no_schema", Description: "Schema-less tool."},
	})
	if len(errs) != 0 || len(added) != 2 {
		t.Fatalf("added=%v errs=%v", added, errs)
	}

	tool, ok := reg.Get("mcp__tor-exit__check_ip")
	if !ok {
		t.Fatal("adapted tool not registered")
	}
	if !tool.Mutating {
		t.Error("MCP tools must require approval")
	}
	if !strings.HasPrefix(tool.Description, "[MCP:tor-exit]") {
		t.Errorf("description = %q", tool.Description)
	}

	// The adapter must call the REMOTE name, not the namespaced one.
	out, err := tool.Run(context.Background(), map[string]any{"ip": "192.0.2.1"})
	if err != nil || out != "remote-result" {
		t.Fatalf("run: %q %v", out, err)
	}
	if len(stub.calls) != 1 || stub.calls[0] != "check_ip" {
		t.Errorf("remote calls = %v", stub.calls)
	}

	// Schema-less tools get a minimal object schema (Gemini requires one).
	noSchema, _ := reg.Get("mcp__tor-exit__no_schema")
	if noSchema.Parameters["type"] != "object" {
		t.Errorf("fallback schema = %v", noSchema.Parameters)
	}
}
