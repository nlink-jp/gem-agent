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
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// ceilingFor keeps the tests reading in the operator's three words
// while the runtime holds the two independent bits behind them.
func ceilingFor(state string) sandbox.Ceiling {
	switch state {
	case "on":
		return sandbox.Ceiling{ReadOnly: true}
	case "auto":
		return sandbox.Ceiling{Auto: true}
	}
	return sandbox.Ceiling{}
}

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
		System: "s", MaxTurns: 5, Ceiling: ceilingFor(state)})
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
			a := ceilingAgent(t, "on", mcp, mem)
			if got := a.decide(tc.call).OverCeiling; got != tc.refused {
				t.Errorf("read-only: OverCeiling = %v, want %v", got, tc.refused)
			}
			// With no ceiling nothing is over it, whatever the call.
			off := ceilingAgent(t, "off", mcp, mem)
			if off.decide(tc.call).OverCeiling {
				t.Errorf("ceiling off: %s was refused anyway", tc.name)
			}
			// The auto state has not tightened yet, so it is off too.
			auto := ceilingAgent(t, "auto", mcp, mem)
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
	a := ceilingAgent(t, "on")
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
	if !a.CeilingState().ReadOnly {
		t.Errorf("the ceiling moved on a declined lift: %+v", a.CeilingState())
	}

	if _, _, _, _, err := a.execCallInner(context.Background(), shellLane("touch y", "write")); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Errorf("asked again after a decline: %v", gate.asked)
	}
}

// The dialog is asked once a turn; the denials are not once a turn.
// Without a line for the suppressed ones, one-shot printed only the
// first refusal of a run — the mode question is the only thing denyGate
// prints — and the TUI showed the operator nothing while the model
// retried (independent review).
func TestCeilingSuppressedRefusalStillSpeaks(t *testing.T) {
	a := ceilingAgent(t, "on")
	a.gate = &liftGate{answer: false}
	var notices []string
	a.onNotice = func(msg string) { notices = append(notices, msg) }

	for _, cmd := range []string{"touch x", "touch y", "touch z"} {
		if _, _, _, _, err := a.execCallInner(context.Background(), shellLane(cmd, "write")); err != nil {
			t.Fatal(err)
		}
	}
	// The first refusal spoke through the dialog, so it owes no notice;
	// the two the dialog never saw do.
	if len(notices) != 2 {
		t.Fatalf("notices = %d, want 2 (one per suppressed refusal): %q", len(notices), notices)
	}
	for _, n := range notices {
		if !strings.Contains(n, "shell_exec") {
			t.Errorf("the notice does not name the call: %q", n)
		}
	}
}

