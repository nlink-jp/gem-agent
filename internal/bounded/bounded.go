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

// HeadTail keeps the first three quarters and the last quarter of limit
// bytes written to it, counts the whole, and — once the stream outgrows
// limit — tees all of it to a spool opened on demand, up to spoolCap
// bytes (ADR-0096 §3). A process whose closing lines carry its result
// keeps them; nothing it printed is lost while the spool holds.
//
// open is called once, on the first write past limit, so output that
// fits leaves no file. A nil open means there is nowhere to save.
type HeadTail struct {
	mu       sync.Mutex
	limit    int
	spoolCap int64
	open     func() (io.WriteCloser, string, error)

	head  []byte // everything until overflow; then the first headLimit bytes
	tail  []byte // after overflow: at least the last tailLimit bytes
	total int64
	over  bool

	spool   io.WriteCloser
	path    string
	saved   int64
	saveErr error
}

// NewHeadTail returns a HeadTail keeping limit bytes in memory.
func NewHeadTail(limit int, spoolCap int64, open func() (io.WriteCloser, string, error)) *HeadTail {
	return &HeadTail{limit: limit, spoolCap: spoolCap, open: open}
}

func (b *HeadTail) headLimit() int { return b.limit * 3 / 4 }
func (b *HeadTail) tailLimit() int { return b.limit - b.headLimit() }

func (b *HeadTail) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(p))
	if !b.over {
		if len(b.head)+len(p) <= b.limit {
			b.head = append(b.head, p...)
			return len(p), nil
		}
		// The first overflowing write: everything so far plus p goes to
		// the spool and is split into the head and the start of the
		// tail. p alone may be larger than the whole limit.
		b.over = true
		all := append(b.head, p...)
		b.startSpool(all)
		hl := b.headLimit()
		b.head = append([]byte(nil), all[:hl]...)
		b.tail = append([]byte(nil), all[hl:]...)
	} else {
		b.toSpool(p)
		b.tail = append(b.tail, p...)
	}
	// Compact only when the tail has doubled, so a stream of small
	// writes costs amortised constant time.
	if n := b.tailLimit(); len(b.tail) > 2*n+utf8.UTFMax {
		b.tail = append([]byte(nil), b.tail[len(b.tail)-n-utf8.UTFMax:]...)
	}
	return len(p), nil
}

func (b *HeadTail) startSpool(first []byte) {
	if b.open == nil {
		return
	}
	f, path, err := b.open()
	if err != nil {
		b.saveErr = err
		return
	}
	b.spool, b.path = f, path
	b.toSpool(first)
}

func (b *HeadTail) toSpool(p []byte) {
	if b.spool == nil || b.saveErr != nil {
		return
	}
	if room := b.spoolCap - b.saved; int64(len(p)) > room {
		p = p[:room]
	}
	if len(p) == 0 {
		return
	}
	n, err := b.spool.Write(p)
	b.saved += int64(n)
	if err != nil {
		b.saveErr = err
	}
}

// Close ends the spool. Call it after the last write: exec.Cmd.Run
// returns only once its pipe copiers have.
func (b *HeadTail) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spool == nil {
		return
	}
	if err := b.spool.Close(); err != nil && b.saveErr == nil {
		b.saveErr = err
	}
	b.spool = nil
}

// HeadTailView is what a HeadTail holds. When Over is false Head is
// the whole output and the rest is zero.
type HeadTailView struct {
	Head, Tail []byte // both cut on rune boundaries
	TailStart  int64  // byte offset of Tail in the whole output
	Total      int64
	Over       bool
	Path       string // the spool, "" when none was opened
	Saved      int64
	SaveErr    error
}

// View returns the kept bytes and the spool's state.
func (b *HeadTail) View() HeadTailView {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := HeadTailView{Total: b.total, Over: b.over, Path: b.path, Saved: b.saved, SaveErr: b.saveErr}
	if !b.over {
		v.Head = b.head
		return v
	}
	// The head is exactly headLimit bytes after the first overflow, so
	// CutRunes would return it whole; the cut can still split a rune.
	v.Head = TrimIncompleteRune(b.head)
	tail := b.tail
	if n := b.tailLimit(); len(tail) > n {
		tail = tail[len(tail)-n:]
	}
	// At most UTFMax-1 leading continuation bytes belong to a rune cut
	// off before the tail; past that the output is not UTF-8 and is kept.
	for i := 0; i < utf8.UTFMax-1 && len(tail) > 0 && !utf8.RuneStart(tail[0]); i++ {
		tail = tail[1:]
	}
	v.Tail, v.TailStart = tail, b.total-int64(len(tail))
	return v
}
