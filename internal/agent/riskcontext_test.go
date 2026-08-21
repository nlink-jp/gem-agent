package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

const okVerdict = `{"approve": true, "confidence": 0.95, "reason": "ok"}`

// ADR-0038 §1–2: an early-round risk evaluation carries the operator's
// typed request inside the nonce wrap, and the prompt gains the
// alignment addendum.
func TestRiskEvalCarriesInstructionEarly(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("make build")}},
			{Content: "done"},
		},
		verdict: okVerdict,
	}
	a, _, _ := newAutoAgent(t, b, &recordingGate{})
	if _, err := a.Run(context.Background(), "README の誤字を直して", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.evals) != 1 {
		t.Fatalf("evals = %d, want 1", len(b.evals))
	}
	payload := b.evals[0]
	if !strings.Contains(payload, "operator instruction (this turn): README の誤字を直して") {
		t.Errorf("instruction missing from payload: %q", payload)
	}
	// Inside the wrap: the instruction appears after the opening tag —
	// pasted text in it must reach the reviewer as data, not directives.
	open := strings.Index(payload, "<proposed_call_")
	instr := strings.Index(payload, "operator instruction")
	if open < 0 || instr < open {
		t.Errorf("instruction not inside the nonce wrap: %q", payload)
	}
	if !strings.Contains(b.evalSystems[0], "operator instruction (this turn)") {
		t.Errorf("prompt lacks the alignment addendum: %q", b.evalSystems[0])
	}
}

// ADR-0038 §3: from round riskContextRounds on, the evaluation is the
// conventional one — no instruction in the payload, base prompt
// byte-identical.
func TestRiskEvalFallsBackOnLateRounds(t *testing.T) {
	list := llm.ToolCall{ID: "r", Name: "list_files", Args: map[string]any{}}
	b := &autoBackend{
		responses: []*llm.Response{
			// Rounds 0–2: read-only calls that never reach the gate or
			// the model tier; round 3: the evaluated mutating call.
			{ToolCalls: []llm.ToolCall{list}},
			{ToolCalls: []llm.ToolCall{list}},
			{ToolCalls: []llm.ToolCall{list}},
			{ToolCalls: []llm.ToolCall{shellCall("make build")}},
			{Content: "done"},
		},
		verdict: okVerdict,
	}
	a, _, _ := newAutoAgent(t, b, &recordingGate{})
	if _, err := a.Run(context.Background(), "ビルドして", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.evals) != 1 {
		t.Fatalf("evals = %d, want 1", len(b.evals))
	}
	if strings.Contains(b.evals[0], "operator instruction") {
		t.Errorf("round %d payload still carries the instruction: %q", riskContextRounds, b.evals[0])
	}
	if strings.Contains(b.evalSystems[0], "operator instruction (this turn)") {
		t.Error("late-round prompt is not the byte-identical base prompt")
	}
}

// An oversized instruction is clipped with the truncation named — the
// risk call must stay cheap and the clip must not masquerade as the
// whole request.
func TestRiskEvalClipsInstruction(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("make build")}},
			{Content: "done"},
		},
		verdict: okVerdict,
	}
	a, _, _ := newAutoAgent(t, b, &recordingGate{})
	long := strings.Repeat("あ", riskInstructionCap+500)
	if _, err := a.Run(context.Background(), long, nil); err != nil {
		t.Fatal(err)
	}
	payload := b.evals[0]
	if strings.Contains(payload, long) {
		t.Error("instruction not clipped")
	}
	if !strings.Contains(payload, "[clipped]") {
		t.Error("clip not disclosed")
	}
}

func TestClipRunes(t *testing.T) {
	if got := clipRunes("あいう", 3); got != "あいう" {
		t.Errorf("under cap: %q", got)
	}
	got := clipRunes("あいうえ", 3)
	if !strings.HasPrefix(got, "あいう") || !strings.Contains(got, "[clipped]") {
		t.Errorf("over cap: %q", got)
	}
}
