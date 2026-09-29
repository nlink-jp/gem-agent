// Package inert makes text from outside the runtime inert for a terminal
// (ADR-0093): the control characters a terminal would act on are removed,
// and nothing else is.
//
// It removes characters, never sequence bodies. Every terminal control
// begins with ESC or a C1 introducer, so once those are gone nothing left
// can start a sequence, and what was a body is ordinary text. Stripping a
// whole sequence would hide text: an OSC that is never terminated runs to
// the end of the string, and in an approval dialog that is the rest of the
// command — the spoof the terminal itself performs (measured, ADR-0093).
//
// The callers are the ingresses — the TUI's messages and callbacks, and the
// plain entrances' terminal streams — and nothing downstream of them. A
// renderer that also called this would be a second mechanism, and one hides
// the absence of the other; internal/archtest pins the callers.
package inert

import (
	"io"
	"strings"
	"unicode/utf8"
)

// Removed reports whether r is a character this package removes: a C0
// control other than tab and newline, DEL, a C1 control, or a bidirectional
// embedding, override or isolate. The set is finite and this is the one
// place it is written.
func Removed(r rune) bool {
	switch {
	case r == '\t' || r == '\n':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// String returns s with every Removed character dropped and invalid UTF-8
// replaced by U+FFFD, so a raw 8-bit C1 byte cannot survive as a byte.
func String(s string) string {
	clean := true
	for _, r := range s {
		if r == utf8.RuneError || Removed(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToValidUTF8(s, string(utf8.RuneError)) {
		if !Removed(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Writer returns a writer that writes String of what it is given to w.
//
// A rune split between two writes is held until the next write rather than
// turned into two U+FFFD: the tail of a write that is an incomplete UTF-8
// sequence (at most three bytes) waits for the bytes that complete it. A
// tail that is never completed is never written — it could not have been a
// character anyway.
func Writer(w io.Writer) io.Writer { return &writer{w: w} }

type writer struct {
	w       io.Writer
	pending []byte
}

func (x *writer) Write(p []byte) (int, error) {
	buf := append(x.pending, p...)
	cut := len(buf) - incompleteTail(buf)
	x.pending = append([]byte(nil), buf[cut:]...)
	if cut == 0 {
		return len(p), nil
	}
	if _, err := io.WriteString(x.w, String(string(buf[:cut]))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// incompleteTail is the length of the suffix of b that begins a UTF-8
// sequence the bytes after it have not completed yet — 0 when b ends on a
// whole rune or on bytes that can never become one.
func incompleteTail(b []byte) int {
	for n := 1; n <= utf8.UTFMax-1 && n <= len(b); n++ {
		c := b[len(b)-n]
		if c < 0x80 {
			return 0 // ASCII: nothing after it is pending
		}
		if c >= 0xc0 { // a leading byte, n bytes from the end
			if !utf8.FullRune(b[len(b)-n:]) {
				return n
			}
			return 0
		}
		// a continuation byte: look further back for its leader
	}
	return 0
}
