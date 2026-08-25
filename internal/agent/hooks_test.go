package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

func hookAgent(t *testing.T, mb *mockBackend, gate Approver,
	hook func(context.Context, string, map[string]any) (bool, string)) (*Agent, *tools.Registry) {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/bash", "-c", command)
		}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{
		Backend: mb, Registry: reg, Gate: gate,
		System: "test system", MaxTurns: 5, PreToolHook: hook,
	}), reg
}

// A hook deny is a deterministic floor (ADR-0044 §2): the tool never
// runs, the approval gate is never consulted — a deny is not a
// question — and the model receives the reason as the tool result
// (the ADR-0043 principle).
func TestPreToolHookDenyIsAFloor(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "write_file",
			Args: map[string]any{"path": "x.txt", "content": "data"}}}},
		{Content: "understood"},
	}}
	gate := &approveAll{}
	var saw string
	a, reg := hookAgent(t, mb, gate, func(_ context.Context, name string, _ map[string]any) (bool, string) {
		saw = name
		return true, "the org guard said no"
	})
	if _, err := a.Run(context.Background(), "write x.txt", nil); err != nil {
		t.Fatal(err)
	}
	if saw != "write_file" {
		t.Fatalf("hook saw %q", saw)
	}
	if _, err := os.Stat(filepath.Join(reg.ProjectDir(), "x.txt")); err == nil {
		t.Error("denied tool ran anyway")
	}
	if len(gate.asked) != 0 {
		t.Errorf("approval gate consulted for a hook-denied call: %v", gate.asked)
	}
	toolMsg := mb.calls[1][2]
	if !strings.Contains(toolMsg.Content, "denied by a pre-tool hook") ||
		!strings.Contains(toolMsg.Content, "the org guard said no") {
		t.Errorf("the model was not told the reason: %q", toolMsg.Content)
	}
}

// A pass-through hook changes nothing: the call still goes through the
// normal ladder and runs (hooks only ever tighten, ADR-0044 §3).
func TestPreToolHookPassThroughStillGates(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "write_file",
			Args: map[string]any{"path": "y.txt", "content": "ok"}}}},
		{Content: "done"},
	}}
	gate := &approveAll{}
	a, reg := hookAgent(t, mb, gate, func(context.Context, string, map[string]any) (bool, string) {
		return false, ""
	})
	if _, err := a.Run(context.Background(), "write y.txt", nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(reg.ProjectDir(), "y.txt")); err != nil || string(data) != "ok" {
		t.Errorf("tool did not run after pass-through: %v %q", err, data)
	}
	if len(gate.asked) != 1 {
		t.Errorf("gate consulted %d times, want 1", len(gate.asked))
	}
}
