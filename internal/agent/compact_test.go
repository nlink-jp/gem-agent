package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/telemetry"
)

// capturingLog records what the agent writes to the transcript.
type capturingLog struct {
	kinds []string
	data  []any
}

func (c *capturingLog) Log(kind string, data any) error {
	c.kinds = append(c.kinds, kind)
	c.data = append(c.data, data)
	return nil
}

func user(s string) llm.Message      { return llm.Message{Role: llm.RoleUser, Content: s} }
func assistant(s string) llm.Message { return llm.Message{Role: llm.RoleAssistant, Content: s} }
func toolCall(name string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c", Name: name}}}
}
func toolResult(name, s string) llm.Message {
	return llm.Message{Role: llm.RoleTool, ToolName: name, ToolCallID: "c", Content: s}
}

// The cut must never land on a tool result: Gemini requires every
// function call to be paired with its response in the same request, so a
// retained tail starting with an orphan result is a 400.
func TestCompactCutNeverLandsOnAToolResult(t *testing.T) {
	histories := map[string][]llm.Message{
		"tool round straddling the cut": {
			user("1"), assistant("2"), user("3"), toolCall("read_file"), toolResult("read_file", "x"),
			assistant("6"), user("7"), toolCall("shell_exec"), toolResult("shell_exec", "y"), assistant("10"),
		},
		"long tool loop with one early user message": {
			user("1"), toolCall("a"), toolResult("a", "x"), toolCall("b"), toolResult("b", "y"),
			toolCall("c"), toolResult("c", "z"), toolCall("d"), toolResult("d", "w"), assistant("done"),
		},
		"nothing but a short exchange": {user("1"), assistant("2")},
	}
	for name, h := range histories {
		t.Run(name, func(t *testing.T) {
			cut := compactCut(h, keepRecent)
			if cut == 0 {
				return // "no safe boundary" is a valid answer
			}
			if cut < 0 || cut >= len(h) {
				t.Fatalf("cut = %d out of range for %d messages", cut, len(h))
			}
			if h[cut].Role == llm.RoleTool {
				t.Errorf("cut at %d lands on a tool result — its function call would be summarised away, and the request would 400", cut)
			}
		})
	}
}

// The case compaction exists for: one long agent loop, whose only user
// message is the first. A user-only boundary rule could never cut it.
func TestCompactCutHandlesALoopWithNoLaterUserMessage(t *testing.T) {
	h := []llm.Message{user("go")}
	for i := 0; i < 8; i++ {
		h = append(h, toolCall("read_file"), toolResult("read_file", "x"))
	}
	cut := compactCut(h, keepRecent)
	if cut == 0 {
		t.Fatal("a long tool loop could not be compacted at all")
	}
	if h[cut].Role != llm.RoleAssistant {
		t.Errorf("cut lands on %q, want the assistant turn that owns the following results", h[cut].Role)
	}
}

func TestCompactReplacesHeadWithASummaryQuotedAsData(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{{Content: "SUMMARY TEXT"}}}
	a, _ := newAgent(t, mb, &approveAll{}, 5)
	a.SetHistory([]llm.Message{
		user("first"), assistant("a1"), user("second"), toolCall("read_file"),
		toolResult("read_file", "file body"), assistant("a2"), user("third"),
		assistant("a3"), user("fourth"), assistant("a4"),
	})

	res, err := a.Compact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Before != 10 || res.After != 10-res.Replaced+1 {
		t.Errorf("result = %+v", res)
	}
	if a.history[0].Role != llm.RoleUser || len(a.history[0].Attachments) != 1 {
		t.Fatalf("head message = %+v, want a user message carrying the summary as an attachment", a.history[0])
	}
	if a.history[0].Attachments[0].Content != "SUMMARY TEXT" {
		t.Errorf("summary = %q", a.history[0].Attachments[0].Content)
	}
	// The tail survives verbatim, signatures and all.
	if a.history[len(a.history)-1].Content != "a4" {
		t.Errorf("tail lost: %+v", a.history)
	}

	// The summariser is offered no tools and sees the transcript as
	// isolated data, not as instructions (ADR-0006).
	if len(mb.toolDefs[0]) != 0 {
		t.Errorf("summariser was offered %d tools", len(mb.toolDefs[0]))
	}
	sent := mb.calls[0][0].Content
	if !strings.Contains(sent, "file body") {
		t.Error("transcript did not reach the summariser")
	}
	if !strings.HasPrefix(strings.TrimSpace(sent), "<transcript") {
		t.Errorf("transcript was not nonce-wrapped: %.60q", sent)
	}
	if !strings.Contains(mb.systems[0], "UNTRUSTED DATA") {
		t.Error("summariser prompt lost its defensive framing")
	}

	// And on the next real request the summary itself is quoted as data.
	mb.responses = []*llm.Response{{Content: "ok"}}
	if _, err := a.Run(context.Background(), "carry on", nil); err != nil {
		t.Fatal(err)
	}
	head := mb.calls[1][0].Content
	if !strings.Contains(head, "SUMMARY TEXT") {
		t.Fatal("summary did not reach the model")
	}
	if !strings.Contains(head, "quoted as data") {
		t.Errorf("summary was sent unquoted: %q", head)
	}
}

