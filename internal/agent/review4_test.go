package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/telemetry"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// Review round 4: a view_image / read_document call refused by a
// pre-tool hook must not attach the bytes anyway. The round-2 fix
// keyed the attachment guard on the gate's denial flag; a hook deny
// is a floor that runs BEFORE the gate, and its result text carries
// neither the "error:" prefix nor the flag — so the pixels rode along
// with the refusal.
func TestHookDeniedViewImageAttachesNothing(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "view_image", Args: map[string]any{"path": "shot.png"}}}},
		{Content: "done"},
	}}
	a, reg := hookAgent(t, mb, &approveAll{}, func(_ context.Context, name string, _ map[string]any) (bool, string) {
		return name == "view_image", "the org guard said no"
	})
	if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "shot.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "look", nil); err != nil {
		t.Fatal(err)
	}
	if len(mb.calls) < 2 {
		t.Fatal("no second round")
	}
	for _, m := range mb.calls[1] {
		if m.Role == llm.RoleTool {
			if len(m.Attachments) > 0 {
				t.Fatalf("hook-denied view_image still attached bytes: %d attachment(s)", len(m.Attachments))
			}
			if !strings.Contains(m.Content, "denied by a pre-tool hook") {
				t.Errorf("denial not recorded in the tool result: %q", m.Content)
			}
		}
	}
}

// Review round 4: Restart (ADR-0071 /clear) hands the agent a fresh
// transcript. A transcript that died in the previous session (a
// conversation write failed, ADR-0021) must not keep the NEW one
// dead: the operator was told "recording stopped" once, about a file
// that no longer receives anything; the new file is a different file.
func TestRestartRevivesADeadTranscript(t *testing.T) {
	old := &recordingLog{fail: true}
	a := newLogAgent(t, old, func(string) {})
	a.AddContext("first") // conversation write fails → transcript dead
	if !a.logDead {
		t.Fatal("precondition: the old transcript should be dead")
	}
	fresh := &recordingLog{}
	a.Restart(fresh)
	a.AddContext("second")
	if len(fresh.kinds) != 1 || fresh.kinds[0] != session.KindMessage {
		t.Fatalf("the new transcript received %v, want one %q record", fresh.kinds, session.KindMessage)
	}
}

// Review round 4: data queued for the next turn belongs to the session
// that queued it. A session_start hook attachment from the old session
// must not ride into the first turn of the session /clear started.
func TestRestartDropsPendingAttachments(t *testing.T) {
	a := newLogAgent(t, &recordingLog{}, nil)
	a.AttachData("session_start", HookAttachmentKind, "old session's context")
	a.Restart(&recordingLog{})
	if n := len(a.pendingAtts); n != 0 {
		t.Fatalf("%d attachment(s) survived Restart", n)
	}
}

// Review round 4: the summary a compaction leaves behind is rendered
// WHOLE for the next compaction. It went through the per-attachment
// tool-result clip (1500 runes), so every compaction after the first
// summarised a summary missing its tail — the "what is open / next
// step" sections the prompt puts last.
func TestRenderTranscriptKeepsThePriorSummaryWhole(t *testing.T) {
	long := strings.Repeat("次の一手: ", 1000) // 6000 runes
	hist := []llm.Message{SummaryMessage(long), user("continue"), assistant("ok")}
	out := renderTranscript(hist)
	if !strings.Contains(out, long) {
		t.Fatalf("the prior summary was clipped: rendered %d runes of it", len([]rune(out)))
	}
	// Other text attachments keep the clip: this is not a blanket lift.
	hist = []llm.Message{{Role: llm.RoleUser, Content: "x", Attachments: []llm.Attachment{{Ref: "-", Kind: "stdin", Content: long}}}}
	if strings.Contains(renderTranscript(hist), long) {
		t.Fatal("a non-summary attachment lost its clip")
	}
}

// Review round 4: a cancel that lands while a pre-tool hook runs must
// not carry the call on to the gate.
func TestCancelDuringHookSkipsTheGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "write_file", Args: map[string]any{"path": "x.txt", "content": "y"}}}},
		{Content: "done"},
	}}
	gate := &approveAll{}
	a, reg := hookAgent(t, mb, gate, func(context.Context, string, map[string]any) (bool, string) {
		cancel()
		return false, ""
	})
	_, _ = a.Run(ctx, "write", nil)
	if len(gate.asked) != 0 {
		t.Fatalf("gate consulted after the interrupt: %v", gate.asked)
	}
	if _, err := os.Stat(filepath.Join(reg.ProjectDir(), "x.txt")); err == nil {
		t.Fatal("the tool ran after the interrupt")
	}
}

// cancelOnCall is a backend that cancels the turn on its n-th call —
// the risk side-call, in the auto-approve test below — and fails it
// the way a cancelled request fails.
type cancelOnCall struct {
	*mockBackend
	n, at  int
	cancel context.CancelFunc
}

func (c *cancelOnCall) ChatStream(ctx context.Context, system string, messages []llm.Message, defs []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	c.n++
	if c.n == c.at {
		c.cancel()
		return nil, ctx.Err()
	}
	return c.mockBackend.ChatStream(ctx, system, messages, defs, onText)
}

