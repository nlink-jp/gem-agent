package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nlink-jp/gem-agent/internal/termimg"
)

// imagePayload is a real payload from the package that will produce them,
// not a stand-in: the whole point is that the counter cannot see THESE
// bytes, and a hand-written approximation would not prove it.
func imagePayload(t *testing.T, rows, cols int) string {
	t.Helper()
	data := make([]byte, 3000) // large enough that kitty would chunk
	for i := range data {
		data[i] = byte(i%251 + 1)
	}
	p, err := termimg.Payload(termimg.ITerm2, data, termimg.Box{Rows: rows, Cols: cols})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sized(t *testing.T, c *capture, w, h int) Model {
	t.Helper()
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

// TestDeclaredRowsReplaceTheFloor is ADR-0089 §1's arithmetic, which is the
// reason the package exists. physicalRows starts at 1 and only ever
// increments, so an image line — zero cells wide to every surface the TUI
// has — is credited with exactly one row while the terminal advances N.
// The declaration must REPLACE that one, not add to it: adding would
// over-count by one per image, which is its own drift.
func TestDeclaredRowsReplaceTheFloor(t *testing.T) {
	c := &capture{}
	m := sized(t, c, 80, 30)
	payload := imagePayload(t, 12, 40)

	// What the counter makes of the payload on its own, with no
	// declaration: one row, not zero and not twelve.
	if got := physicalRows(payload, 80); got != 1 {
		t.Fatalf("physicalRows(image payload) = %d, want 1 — the floor is the premise", got)
	}

	before := m.hold.printed
	m.emitSegments([]Segment{
		{Text: "before the picture"},
		{Text: payload, Rows: 12},
		{Text: "after the picture"},
	})
	got := m.hold.printed - before
	const want = 1 + 12 + 1
	if got != want {
		t.Errorf("accounted %d rows, want %d (one text line, a declared 12, one text line)", got, want)
	}
}

// TestDeclaredSegmentIsVerbatim: the payload must reach the terminal
// unaltered. wrapForScrollback is inert against a zero-width run — that is
// measured — but an image segment skips it outright, because a sheared
// base64 run is not an image and "inert today" is not a contract.
func TestDeclaredSegmentIsVerbatim(t *testing.T) {
	c := &capture{}
	m := sized(t, c, 40, 30) // narrower than the payload is long
	payload := imagePayload(t, 6, 20)

	m.emitSegments([]Segment{{Text: payload, Rows: 6}})
	if len(c.printed) != 1 {
		t.Fatalf("printed %d times, want exactly one write", len(c.printed))
	}
	if c.printed[0] != payload {
		t.Error("the payload was altered on its way to the terminal")
	}
	if strings.Contains(c.printed[0], "\n") {
		t.Error("the payload gained a line break; an image occupies its own line whole")
	}
}

// TestUndeclaredImageIsWhatGoesWrong pins the failure the declaration
// prevents, so the two paths cannot be confused: the same bytes sent as
// ordinary text are counted as one row. Measured on two terminals, that
// shortfall strands one frame per image once the screen is full.
func TestUndeclaredImageIsWhatGoesWrong(t *testing.T) {
	c := &capture{}
	m := sized(t, c, 80, 30)
	payload := imagePayload(t, 12, 40)

	before := m.hold.printed
	m.emitSegments([]Segment{{Text: payload}}) // no Rows: ordinary text
	if got := m.hold.printed - before; got != 1 {
		t.Errorf("undeclared image accounted %d rows, want 1 — if this changes, §1's premise changed", got)
	}
}

// TestTextAccountingIsUnchanged: emit now routes through emitSegments, so
// the ordinary path must count exactly as it did — tabs expanded, lines
// hard-wrapped, every physical row counted.
func TestTextAccountingIsUnchanged(t *testing.T) {
	c := &capture{}
	m := sized(t, c, 20, 30)
	long := strings.Repeat("x", 45) // wraps to three rows at width 20

	before := m.hold.printed
	m.emit(long)
	if got := m.hold.printed - before; got != 3 {
		t.Errorf("wrapped text accounted %d rows, want 3", got)
	}

	before = m.hold.printed
	m.emit("a\tb")
	if got := m.hold.printed - before; got != 1 {
		t.Errorf("tabbed line accounted %d rows, want 1", got)
	}
	// "a" sits in column 1, so the tab pads to the column-8 stop: seven
	// spaces, not eight.
	if got := c.printed[len(c.printed)-1]; got != "a"+strings.Repeat(" ", 7)+"b" {
		t.Errorf("tab expansion = %q; count and drawing must be equal", got)
	}
}