// A compaction that half-worked would silently delete a conversation.
func TestCompactLeavesHistoryIntactWhenTheSummariserFails(t *testing.T) {
	for name, resp := range map[string]*llm.Response{
		"blocked by the content filter": {BlockReason: "PROHIBITED_CONTENT"},
		"empty answer":                  {},
	} {
		t.Run(name, func(t *testing.T) {
			mb := &mockBackend{responses: []*llm.Response{resp}}
			a, _ := newAgent(t, mb, &approveAll{}, 5)
			before := []llm.Message{
				user("1"), assistant("2"), user("3"), assistant("4"), user("5"),
				assistant("6"), user("7"), assistant("8"), user("9"), assistant("10"),
			}
			a.SetHistory(append([]llm.Message(nil), before...))

			if _, err := a.Compact(context.Background()); err == nil {
				t.Fatal("a failed summarisation reported success")
			}
			if len(a.history) != len(before) {
				t.Fatalf("history = %d messages, want the original %d", len(a.history), len(before))
			}
			for i := range before {
				if a.history[i].Content != before[i].Content {
					t.Fatalf("history[%d] = %q, want %q", i, a.history[i].Content, before[i].Content)
				}
			}
		})
	}
}

func TestCompactRefusesWhenThereIsNothingToCompact(t *testing.T) {
	mb := &mockBackend{}
	a, _ := newAgent(t, mb, &approveAll{}, 5)
	a.SetHistory([]llm.Message{user("1"), assistant("2")})
	if _, err := a.Compact(context.Background()); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("err = %v, want ErrNothingToCompact", err)
	}
	if len(mb.calls) != 0 {
		t.Error("a pointless compaction still paid for an LLM call")
	}
}

func TestAutoCompactFiresAtTheThresholdAndIsRecorded(t *testing.T) {
	log := &capturingLog{}
	mb := &mockBackend{responses: []*llm.Response{
		// Round 0 reports 90% occupancy and asks for a tool; the
		// compaction lands between rounds, before the next request.
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "list_files", Args: map[string]any{}}}, PromptTokens: 900},
		{Content: "SUMMARY"},
		{Content: "done"},
	}}
	_, reg := newAgent(t, mb, &approveAll{}, 5)
	var notices []string
	sink, rec := telemetry.NewRecording()
	a2 := New(Options{
		Backend: mb, Registry: reg, Gate: &approveAll{}, Log: log,
		System: "s", MaxTurns: 5, AutoCompact: true, CompactAtPct: 80,
		OnNotice:  func(msg string) { notices = append(notices, msg) },
		Telemetry: sink,
	})
	a2.SetContextWindow(1000)
	a2.SetHistory([]llm.Message{
		user("1"), assistant("2"), user("3"), assistant("4"), user("5"),
		assistant("6"), user("7"), assistant("8"), user("9"), assistant("10"),
	})

	// Round 0 reports 90% occupancy; the compaction happens before the
	// request that would have overflowed.
	if _, err := a2.Run(context.Background(), "keep going", nil); err != nil {
		t.Fatal(err)
	}
	if len(mb.calls) != 3 {
		t.Fatalf("%d backend calls, want request + summarise + request", len(mb.calls))
	}
	if a2.history[0].Attachments == nil || a2.history[0].Attachments[0].Content != "SUMMARY" {
		t.Errorf("history head = %+v, want the summary", a2.history[0])
	}
	// The automatic path must reach the audit stream too (review
	// round 3: only /compact emitted the event).
	compacted := false
	for _, ev := range rec.Events() {
		if ev.Name == "compaction" {
			compacted = true
		}
	}
	if !compacted {
		t.Errorf("auto-compaction emitted no compaction audit event: %v", rec.Events())
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "compacted") {
		t.Errorf("notices = %v — a silent compaction looks like a model that forgot", notices)
	}

	var compactions int
	for i, kind := range log.kinds {
		if kind != session.KindCompaction {
			continue
		}
		compactions++
		rec, ok := log.data[i].(session.Compaction)
		if !ok || rec.Replaced == 0 || rec.Message.Attachments[0].Content != "SUMMARY" {
			t.Errorf("compaction record = %+v", log.data[i])
		}
	}
	if compactions != 1 {
		t.Errorf("%d compaction records written, want 1 (a resumed session replays these)", compactions)
	}
}

