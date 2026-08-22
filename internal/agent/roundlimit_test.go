package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// rlBackend answers tool rounds from a script (repeating the last
// response) and progress-review side-calls from a fixed verdict.
type rlBackend struct {
	responses []*llm.Response
	verdict   string
	calls     int // tool rounds only
	evals     []string
}

func (b *rlBackend) ChatStream(ctx context.Context, system string, msgs []llm.Message, defs []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	if len(defs) == 0 && strings.Contains(system, "review ONE running turn") {
		if len(msgs) > 0 {
			b.evals = append(b.evals, msgs[0].Content)
		}
		return &llm.Response{Content: b.verdict}, nil
	}
	i := b.calls
	if i >= len(b.responses) {
		i = len(b.responses) - 1
	}
	b.calls++
	return b.responses[i], nil
}

func listCall(path string) llm.ToolCall {
	return llm.ToolCall{ID: "c", Name: "list_files", Args: map[string]any{"path": path}}
}

// loopRounds builds n tool-call rounds with DISTINCT args (so the loop
// detector stays quiet) followed by a final text answer.
func loopRounds(n int) []*llm.Response {
	var out []*llm.Response
	for i := 0; i < n; i++ {
		out = append(out, &llm.Response{ToolCalls: []llm.ToolCall{listCall(fmt.Sprintf("d%d", i))}})
	}
	return append(out, &llm.Response{Content: "done"})
}

func newRLAgent(t *testing.T, b llm.Backend, maxTurns int, onLimit func(context.Context, RoundLimitInfo) bool) *Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Backend: b, Registry: reg, Gate: &recordingGate{}, System: "sys",
		MaxTurns: maxTurns, RoundReview: true, OnRoundLimit: onLimit})
}

const progressingVerdict = `{"progressing": true, "confidence": 0.9, "reason": "writing distinct sections"}`
const stuckVerdict = `{"progressing": false, "confidence": 0.9, "reason": "no visible change"}`

// ADR-0040 §2, non-interactive: a confident "progressing" extends past
// the limit; the reviewer sees the instruction and the activity trace.
func TestRoundLimitExtendsOnProgress(t *testing.T) {
	b := &rlBackend{responses: loopRounds(3), verdict: progressingVerdict}
	a := newRLAgent(t, b, 2, nil)
	out, err := a.Run(context.Background(), "調査して", nil)
	if err != nil || out != "done" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if len(b.evals) == 0 {
		t.Fatal("progress review never ran")
	}
	if !strings.Contains(b.evals[0], "operator instruction (this turn): 調査して") ||
		!strings.Contains(b.evals[0], "list_files") ||
		!strings.Contains(b.evals[0], "<turn_activity_") {
		t.Errorf("review evidence = %q", b.evals[0])
	}
}

// Fail-closed: a stuck verdict stops the turn with a message that
// teaches recovery and never recommends /clear (ADR-0040 §4).
func TestRoundLimitStopsFailClosed(t *testing.T) {
	b := &rlBackend{responses: loopRounds(9), verdict: stuckVerdict}
	a := newRLAgent(t, b, 2, nil)
	_, err := a.Run(context.Background(), "q", nil)
	if err == nil || !strings.Contains(err.Error(), "round limit") ||
		!strings.Contains(err.Error(), "continue") || strings.Contains(err.Error(), "/clear") {
		t.Fatalf("err = %v", err)
	}
	if b.calls != 2 {
		t.Errorf("backend tool rounds = %d, want 2 (no extension)", b.calls)
	}
}

// §3: the absolute cap is a ceiling no verdict can lift.
func TestRoundCapIsCeiling(t *testing.T) {
	b := &rlBackend{responses: loopRounds(50), verdict: progressingVerdict}
	a := newRLAgent(t, b, 2, nil)
	_, err := a.Run(context.Background(), "q", nil)
	if err == nil || !strings.Contains(err.Error(), "absolute round cap") {
		t.Fatalf("err = %v", err)
	}
	if cap := 2 * roundCapMultiplier; b.calls != cap {
		t.Errorf("backend tool rounds = %d, want the cap %d", b.calls, cap)
	}
}

