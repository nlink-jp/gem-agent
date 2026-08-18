package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func lipglossWidth(s string) int { return ansi.StringWidth(s) }

// capture collects Println output and turn starts.
type capture struct {
	mu      sync.Mutex
	printed []string
	turns   []string
}

func (c *capture) printer(args ...any) tea.Cmd {
	c.mu.Lock()
	for _, a := range args {
		if s, ok := a.(string); ok {
			c.printed = append(c.printed, s)
		}
	}
	c.mu.Unlock()
	return nil
}

func (c *capture) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.printed, "\n")
}

func newTestModel(c *capture) Model {
	return New(Options{
		StartTurn: func(ctx context.Context, input string) {
			c.turns = append(c.turns, input)
		},
		Slash:   slashStub,
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return "MD[" + s + "]" }
		},
	})
}

func slashStub(cmd string) (string, bool, bool) {
	switch cmd {
	case "/quit":
		return "bye", false, true
	case "/nope":
		return "unknown command \"/nope\"", true, false
	default:
		return "slash:" + cmd, false, false
	}
}

func press(m Model, key tea.KeyMsg) Model {
	next, _ := m.Update(key)
	return next.(Model)
}

func enter() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }

func runeMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestSubmitStartsTurn(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("こんにちは")
	m = press(m, enter())

	if m.phase != phaseRunning {
		t.Fatalf("phase = %v, want running", m.phase)
	}
	if len(c.turns) != 1 || c.turns[0] != "こんにちは" {
		t.Errorf("turns = %v", c.turns)
	}
	if !strings.Contains(c.all(), "こんにちは") {
		t.Error("user line not flushed to scrollback")
	}
	if m.ta.Value() != "" {
		t.Error("input not cleared after submit")
	}
}

func TestEmptySubmitIgnored(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m = press(m, enter())
	if m.phase != phaseInput || len(c.turns) != 0 {
		t.Error("empty submit must be a no-op")
	}
}

func TestSlashCommandDoesNotStartTurn(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("/tools")
	m = press(m, enter())
	if m.phase != phaseInput || len(c.turns) != 0 {
		t.Error("slash command must not start a turn")
	}
	if !strings.Contains(c.all(), "slash:/tools") {
		t.Error("slash output not printed")
	}
}

// TestUnknownSlashCommandStandsOut: an unknown command is an error, and
// errors carry the ✗ marker so they never blend into dim meta text
// (color alone is not relied upon — plain theme keeps the marker).
func TestUnknownSlashCommandStandsOut(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("/nope")
	m = press(m, enter())
	if m.phase != phaseInput {
		t.Fatal("unknown command must not start a turn")
	}
	if !strings.Contains(c.all(), "✗") || !strings.Contains(c.all(), "/nope") {
		t.Errorf("error output should carry the ✗ marker: %q", c.all())
	}
}

// TestHistoryDownGuard is the regression test for the recorded org
// lesson: Down outside history navigation must not touch the draft.
func TestHistoryDownGuard(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.history = []string{"first", "second"}
	m.ta.SetValue("typing in progress")

	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.ta.Value() != "typing in progress" {
		t.Fatalf("Down with histIdx=-1 destroyed the draft: %q", m.ta.Value())
	}
}