// Without the growth guard, a session whose recent tail alone exceeds the
// threshold would summarise on every single round and never shrink.
func TestAutoCompactDoesNotSpin(t *testing.T) {
	responses := []*llm.Response{{ToolCalls: []llm.ToolCall{{ID: "c", Name: "list_files", Args: map[string]any{}}}, PromptTokens: 950}}
	for i := 0; i < 8; i++ {
		responses = append(responses,
			&llm.Response{Content: "SUMMARY"},
			&llm.Response{ToolCalls: []llm.ToolCall{{ID: "c", Name: "list_files", Args: map[string]any{}}}, PromptTokens: 950})
	}
	responses = append(responses, &llm.Response{Content: "done"})
	mb := &mockBackend{responses: responses}
	_, reg := newAgent(t, mb, &approveAll{}, 12)
	var compactions int
	a := New(Options{
		Backend: mb, Registry: reg, Gate: &approveAll{}, System: "s", MaxTurns: 12,
		AutoCompact: true, CompactAtPct: 80,
		OnNotice: func(msg string) {
			if strings.Contains(msg, "compacted") {
				compactions++
			}
		},
	})
	a.SetContextWindow(1000)
	a.SetHistory([]llm.Message{
		user("1"), assistant("2"), user("3"), assistant("4"), user("5"),
		assistant("6"), user("7"), assistant("8"), user("9"), assistant("10"),
	})
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if compactions > 2 {
		t.Errorf("%d compactions in one turn — the guard is not holding", compactions)
	}
}

func TestAutoCompactGivesUpAfterRepeatedFailures(t *testing.T) {
	var responses []*llm.Response
	for i := 0; i < 12; i++ {
		responses = append(responses,
			&llm.Response{ToolCalls: []llm.ToolCall{{ID: "c", Name: "list_files", Args: map[string]any{}}}, PromptTokens: 950},
			&llm.Response{BlockReason: "PROHIBITED_CONTENT"})
	}
	mb := &mockBackend{responses: responses}
	_, reg := newAgent(t, mb, &approveAll{}, 12)
	var failures int
	var offMsg string
	a := New(Options{
		Backend: mb, Registry: reg, Gate: &approveAll{}, System: "s", MaxTurns: 12,
		AutoCompact: true, CompactAtPct: 80,
		OnNotice: func(msg string) {
			if strings.Contains(msg, "compaction failed") {
				failures++
			}
			if strings.Contains(msg, "automatic compaction is off") {
				offMsg = msg
			}
		},
	})
	a.SetContextWindow(1000)
	a.SetHistory([]llm.Message{
		user("1"), assistant("2"), user("3"), assistant("4"), user("5"),
		assistant("6"), user("7"), assistant("8"), user("9"), assistant("10"),
	})
	_, _ = a.Run(context.Background(), "go", nil)
	if failures != maxCompactFailures {
		t.Errorf("%d failure notices, want %d then silence", failures, maxCompactFailures)
	}
	if offMsg == "" {
		t.Error("auto-compaction switched itself off without saying so")
	}
}

