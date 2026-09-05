package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	}, sandbox.Enforcement{Confined: true, ReadLane: readLane})
	return a
}

// unconfinedAgent is laneAgent with the sandbox off (--no-sandbox).
func unconfinedAgent(t *testing.T, mb *mockBackend, gate Approver, policyTools map[string]string) *Agent {
	t.Helper()
	a, reg := policyAgent(t, mb, gate, policyTools)
	reg.SetLaneExec(func(ctx context.Context, c string, _ sandbox.Lane) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", c)
	}, sandbox.Enforcement{})
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

// ADR-0073 §5: with the sandbox off no lane bounds the command, so a
// shell call is the operator's alone in every lane — a `never` policy
// (the --allow grant) does not lift it, and the audit record says
// "unconfined:".
func TestUnconfinedShellIsTheOperatorsAlone(t *testing.T) {
	for _, access := range []string{"", "read", "write"} {
		mb := &mockBackend{responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{laneCall("echo hi", access)}},
			{Content: "done"},
		}}
		gate := &laneGate{}
		a := unconfinedAgent(t, mb, gate, map[string]string{"shell_exec": "never"})
		if _, err := a.Run(context.Background(), "run it", nil); err != nil {
			t.Fatal(err)
		}
		if len(gate.asked) != 1 || !gate.mustPrompt[0] {
			t.Errorf("unconfined %q lane under a never policy: asked=%v mustPrompt=%v", access, gate.asked, gate.mustPrompt)
		}
		d := a.decide(laneCall("echo hi", access))
		if !d.Verdict.OperatorOnly || !d.Mutating {
			t.Errorf("unconfined decision = %+v", d)
		}
		if got := a.laneOf(laneCall("echo hi", access)); !strings.HasPrefix(got, "unconfined:") {
			t.Errorf("laneOf = %q", got)
		}
	}
	// The Block floor still outranks the unconfined verdict.
	a := unconfinedAgent(t, &mockBackend{}, &laneGate{}, nil)
	if d := a.decide(laneCall("sudo id", "read")); d.Verdict.Tier.String() != "block" {
		t.Errorf("floor under unconfined = %+v", d)
	}
}

// The boundary does not move with the mode (design review of ADR-0073,
// class E): the same Decision is read in the default mode, under a
// never policy and by the auto ladder, so for every lane × command the
// floor, the mutation flag and the gate agree.
func TestDecisionBoundaryIsModeIndependent(t *testing.T) {
	type want struct{ mutating, floor bool }
	cases := []struct {
		command, access string
		want            want
	}{
		{"ls", "read", want{false, false}},
		{"ls", "", want{false, false}},
		{"echo hi > f", "write", want{true, false}},
		{"ls", "operator", want{true, true}},
		{"sudo id", "read", want{false, true}},
		{"rm -rf x", "write", want{true, true}},
		{"cat ~/.ssh/id_rsa", "read", want{false, true}},
	}
	for _, policyTools := range []map[string]string{nil, {"shell_exec": "never"}} {
		a := laneAgent(t, &mockBackend{}, &laneGate{}, true)
		if policyTools != nil {
			a, _ = policyAgent(t, &mockBackend{}, &laneGate{}, policyTools)
			a.registry.SetLaneExec(func(ctx context.Context, c string, _ sandbox.Lane) *exec.Cmd {
				return exec.CommandContext(ctx, "/bin/bash", "-c", c)
			}, sandbox.Enforcement{Confined: true, ReadLane: true})
		}
		for _, c := range cases {
			tc := laneCall(c.command, c.access)
			d := a.decide(tc)
			if d.Mutating != c.want.mutating || d.Floor() != c.want.floor {
				t.Errorf("policy=%v %q/%q: mutating=%v floor=%v, want %+v", policyTools, c.command, c.access, d.Mutating, d.Floor(), c.want)
			}
			// gated: the default mode gates a mutating call or a floor;
			// a never policy gates a floor only.
			gated := a.gated(d, tc)
			wantGated := d.Mutating || d.Floor()
			if policyTools != nil {
				wantGated = d.Floor()
			}
			if gated != wantGated {
				t.Errorf("policy=%v %q/%q: gated=%v, want %v", policyTools, c.command, c.access, gated, wantGated)
			}
		}
	}
}

// Review F1: the approval detail leads with the lane, so a write-lane,
// an operator-lane, an unverified and an unconfined run read
// differently in the box and in the gate_decision record.
func TestDescribeLeadsWithTheLane(t *testing.T) {
	a := laneAgent(t, &mockBackend{}, &laneGate{}, true)
	for _, c := range []struct{ access, want string }{
		{"", "[read] ls"}, {"read", "[read] ls"}, {"write", "[write] ls"}, {"operator", "[operator] ls"}, {"bogus", "[invalid] ls"},
	} {
		if detail, _ := a.Describe(laneCall("ls", c.access)); detail != c.want {
			t.Errorf("access %q: detail = %q, want %q", c.access, detail, c.want)
		}
	}
	a = laneAgent(t, &mockBackend{}, &laneGate{}, false)
	if detail, _ := a.Describe(laneCall("ls", "")); detail != "[unverified:read] ls" {
		t.Errorf("unverified detail = %q", detail)
	}
	a = unconfinedAgent(t, &mockBackend{}, &laneGate{}, nil)
	if detail, _ := a.Describe(laneCall("ls", "write")); detail != "[unconfined:write] ls" {
		t.Errorf("unconfined detail = %q", detail)
	}
	// Review F7: an access value that names no lane is refused before
	// any gate — neither run unasked nor prompted about.
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{laneCall("ls", "bogus")}},
		{Content: "done"},
	}}
	gate := &laneGate{}
	a = laneAgent(t, mb, gate, true)
	if _, err := a.Run(context.Background(), "run it", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 0 {
		t.Errorf("an invalid lane reached the gate: %v", gate.asked)
	}
	if d := a.decide(laneCall("ls", "bogus")); d.Invalid == nil || !d.Mutating {
		t.Errorf("invalid lane decision = %+v", d)
	}
}

// Final review R2: a write_file to a link named like an ordinary file
// is judged by what it points at.
func TestFileToolVerdictFollowsTheLink(t *testing.T) {
	a := laneAgent(t, &mockBackend{}, &laneGate{}, true)
	proj := a.registry.ProjectDir()
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(proj, "notes.md")); err != nil {
		t.Fatal(err)
	}
	d := a.decide(llm.ToolCall{Name: "write_file", Args: map[string]any{"path": "notes.md", "content": "x"}})
	if !d.Verdict.OperatorOnly {
		t.Errorf("write through a link to AGENTS.md = %+v, want operator-only", d.Verdict)
	}
	d = a.decide(llm.ToolCall{Name: "write_file", Args: map[string]any{"path": "plain.md", "content": "x"}})
	if d.Verdict.OperatorOnly || d.Floor() {
		t.Errorf("ordinary new file = %+v", d.Verdict)
	}
}
