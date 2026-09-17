package tui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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
	// Erase-below, then the payload byte for byte. The erase is required:
	// Bubble Tea flushes from the top of its own frame with only
	// EraseLineRight, so without it every cell of the old frame to the
	// right of a narrower picture survives on every row the picture
	// covers — measured on iTerm2 as three stranded footers for three
	// images, with the row count already correct.
	if want := ansi.EraseScreenBelow + payload; c.printed[0] != want {
		t.Error("an image line must be exactly erase-below plus the payload, unaltered")
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

func realPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x80, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestDrawImageAccountsForWhatItDraws is the end of the lane: a tool's
// picture arrives, a box is declared, and the counter is told that box.
// The payload on screen and the number in the accounting must be the same
// box, because a mismatch is what strands a frame.
func TestDrawImageAccountsForWhatItDraws(t *testing.T) {
	c := &capture{}
	m := sized(t, c, 80, 30)
	m.images = termimg.ITerm2

	before := m.hold.printed
	m.drawImage(Image{Data: realPNG(t, 320, 180), MIME: "image/png"})
	if len(c.printed) != 1 {
		t.Fatalf("printed %d times, want one write", len(c.printed))
	}
	drew := c.printed[0]
	accounted := m.hold.printed - before

	// The box in the bytes and the box in the accounting are one box.
	want := termimg.BoxFor(320, 180, 80, maxImageRows(30))
	if !strings.Contains(drew, fmt.Sprintf("height=%d", want.Rows)) ||
		!strings.Contains(drew, fmt.Sprintf("width=%d", want.Cols)) {
		t.Errorf("payload does not declare the box %+v", want)
	}
	if accounted != want.Rows {
		t.Errorf("accounted %d rows, drew a box of %d", accounted, want.Rows)
	}
	if want.Cols >= 80 {
		t.Errorf("box reaches the terminal width (%d); the terminal would wrap the picture", want.Cols)
	}
}

// TestDrawImageRefusesSilently: every refusal costs nothing on screen. A
// line per undrawable image is a report rather than a control, and the
// model's own note — path and view_image — is unchanged either way.
func TestDrawImageRefusesSilently(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proto termimg.Protocol
		data  []byte
	}{
		{"no protocol this session", termimg.None, realPNG(t, 64, 64)},
		{"bytes that are not an image", termimg.ITerm2, []byte("MIME said image; bytes disagree")},
		{"nothing at all", termimg.ITerm2, nil},
		{"past the ceiling", termimg.ITerm2, make([]byte, termimg.MaxBytes+1)},
	} {
		c := &capture{}
		m := sized(t, c, 80, 30)
		m.images = tc.proto
		before := m.hold.printed
		if cmd := m.drawImage(Image{Data: tc.data, MIME: "image/png"}); cmd != nil {
			t.Errorf("%s: returned a command", tc.name)
		}
		if len(c.printed) != 0 {
			t.Errorf("%s: printed %q", tc.name, c.printed)
		}
		if m.hold.printed != before {
			t.Errorf("%s: accounted %d rows for something it did not draw", tc.name, m.hold.printed-before)
		}
	}
}

// TestMaxImageRowsLeavesTheConversation: printing N rows scrolls N rows of
// history away, so a picture that fills the screen costs the operator the
// reply it belongs to.
func TestMaxImageRowsLeavesTheConversation(t *testing.T) {
	for _, tc := range []struct{ height, want int }{
		{0, 10}, {1, 1}, {3, 1}, {12, 4}, {30, 10}, {60, 20},
	} {
		if got := maxImageRows(tc.height); got != tc.want {
			t.Errorf("maxImageRows(%d) = %d, want %d", tc.height, got, tc.want)
		}
		// Never more than the screen minus a row — except on a screen of
		// one row, where one row is all there is and the question is
		// moot.
		if tc.height > 0 {
			ceiling := tc.height - 1
			if ceiling < 1 {
				ceiling = 1
			}
			if got := maxImageRows(tc.height); got > ceiling {
				t.Errorf("height %d: %d rows leaves no screen", tc.height, got)
			}
		}
	}
}