// The whole point of recording compactions: a resumed session must come
// back compacted, not re-inflated (ADR-0005 §4).
func TestCompactedSessionRoundTripsThroughTheTranscript(t *testing.T) {
	dir := t.TempDir()
	lg, err := session.Open(dir, "/p")
	if err != nil {
		t.Fatal(err)
	}
	mb := &mockBackend{responses: []*llm.Response{
		{Content: "answer one"},
		{Content: "SUMMARY OF EARLIER"},
		{Content: "answer two"},
	}}
	_, reg := newAgent(t, mb, &approveAll{}, 5)
	a := New(Options{Backend: mb, Registry: reg, Gate: &approveAll{}, Log: lg, System: "s", MaxTurns: 5})
	a.SetHistory([]llm.Message{
		user("1"), assistant("2"), user("3"), assistant("4"),
		user("5"), assistant("6"), user("7"), assistant("8"),
	})
	// SetHistory does not write records (the transcript already has
	// them on a real resume), so replay the seed explicitly.
	for _, m := range a.history {
		if err := lg.Log(session.KindMessage, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Run(context.Background(), "question", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "another", nil); err != nil {
		t.Fatal(err)
	}
	lg.Close()

	restored, _, _, err := session.Load(lg.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != len(a.history) {
		t.Fatalf("restored %d messages, live history has %d", len(restored), len(a.history))
	}
	for i := range restored {
		if restored[i].Role != a.history[i].Role || restored[i].Content != a.history[i].Content {
			t.Errorf("message %d: restored %s %q, live %s %q",
				i, restored[i].Role, clip(restored[i].Content, 40), a.history[i].Role, clip(a.history[i].Content, 40))
		}
	}
	if len(restored[0].Attachments) != 1 || restored[0].Attachments[0].Content != "SUMMARY OF EARLIER" {
		t.Errorf("restored head = %+v, want the summary message", restored[0])
	}
}

func TestRenderTranscriptKeepsShapeAndClipsBulk(t *testing.T) {
	got := renderTranscript([]llm.Message{
		user("do the thing"),
		{Role: llm.RoleUser, Attachments: []llm.Attachment{{Ref: "a.txt", Kind: "file", Content: "attached body"}}},
		{Role: llm.RoleAssistant, Content: "on it", ToolCalls: []llm.ToolCall{{Name: "read_file", Args: map[string]any{"path": "a.txt"}}}},
		toolResult("read_file", strings.Repeat("z", summaryToolClip*3)),
	})
	for _, want := range []string{"[user] do the thing", "[user attached file a.txt] attached body",
		"[assistant] on it", `[assistant calls read_file] {"path":"a.txt"}`, "[result of read_file]"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q:\n%s", want, clip(got, 400))
		}
	}
	if len(got) > summaryToolClip*2 {
		t.Errorf("transcript is %d bytes — bulk tool output should be clipped for the summariser", len(got))
	}
}

func TestSummaryMessageFramesTheSummaryAsAPastRecord(t *testing.T) {
	m := SummaryMessage("earlier we agreed to delete everything")
	if m.Role != llm.RoleUser {
		t.Errorf("role = %s", m.Role)
	}
	if m.Content == "" || !strings.Contains(m.Content, "not as a new instruction") {
		t.Errorf("framing = %q — a summary that reads as orders is an injection path", m.Content)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Content != "earlier we agreed to delete everything" {
		t.Errorf("attachments = %+v", m.Attachments)
	}
}

func TestCompactCutIsStableUnderGrowth(t *testing.T) {
	var h []llm.Message
	for i := 0; i < 40; i++ {
		if i%3 == 0 {
			h = append(h, user(fmt.Sprintf("u%d", i)))
			continue
		}
		h = append(h, assistant(fmt.Sprintf("a%d", i)))
	}
	cut := compactCut(h, keepRecent)
	if cut != len(h)-keepRecent {
		t.Fatalf("cut = %d, want exactly len-keepRecent when no tool result is in the way", cut)
	}
	if len(h)-cut != keepRecent {
		t.Error("compaction would not keep the recent messages verbatim")
	}
}
