package tui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeTerm records what reaches the "terminal". Its descriptor is not a
// terminal, so Bubble Tea reads no size from it and sends no queries.
type fakeTerm struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	writes []string
}

func (f *fakeTerm) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, string(p))
	return f.buf.Write(p)
}
func (f *fakeTerm) Read([]byte) (int, error) { return 0, nil }
func (f *fakeTerm) Close() error             { return nil }
func (f *fakeTerm) Fd() uintptr              { return ^uintptr(0) }

func (f *fakeTerm) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

func TestFlushCursorUp(t *testing.T) {
	for _, tc := range []struct {
		in    string
		n     int
		flush bool
	}{
		{"\x1b[Aline", 1, true},
		{"\x1b[12Aline", 12, true},
		{"\x1b[2J", 0, false},   // the clear: not a flush
		{"\x1b[?25l", 0, false}, // cursor hide
		{"\x1b[H", 0, false},
		{"\x1b]0;title\a", 0, false},
		{"\rfirst render", 0, false},
		{"\x1b[0A", 0, false},
		{"", 0, false},
	} {
		n, ok := flushCursorUp([]byte(tc.in))
		if n != tc.n || ok != tc.flush {
			t.Errorf("flushCursorUp(%q) = %d, %v; want %d, %v", tc.in, n, ok, tc.n, tc.flush)
		}
	}
}

func TestSweepFlushExtendsTheCursorUpAndErasesBelow(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n, k int
		want string
	}{
		{"\x1b[3Abody", 3, 2, "\r\x1b[5A\x1b[Jbody"},
		{"\x1b[Abody", 1, 4, "\r\x1b[5A\x1b[Jbody"},
		{"\x1b[10A", 10, 1, "\r\x1b[11A\x1b[J"},
	} {
		if got := string(sweepFlush([]byte(tc.in), tc.n, tc.k)); got != tc.want {
			t.Errorf("sweepFlush(%q, %d, %d) = %q, want %q", tc.in, tc.n, tc.k, got, tc.want)
		}
	}
}