// Review round 4: a cancel that lands during the model-tier risk
// review comes back as "risk evaluation failed" — an escalation — and
// used to reach the gate: the TUI's auto-'n' then recorded a
// gate_decision no human made, and the plain REPL prompt would have
// eaten the next stdin line as its answer.
func TestCancelDuringRiskReviewSkipsTheGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "write_file", Args: map[string]any{"path": "x.txt", "content": "y"}}}},
		{Content: "done"},
	}}
	be := &cancelOnCall{mockBackend: mb, at: 2, cancel: cancel}
	gate := &approveAll{}
	log := &recordingLog{}
	reg := newAgentRegistry(t)
	a := New(Options{Backend: be, Registry: reg, Gate: gate, Log: log,
		System: "test system", MaxTurns: 5, AutoApprove: true})
	_, _ = a.Run(ctx, "write", nil)
	if len(gate.asked) != 0 {
		t.Fatalf("gate consulted after the interrupt: %v", gate.asked)
	}
	for _, k := range log.kinds {
		if k == "gate_decision" {
			t.Fatal("a gate_decision was recorded for a decision no one made")
		}
	}
}

// Review round 4: Restart drops the ended session's late-return notes
// and its compaction switches beside the queued attachments.
func TestRestartDropsTheOldSessionsNotes(t *testing.T) {
	a := newLogAgent(t, &recordingLog{}, nil)
	a.lateNotices = []string{"note"}
	a.compactFailures = maxCompactFailures
	a.warnedNoCut = true
	a.Restart(&recordingLog{})
	if len(a.lateNotices) != 0 || a.compactFailures != 0 || a.warnedNoCut {
		t.Fatalf("old-session state survived Restart: notes=%d failures=%d warned=%v", len(a.lateNotices), a.compactFailures, a.warnedNoCut)
	}
}

// Review round 4: the pre-tool hook payload carries the call as the
// tool will receive it — without the declared purpose (ADR-0047 §2).
func TestPreToolHookDoesNotSeeThePurpose(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "write_file", Args: map[string]any{
			"path": "x.txt", "content": "y", PurposeArg: "because"}}}},
		{Content: "done"},
	}}
	var seen map[string]any
	a, _ := hookAgent(t, mb, &approveAll{}, func(_ context.Context, _ string, args map[string]any) (bool, string) {
		seen = args
		return false, ""
	})
	if _, err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen[PurposeArg]; ok {
		t.Fatalf("the hook saw the purpose: %v", seen)
	}
	if seen["path"] != "x.txt" {
		t.Fatalf("the hook did not see the real arguments: %v", seen)
	}
}

// Review after v0.68.0: an abandoned call that returns after /clear
// belongs to the session that made it — no note for the new
// conversation, no tool_late_return in the new transcript.
func TestLateReturnAfterRestartStaysWithTheOldSession(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	writer := &tools.Tool{
		Name: "slow_write", Description: "writes, slowly, ignoring ctx",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		Mutating:   true,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			started <- struct{}{}
			<-release
			return "written", nil
		},
	}
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "slow_write", Args: map[string]any{}}}},
		{Content: "done"},
	}}
	oldLog := &safeLog{}
	sink, rec := telemetry.NewRecording()
	a := newFloorTestAgent(t, mb, writer, oldLog, sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _, _ = a.Run(ctx, "go", nil); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never returned")
	}
	newLog := &safeLog{}
	a.Restart(newLog)
	if err := sink.Restart(context.Background(), "new-session"); err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for !oldLog.has("tool_late_return") {
		if time.Now().After(deadline) {
			t.Fatal("the old transcript never received the late-return record")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if newLog.has("tool_late_return") {
		t.Error("the new transcript received the old session's late-return record")
	}
	if notes := a.takeLateNotices(); len(notes) != 0 {
		t.Errorf("the new conversation received the old session's note: %v", notes)
	}
	// The audit event names the session that made the call.
	var late *telemetry.RecordedEvent
	for _, ev := range rec.Events() {
		if ev.Name == "tool.late_return" {
			e := ev
			late = &e
		}
	}
	if late == nil {
		t.Fatal("no tool.late_return event")
	}
	if late.Attrs["origin_session_id"] != "recording" {
		t.Errorf("origin_session_id = %q, want the old session", late.Attrs["origin_session_id"])
	}
}

// allowlistGate answers from a session allowlist unless the call must
// prompt; it records which it did.
type allowlistGate struct{ prompted, allowlisted []string }

func (g *allowlistGate) Approve(name, detail, purpose, reason string, mustPrompt bool) (bool, bool, string) {
	if mustPrompt {
		g.prompted = append(g.prompted, name+" "+detail)
		return true, false, ""
	}
	g.allowlisted = append(g.allowlisted, name+" "+detail)
	return true, true, ""
}

// ADR-0072 §4.5: OperatorOnly is a floor like Block — an earlier 'a'
// for write_file must not answer a write to AGENTS.md.
func TestOperatorOnlyIsNotAnsweredByTheAllowlist(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "write_file", Args: map[string]any{"path": "notes.md", "content": "x"}}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: "write_file", Args: map[string]any{"path": "AGENTS.md", "content": "x"}}}},
		{Content: "done"},
	}}
	gate := &allowlistGate{}
	a, _ := newAgent(t, mb, gate, 5)
	if _, err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.allowlisted) != 1 || !strings.Contains(gate.allowlisted[0], "notes.md") {
		t.Errorf("the ordinary write should have been the allowlist's: %v", gate.allowlisted)
	}
	if len(gate.prompted) != 1 || !strings.Contains(gate.prompted[0], "AGENTS.md") {
		t.Errorf("the instruction-file write must prompt: %v", gate.prompted)
	}
}