// §2, interactive: auto OFF always asks (verdict rides as evidence);
// the operator's answer decides.
func TestRoundLimitOperatorDecides(t *testing.T) {
	var infos []RoundLimitInfo
	decide := true
	onLimit := func(_ context.Context, info RoundLimitInfo) bool {
		infos = append(infos, info)
		return decide
	}
	b := &rlBackend{responses: loopRounds(3), verdict: progressingVerdict}
	a := newRLAgent(t, b, 2, onLimit)
	if out, err := a.Run(context.Background(), "q", nil); err != nil || out != "done" {
		t.Fatalf("continue path: out=%q err=%v", out, err)
	}
	if len(infos) == 0 || infos[0].Trigger != "round-limit" || !infos[0].Progressing {
		t.Fatalf("infos = %+v", infos)
	}

	decide = false
	b2 := &rlBackend{responses: loopRounds(9), verdict: progressingVerdict}
	a2 := newRLAgent(t, b2, 2, onLimit)
	if _, err := a2.Run(context.Background(), "q", nil); err == nil ||
		!strings.Contains(err.Error(), "stopped at the round limit") {
		t.Fatalf("stop path: %v", err)
	}
}

// §2, interactive + auto ON: a confident "progressing" continues with
// a notice, never a dialog — auto exists to reduce interruptions.
func TestRoundLimitAutoContinues(t *testing.T) {
	called := false
	onLimit := func(context.Context, RoundLimitInfo) bool { called = true; return false }
	b := &rlBackend{responses: loopRounds(3), verdict: progressingVerdict}
	a := newRLAgent(t, b, 2, onLimit)
	a.SetAutoApprove(true)
	var notices []string
	a.onNotice = func(msg string) { notices = append(notices, msg) }
	if out, err := a.Run(context.Background(), "q", nil); err != nil || out != "done" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if called {
		t.Error("auto mode with a confident verdict must not open the dialog")
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "progress review") {
		t.Errorf("auto-continue notice missing: %v", notices)
	}
}

// §1: three identical calls escalate immediately; a "continue" blesses
// the signature (polling asks once), a "stop" ends the turn with every
// pending call still answered — no orphaned function calls.
func TestLoopDetector(t *testing.T) {
	same := &llm.Response{ToolCalls: []llm.ToolCall{listCall("poll")}}
	var triggers []string
	cont := true
	onLimit := func(_ context.Context, info RoundLimitInfo) bool {
		triggers = append(triggers, info.Trigger+":"+info.Detail)
		return cont
	}
	b := &rlBackend{responses: []*llm.Response{same, same, same, same, same, {Content: "done"}},
		verdict: progressingVerdict}
	a := newRLAgent(t, b, 20, onLimit)
	if out, err := a.Run(context.Background(), "poll the job", nil); err != nil || out != "done" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if len(triggers) != 1 || !strings.HasPrefix(triggers[0], "loop:") {
		t.Fatalf("triggers = %v (want exactly one loop escalation, then whitelisted)", triggers)
	}

	cont = false
	b2 := &rlBackend{responses: []*llm.Response{same, same, same, same, {Content: "done"}},
		verdict: progressingVerdict}
	a2 := newRLAgent(t, b2, 20, onLimit)
	_, err := a2.Run(context.Background(), "q", nil)
	if err == nil || !strings.Contains(err.Error(), "loop guard") {
		t.Fatalf("err = %v", err)
	}
	last := a2.history[len(a2.history)-1]
	if last.Role != llm.RoleTool || !strings.Contains(last.Content, "not executed") {
		t.Errorf("pending call not answered: %+v", last)
	}
}
