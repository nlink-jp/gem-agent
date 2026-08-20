package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func frameLines(m Model) int {
	return strings.Count(m.View(), "\n") + 1
}

// heldWant is the ADR-0024 invariant: the held height minus consumed
// scrollback lines, floored at the view's own core height (a frame can
// never render below its content).
func heldWant(m *Model, held int) int {
	save := m.hold.lastTotal
	m.hold.lastTotal = 0
	core := frameLines(*m)
	m.hold.lastTotal = save
	if held < core {
		return core
	}
	return held
}

// ADR-0024's ground truth: in the full-screen regime, the rendered
// frame's line count must not DECREASE except by exactly the lines
// printed to scrollback in between — a shrinking view (flush reset,
// dialog close) must not lift the footer.
func TestBottomHoldKeepsFrameHeightThroughFlushAndDialog(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = next.(Model)
	m.printed = 500 // the screen filled long ago

	// A streaming turn with a tall live region.
	m.ta.SetValue("go")
	m = press(m, enter())
	next, _ = m.Update(TextDelta(strings.Repeat("streamed line\n", 10)))
	m = next.(Model)
	tall := frameLines(m)
	if tall < 10 {
		t.Fatalf("streaming frame is %d lines — test setup wrong", tall)
	}

	// MCP-call boundary: flushLive empties the live region (the view
	// core shrinks by ~10 lines). The frame must hold, minus exactly
	// what the flush printed to scrollback.
	printedBefore := m.printed
	next, _ = m.Update(ToolCall{Name: "mcp__x__y", Detail: "args"})
	m = next.(Model)
	consumed := m.printed - printedBefore
	if got, want := frameLines(m), heldWant(&m, tall-consumed); got != want {
		t.Errorf("after flush: frame %d lines, want %d (%d printed) — the footer bounced", got, want, consumed)
	}

	// Approval dialog opens (frame grows or holds), then closes: the
	// close must not shrink the frame either.
	next, _ = m.Update(ApprovalRequest{Tool: "mcp__x__y", Detail: "d", Resp: make(chan byte, 1)})
	m = next.(Model)
	m = dialogSeen(m)
	open := frameLines(m)
	printedBefore = m.printed
	m = press(m, runeMsg("y")) // answer: dialog closes, back to running
	consumed = m.printed - printedBefore
	if got, want := frameLines(m), heldWant(&m, open-consumed); got != want {
		t.Errorf("after dialog close: frame %d lines, want %d — the footer bounced", got, want)
	}

	// Scrollback lines eat the gap one for one.
	before := frameLines(m)
	m.emit("one line")
	if got := frameLines(m); got != before-1 {
		t.Errorf("after one printed line: frame %d, want %d — history must flow into the gap", got, before-1)
	}
}

// Below the fold the pad absorbs everything, exactly as before ADR-0024
// — the hold must stay disarmed.
func TestBottomHoldDisarmedWhileScreenNotFull(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = next.(Model)
	m.printed = 3

	m.ta.SetValue("go")
	m = press(m, enter())
	next, _ = m.Update(TextDelta(strings.Repeat("x\n", 8)))
	m = next.(Model)
	if got := frameLines(m); got != 29-m.printed {
		t.Errorf("padded frame = %d lines, want %d (pad absorbs)", got, 29-m.printed)
	}
	if m.hold.lastTotal != 0 {
		t.Errorf("hold armed below the fold: %d", m.hold.lastTotal)
	}

	// The frame never exceeds height-1 even when the hold would ask.
	m.printed = 500
	_ = m.View()
	if m.hold.lastTotal > 29 {
		t.Errorf("hold exceeds height-1: %d", m.hold.lastTotal)
	}
}

// A resize resets the accounting along with the clear.
func TestBottomHoldResetsOnResize(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = next.(Model)
	m.printed = 500
	m.ta.SetValue("go")
	m = press(m, enter())
	next, _ = m.Update(TextDelta(strings.Repeat("x\n", 10)))
	m = next.(Model)
	_ = m.View()
	if m.hold.lastTotal == 0 {
		t.Fatal("hold did not arm in the full regime")
	}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 30}) // shrink → clear
	m = next.(Model)
	if m.hold.lastTotal != 0 {
		t.Errorf("hold survived the resize clear: %d", m.hold.lastTotal)
	}
}
