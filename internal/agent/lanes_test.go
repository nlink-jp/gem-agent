package agent

import (
	"context"
	"os/exec"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// laneAgent is policyAgent with a kernel-enforced read lane declared
// on the registry (the runner itself is plain bash: these tests are
// about the gates, the sandbox package tests the cage).
func laneAgent(t *testing.T, mb *mockBackend, gate Approver, readLane bool) *Agent {
	t.Helper()
	a, reg := policyAgent(t, mb, gate, nil)
	reg.SetLaneExec(func(ctx context.Context, c string, _ sandbox.Lane) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", c)
	}, readLane)
	return a
}

// laneGate records each ask and whether the floor made it mandatory.
type laneGate struct {
	asked      []string
	mustPrompt []bool
}

func (g *laneGate) Approve(name, detail, purpose, reason string, mustPrompt bool) (bool, bool, string) {
	g.asked = append(g.asked, name+": "+detail)
	g.mustPrompt = append(g.mustPrompt, mustPrompt)
	return false, false, ""
}

func laneCall(command, access string) llm.ToolCall {
	args := map[string]any{"command": command}
	if access != "" {
		args["access"] = access
	}
	return llm.ToolCall{ID: "c", Name: "shell_exec", Args: args}
}

// ADR-0073 §1: a read-lane command is non-mutating by construction and
// runs without a prompt in the default mode; the same command with no
// read lane behind it (sandbox off) is gated like any mutating call.
func TestReadLaneRunsUngated(t *testing.T) {
	for _, readLane := range []bool{true, false} {
		mb := &mockBackend{responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{laneCall("echo hi", "")}},
			{Content: "done"},
		}}
		gate := &laneGate{}
		a := laneAgent(t, mb, gate, readLane)
		if _, err := a.Run(context.Background(), "run it", nil); err != nil {
			t.Fatal(err)
		}
		if readLane && len(gate.asked) != 0 {
			t.Errorf("read lane with a sandbox: the gate was consulted: %v", gate.asked)
		}
		if !readLane && len(gate.asked) != 1 {
			t.Errorf("read lane without a sandbox: the gate was not consulted: %v", gate.asked)
		}
	}
}

// The Block floor is not lifted by the lane: a `sudo` in the read lane
// asks (the cage would refuse it; the operator still sees the attempt).
func TestFloorHoldsInTheReadLane(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{laneCall("sudo id", "read")}},
		{Content: "done"},
	}}
	gate := &laneGate{}
	a := laneAgent(t, mb, gate, true)
	if _, err := a.Run(context.Background(), "run it", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 || !gate.mustPrompt[0] {
		t.Errorf("floor in the read lane: asked=%v mustPrompt=%v", gate.asked, gate.mustPrompt)
	}
}

// Write and operator lanes are gated; the operator lane is a floor the
// session allowlist may not answer.
func TestWriteAndOperatorLanesAreGated(t *testing.T) {
	for _, c := range []struct {
		access     string
		mustPrompt bool
	}{{"write", false}, {"operator", true}} {
		mb := &mockBackend{responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{laneCall("echo hi", c.access)}},
			{Content: "done"},
		}}
		gate := &laneGate{}
		a := laneAgent(t, mb, gate, true)
		if _, err := a.Run(context.Background(), "run it", nil); err != nil {
			t.Fatal(err)
		}
		if len(gate.asked) != 1 {
			t.Fatalf("%s lane: asked=%v", c.access, gate.asked)
		}
		if gate.mustPrompt[0] != c.mustPrompt {
			t.Errorf("%s lane: mustPrompt=%v, want %v", c.access, gate.mustPrompt[0], c.mustPrompt)
		}
	}
}

// The one decision point: every gate reads the same Decision, so the
// floors agree by construction.
func TestDecisionIsOneReading(t *testing.T) {
	a := laneAgent(t, &mockBackend{}, &laneGate{}, true)
	d := a.decide(laneCall("ls", "read"))
	if d.Mutating || d.Floor() || d.Tool == nil {
		t.Errorf("read lane decision = %+v", d)
	}
	d = a.decide(laneCall("cat ~/.ssh/id_rsa", "read"))
	if !d.Floor() {
		t.Errorf("credential read in the read lane is a floor: %+v", d)
	}
	d = a.decide(laneCall("ls", "operator"))
	if !d.Mutating || !d.Floor() || !d.Verdict.OperatorOnly {
		t.Errorf("operator lane decision = %+v", d)
	}
	d = a.decide(llm.ToolCall{Name: "no_such_tool"})
	if d.Tool != nil || !d.Mutating {
		t.Errorf("unknown tool decision = %+v", d)
	}
}
