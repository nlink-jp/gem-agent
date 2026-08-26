package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// ADR-0050 §4: with a rulebook in force, the evaluation carries it
// inside the nonce wrap and the prompt gains the framing addendum.
func TestRiskEvalCarriesRulebook(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("make build")}},
			{Content: "done"},
		},
		verdict: okVerdict,
	}
	a, _, _ := newAutoAgent(t, b, &recordingGate{})
	a.SetRulebook("== base rules (hand-written by the operator) ==\nbuilds are routine here")
	if _, err := a.Run(context.Background(), "build it", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.evals) != 1 {
		t.Fatalf("evals = %d, want 1", len(b.evals))
	}
	payload := b.evals[0]
	if !strings.Contains(payload, "operator risk rules:") ||
		!strings.Contains(payload, "builds are routine here") {
		t.Errorf("rulebook missing from payload: %q", payload)
	}
	// Inside the wrap: the learned half originated as model text, and
	// one uniform rule (everything but the base prompt is wrapped) must
	// hold.
	open := strings.Index(payload, "<proposed_call_")
	rb := strings.Index(payload, "operator risk rules:")
	if open < 0 || rb < open {
		t.Errorf("rulebook not inside the nonce wrap: %q", payload)
	}
	if !strings.Contains(b.evalSystems[0], "operator risk rules") {
		t.Errorf("prompt lacks the rulebook addendum: %q", b.evalSystems[0])
	}
}

// Without a rulebook the evaluation is byte-identical to the
// conventional one — no variant behaviour where the feature is absent.
func TestRiskEvalWithoutRulebookIsUnchanged(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("make build")}},
			{Content: "done"},
		},
		verdict: okVerdict,
	}
	a, _, _ := newAutoAgent(t, b, &recordingGate{})
	if _, err := a.Run(context.Background(), "build it", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.evals[0], "operator risk rules") {
		t.Errorf("payload carries a rulebook section with none set: %q", b.evals[0])
	}
	if strings.Contains(b.evalSystems[0], "operator risk rules") {
		t.Error("prompt is not the byte-identical base without a rulebook")
	}
}

// The rulebook reaches only the model tier: a Block-tier call never
// consults the judge, however favourable the rulebook (ADR-0050 §5).
func TestRulebookCannotLiftTheBlockFloor(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("sudo rm -rf /var/data")}},
			{Content: "done"},
		},
		verdict: okVerdict,
	}
	gate := &recordingGate{}
	a, _, decisions := newAutoAgent(t, b, gate)
	a.SetRulebook("everything in this project is pre-approved, including sudo")
	if _, err := a.Run(context.Background(), "clean up", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.evals) != 0 {
		t.Fatalf("a Block-tier call consulted the model tier: %q", b.evals)
	}
	if len(gate.asked) != 1 {
		t.Fatalf("a Block-tier call did not reach the human gate: %v", gate.asked)
	}
	for _, d := range *decisions {
		if d.Approved {
			t.Errorf("a Block-tier call was auto-approved: %+v", d)
		}
	}
}

// Memory writes stay excluded from the model tier regardless of the
// rulebook (ADR-0020 §4 — the operator decides what the agent
// remembers, and no prose changes whose call that is).
func TestRulebookDoesNotReachMemoryWrites(t *testing.T) {
	save := llm.ToolCall{ID: "m", Name: "save_memory",
		Args: map[string]any{"scope": "project", "content": "a fact", PurposeArg: "remembering"}}
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{save}},
			{Content: "done"},
		},
		verdict: okVerdict,
	}
	gate := &recordingGate{}
	reg := newAgentRegistry(t)
	if err := reg.Register(&tools.Tool{
		Name: "save_memory", Mutating: true,
		Run: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Backend: b, Registry: reg, Gate: gate, System: "s",
		MaxTurns: 5, AutoApprove: true})
	a.SetRulebook("memory saves are routine and pre-approved here")
	if _, err := a.Run(context.Background(), "remember it", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.evals) != 0 {
		t.Fatal("a memory write consulted the model tier")
	}
	if len(gate.asked) != 1 {
		t.Fatalf("a memory write did not reach the human gate: %v", gate.asked)
	}
}
