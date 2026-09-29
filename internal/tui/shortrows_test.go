package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestShortRow(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"no padding", "┃ hello", "┃ hello"},
		{"plain padding dropped", "┃ hello      ", "┃ hello"},
		{"a blank row", "      ", ""},
		{"escapes kept, padding dropped", "\x1b[1m┃ hi\x1b[0m    ", "\x1b[1m┃ hi\x1b[0m"},
		{"padding on a background becomes an erase to the edge",
			"\x1b[48;5;236m┃ hi     \x1b[0m", "\x1b[48;5;236m┃ hi\x1b[K\x1b[999C\x1b[0m"},
		{"a truecolour background",
			"\x1b[38;2;1;2;3;48;2;9;9;9mhi  \x1b[m", "\x1b[38;2;1;2;3;48;2;9;9;9mhi\x1b[K\x1b[999C\x1b[m"},
		{"a foreground colour is not a background",
			"\x1b[38;5;48mhi   \x1b[0m", "\x1b[38;5;48mhi\x1b[0m"},
		{"a background reset before the padding",
			"\x1b[44mhi\x1b[49m   ", "\x1b[44mhi\x1b[49m"},
		{"inner spaces stay", "a  b   ", "a  b"},
		// The input box's cursor is a reversed blank at the end of the
		// text; dropping it hid the cursor and painted the bar in reverse
		// (seen on kitty, 2026-09-29).
		{"the cursor cell is kept and the bar follows it",
			"\x1b[48;5;0mhi\x1b[7m \x1b[0m\x1b[48;5;0m    \x1b[0m", "\x1b[48;5;0mhi\x1b[7m \x1b[0m\x1b[48;5;0m\x1b[K\x1b[999C\x1b[0m"},
		{"reverse ended before the padding", "\x1b[7mX\x1b[27m   ", "\x1b[7mX\x1b[27m"},
		{"wide runes stay whole", "日本  ", "日本"},
	} {
		if got := shortRow(tc.in); got != tc.want {
			t.Errorf("%s: shortRow(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// The point of it: the input line of an idle session is as wide as its
// text, not as the terminal — measured on the real model's view.
func TestTheInputRowIsAsShortAsItsText(t *testing.T) {
	for _, width := range []int{60, 120, 200} {
		m := sized(t, &capture{}, width, 30)
		m.ta.SetValue("short draft")
		for _, line := range strings.Split(m.View(), "\n") {
			if strings.Contains(line, "short draft") {
				if w := ansi.StringWidth(line); w > 20 {
					t.Errorf("width %d: the input row is %d cells for 11 of text: %q", width, w, line)
				}
			}
		}
	}
}
