// Package bounded is the one place a read, a listing or a process
// output is capped (ADR-0073 §4). Every function that takes a cap
// returns whether the cap was reached, so a caller cannot obtain the
// bytes without also holding the fact that they are not all of them —
// twenty of the findings in ADR-0072 were caps without that fact, or
// reads with no cap at all. An architecture test (internal/archtest)
// forbids the unbounded primitives outside this package.
package bounded

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"sync"
	"unicode/utf8"
)

// ReadAll reads at most cap bytes from r. more reports that r held
// more than cap bytes; the returned data is then exactly cap bytes.
func ReadAll(r io.Reader, cap int) (data []byte, more bool, err error) {
	data, err = io.ReadAll(io.LimitReader(r, int64(cap)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > cap {
		return data[:cap], true, nil
	}
	return data, false, nil
}

// ReadDir lists at most n entries of the opened directory, reporting
// whether there were more. The caller owns d.
func ReadDir(d *os.File, n int) (entries []os.DirEntry, more bool, err error) {
	entries, err = d.ReadDir(n + 1)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	if len(entries) > n {
		return entries[:n], true, nil
	}
	return entries, false, nil
}

// Scanner returns a line scanner whose longest accepted line is max
// bytes: bufio.Scanner's default would stop at 64 KiB with an error
// the caller must know to look for.
func Scanner(r io.Reader, initial, max int) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, initial), max)
	return s
}

// Writer keeps the first limit bytes written to it and counts the
// rest: the output of a process that prints without end is bounded as
// it arrives, not after it exits (ADR-0072 §4.5). The kept bytes are
// cut on a rune boundary.
type Writer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
	total int64
}

// NewWriter returns a Writer keeping limit bytes.
func NewWriter(limit int) *Writer { return &Writer{limit: limit} }

// Write accepts everything and keeps a cap's worth.
func (b *Writer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += int64(n)
	if room := b.limit + 1 - len(b.buf); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf = append(b.buf, p...)
	}
	return n, nil
}

// Bytes returns the kept bytes, cut whole-rune at the limit, and
// whether more arrived than was kept.
func (b *Writer) Bytes() (data []byte, more bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) <= b.limit {
		return b.buf, false
	}
	return CutRunes(b.buf, b.limit), true
}

// Total is how many bytes were written in all.
func (b *Writer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// CutRunes returns the longest prefix of b within max bytes that ends
// on a rune boundary, so a cut never leaves a broken character. A b
// that already fits is returned whole — a complete b is not cut.
func CutRunes(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	return TrimIncompleteRune(b[:max])
}

// TrimIncompleteRune drops a UTF-8 sequence a byte cut left unfinished
// at the end of b: what ReadAll returns when more was true ends on
// whatever byte the cap landed on.
func TrimIncompleteRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// CombinedOutput runs cmd with both streams into a Writer of limit
// bytes and returns the kept output, whether it was cut, and the run
// error — exec.Cmd.CombinedOutput with a cap.
func CombinedOutput(cmd *exec.Cmd, limit int) (out []byte, more bool, err error) {
	w := NewWriter(limit)
	cmd.Stdout, cmd.Stderr = w, w
	err = cmd.Run()
	out, more = w.Bytes()
	return out, more, err
}
