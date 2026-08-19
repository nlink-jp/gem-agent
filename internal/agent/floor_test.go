package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/approve"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// floorGate records the mustPrompt flag execCall passes (ADR-0021 §5).
type floorGate struct {
	calls []bool
}

func (g *floorGate) Approve(name, detail, reason string, mustPrompt bool) bool {
	g.calls = append(g.calls, mustPrompt)
	return false
}

func newFloorAgent(t *testing.T, gate Approver, pol policy.Policy) *Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/true") },
		time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Registry: reg, Gate: gate, Policy: pol})
}

// The measured hole: one 'a' on a benign shell_exec waved every later
// Block-tier command through. execCall must pass mustPrompt=true for a
// Block verdict so the gates skip their allowlist.
func TestBlockTierSetsMustPrompt(t *testing.T) {
	gate := &floorGate{}
	a := newFloorAgent(t, gate, policy.Policy{})

	a.execCall(context.Background(), llm.ToolCall{
		Name: "shell_exec", Args: map[string]any{"command": "sudo rm -rf /tmp/x"},
	})
	a.execCall(context.Background(), llm.ToolCall{
		Name: "shell_exec", Args: map[string]any{"command": "make build"},
	})

	if len(gate.calls) != 2 {
		t.Fatalf("gate consulted %d times, want 2", len(gate.calls))
	}
	if !gate.calls[0] {
		t.Error("Block-tier call reached the gate with mustPrompt=false — the session allowlist could answer it")
	}
	if gate.calls[1] {
		t.Error("ordinary mutating call reached the gate with mustPrompt=true — 'a' would never stick")
	}
}

// An "always" policy is the operator's explicit revocation of unattended
// running; the allowlist may not override it either.
func TestAlwaysPolicySetsMustPrompt(t *testing.T) {
	pol, _, err := policy.Build(map[string]string{"write_file": "always"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	gate := &floorGate{}
	a := newFloorAgent(t, gate, pol)
	a.execCall(context.Background(), llm.ToolCall{
		Name: "write_file", Args: map[string]any{"path": "x.txt", "content": "y"},
	})
	if len(gate.calls) != 1 || !gate.calls[0] {
		t.Errorf("gate calls = %v — an always-policy tool must carry mustPrompt=true", gate.calls)
	}
}

// End-to-end through the plain gate: 'a' answered on a benign call must
// not silence a later Block-tier prompt — and the prompt must appear.
func TestSessionAllowlistDoesNotLiftBlockFloor(t *testing.T) {
	var out strings.Builder
	// First answer: 'a' (always this session). Second: 'n'.
	in := strings.NewReader("a\nn\n")
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/true") },
		time.Second)
	if err != nil {
		t.Fatal(err)
	}
	gate := approve.New(in, &out)
	a := New(Options{Registry: reg, Gate: gate})

	a.execCall(context.Background(), llm.ToolCall{
		Name: "shell_exec", Args: map[string]any{"command": "mkdir -p build"},
	})
	promptsAfterFirst := strings.Count(out.String(), "[approval]")
	if promptsAfterFirst != 1 {
		t.Fatalf("first (benign) call: %d prompts, want 1", promptsAfterFirst)
	}

	result := a.execCall(context.Background(), llm.ToolCall{
		Name: "shell_exec", Args: map[string]any{"command": "sudo whoami"},
	})
	if strings.Count(out.String(), "[approval]") != 2 {
		t.Errorf("Block-tier call after 'a' did not prompt — the allowlist lifted the floor:\n%s", out.String())
	}
	if !strings.Contains(result, "denied") {
		t.Errorf("denied Block-tier call result = %q", result)
	}
	// And the ordinary case still benefits from 'a': no third prompt.
	a.execCall(context.Background(), llm.ToolCall{
		Name: "shell_exec", Args: map[string]any{"command": "mkdir -p dist"},
	})
	if strings.Count(out.String(), "[approval]") != 2 {
		t.Errorf("allowlisted benign call prompted — 'a' must keep working below the floor:\n%s", out.String())
	}
}