func TestHistoryNavigation(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.history = []string{"first", "second"}
	m.ta.SetValue("draft")

	m = press(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.ta.Value() != "second" {
		t.Fatalf("Up should recall the latest entry, got %q", m.ta.Value())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.ta.Value() != "first" {
		t.Fatalf("second Up should go older, got %q", m.ta.Value())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.ta.Value() != "first" {
		t.Fatalf("Up at the oldest entry should stay, got %q", m.ta.Value())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.ta.Value() != "second" {
		t.Fatalf("Down should go newer, got %q", m.ta.Value())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.ta.Value() != "draft" {
		t.Fatalf("Down past the newest entry should restore the draft, got %q", m.ta.Value())
	}
	if m.histIdx != -1 {
		t.Error("navigation state should be cleared after draft restore")
	}
}

func TestEditLeavesNavigationMode(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.history = []string{"recalled"}
	m = press(m, tea.KeyMsg{Type: tea.KeyUp})
	m = press(m, runeMsg("x"))
	if m.histIdx != -1 {
		t.Error("editing a recalled entry should leave navigation mode")
	}
}

// TestPasteDoesNotSubmit: pasted newlines insert into the textarea; only
// a human Enter submits.
func TestPasteDoesNotSubmit(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line1\nline2"), Paste: true}
	m = press(m, paste)
	if m.phase != phaseInput || len(c.turns) != 0 {
		t.Fatal("paste must not start a turn")
	}
	if !strings.Contains(m.ta.Value(), "line1") || !strings.Contains(m.ta.Value(), "line2") {
		t.Errorf("pasted content lost: %q", m.ta.Value())
	}
}

func TestStreamFlushOrder(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("go")
	m = press(m, enter())

	next, _ := m.Update(TextDelta("before tool"))
	m = next.(Model)
	next, _ = m.Update(ToolCall{Name: "read_file", Detail: "path=a"})
	m = next.(Model)
	next, _ = m.Update(TextDelta("after tool"))
	m = next.(Model)
	next, _ = m.Update(TurnDone{})
	m = next.(Model)

	out := c.all()
	iText := strings.Index(out, "MD[before tool]")
	iTool := strings.Index(out, "read_file")
	iAfter := strings.Index(out, "MD[after tool]")
	if iText == -1 || iTool == -1 || iAfter == -1 {
		t.Fatalf("missing segments in scrollback: %q", out)
	}
	if !(iText < iTool && iTool < iAfter) {
		t.Errorf("scrollback order broken: %q", out)
	}
	if m.phase != phaseInput {
		t.Error("TurnDone should return to input phase")
	}
	if m.live.Len() != 0 {
		t.Error("live buffer should be empty after TurnDone")
	}
}

// TestConsecutiveTextDeltas reproduces the live crash: Bubble Tea copies
// the model by value on every Update, and a strings.Builder held by
// value panics on the second WriteString after a copy. Two consecutive
// deltas with reassignment in between mimic the real event loop.
func TestConsecutiveTextDeltas(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("go")
	m = press(m, enter())

	for _, chunk := range []string{"こん", "にち", "は！"} {
		next, _ := m.Update(TextDelta(chunk))
		m = next.(Model)
	}
	next, _ := m.Update(TurnDone{})
	m = next.(Model)

	if !strings.Contains(c.all(), "MD[こんにちは！]") {
		t.Errorf("accumulated stream lost: %q", c.all())
	}
}

func TestFooterShowsModelUsageAndProject(t *testing.T) {
	c := &capture{}
	m := New(Options{
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
		Slash:      slashStub,
		ModelName:  "gemini-3.7-flash",
		ProjectDir: "~/works/demo",
	})

	// Before any usage: placeholders, never garbage.
	v := m.View()
	if !strings.Contains(v, "gemini-3.7-flash") || !strings.Contains(v, "~/works/demo") {
		t.Fatalf("footer missing static fields: %q", v)
	}
	if !strings.Contains(v, "ctx –/–") {
		t.Errorf("unknown usage/window should show placeholders: %q", v)
	}

	// Usage accumulates: ctx tracks the last round, total accumulates.
	next, _ := m.Update(Usage{Prompt: 1000, Output: 200})
	m = next.(Model)
	next, _ = m.Update(Usage{Prompt: 11800, Output: 500})
	m = next.(Model)
	next, _ = m.Update(ContextWindow(1_048_576))
	m = next.(Model)

	v = m.View()
	if !strings.Contains(v, "ctx 12.3k/1.0M (1%)") {
		t.Errorf("footer occupancy wrong: %q", v)
	}
	if !strings.Contains(v, "total 13.5k") {
		t.Errorf("footer total wrong: %q", v)
	}
}

func TestHumanTokens(t *testing.T) {
	cases := map[int]string{999: "999", 1000: "1.0k", 12345: "12.3k", 1_048_576: "1.0M"}
	for n, want := range cases {
		if got := humanTokens(n); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestApprovalFlow(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("do it")
	m = press(m, enter())

	resp := make(chan byte, 1)
	next, _ := m.Update(ApprovalRequest{Tool: "shell_exec", Detail: "make build", Resp: resp})
	m = next.(Model)
	if m.phase != phaseApproval {
		t.Fatal("approval request should switch phase")
	}
	if !strings.Contains(m.View(), "shell_exec") {
		t.Error("approval view should show the tool")
	}

	m = press(m, runeMsg("a"))
	select {
	case got := <-resp:
		if got != 'a' {
			t.Errorf("answer = %c", got)
		}
	case <-time.After(time.Second):
		t.Fatal("gate never received the answer")
	}
	if m.phase != phaseRunning {
		t.Error("answer should return to running phase")
	}
}

// TestViewLinesClippedToWidth pins the resize-artifact fix: no line of
// the managed view may exceed the terminal width, or soft wrapping
// desyncs the inline renderer's height accounting and stale frames
// stack up on screen.
func TestViewLinesClippedToWidth(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 24, Height: 40})
	m = next.(Model)

	// Streaming phase with long lines is the worst case.
	m.ta.SetValue("go")
	m = press(m, enter())
	next, _ = m.Update(TextDelta(strings.Repeat("長い行です。", 40) + "\n" + strings.Repeat("x", 500)))
	m = next.(Model)

	for i, line := range strings.Split(m.View(), "\n") {
		if w := lipglossWidth(line); w >= 24 {
			t.Errorf("view line %d is %d cells wide (must stay under 24)", i, w)
		}
	}
	// Input phase (hint line) must clip too.
	next, _ = m.Update(TurnDone{})
	m = next.(Model)
	for i, line := range strings.Split(m.View(), "\n") {
		if w := lipglossWidth(line); w >= 24 {
			t.Errorf("input view line %d is %d cells wide", i, w)
		}
	}
}

// TestShrinkClearsScreenOnce: a genuine width shrink returns a clear
// command to sweep re-wrapped stale frames; the first size report and
// growth do not (clearing at startup would wipe the banner).
func TestShrinkClearsScreenOnce(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	m = next.(Model)
	if cmd != nil {
		t.Error("first size report must not clear the screen")
	}
	next, cmd = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	if cmd != nil {
		t.Error("growth must not clear the screen")
	}
	next, cmd = m.Update(tea.WindowSizeMsg{Width: 50, Height: 40})
	m = next.(Model)
	if cmd == nil {
		t.Error("shrink must trigger a screen clear")
	}
	_ = m
}

// TestResizeNeverQueriesTerminal pins the OSC-leak fix: resizing must
// rebuild the renderer through the injected factory only — a renderer
// that queries the terminal mid-session turns the reply into phantom
// keystrokes in the input box.
func TestResizeNeverQueriesTerminal(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	if got := m.render("x"); got != "MD[x]" {
		t.Fatalf("resize replaced the injected renderer factory: %q", got)
	}
}

func TestCtrlCClearsThenQuits(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("half-typed")
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.ta.Value() != "" {
		t.Fatal("first Ctrl+C should clear the input")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C on empty input should quit")
	}
}

func TestCtrlCInterruptsTurn(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	canceled := make(chan struct{})
	m.startTurn = func(ctx context.Context, input string) {
		go func() {
			<-ctx.Done()
			close(canceled)
		}()
	}
	m.ta.SetValue("long task")
	m = press(m, enter())
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Error("Ctrl+C during a turn should cancel the turn context")
	}
}
