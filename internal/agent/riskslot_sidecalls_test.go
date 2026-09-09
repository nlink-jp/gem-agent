package agent

import (
	"context"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
)

// ADR-0082 §1: the progress review and the read-only watcher are model
// tier and run on the slot; compaction is not and stays on the main
// backend. Each side call is driven directly so the routing is asserted
// without the surrounding state machinery.
func TestSideCallsSplitBetweenSlotAndMain(t *testing.T) {
	main := &mockBackend{responses: []*llm.Response{
		{Content: "a summary", PromptTokens: 50, OutputTokens: 5, TotalTokens: 55},
	}}
	judge := &mockBackend{responses: []*llm.Response{
		{Content: `{"progressing": true, "confidence": 0.9, "reason": "moving"}`,
			PromptTokens: 300, OutputTokens: 20, TotalTokens: 320},
		{Content: `{"read_only": false, "quote": ""}`,
			PromptTokens: 200, OutputTokens: 10, TotalTokens: 210},
	}}
	log := &capturingLog{}
	a := New(Options{Backend: main, RiskBackend: judge, Registry: newAgentRegistry(t),
		Gate: &approveAll{}, System: "s", MaxTurns: 5, Log: log,
		Model: "main-model", RiskModel: "judge-model"})
	ctx := context.Background()

	a.turnInput = "do the thing"
	if v, err := a.evaluateProgress(ctx); err != nil || !v.Progressing {
		t.Fatalf("progress review: %+v %v", v, err)
	}
	if _, err := a.evaluateCeiling(ctx, "only read things"); err != nil {
		t.Fatalf("ceiling watcher: %v", err)
	}
	if s, err := a.summarize(ctx, []llm.Message{{Role: llm.RoleUser, Content: "hello"}}); err != nil || s != "a summary" {
		t.Fatalf("compaction summary: %q %v", s, err)
	}

	if len(judge.calls) != 2 {
		t.Errorf("slot backend answered %d calls, want 2 (progress review + watcher)", len(judge.calls))
	}
	if len(main.calls) != 1 {
		t.Errorf("main backend answered %d calls, want 1 (compaction)", len(main.calls))
	}
	for _, r := range usageRecords(t, log) {
		switch r.Source {
		case session.UsageProgress, session.UsageRisk:
			if r.Model != "judge-model" {
				t.Errorf("%s record bills %q, want judge-model", r.Source, r.Model)
			}
		case session.UsageCompact:
			if r.Model != "main-model" {
				t.Errorf("compaction record bills %q, want main-model", r.Model)
			}
		}
	}
}
