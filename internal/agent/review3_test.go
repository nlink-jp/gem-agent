package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/telemetry"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// Review round 3 regressions.

// NoMentions: a model-authored input must not trigger the @ grammar's
// out-of-project grants (the agentic_file_search child).
func TestNoMentionsSkipsExpansion(t *testing.T) {
	for _, noMentions := range []bool{false, true} {
		reg, err := tools.New(t.TempDir(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "README.md"), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		b := backendFunc(func(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolDef, _ func(string)) (*llm.Response, error) {
			return &llm.Response{Content: "ok"}, nil
		})
		a := New(Options{Backend: b, Registry: reg, Gate: &recordingGate{}, System: "s", MaxTurns: 3, NoMentions: noMentions})
		if _, err := a.Run(context.Background(), "look at @README.md please", nil); err != nil {
			t.Fatal(err)
		}
		// The stored history is the truth (the wire copy folds text
		// attachments into wrapped content).
		got := len(a.history[0].Attachments)
		if noMentions && got != 0 {
			t.Errorf("NoMentions: %d attachments expanded from a model-authored input", got)
		}
		if !noMentions && got == 0 {
			t.Error("operator path: @README.md was not attached (test setup wrong)")
		}
	}
}

// A cancelled turn asks nothing at a checkpoint — no review, no dialog.
func TestInterventionSkipsWhenCancelled(t *testing.T) {
	called := false
	b := &rlBackend{responses: loopRounds(9), verdict: progressingVerdict}
	a := newRLAgent(t, b, 2, func(context.Context, RoundLimitInfo) bool { called = true; return true })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.Run(ctx, "q", nil)
	if err == nil {
		t.Fatal("expected an error on a cancelled turn")
	}
	if called {
		t.Error("dialog opened on behalf of a cancelled turn")
	}
	if len(b.evals) != 0 {
		t.Error("progress review ran on a cancelled turn")
	}
}

// The loop trigger's evidence shows the repetition it escalates, and
// the skipped calls are audited as such; OnToolDone fires per call.
func TestLoopEvidenceAuditAndToolDone(t *testing.T) {
	same := &llm.Response{ToolCalls: []llm.ToolCall{listCall("poll")}}
	b := &rlBackend{responses: []*llm.Response{same, same, same, same, {Content: "done"}}, verdict: progressingVerdict}
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	sink, rec := telemetry.NewRecording()
	done := 0
	a := New(Options{Backend: b, Registry: reg, Gate: &recordingGate{}, System: "s", MaxTurns: 20,
		RoundReview: true, Telemetry: sink,
		OnRoundLimit: func(context.Context, RoundLimitInfo) bool { return false },
		OnToolDone:   func(llm.ToolCall) { done++ },
	})
	_, err = a.Run(context.Background(), "poll", nil)
	if err == nil || !strings.Contains(err.Error(), "loop guard") {
		t.Fatalf("err = %v", err)
	}
	if len(b.evals) != 1 || strings.Count(b.evals[0], "list_files") < 3 {
		t.Errorf("loop evidence should include the triggering call (3 list_files): %q", b.evals)
	}
	skipped := 0
	for _, ev := range rec.Events() {
		if ev.Name == "tool.call" && ev.Attrs["outcome"] == "skipped" {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("skipped calls audited = %d, want 1", skipped)
	}
	// 2 executed + 1 skipped = 3 calls proposed; OnToolDone fires for each.
	if done != 3 {
		t.Errorf("OnToolDone fired %d times, want 3", done)
	}
}

// Non-interactive continue is announced, never silent.
func TestNonInteractiveContinueNotifies(t *testing.T) {
	b := &rlBackend{responses: loopRounds(3), verdict: progressingVerdict}
	a := newRLAgent(t, b, 2, nil)
	var notices []string
	a.onNotice = func(s string) { notices = append(notices, s) }
	if _, err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatal(err)
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "progress review says progressing") {
		t.Errorf("silent extension: notices = %v", notices)
	}
}
