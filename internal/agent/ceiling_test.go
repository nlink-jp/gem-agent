package agent

// The session's lane ceiling refuses a call whose effect needs a lane
// above it, before any gate (ADR-0080 §2-3). One setting bounds every
// tool: what is pinned here is which calls it reaches and which it
// deliberately does not.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

func ceilingAgent(t *testing.T, state string, extra ...*tools.Tool) *Agent {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := tools.New(dir,
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/bash", "-c", c) },
		5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range extra {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	return New(Options{Backend: &mockBackend{}, Registry: reg, Gate: &recordingGate{},
		System: "s", MaxTurns: 5, ReadOnly: state})
}

func shellLane(command, access string) llm.ToolCall {
	args := map[string]any{"command": command}
	if access != "" {
		args["access"] = access
	}
	return llm.ToolCall{ID: "c", Name: "shell_exec", Args: args}
}

func TestCeilingRefusesWhatNeedsAHigherLane(t *testing.T) {
	mcp := &tools.Tool{Name: "mcp__srv__patch", Description: "Patch a remote note.", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Run:        func(context.Context, map[string]any) (string, error) { return "", nil }}
	// save_memory is registered by the command layer, not by the tool
	// registry, so the agent-level test stands one in.
	mem := &tools.Tool{Name: "save_memory", Description: "Remember a fact.", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Run:        func(context.Context, map[string]any) (string, error) { return "", nil }}

	cases := []struct {
		name    string
		call    llm.ToolCall
		refused bool
	}{
		{"write-lane shell", shellLane("touch x", "write"), true},
		{"operator-lane shell", shellLane("git config a b", "operator"), true},
		{"read-lane shell", shellLane("ls", "read"), false},
		{"shell with no declared lane", shellLane("ls", ""), false},
		{"write_file", writeCall("f.txt"), true},
		{"edit_file", llm.ToolCall{ID: "c", Name: "edit_file",
			Args: map[string]any{"path": "f.txt", "old_string": "x", "new_string": "y"}}, true},
		{"save_memory", llm.ToolCall{ID: "c", Name: "save_memory",
			Args: map[string]any{"scope": "project", "name": "n", "content": "c"}}, true},
		{"read_file", llm.ToolCall{ID: "c", Name: "read_file", Args: map[string]any{"path": "f.txt"}}, false},
		{"list_files", llm.ToolCall{ID: "c", Name: "list_files", Args: map[string]any{}}, false},
		// The rule tier cannot read another server's effects, so the
		// ceiling does not pretend to bound them (ADR-0077). ADR-0080 §5
		// states the ceiling to the model tier instead.
		{"an MCP tool that writes", llm.ToolCall{ID: "c", Name: "mcp__srv__patch", Args: map[string]any{}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := ceilingAgent(t, sandbox.CeilingOn, mcp, mem)
			if got := a.decide(tc.call).OverCeiling; got != tc.refused {
				t.Errorf("read-only: OverCeiling = %v, want %v", got, tc.refused)
			}
			// With no ceiling nothing is over it, whatever the call.
			off := ceilingAgent(t, sandbox.CeilingOff, mcp, mem)
			if off.decide(tc.call).OverCeiling {
				t.Errorf("ceiling off: %s was refused anyway", tc.name)
			}
			// The auto state has not tightened yet, so it is off too.
			auto := ceilingAgent(t, sandbox.CeilingAuto, mcp, mem)
			if auto.decide(tc.call).OverCeiling {
				t.Errorf("auto (untightened): %s was refused anyway", tc.name)
			}
		})
	}
}

// liftGate answers the ceiling's lift question and records how it was
// asked. A mode is not a call, so the question must be must-prompt.
type liftGate struct {
	answer      bool
	asked       []string
	mustPrompts []bool
	lifts       []bool // whether each question was the mode change
}

func (g *liftGate) Approve(name, detail, purpose, reason string, mustPrompt bool) (bool, bool, string) {
	g.asked = append(g.asked, name+"|"+reason)
	g.mustPrompts = append(g.mustPrompts, mustPrompt)
	g.lifts = append(g.lifts, false)
	return g.answer, false, ""
}

// The mode question arrives here, and it has no allowlist answer to
// give: a mode is not a call.
func (g *liftGate) ApproveLift(name, detail, purpose, reason string) (bool, string) {
	g.asked = append(g.asked, name+"|"+reason)
	g.mustPrompts = append(g.mustPrompts, true)
	g.lifts = append(g.lifts, true)
	return g.answer, ""
}

// Declining leaves the ceiling in place, and the rest of the turn is not
// asked again: a model pushed by a poisoned tool result must not be able
// to raise one prompt per proposed write (ADR-0080 §4).
func TestCeilingLiftDeclinedIsNotAskedTwiceInATurn(t *testing.T) {
	a := ceilingAgent(t, sandbox.CeilingOn)
	gate := &liftGate{answer: false}
	a.gate = gate

	out, denied, _, _, err := a.execCallInner(context.Background(), shellLane("touch x", "write"))
	if err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Fatalf("asked %d times, want 1: %v", len(gate.asked), gate.asked)
	}
	// It arrives as the mode question, not as a tool approval. That is
	// the whole point: the tool-approval dialog's 'a' registers the tool
	// in the session allowlist even when the allowlist may not answer,
	// which would grant exactly what the ceiling withholds.
	if !gate.lifts[0] {
		t.Error("the ceiling asked through the ordinary tool-approval path")
	}
	if !strings.Contains(gate.asked[0], "capped at the read lane") {
		t.Errorf("the question does not name the ceiling: %q", gate.asked[0])
	}
	if !strings.Contains(out, "capped at the read lane") || !strings.Contains(out, "/readonly off") {
		t.Errorf("result = %q", out)
	}
	// Not an operator's denial of the tool: the learner reads that from
	// the exact deniedResult text (ADR-0045).
	if denied || out == deniedResult {
		t.Error("a ceiling refusal was reported as an operator denial")
	}
	if a.ReadOnly() != sandbox.CeilingOn {
		t.Errorf("the ceiling moved on a declined lift: %q", a.ReadOnly())
	}

	if _, _, _, _, err := a.execCallInner(context.Background(), shellLane("touch y", "write")); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Errorf("asked again after a decline: %v", gate.asked)
	}
}

