package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// shortRows keeps every frame row as short as its text (ADR-0094).
//
// The input box is drawn padded with spaces to the full width, so every
// frame row it owns is as wide as the terminal less one column whatever
// the operator typed. A row that wide breaks during a live resize: the
// renderer repaints at the width it was last told — the cursor blink alone
// repaints every ~530 ms — while the terminal is already narrower, the
// terminal wraps the row where the renderer does not count it, and a stale
// copy of the box stays on the screen (measured on iTerm2 and kitty with
// resizeprobe's trace, 2026-09-29). A row as short as its text wraps only
// when the text itself is wider than the window.
//
// Padding that carries a background — the input line's highlight — is not
// dropped from sight: it becomes an erase to the end of the line under the
// same background, which terminals fill with that background, followed by a
// move to the last column so that the renderer's own erase, which comes
// after the row's reset, clears only that one cell. The row looks as it
// did — the bar ends one column short of the edge, as the clipped padding
// did — and it follows the window's width instead of fixing it.
func shortRows(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = shortRow(l)
	}
	return strings.Join(lines, "\n")
}

// shortRow trims one row. It keeps every escape sequence, drops the spaces
// after the last visible character, and — when a dropped space was drawn
// on a background — erases to the end under that background.
func shortRow(line string) string {
	if !strings.HasSuffix(ansi.Strip(line), " ") {
		return line // no padding: nothing to do
	}
	type tok struct {
		s     string
		space bool
		esc   bool
		bg    bool // background active when this token is drawn
	}
	var toks []tok
	bg, rev := false, false
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			j := escEnd(line, i)
			seq := line[i:j]
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				bg, rev = sgrState(seq[2:len(seq)-1], bg, rev)
			}
			toks = append(toks, tok{s: seq, esc: true})
			i = j
			continue
		}
		j := i + 1
		for j < len(line) && line[j] != 0x1b && line[j]&0xC0 == 0x80 {
			j++ // the rest of a UTF-8 rune
		}
		// A space in reverse video is not padding: it is the input box's
		// cursor, drawn as an inverted blank cell.
		toks = append(toks, tok{s: line[i:j], space: line[i] == ' ' && !rev, bg: bg})
		i = j
	}
	last := -1 // the last visible, non-space token
	for i, t := range toks {
		if !t.esc && !t.space {
			last = i
		}
	}
	var b strings.Builder
	barred := false
	for i, t := range toks {
		switch {
		case i <= last || t.esc:
			b.WriteString(t.s)
		case t.bg && !barred:
			// The first padding cell on a background: paint the rest of
			// the row with it, and leave the cursor where the renderer's
			// own erase can take only the last cell.
			b.WriteString("\x1b[K\x1b[999C")
			barred = true
		}
	}
	return b.String()
}

// escEnd returns the index just past the escape sequence starting at i:
// CSI up to its final byte, OSC up to BEL or ST, anything else two bytes.
func escEnd(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[':
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
		return len(s)
	case ']':
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	}
	return i + 2
}

// sgrState applies one SGR parameter list to two facts: a background is
// set, and video is reversed.
func sgrState(params string, bg, rev bool) (bool, bool) {
	if params == "" {
		return false, false // CSI m is a reset
	}
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		n, err := strconv.Atoi(ps[i])
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			bg, rev = false, false
		case n == 7:
			rev = true
		case n == 27:
			rev = false
		case n == 49:
			bg = false
		case (n >= 40 && n <= 47) || (n >= 100 && n <= 107):
			bg = true
		case n == 48:
			bg = true
			// 48;5;N or 48;2;R;G;B: skip the colour's own numbers.
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		case n == 38:
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
	return bg, rev
}
