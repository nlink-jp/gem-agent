package agent

import (
	"context"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// riskSlotRun drives one auto-approved MCP call — a Review-tier call
// that reaches the model tier (both ADR-0081 rounds) — with the main
// loop and the tier on separate mock backends, and returns the log.
func riskSlotRun(t *testing.T, riskBackend llm.Backend, riskModel string) (*mockBackend, *capturingLog) {
	t.Helper()
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "mcp__x__post", Args: map[string]any{"data": "hi"}}},
			PromptTokens: 100, OutputTokens: 10, TotalTokens: 110},
		{Content: "done", PromptTokens: 120, OutputTokens: 5, TotalTokens: 125},
	}}
	_, reg := newAgent(t, mb, &approveAll{}, 5)
	if err := reg.Register(&tools.Tool{Name: "mcp__x__post", Description: "d",
		Parameters: map[string]any{}, Mutating: true,
		Run: func(ctx context.Context, args map[string]any) (string, error) { return "posted", nil },
	}); err != nil {
		t.Fatal(err)
	}
	log := &capturingLog{}
	a := New(Options{Backend: mb, RiskBackend: riskBackend, Registry: reg, Gate: &approveAll{},
		System: "s", MaxTurns: 5, AutoApprove: true, Log: log,
		Model: "main-model", RiskModel: riskModel})
	if _, err := a.Run(context.Background(), "post it", nil); err != nil {
		t.Fatal(err)
	}
	return mb, log
}

func verdictResponses() []*llm.Response {
	v := `{"approve": true, "confidence": 0.95, "reason": "benign"}`
	return []*llm.Response{
		{Content: v, PromptTokens: 700, OutputTokens: 30, ThoughtTokens: 40, TotalTokens: 770},
		{Content: v, PromptTokens: 700, OutputTokens: 30, ThoughtTokens: 40, TotalTokens: 770},
	}
}

func evaluatorModels(t *testing.T, log *capturingLog) []string {
	t.Helper()
	var out []string
	for i, kind := range log.kinds {
		if kind != "auto_decision" {
			continue
		}
		rec, ok := log.data[i].(map[string]any)
		if !ok {
			t.Fatalf("auto_decision payload %d is %T", i, log.data[i])
		}
		out = append(out, rec["evaluator_model"].(string))
	}
	return out
}

// ADR-0082 §1, §4: with a slot named, the model tier's calls go to the
// slot backend and bill against the slot model — while the main loop
// stays on the main backend under the main name.
func TestModelTierRunsOnTheRiskSlot(t *testing.T) {
	judge := &mockBackend{responses: verdictResponses()}
	mb, log := riskSlotRun(t, judge, "judge-model")

	if len(judge.calls) != 2 {
		t.Fatalf("slot backend answered %d calls, want the 2 evaluation rounds", len(judge.calls))
	}
	if len(mb.calls) != 2 {
		t.Errorf("main backend answered %d calls, want the 2 conversation rounds", len(mb.calls))
	}
	for _, r := range usageRecords(t, log) {
		switch r.Source {
		case session.UsageRisk:
			if r.Model != "judge-model" {
				t.Errorf("risk record bills %q, want judge-model", r.Model)
			}
		case session.UsageMain:
			if r.Model != "main-model" {
				t.Errorf("main record bills %q, want main-model", r.Model)
			}
		}
	}
	if got := evaluatorModels(t, log); len(got) != 1 || got[0] != "judge-model" {
		t.Errorf("evaluator_model = %v, want [judge-model]", got)
	}
}

// ADR-0082 §2, first row: nothing named, nothing changes — the tier
// rides the main backend and bills against the main model, as before.
func TestModelTierRidesMainBackendWithoutASlot(t *testing.T) {
	// The main mock now has to answer the evaluation rounds too, in
	// the order the agent makes them: round 1, two verdicts, round 2.
	mb := &mockBackend{}
	mb.responses = append([]*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "mcp__x__post", Args: map[string]any{"data": "hi"}}},
			PromptTokens: 100, OutputTokens: 10, TotalTokens: 110}},
		verdictResponses()...)
	mb.responses = append(mb.responses, &llm.Response{Content: "done", PromptTokens: 120, OutputTokens: 5, TotalTokens: 125})
	_, reg := newAgent(t, mb, &approveAll{}, 5)
	if err := reg.Register(&tools.Tool{Name: "mcp__x__post", Description: "d",
		Parameters: map[string]any{}, Mutating: true,
		Run: func(ctx context.Context, args map[string]any) (string, error) { return "posted", nil },
	}); err != nil {
		t.Fatal(err)
	}
	log := &capturingLog{}
	a := New(Options{Backend: mb, Registry: reg, Gate: &approveAll{}, System: "s",
		MaxTurns: 5, AutoApprove: true, Log: log, Model: "main-model"})
	if _, err := a.Run(context.Background(), "post it", nil); err != nil {
		t.Fatal(err)
	}
	if len(mb.calls) != 4 {
		t.Errorf("main backend answered %d calls, want 4 (2 rounds + 2 evaluations)", len(mb.calls))
	}
	for _, r := range usageRecords(t, log) {
		if r.Model != "main-model" {
			t.Errorf("%s record bills %q, want main-model", r.Source, r.Model)
		}
	}
	if got := evaluatorModels(t, log); len(got) != 1 || got[0] != "main-model" {
		t.Errorf("evaluator_model = %v, want [main-model]", got)
	}
}
