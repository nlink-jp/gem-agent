package tui

import (
	"io"
	"strconv"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// ShrinkMode is what the model does when the terminal narrows (ADR-0094).
//
// The terminal re-wraps the frame's lines before the model hears of the
// resize, and Bubble Tea repaints relative to the cursor, so the rows the
// frame gained stay on screen as stale copies of the input box. The zero
// value is ADR-0021 §9's answer: clear the screen. The other two are
// measurement arms, built so `make resizeprobe` can compare them in a real
// terminal before ADR-0094 chooses; the product uses the zero value.
type ShrinkMode int

const (
	// ShrinkClearScreen clears the whole screen and resets the row
	// counter (ADR-0021 §9). The control, and the product's behaviour.
	ShrinkClearScreen ShrinkMode = iota
	// ShrinkLeaveAlone sweeps nothing and leaves the counter as it is:
	// the control for the reflow itself, the only arm that does not act
	// on the rows it is measuring.
	ShrinkLeaveAlone
	// ShrinkEraseFrame erases only the rows the drawn frame occupies
	// after re-wrapping, through a SweepWriter. Without one it falls back
	// to ShrinkClearScreen: an arm that silently did nothing would be
	// measured as the other control.
	ShrinkEraseFrame
)

// String names the arm as the probe and its report spell it.
func (s ShrinkMode) String() string {
	switch s {
	case ShrinkLeaveAlone:
		return "none"
	case ShrinkEraseFrame:
		return "erase"
	default:
		return "clear"
	}
}

// File is what Bubble Tea needs of its output to treat it as a terminal
// (its term.File): the size is read through Fd, so a writer that hid the
// descriptor would silence every resize.
type File interface {
	io.ReadWriteCloser
	Fd() uintptr
}

// SweepWriter sits between Bubble Tea's renderer and the terminal and
// delivers ShrinkEraseFrame's sweep (ADR-0094 C).
//
// Neither of the model's own channels can: a line queued with tea.Println
// is always followed by "\r\n" and so costs a row of history per resize,
// and a prefix in View() is lost when a later view replaces it before the
// tick, or repeated when a repaint sends it again. What the renderer does
// guarantee is its cursor: between flushes it rests at column 0 of the
// region's last line, and every flush of a region taller than one line
// begins with CSI n A, n = linesRendered-1. The writer rewrites the first
// such flush after an arm into CSI n+K A followed by an erase-below — one
// write, from the renderer's own count.
//
// It also tracks which frame was last FLUSHED, because K has to describe
// what is on the screen: the model sees views the renderer may replace
// before a tick ever writes them.
type SweepWriter struct {
	out File

	mu      sync.Mutex
	latest  []string // the newest view the model produced
	drawn   []string // the view current at the last flush
	pending int      // rows to add to the next flush's cursor-up; 0 = none
	flushes int
	sweeps  int
}

// NewSweepWriter wraps the terminal Bubble Tea would otherwise write to.
func NewSweepWriter(out File) *SweepWriter { return &SweepWriter{out: out} }

// Write passes the renderer's bytes through, rewriting the first flush
// after an arm.
func (w *SweepWriter) Write(p []byte) (int, error) {
	written := len(p)
	w.mu.Lock()
	n, isFlush := flushCursorUp(p)
	if isFlush {
		w.flushes++
		w.drawn = w.latest
		if w.pending > 0 {
			p = sweepFlush(p, n, w.pending)
			w.pending = 0
			w.sweeps++
		}
	}
	w.mu.Unlock()
	if _, err := w.out.Write(p); err != nil {
		return 0, err
	}
	// The caller wrote all of ITS bytes; the rewrite is ours.
	return written, nil
}

func (w *SweepWriter) Read(p []byte) (int, error) { return w.out.Read(p) }
func (w *SweepWriter) Close() error               { return w.out.Close() }
func (w *SweepWriter) Fd() uintptr                { return w.out.Fd() }

// Stats reports how many flushes passed and how many carried a sweep, so a
// probe can tell how often a drag's size reports arrived with no flush
// between them (ADR-0094, question 4).
func (w *SweepWriter) Stats() (flushes, sweeps int) {
	if w == nil {
		return 0, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushes, w.sweeps
}

// note records the view the model just produced. Nil-safe: a model
// without a writer calls it too.
func (w *SweepWriter) note(view string) {
	if w == nil {
		return
	}
	lines := strings.Split(view, "\n")
	w.mu.Lock()
	w.latest = lines
	w.mu.Unlock()
}

// arm computes K for the drawn frame at the new width and schedules the
// sweep. It returns the drawn frame's line count and K, which the model
// needs to set its counter. A second arm before a flush REPLACES the
// first: the screen still holds the frame drawn before both, re-wrapped
// to the latest width, and nothing was swept yet.
func (w *SweepWriter) arm(width int) (lines, k int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	k = grownRows(w.drawn, width)
	w.pending = k
	return len(w.drawn), k
}

// grownRows is how many rows a drawn frame gained by re-wrapping at width:
// every line above the last adds its extra rows. The last line is where
// the cursor rests; its own tail wraps below the cursor, where the erase
// reaches anyway. physicalRows is the counter's wrap model, so K and the
// counter agree about what a line costs.
func grownRows(view []string, width int) int {
	k := 0
	for i := 0; i+1 < len(view); i++ {
		k += physicalRows(view[i], width) - 1
	}
	return k
}

// flushCursorUp reports whether p is a renderer flush and the cursor-up it
// begins with. A flush of a multi-line region begins with CSI n A (CSI A
// when n is 1); nothing else the renderer writes does — its other writes
// are mode switches, the title, and the clear. A flush of a one-line region
// begins without a cursor-up and is not recognised; the inline frame is
// never shorter than two lines.
func flushCursorUp(p []byte) (int, bool) {
	if len(p) < 3 || p[0] != 0x1b || p[1] != '[' {
		return 0, false
	}
	i := 2
	for i < len(p) && p[i] >= '0' && p[i] <= '9' {
		i++
	}
	if i >= len(p) || p[i] != 'A' {
		return 0, false
	}
	if i == 2 {
		return 1, true
	}
	n, err := strconv.Atoi(string(p[2:i]))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// sweepFlush replaces a flush's leading CSI n A with a move k rows higher
// and an erase of everything below: the renderer then paints its frame
// from the re-wrapped frame's top. The carriage return first, because CSI
// J erases from the cursor, and the cursor's column is the one thing this
// does not take on trust.
func sweepFlush(p []byte, n, k int) []byte {
	head := len("\x1b[A")
	if n > 1 {
		head = len("\x1b[" + strconv.Itoa(n) + "A")
	}
	out := make([]byte, 0, len(p)+16)
	out = append(out, '\r', 0x1b, '[')
	out = strconv.AppendInt(out, int64(n+k), 10)
	out = append(out, 'A', 0x1b, '[', 'J')
	return append(out, p[head:]...)
}

// shrinkSweep answers a genuine width shrink (ADR-0094). termWidth is the
// terminal's own width — what it re-wrapped to — not the model's clamped
// one.
func (m *Model) shrinkSweep(termWidth int) tea.Cmd {
	switch {
	case m.shrink == ShrinkLeaveAlone:
		return nil
	case m.shrink == ShrinkEraseFrame && m.sweep != nil:
		lines, k := m.sweep.arm(termWidth)
		if k == 0 {
			return nil // nothing re-wrapped: the frame is where it was drawn
		}
		// The new frame occupies exactly the rows the sweep erased: the
		// drawn frame's lines plus the K it gained, ending where it ended.
		rows := lines + k
		if max := m.height - 1; rows > max {
			rows = max
		}
		m.hold.printed = m.height - 1 - rows
		if m.hold.printed < 0 {
			m.hold.printed = 0
		}
		m.hold.lastTotal = rows
		return nil
	}
	m.hold.printed = 0 // the clear empties the viewport
	m.hold.lastTotal = 0
	return tea.ClearScreen
}
