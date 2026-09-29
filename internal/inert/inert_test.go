package inert

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStringRemovesControlsAndKeepsText(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain text is returned as is", "日本語 and ascii\tcol\nnext", "日本語 and ascii\tcol\nnext"},
		// The body of a sequence stays as text: only the introducer and the
		// terminator are controls (ADR-0093 §1).
		{"OSC title, BEL", "a\x1b]0;PWN\ab", "a]0;PWNb"},
		{"OSC, ST", "a\x1b]2;PWN\x1b\\b", "a]2;PWN\\b"},
		{"OSC 52", "\x1b]52;c;RVNDUFdO\a", "]52;c;RVNDUFdO"},
		{"CSI cursor and erase", "\x1b[2J\x1b[3;60Hx", "[2J[3;60Hx"},
		{"SGR", "\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"APC kitty", "\x1b_Gf=100;AAAA\x1b\\", "_Gf=100;AAAA\\"},
		{"DCS", "\x1bPq#0\x1b\\", "Pq#0\\"},
		// An unterminated OSC must not take the rest of the text with it:
		// that is the approval-dialog spoof the terminal performs.
		{"unterminated OSC keeps what follows", "ls \x1b]0; ; rm -rf ~", "ls ]0; ; rm -rf ~"},
		{"CR", "safe\rrm -rf ~", "saferm -rf ~"},
		{"CRLF keeps the newline", "a\r\nb", "a\nb"},
		{"BS BEL VT FF NUL", "a\b\a\v\f\x00b", "ab"},
		{"DEL", "a\x7fb", "ab"},
		{"C1 as UTF-8", "a\u009b2J\u009d0;x\u009cb", "a2J0;xb"},
		{"C1 as a raw byte becomes U+FFFD", "a\x9b2Jb", "a�2Jb"},
		{"bidi override and isolate", "abc\u202edef\u2066g\u2069", "abcdefg"},
		{"marks that do not reorder stay", "a\u200eb\u200fc", "a\u200eb\u200fc"},
		{"emoji with ZWJ stays", "👩\u200d💻", "👩\u200d💻"},
	} {
		if got := String(tc.in); got != tc.want {
			t.Errorf("%s: String(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// Every rune the terminal could act on is in the set, and nothing printable
// is: the predicate is checked over the whole range it decides on.
func TestRemovedIsExactlyTheControls(t *testing.T) {
	for r := rune(0); r <= 0x2fff; r++ {
		want := (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f ||
			(r >= 0x80 && r <= 0x9f) ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
		if Removed(r) != want {
			t.Errorf("Removed(%U) = %v, want %v", r, Removed(r), want)
		}
	}
}

// Whatever goes in, what comes out is valid UTF-8 with no removed rune in
// it. The seeds are the classes above; `go test -fuzz` widens them.
func FuzzStringIsInert(f *testing.F) {
	for _, s := range []string{"\x1b]0;x\a", "\x9b\x9d", "a\r\nb", "\u202e", "日本", "\xe3\x81"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := String(s)
		if !utf8.ValidString(out) {
			t.Fatalf("String(%q) = %q is not valid UTF-8", s, out)
		}
		for _, r := range out {
			if Removed(r) {
				t.Fatalf("String(%q) = %q still holds %U", s, out, r)
			}
		}
	})
}

// A rune split across two writes arrives whole: the model's deltas are
// whole strings, but a plain writer may be handed any cut of the bytes.
func TestWriterJoinsARuneSplitAcrossWrites(t *testing.T) {
	var buf bytes.Buffer
	w := Writer(&buf)
	whole := []byte("前\x1b]0;x\a後")
	for i := range whole {
		n, err := w.Write(whole[i : i+1])
		if err != nil || n != 1 {
			t.Fatalf("Write byte %d: n=%d err=%v", i, n, err)
		}
	}
	if got := buf.String(); got != "前]0;x後" {
		t.Fatalf("byte-at-a-time writes produced %q", got)
	}
}

func TestWriterReportsTheBytesItWasGiven(t *testing.T) {
	var buf bytes.Buffer
	w := Writer(&buf)
	in := []byte("a\x1b[2Jb\xe3")
	n, err := w.Write(in)
	if err != nil || n != len(in) {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(in))
	}
	if buf.String() != "a[2Jb" {
		t.Fatalf("wrote %q; the incomplete tail should wait", buf.String())
	}
	if _, err := w.Write([]byte("\x81\x82!")); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "あ!") {
		t.Fatalf("the completed rune did not arrive: %q", buf.String())
	}
}

func TestIncompleteTail(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0}, {"abc", 0}, {"あ", 0},
		{"a\xe3", 1}, {"a\xe3\x81", 2}, {"a\xf0\x9f\x91", 3},
		{"a\x81", 0},     // a stray continuation byte can never complete
		{"a\xff", 0},     // nor can a byte that is never a leader
		{"\x81\x81\x81", 0},
	} {
		if got := incompleteTail([]byte(tc.in)); got != tc.want {
			t.Errorf("incompleteTail(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