// The suppression is per turn, so a new turn asks again: an operator
// who declined a lift while asking one thing has not answered for the
// next thing they ask. Nothing covered the reset (independent review).
func TestCeilingLiftDeclineDoesNotOutlastTheTurn(t *testing.T) {
	a := ceilingAgent(t, "on")
	gate := &liftGate{answer: false}
	a.gate = gate
	a.backend = &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{shellLane("touch x", "write")}},
		{Content: "done"},
		{ToolCalls: []llm.ToolCall{shellLane("touch y", "write")}},
		{Content: "done"},
	}}
	for _, turn := range []string{"最初の依頼", "次の依頼"} {
		if _, err := a.Run(context.Background(), turn, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(gate.asked) != 2 {
		t.Errorf("asked %d times over two turns, want 2: %v", len(gate.asked), gate.asked)
	}
}

// Approving is a mode change and nothing more. The call then goes
// through the ordinary rules, which is a second, separate question —
// treating the lift as the call's approval spent an "always" policy
// without its prompt and skipped the ladder entirely (independent
// review, 2026-09-09).
func TestCeilingLiftApprovedTurnsTheModeOff(t *testing.T) {
	a := ceilingAgent(t, "on")
	gate := &liftGate{answer: true}
	a.gate = gate

	if _, _, _, _, err := a.execCallInner(context.Background(), writeCall("f.txt")); err != nil {
		t.Fatal(err)
	}
	if a.CeilingState().ReadOnly {
		t.Fatalf("the mode did not change: %+v", a.CeilingState())
	}
	if len(gate.asked) != 2 {
		t.Fatalf("asked %d times, want 2 (the mode, then the call): %v", len(gate.asked), gate.asked)
	}
	if !gate.lifts[0] || gate.lifts[1] {
		t.Errorf("questions = %v, want the mode change then a tool approval", gate.lifts)
	}

	// What follows is an ordinary session again: the ceiling is gone, so
	// the next write asks as itself and not as another lift.
	if _, _, _, _, err := a.execCallInner(context.Background(), writeCall("f.txt")); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 3 || gate.lifts[2] {
		t.Errorf("the ceiling asked again after it was lifted: %v", gate.lifts)
	}
}

// The lift answers the ceiling's question, never the tool's. A tool the
// operator marked "always" still gets its own prompt, and under auto the
// ladder still runs — before this, a lift skipped both.
func TestCeilingLiftDoesNotSpendTheToolsOwnGate(t *testing.T) {
	pol, _, err := policy.Build(map[string]string{"write_file": "always"}, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	a := ceilingAgent(t, "on")
	a.policy = pol
	gate := &liftGate{answer: true}
	a.gate = gate
	if _, _, _, _, err := a.execCallInner(context.Background(), writeCall("f.txt")); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 2 {
		t.Fatalf("asked %d times, want 2: %v", len(gate.asked), gate.asked)
	}
	// The second is the tool's own must-prompt question, not the lift.
	if gate.lifts[1] || !gate.mustPrompts[1] {
		t.Errorf("the \"always\" policy was spent by the lift: lifts=%v mustPrompts=%v",
			gate.lifts, gate.mustPrompts)
	}
}

// A floor is a different question and is asked on its own terms: the
// lift does not carry a Block-tier call past the floor that stops it.
func TestCeilingLiftDoesNotCarryACallPastAFloor(t *testing.T) {
	a := ceilingAgent(t, "on")
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

// The ceiling and the watcher are independent, and both directions of
// that matter (ADR-0080 §1). A first draft made them one tri-state, so
// tightening destroyed the fact that the session was watching and
// lifting silently disarmed the watcher the operator had asked for.
func TestCeilingAndWatcherAreIndependent(t *testing.T) {
	a := ceilingAgent(t, "auto")
	if c := a.CeilingState(); c.ReadOnly || !c.Auto {
		t.Fatalf("start = %+v, want the watcher armed and no ceiling", c)
	}
	// Raising the ceiling — however it happens — leaves the watcher.
	a.SetReadOnly(true, "test")
	if c := a.CeilingState(); !c.ReadOnly || !c.Auto {
		t.Errorf("after tightening = %+v, want both", c)
	}
	// And lifting it leaves the watcher armed, so a later read-only
	// request is caught the same way the first one was.
	a.SetReadOnly(false, "test")
	if c := a.CeilingState(); c.ReadOnly || !c.Auto {
		t.Errorf("after lifting = %+v, want the watcher still armed", c)
	}
	// Disarming does not lift a ceiling in force, either.
	a.SetReadOnly(true, "test")
	a.SetReadOnlyAuto(false, "test")
	if c := a.CeilingState(); !c.ReadOnly || c.Auto {
		t.Errorf("after disarming = %+v, want the ceiling kept", c)
	}
	if a.Ceiling() != sandbox.LaneRead {
		t.Errorf("lane = %v, want read", a.Ceiling())
	}
	a.SetReadOnly(false, "test")
	if a.Ceiling() != sandbox.LaneOperator {
		t.Errorf("lane = %v, want operator", a.Ceiling())
	}
}

// An approved lift moves the ceiling and nothing else.
func TestCeilingLiftLeavesTheWatcherArmed(t *testing.T) {
	a := ceilingAgent(t, "auto")
	a.SetReadOnly(true, "test")
	a.gate = &liftGate{answer: true}
	if _, _, _, _, err := a.execCallInner(context.Background(), writeCall("f.txt")); err != nil {
		t.Fatal(err)
	}
	if c := a.CeilingState(); c.ReadOnly || !c.Auto {
		t.Errorf("after an approved lift = %+v, want the watcher still armed", c)
	}
}

// ceilingAllowGate answers from a session allowlist the way the real gates
// do after the operator has pressed 'a' once.
type ceilingAllowGate struct {
	always      map[string]bool
	prompts     []string
	mustPrompts []bool
	reasons     []string
}

func (g *ceilingAllowGate) Approve(name, detail, purpose, reason string, mustPrompt bool) (bool, bool, string) {
	if !mustPrompt && g.always[name] {
		return true, true, "" // answered without the operator
	}
	g.prompts = append(g.prompts, name)
	g.mustPrompts = append(g.mustPrompts, mustPrompt)
	g.reasons = append(g.reasons, reason)
	return true, false, ""
}
func (g *ceilingAllowGate) ApproveLift(name, detail, purpose, reason string) (bool, string) {
	return false, ""
}

// A call the ceiling cannot bound — an MCP tool, whose server runs
// outside every profile — is the operator's while the ceiling is in
// force. Before this, an allowlisted MCP write ran with no prompt at
// all while the banner said the session changed nothing (independent
// review, 2026-09-09).
func TestCeilingMakesUnboundedCallsTheOperatorsOwn(t *testing.T) {
	mcp := &tools.Tool{Name: "mcp__vault__patch", Description: "Patch a note.", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Run:        func(context.Context, map[string]any) (string, error) { return "PATCHED", nil }}
	call := llm.ToolCall{ID: "c", Name: "mcp__vault__patch", Args: map[string]any{"path": "n.md"}}

	// With the ceiling on, neither an allowlist entry nor a "never"
	// policy answers it.
	for _, tc := range []struct {
		name string
		pol  map[string]string
	}{
		{"session allowlist", nil},
		{"never policy", map[string]string{"mcp__vault__patch": "never"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ceilingAgent(t, "on", mcp)
			if tc.pol != nil {
				pol, _, err := policy.Build(tc.pol, nil, nil, false)
				if err != nil {
					t.Fatal(err)
				}
				a.policy = pol
			}
			gate := &ceilingAllowGate{always: map[string]bool{"mcp__vault__patch": true}}
			a.gate = gate
			if _, _, _, _, err := a.execCallInner(context.Background(), call); err != nil {
				t.Fatal(err)
			}
			if len(gate.prompts) != 1 {
				t.Fatalf("the operator was asked %d times, want 1: %v", len(gate.prompts), gate.prompts)
			}
			if !gate.mustPrompts[0] {
				t.Error("the question was answerable by the allowlist")
			}
			if !strings.Contains(gate.reasons[0], "read-only") {
				t.Errorf("the reason does not say why: %q", gate.reasons[0])
			}
		})
	}

	// With no ceiling, nothing changes: the allowlist answers as before.
	a := ceilingAgent(t, "off", mcp)
	gate := &ceilingAllowGate{always: map[string]bool{"mcp__vault__patch": true}}
	a.gate = gate
	if _, _, _, _, err := a.execCallInner(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if len(gate.prompts) != 0 {
		t.Errorf("the ceiling was off and the operator was asked anyway: %v", gate.prompts)
	}
}

// Every ceiling change leaves a record, whoever made it: the record is
// written by the setter, so a new caller cannot forget one. /readonly
// changed the ceiling with no record at all while the ADR said every
// change was recorded (independent review, 2026-09-09).
func TestCeilingChangesAreRecorded(t *testing.T) {
	a := ceilingAgent(t, "off")
	log := &capturingLog{}
	a.log = log

	a.SetReadOnly(true, "operator")
	a.SetReadOnlyAuto(true, "operator")
	a.SetReadOnly(true, "operator")  // no change, no record
	a.SetReadOnlyAuto(false, "auto") // a change

	var got []string
	for i, kind := range log.kinds {
		if kind != "mode_change" {
			continue
		}
		rec, ok := log.data[i].(map[string]any)
		if !ok {
			t.Fatalf("mode_change payload is %T", log.data[i])
		}
		got = append(got, rec["setting"].(string)+"="+rec["to"].(string)+" by "+rec["by"].(string))
	}
	want := []string{"read_only=on by operator", "read_only_auto=on by operator", "read_only_auto=off by auto"}
	if len(got) != len(want) {
		t.Fatalf("records = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Each refusal kind gets its own sentence. The shell one names the lane
// the call declared, memory names what a memory write costs, and the
// rest says "state", not "files" — web_search is mutating for its
// egress and changes no file (independent review, 2026-09-09).
func TestCeilingReasonNamesTheRightThing(t *testing.T) {
	egress := &tools.Tool{Name: "web_search", Description: "Search the web.", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Run:        func(context.Context, map[string]any) (string, error) { return "", nil }}
	mem := &tools.Tool{Name: "save_memory", Description: "Remember.", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Run:        func(context.Context, map[string]any) (string, error) { return "", nil }}
	a := ceilingAgent(t, "on", egress, mem)

	for _, tc := range []struct {
		call    llm.ToolCall
		want    string
		wantNot string
	}{
		{shellLane("touch x", "write"), "declared write", ""},
		{llm.ToolCall{ID: "c", Name: "save_memory", Args: map[string]any{}}, "later session", ""},
		{writeCall("f.txt"), "changes state", "changes files"},
		// The one the false sentence was written for.
		{llm.ToolCall{ID: "c", Name: "web_search", Args: map[string]any{"query": "x"}}, "changes state", "files"},
	} {
		d := a.decide(tc.call)
		if !d.OverCeiling {
			t.Fatalf("%s was not refused", tc.call.Name)
		}
		got := a.ceilingPrompt(d, tc.call)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: reason %q lacks %q", tc.call.Name, got, tc.want)
		}
		if tc.wantNot != "" && strings.Contains(got, tc.wantNot) {
			t.Errorf("%s: reason %q says %q, which is not true of it", tc.call.Name, got, tc.wantNot)
		}
	}
}

// Under --auto the model tier still judges an unbounded call — that is
// ADR-0080 §5, measured escalating an MCP write and passing an MCP read,
// and making these operator-only would have removed it and stopped every
// lookup in a read-only session. What the ceiling guarantees is narrower
// and is what the two reviews found missing: no standing shortcut
// answers one. When the ladder escalates, the operator is asked and an
// earlier 'a' does not answer for them.
func TestCeilingUnboundedUnderAuto(t *testing.T) {
	mcp := &tools.Tool{Name: "mcp__vault__patch", Description: "Patch a note.", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Run:        func(context.Context, map[string]any) (string, error) { return "PATCHED", nil }}
	call := llm.ToolCall{ID: "c", Name: "mcp__vault__patch", Args: map[string]any{}}

	for _, tc := range []struct {
		name       string
		verdict    string
		wantPrompt bool
	}{
		// The measured case: the mode is in the aligned round's payload
		// and the write is escalated, so the operator decides.
		{"the ladder escalates", `{"approve": false, "confidence": 0.95, "reason": "read-only"}`, true},
		// And when it approves, it has judged the call with the mode in
		// view. A judgment, not the kernel denial §3 gives.
		{"the ladder approves", `{"approve": true, "confidence": 0.99, "reason": "a read"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &autoBackend{responses: []*llm.Response{{Content: "done"}}, verdict: tc.verdict}
			a := ceilingAgent(t, "on", mcp)
			a.backend = b
			a.SetAutoApprove(true)
			gate := &ceilingAllowGate{always: map[string]bool{"mcp__vault__patch": true}}
			a.gate = gate
			if _, _, _, _, err := a.execCallInner(context.Background(), call); err != nil {
				t.Fatal(err)
			}
			if got := len(gate.prompts) == 1; got != tc.wantPrompt {
				t.Fatalf("operator asked = %v, want %v (prompts=%v)", got, tc.wantPrompt, gate.prompts)
			}
			if tc.wantPrompt && !gate.mustPrompts[0] {
				t.Error("an earlier 'a' could have answered it")
			}
		})
	}
}
