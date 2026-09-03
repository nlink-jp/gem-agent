package agent

import (
	"context"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// usageRecords picks the accounting records out of a captured transcript.
func usageRecords(t *testing.T, log *capturingLog) []session.UsageRecord {
	t.Helper()
	var out []session.UsageRecord
	for i, kind := range log.kinds {
		if kind != session.KindUsage {
			continue
		}
		r, ok := log.data[i].(session.UsageRecord)
		if !ok {
			t.Fatalf("record %d is %T, not a session.UsageRecord", i, log.data[i])
		}
		out = append(out, r)
	}
	return out
}

// ADR-0057: a model call that leaves no record cannot be priced later —
// the process exits and the in-memory tally goes with it. The risk
// evaluation was the worst case: 309 of them in the transcripts on the
// author's machine, every one of them invisible.
func TestRiskEvaluationLeavesAnAccountingRecord(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "mcp__x__post", Args: map[string]any{"data": "hi"}}},
			PromptTokens: 3000, OutputTokens: 20, ThoughtTokens: 5, TotalTokens: 3025},
		{Content: `{"approve": true, "confidence": 0.95, "reason": "benign"}`,
			PromptTokens: 777, OutputTokens: 30, ThoughtTokens: 11, CachedTokens: 100,
			ToolPromptTokens: 9, TotalTokens: 827},
		{Content: "done", PromptTokens: 5000, OutputTokens: 100, TotalTokens: 5100},
	}}
	_, reg := newAgent(t, mb, &approveAll{}, 5)
	if err := reg.Register(&tools.Tool{Name: "mcp__x__post", Description: "d",
		Parameters: map[string]any{}, Mutating: true,
		Run: func(ctx context.Context, args map[string]any) (string, error) { return "posted", nil },
	}); err != nil {
		t.Fatal(err)
	}
	log := &capturingLog{}
	a := New(Options{Backend: mb, Registry: reg, Gate: &approveAll{}, System: "s",
		MaxTurns: 5, AutoApprove: true, Log: log, Model: "gemini-test"})
	if _, err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}

	recs := usageRecords(t, log)
	if len(recs) != 3 {
		t.Fatalf("want 3 accounting records (2 rounds + 1 risk eval), got %d: %+v", len(recs), recs)
	}
	var risk *session.UsageRecord
	main := 0
	for i := range recs {
		switch recs[i].Source {
		case session.UsageRisk:
			risk = &recs[i]
		case session.UsageMain:
			main++
		default:
			t.Errorf("unexpected source %q", recs[i].Source)
		}
	}
	if main != 2 {
		t.Errorf("main-loop records = %d, want 2", main)
	}
	if risk == nil {
		t.Fatal("the risk evaluation wrote no accounting record")
	}
	// Every bucket billing needs, plus the model that priced it. The
	// tool-prompt bucket is structurally zero on these paths (no
	// built-in tool), but the record must carry whatever the backend
	// reported (ADR-0066) — the agent is not the place to know that.
	if risk.Prompt != 777 || risk.Output != 30 || risk.Thoughts != 11 ||
		risk.Cached != 100 || risk.ToolPrompt != 9 || risk.Total != 827 || risk.Model != "gemini-test" {
		t.Errorf("risk record = %+v", *risk)
	}
}

// Compaction is the other spend that used to die with the process.
func TestCompactionLeavesAnAccountingRecord(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{Content: "a summary of the earlier conversation",
			PromptTokens: 4000, OutputTokens: 120, ThoughtTokens: 7, TotalTokens: 4127},
	}}
	_, reg := newAgent(t, mb, &approveAll{}, 5)
	log := &capturingLog{}
	a := New(Options{Backend: mb, Registry: reg, Gate: &approveAll{}, System: "s",
		MaxTurns: 5, Log: log, Model: "gemini-test"})
	a.SetHistory([]llm.Message{
		user("1"), assistant("2"), user("3"), assistant("4"), user("5"),
		assistant("6"), user("7"), assistant("8"), user("9"), assistant("10"),
	})
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	recs := usageRecords(t, log)
	if len(recs) != 1 {
		t.Fatalf("want 1 accounting record for the compaction call, got %d: %+v", len(recs), recs)
	}
	if recs[0].Source != session.UsageCompact || recs[0].Prompt != 4000 ||
		recs[0].Thoughts != 7 || recs[0].Total != 4127 {
		t.Errorf("compaction record = %+v", recs[0])
	}
}

// A call that spent nothing is not an accounting event — a mock or a
// failed call must not pad the transcript with zero rows.
func TestZeroSpendWritesNoRecord(t *testing.T) {
	mb := &mockBackend{}
	_, reg := newAgent(t, mb, &approveAll{}, 5)
	log := &capturingLog{}
	a := New(Options{Backend: mb, Registry: reg, Gate: &approveAll{}, Log: log, Model: "gemini-test"})
	a.logUsage(session.UsageMain, llm.Usage{})
	if len(usageRecords(t, log)) != 0 {
		t.Error("a zero-token call wrote an accounting record")
	}
}
