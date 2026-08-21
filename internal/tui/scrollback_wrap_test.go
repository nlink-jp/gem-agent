package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// The footer/thought leak (fixed after v0.34.0): Bubble Tea's inline
// renderer prints tea.Println lines VERBATIM — no truncation, and no
// EraseLineRight for a line at or beyond the terminal width — so one
// over-wide scrollback line desyncs the cursor accounting and deposits
// the previous frame's top rows (thought line, running status) into
// scrollback. The invariant that kills the class: nothing handed to
// the printer may be as wide as the terminal.

func TestWrapForScrollbackBoundsEveryLine(t *testing.T) {
	cases := map[string]string{
		"ascii":  strings.Repeat("x", 100),
		"cjk":    strings.Repeat("あ", 60),
		"styled": "\x1b[38;5;242m" + strings.Repeat("y", 90) + "\x1b[0m",
		"mixed":  "⚙ save_memory content=" + strings.Repeat("慎重・知的な人物の", 20),
	}
	for name, in := range cases {
		out := wrapForScrollback(in, 40)
		for i, line := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(line); w > 39 {
				t.Errorf("%s line %d is %d cells (max 39): %q", name, i, w, line)
			}
		}
	}
	// Wrapping, not truncation: the ascii case loses nothing.
	out := wrapForScrollback(strings.Repeat("x", 100), 40)
	if joined := strings.ReplaceAll(out, "\n", ""); joined != strings.Repeat("x", 100) {
		t.Errorf("content lost: %d chars left", len(joined))
	}
	// Unknown width passes through untouched (startup, before the
	// first WindowSizeMsg).
	if got := wrapForScrollback("abc", 0); got != "abc" {
		t.Errorf("width 0: %q", got)
	}
}

// End-to-end through the emit funnel: a tool event with a long detail
// (the trigger observed in the field — save_memory with ~250 cells of
// content) must reach the printer only as lines strictly narrower than
// the terminal, and the printed-row accounting must match exactly.
func TestEmitNeverHandsOverwideLinesToPrintln(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	m = next.(Model)
	m.hold.printed = 500 // full-screen regime, accounting armed

	before := m.hold.printed
	next, _ = m.Update(ToolCall{Name: "save_memory",
		Detail: "content=" + strings.Repeat("露見や恥を恐れる人物が感情を曝け出す", 12) + " name=climax scope=project"})
	m = next.(Model)

	var lines []string
	for _, p := range c.printed {
		lines = append(lines, strings.Split(p, "\n")...)
	}
	if len(lines) < 2 {
		t.Fatalf("expected a wrapped multi-line tool event, got %d lines", len(lines))
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w > 59 {
			t.Errorf("printed line %d is %d cells — reaches the terminal width and will soft-wrap: %q", i, w, line)
		}
	}
	if got := m.hold.printed - before; got != len(lines) {
		t.Errorf("printed accounting %d != %d physical lines handed to Println", got, len(lines))
	}
}
