package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// printCalls runs the returned command tree and counts how many
// separate Println writes it produces.
func printCalls(c *capture, before int) int {
	return len(c.printedCalls()) - before
}

// Each scrollback burst must be ONE write: every tea.Println is a
// separate clear-insert-repaint cycle, and over a slow terminal the
// intermediate frames flash through the output area (the operator saw
// the Ctrl+C "(interrupted)" line do this on the remote test machine).
func TestInterruptFlushIsOneWrite(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("go")
	m = press(m, enter())
	next, _ := m.Update(TextDelta("streamed partial answer"))
	m = next.(Model)

	before := len(c.printedCalls())
	next, _ = m.Update(TurnDone{Err: context.Canceled})
	m = next.(Model)
	if got := printCalls(c, before); got != 1 {
		t.Errorf("interrupted TurnDone produced %d writes, want 1", got)
	}
	joined := c.all()
	if !strings.Contains(joined, "streamed partial answer") || !strings.Contains(joined, "(interrupted)") {
		t.Errorf("flush or outcome line missing: %q", joined)
	}
	// Order inside the single write: text before the outcome line.
	last := c.printedCalls()[len(c.printedCalls())-1]
	if strings.Index(last, "partial answer") > strings.Index(last, "(interrupted)") {
		t.Errorf("outcome line printed before the text it follows: %q", last)
	}
}

func TestToolCallFlushIsOneWrite(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("go")
	m = press(m, enter())
	next, _ := m.Update(TextDelta("text before the call"))
	m = next.(Model)

	before := len(c.printedCalls())
	next, _ = m.Update(ToolCall{Name: "mcp__x__y", Detail: "d"})
	m = next.(Model)
	if got := printCalls(c, before); got != 1 {
		t.Errorf("ToolCall produced %d writes, want 1", got)
	}
	last := c.printedCalls()[len(c.printedCalls())-1]
	if strings.Index(last, "text before the call") > strings.Index(last, "⚙") {
		t.Errorf("tool event printed before the text it follows: %q", last)
	}
}

func TestErrorTurnDoneIsOneWrite(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("go")
	m = press(m, enter())
	before := len(c.printedCalls())
	next, _ := m.Update(TurnDone{Err: errors.New("boom")})
	_ = next.(Model)
	if got := printCalls(c, before); got != 1 {
		t.Errorf("error TurnDone produced %d writes, want 1", got)
	}
}

func TestShellDoneInterruptedIsOneWrite(t *testing.T) {
	c := &capture{}
	m := New(Options{
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
		Slash: slashStub,
		Shell: func(ctx context.Context, command string) {},
	})
	m.ta.SetValue("!sleep 5")
	m = press(m, enter())
	before := len(c.printedCalls())
	next, _ := m.Update(ShellDone{Output: "partial output", Interrupted: true})
	_ = next.(Model)
	if got := printCalls(c, before); got != 1 {
		t.Errorf("interrupted ShellDone produced %d writes, want 1", got)
	}
}

var _ = tea.Println // keep the import stable if assertions change

// printedCalls snapshots the individual Println writes (the test
// printer receives exactly one string per write).
func (c *capture) printedCalls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.printed...)
}
