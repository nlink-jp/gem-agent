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
// plain entrances' terminal streams — plus the one thing downstream that can
// make a control out of text with none: the output of a transform over it
// (Styled). internal/archtest pins the callers.
package inert

import (
	"io"
	"strings"
	"sync"
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

// Styled is String for the output of a renderer this runtime runs over
// outside text: SGR sequences (ESC [ digits ; : m) — the only escapes the
// renderer writes, measured for glamour's dark, light and notty styles — are
// kept, and every other control goes as String removes it.
//
// It exists because a renderer is a transform, and a transform can make a
// control out of text that had none: goldmark decodes the character reference
// &#27; into a real ESC, after the ingress has already seen only "&#27;"
// (ADR-0093 §2). What the renderer is held to is what the runtime writes, not
// what the text contained. An SGR decoded that way can only style text.
func Styled(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r == utf8.RuneError || Removed(r) }) {
		return s
	}
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if n := sgrLen(s[i:]); n > 0 {
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if !Removed(r) {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// sgrLen is the length of the SGR sequence s begins with, or 0.
func sgrLen(s string) int {
	if !strings.HasPrefix(s, "\x1b[") {
		return 0
	}
	for i := 2; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9', c == ';', c == ':':
		case c == 'm':
			return i + 1
		default:
			return 0
		}
	}
	return 0
}

// Writer returns a writer that writes String of what it is given to w.
//
// A rune split between two writes is held until the next write rather than
// turned into two U+FFFD: the tail of a write that is an incomplete UTF-8
// sequence (at most three bytes) waits for the bytes that complete it. A
// tail that is never completed is never written — it could not have been a
// character anyway.
//
// It is safe for concurrent use, as the *os.File it usually wraps is: the
// plain REPL's interrupt handler and its turn both write to stderr.
func Writer(w io.Writer) io.Writer { return &writer{w: w} }

type writer struct {
	mu      sync.Mutex
	w       io.Writer
	pending []byte
}

func (x *writer) Write(p []byte) (int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
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