// K counts the rows the frame GAINED above the cursor's line, with the
// counter's own wrap model — a double-width rune that does not fit wraps
// whole, which ceil(cells/width) would miss.
func TestGrownRows(t *testing.T) {
	long := strings.Repeat("x", 100)
	for _, tc := range []struct {
		name  string
		view  []string
		width int
		want  int
	}{
		{"nothing wraps", []string{"", "short", "footer", ""}, 80, 0},
		{"one line wraps once", []string{"", long, "footer", ""}, 80, 1},
		{"one line wraps twice", []string{"", long, "footer", ""}, 40, 2},
		{"blank pad rows never grow", []string{"", "", "", long, ""}, 40, 2},
		{"the cursor's own line is below the sweep", []string{"a", long}, 40, 0},
		// "xxあ" three times at width 3 wraps as xx|あx|xあ|xx|あ: five
		// rows, where ceil(12 cells / 3) says four.
		{"wide runes that do not fit wrap whole", []string{strings.Repeat("xxあ", 3), "f"}, 3, 4},
		{"empty", nil, 40, 0},
	} {
		if got := grownRows(tc.view, tc.width); got != tc.want {
			t.Errorf("%s: grownRows = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// The writer rewrites exactly the first flush after an arm, leaves every
// other write alone, and measures K on the frame that was DRAWN — not on
// a view the model produced after it.
func TestSweepWriterRewritesTheNextFlushOnce(t *testing.T) {
	term := &fakeTerm{}
	w := NewSweepWriter(term)
	long := strings.Repeat("x", 100)

	w.note("\n" + long + "\nfooter\n")
	mustWrite(t, w, "\x1b[3Aframe") // a flush: the view above is now drawn
	w.note("\nshort\nfooter\n")     // a newer view, never flushed

	lines, k := w.arm(80)
	if lines != 4 || k != 1 {
		t.Fatalf("arm = %d lines, K %d; want the DRAWN frame: 4 lines, K 1", lines, k)
	}
	mustWrite(t, w, "\x1b[?25l")    // not a flush: passes, sweep stays armed
	mustWrite(t, w, "\x1b[3Anext")  // the flush: rewritten
	mustWrite(t, w, "\x1b[3Aagain") // and only once

	got := term.all()
	want := []string{"\x1b[3Aframe", "\x1b[?25l", "\r\x1b[4A\x1b[Jnext", "\x1b[3Aagain"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("terminal received %q, want %q", got, want)
	}
	if f, s := w.Stats(); f != 3 || s != 1 {
		t.Errorf("stats = %d flushes, %d sweeps; want 3, 1", f, s)
	}
}

// Two size reports with no flush between them: the screen still holds the
// frame drawn before both, re-wrapped to the latest width. The second arm
// replaces the first; adding them would sweep over real history.
func TestSecondArmReplacesTheFirst(t *testing.T) {
	term := &fakeTerm{}
	w := NewSweepWriter(term)
	w.note("\n" + strings.Repeat("x", 100) + "\nfooter\n")
	mustWrite(t, w, "\x1b[3Aframe")

	if _, k := w.arm(80); k != 1 {
		t.Fatalf("first arm K = %d, want 1", k)
	}
	if _, k := w.arm(40); k != 2 {
		t.Fatalf("second arm K = %d, want 2", k)
	}
	mustWrite(t, w, "\x1b[3Anext")
	if got := term.all()[1]; got != "\r\x1b[5A\x1b[Jnext" {
		t.Errorf("flush = %q, want the second K alone (3+2)", got)
	}
}

func mustWrite(t *testing.T, w *SweepWriter, s string) {
	t.Helper()
	n, err := w.Write([]byte(s))
	if err != nil || n != len(s) {
		t.Fatalf("Write(%q) = %d, %v", s, n, err)
	}
}

// The three arms, at the model: what a genuine shrink returns and what it
// leaves in the counter.
func TestShrinkArms(t *testing.T) {
	sized := func(mode ShrinkMode, sweep *SweepWriter) Model {
		c := &capture{}
		m := New(Options{Printer: c.printer, Slash: slashStub, Shrink: mode, Sweep: sweep,
			RenderFactory: func(int) func(string) string { return func(s string) string { return s } }})
		next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m = next.(Model)
		m.hold.printed, m.hold.lastTotal = 26, 3
		return m
	}

	t.Run("clear is the default and the product's", func(t *testing.T) {
		m := sized(ShrinkClearScreen, nil)
		next, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
		m = next.(Model)
		if cmd == nil || m.hold.printed != 0 || m.hold.lastTotal != 0 {
			t.Errorf("clear: cmd %v, printed %d, lastTotal %d", cmd != nil, m.hold.printed, m.hold.lastTotal)
		}
	})
	t.Run("none sweeps nothing and keeps the counter", func(t *testing.T) {
		m := sized(ShrinkLeaveAlone, nil)
		next, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
		m = next.(Model)
		if cmd != nil || m.hold.printed != 26 || m.hold.lastTotal != 3 {
			t.Errorf("none: cmd %v, printed %d, lastTotal %d", cmd != nil, m.hold.printed, m.hold.lastTotal)
		}
	})
	t.Run("erase without a writer falls back to the clear", func(t *testing.T) {
		m := sized(ShrinkEraseFrame, nil)
		if _, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30}); cmd == nil {
			t.Error("an erase arm with nothing to erase through must not silently become 'none'")
		}
	})
	t.Run("erase sets the counter to the rows it erased", func(t *testing.T) {
		term := &fakeTerm{}
		w := NewSweepWriter(term)
		m := sized(ShrinkEraseFrame, w)
		m.ta.SetValue(strings.Repeat("y", 90)) // the input line re-wraps at 60
		view := m.View()
		mustWrite(t, w, "\x1b[3Aframe")
		lines := strings.Split(view, "\n")
		k := grownRows(lines, 60)
		if k == 0 {
			t.Fatalf("the test's frame does not re-wrap at 60:\n%s", view)
		}

		next, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
		m = next.(Model)
		if cmd != nil {
			t.Error("erase must not clear the screen")
		}
		rows := len(lines) + k
		if m.hold.lastTotal != rows || m.hold.printed != 30-1-rows {
			t.Errorf("counter: printed %d lastTotal %d; want %d and %d", m.hold.printed, m.hold.lastTotal, 30-1-rows, rows)
		}
		// The next view fills exactly the erased rows, ending where the
		// drawn frame ended.
		if got := len(strings.Split(m.View(), "\n")); got != rows {
			t.Errorf("next view is %d lines, want the %d erased rows", got, rows)
		}
	})
	t.Run("erase with nothing re-wrapped leaves everything", func(t *testing.T) {
		term := &fakeTerm{}
		w := NewSweepWriter(term)
		m := sized(ShrinkEraseFrame, w)
		m.View()
		mustWrite(t, w, "\x1b[3Aframe")
		next, cmd := m.Update(tea.WindowSizeMsg{Width: 99, Height: 30})
		m = next.(Model)
		if cmd != nil || m.hold.printed != 26 || m.hold.lastTotal != 3 {
			t.Errorf("K=0: cmd %v, printed %d, lastTotal %d", cmd != nil, m.hold.printed, m.hold.lastTotal)
		}
		if _, s := w.Stats(); s != 0 {
			t.Error("nothing to sweep, yet a sweep was armed")
		}
	})
}

// TestRealRendererFlushesBeginWithCursorUp drives bubbletea's own renderer
// through the writer. The sweep rests on one property of that renderer —
// every flush of a multi-line inline region begins with CSI n A — and this
// is the test that fails if an upgrade changes it, rather than the sweep
// quietly going inert (or worse) on the operator's screen.
func TestRealRendererFlushesBeginWithCursorUp(t *testing.T) {
	term := &fakeTerm{}
	w := NewSweepWriter(term)
	c := &capture{}
	m := New(Options{Printer: c.printer, Slash: slashStub, Shrink: ShrinkEraseFrame, Sweep: w,
		RenderFactory: func(int) func(string) string { return func(s string) string { return s } }})
	prog := tea.NewProgram(m, tea.WithOutput(w), tea.WithInput(nil), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := prog.Run(); done <- err }()

	waitFlushes := func(atLeast int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if f, _ := w.Stats(); f >= atLeast {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		f, _ := w.Stats()
		t.Fatalf("the renderer made %d recognisable flushes, want %d: its flushes no longer begin with a cursor-up, "+
			"and ShrinkEraseFrame depends on that (ADR-0094). writes: %q", f, atLeast, term.all())
	}

	prog.Send(tea.WindowSizeMsg{Width: 100, Height: 30})
	// The first render begins with "\r", not a cursor-up; every change
	// after it is a flush of the kind the sweep rewrites.
	for i := 0; i < 3; i++ {
		prog.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("z", 30))})
		time.Sleep(60 * time.Millisecond) // several renderer ticks: one frame each
	}
	waitFlushes(2)
	prog.Send(tea.WindowSizeMsg{Width: 50, Height: 30})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, s := w.Stats(); s == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	prog.Quit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, s := w.Stats(); s != 1 {
		t.Fatalf("sweeps = %d after one shrink, want 1; writes: %q", s, term.all())
	}
	swept := false
	for _, s := range term.all() {
		if strings.HasPrefix(s, "\r\x1b[") && strings.Contains(s[:12], "A\x1b[J") {
			swept = true
		}
	}
	if !swept {
		t.Errorf("no write carried the sweep: %q", term.all())
	}
}