// Approving is a mode change, and the call the operator was shown then
// proceeds without a second prompt about the same thing.
func TestCeilingLiftApprovedTurnsTheModeOff(t *testing.T) {
	a := ceilingAgent(t, sandbox.CeilingOn)
	gate := &liftGate{answer: true}
	a.gate = gate

	if _, _, _, _, err := a.execCallInner(context.Background(), writeCall("f.txt")); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Errorf("asked %d times, want 1: %v", len(gate.asked), gate.asked)
	}
	if a.ReadOnly() != sandbox.CeilingOff {
		t.Fatalf("the mode did not change: %q", a.ReadOnly())
	}
	// What follows is an ordinary session again: in the default mode a
	// mutating call still asks, but as itself — not as another lift.
	if _, _, _, _, err := a.execCallInner(context.Background(), writeCall("f.txt")); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 2 {
		t.Fatalf("the next write did not go through the ordinary gate: %v", gate.asked)
	}
	if gate.lifts[1] {
		t.Error("the ceiling asked again after it was lifted")
	}
}

// A floor is a different question and is asked on its own terms: the
// lift does not carry a Block-tier call past the floor that stops it.
func TestCeilingLiftDoesNotCarryACallPastAFloor(t *testing.T) {
	a := ceilingAgent(t, sandbox.CeilingOn)
	gate := &liftGate{answer: true}
	a.gate = gate
	if _, _, _, _, err := a.execCallInner(context.Background(), shellLane("sudo rm -rf /", "write")); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 2 {
		t.Fatalf("asked %d times, want 2 (the lift, then the floor): %v", len(gate.asked), gate.asked)
	}
	if !gate.lifts[0] || gate.lifts[1] {
		t.Errorf("questions = %v, want the mode change then a tool approval", gate.lifts)
	}
	if !gate.mustPrompts[1] {
		t.Error("the floor question was not must-prompt")
	}
}

// The state is the operator's, and reading it back is how the status
// line and the evaluator payload stay honest about which one is on.
func TestCeilingStateRoundTrips(t *testing.T) {
	a := ceilingAgent(t, "")
	if a.ReadOnly() != sandbox.CeilingOff || a.Ceiling() != sandbox.LaneOperator {
		t.Fatalf("empty state = %q / %v", a.ReadOnly(), a.Ceiling())
	}
	a.SetReadOnly(sandbox.CeilingOn)
	if a.ReadOnly() != sandbox.CeilingOn || a.Ceiling() != sandbox.LaneRead {
		t.Fatalf("on = %q / %v", a.ReadOnly(), a.Ceiling())
	}
	a.SetReadOnly(sandbox.CeilingOff)
	if a.Ceiling() != sandbox.LaneOperator {
		t.Fatalf("off = %v", a.Ceiling())
	}
}
