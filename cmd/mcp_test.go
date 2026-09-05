package cmd

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

type stubCaller struct {
	name   string
	calls  []string
	blocks []mcp.Content
	isErr  bool
	err    error
}

func (s *stubCaller) Name() string { return s.name }
func (s *stubCaller) CallTool(ctx context.Context, tool string, args map[string]any) ([]mcp.Content, bool, error) {
	s.calls = append(s.calls, tool)
	if s.err != nil {
		return nil, false, s.err
	}
	if s.blocks != nil {
		return s.blocks, s.isErr, nil
	}
	return []mcp.Content{{Type: "text", Text: "remote-result"}}, false, nil
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

// ADR-0075 §1: a failed remote call reaches the executor as a typed
// RemoteError whose kind is read from the error value — the server's
// isError result, the server's JSON-RPC rejection carried inside the
// client's CallError, or a cause of gem-agent's own — never from text.
func TestAdapterTypesRemoteFailuresByProvenance(t *testing.T) {
	cases := []struct {
		name string
		stub *stubCaller
		kind tools.RemoteErrorKind
		text string
		head string
	}{
		{"isError result", &stubCaller{name: "srv", blocks: textBlock("quota exceeded"), isErr: true},
			tools.RemoteResult, "quota exceeded", `MCP server "srv" answered q with an error:`},
		{"rpc rejection", &stubCaller{name: "srv", err: &mcp.CallError{Server: "srv", Tool: "q", Err: &mcp.RPCError{Code: -32602, Message: "Invalid params"}}},
			tools.RemoteRejected, "rpc error -32602: Invalid params", `MCP server "srv" rejected the call to q:`},
		{"transport", &stubCaller{name: "srv", err: &mcp.CallError{Server: "srv", Tool: "q", Err: errors.New("tools/call timed out after 1s (server killed; it restarts on the next call)")}},
			tools.RemoteIncomplete, "tools/call timed out after 1s (server killed; it restarts on the next call)", `gem-agent could not complete q on MCP server "srv":`},
	}
	for _, c := range cases {
		reg, err := tools.New(t.TempDir(), func(ctx context.Context, cmd string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/true")
		}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, errs := registerMCPTools(reg, c.stub, []mcp.Tool{{Name: "q", Description: "q"}}); len(errs) != 0 {
			t.Fatalf("%s: %v", c.name, errs)
		}
		tool, _ := reg.Get("mcp__srv__q")
		out, err := tool.Run(context.Background(), map[string]any{"x": 1})
		var re *tools.RemoteError
		if out != "" || !errors.As(err, &re) {
			t.Fatalf("%s: out=%q err=%v — a failed remote call must come back as a RemoteError", c.name, out, err)
		}
		if re.Kind != c.kind || re.Text != c.text || re.Server != "srv" || re.Tool != "q" {
			t.Errorf("%s: got %+v, want kind %v text %q", c.name, *re, c.kind, c.text)
		}
		if !strings.HasPrefix(re.Error(), c.head) {
			t.Errorf("%s: rendered %q, want prefix %q", c.name, re.Error(), c.head)
		}
	}
}
