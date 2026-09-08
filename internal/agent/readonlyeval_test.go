package agent

// The session mode reaches the risk evaluator (ADR-0080 §5). This is
// what covers MCP: a call the ceiling refuses never reaches the model
// tier at all, so the calls that see this line are exactly the ones §3
// cannot bound — the ones on someone else's server.

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

func readOnlyEvalAgent(t *testing.T, b *autoBackend, state string) *Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/bash", "-c", c) },
		5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = reg.Register(&tools.Tool{Name: "mcp__srv__patch", Description: "Patch a remote note.",
		Mutating: true, Parameters: map[string]any{"type": "object"},
		Run: func(context.Context, map[string]any) (string, error) { return "patched", nil }})
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Backend: b, Registry: reg, Gate: &recordingGate{}, System: "s",
		MaxTurns: 5, AutoApprove: true, ReadOnly: state})
}

func TestReadOnlyReachesTheEvaluator(t *testing.T) {
	mcpCall := llm.ToolCall{ID: "c", Name: "mcp__srv__patch", Args: map[string]any{"path": "n.md"}}
	for _, tc := range []struct {
		state string
		want  bool
	}{
		{sandbox.CeilingOn, true},
		{sandbox.CeilingOff, false},
		// Auto has not tightened, so the session is not read-only and
		// the evaluator must not be told that it is.
		{sandbox.CeilingAuto, false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			b := &autoBackend{
				responses: []*llm.Response{
					{ToolCalls: []llm.ToolCall{mcpCall}},
					{Content: "done"},
				},
				verdict: okVerdict,
			}
			a := readOnlyEvalAgent(t, b, tc.state)
			if _, err := a.Run(context.Background(), "ノートを整理して", nil); err != nil {
				t.Fatal(err)
			}
			payload, system := alignedEval(t, b)
			gotPayload := strings.Contains(payload, "session mode: read-only")
			gotPrompt := strings.Contains(system, `"session mode" line`)
			// The baseline never sees it: that is what makes the
			// context subtractive (ADR-0081 §1).
			if base, _ := baselineEval(t, b); strings.Contains(base, "session mode") {
				t.Error("the baseline round was told the session mode")
			}
			if gotPayload != tc.want || gotPrompt != tc.want {
				t.Errorf("state %q: payload=%v prompt=%v, want %v", tc.state, gotPayload, gotPrompt, tc.want)
			}
		})
	}
}

// The line states intent, not enforcement. State-shaped wording made the
// model call an MCP write "not permitted" — false, since nothing
// prevents it and the operator may still approve — and the reason is
// shown to the operator (measured 2026-09-09).
func TestReadOnlyEvidenceStatesIntentNotEnforcement(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{{ID: "c", Name: "mcp__srv__patch", Args: map[string]any{}}}},
			{Content: "done"},
		},
		verdict: okVerdict,
	}
	a := readOnlyEvalAgent(t, b, sandbox.CeilingOn)
	if _, err := a.Run(context.Background(), "整理して", nil); err != nil {
		t.Fatal(err)
	}
	payload, _ := alignedEval(t, b)
	if !strings.Contains(payload, "the operator has asked") {
		t.Errorf("the line does not state intent: %q", readOnlyEvidence)
	}
	for _, forbidden := range []string{"sandbox", "denied", "not permitted", "enforced", "blocked"} {
		if strings.Contains(strings.ToLower(readOnlyEvidence), forbidden) {
			t.Errorf("the line describes enforcement (%q): %q", forbidden, readOnlyEvidence)
		}
	}
	// It rides inside the isolation wrap with the rest of the evidence,
	// not in the system prompt where it would read as an instruction.
	if _, system := alignedEval(t, b); strings.Contains(system, "the operator has asked") {
		t.Error("the session-mode evidence leaked into the system prompt")
	}
}
