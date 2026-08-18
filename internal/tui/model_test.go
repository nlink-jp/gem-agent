package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

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
		Slash:   func(cmd string) (string, bool) { return "slash:" + cmd, cmd == "/quit" },
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return "MD[" + s + "]" }
		},
	})
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
