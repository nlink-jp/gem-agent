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
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unicode/utf8"
)

// ErrNotRegular: the name is not a regular file.
var ErrNotRegular = errors.New("not a regular file")

// OpenRegular opens rel under root for reading, and only if it is a
// regular file. A plain open of a FIFO with no writer waits until one
// arrives, so a name the project chooses — a skill's SKILL.md, an
// instruction file, .mcp.json, the project config — could stop the
// caller for as long as it liked, at startup and before anything was
// asked. The kind is looked at first, and what is not a regular file
// is never opened.
//
// The open itself cannot wait either (O_NONBLOCK), and the kind is
// looked at again on what was opened: a name swapped for a FIFO between
// the look and the open is refused, where a plain open after the look
// waited on it — the swap hit within a few hundred opens every time it
// was tried (ADR-0120 §7). The flag is cleared before the file is
// handed back, so every read is what it was. The first form of this
// left the plain open in, because how such an open behaves on a file
// whose content is not on this disk — an online-only file of a cloud
// folder — had not been observed. It was, on iCloud Drive: the open
// returns at once and downloads nothing with the flag or without it,
// and the first read waits for the download and returns the bytes.
func OpenRegular(root *os.Root, rel string) (*os.File, error) {
	st, err := root.Stat(rel)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open", Path: rel, Err: ErrNotRegular}
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return regular(f, rel)
}

// OpenRegularPath is OpenRegular for a file named by path.
func OpenRegularPath(path string) (*os.File, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return regular(f, path)
}

// OpenRegularPathNoFollow is OpenRegularPath for a name that has to be
// the file itself: a link is refused, to a regular file as well. It is
// for a name in a directory the session's commands and the MCP servers
// write, where a look and then a plain open waited on a name swapped
// for a FIFO, and a link could lead to a file that never ends
// (ADR-0120 §9).
func OpenRegularPathNoFollow(path string) (*os.File, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	return openRegularNoFollow(path)
}

// openRegularNoFollow is OpenRegularPathNoFollow after its look: what
// still holds when the name is something else by now.
func openRegularNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return regular(f, path)
}

// regular takes a file opened without waiting, refuses it unless it is
// a regular file, and clears the flag the open carried.
func regular(f *os.File, name string) (*os.File, error) {
	st, err := f.Stat()
	if err == nil && !st.Mode().IsRegular() {
		err = &os.PathError{Op: "open", Path: name, Err: ErrNotRegular}
	}
	if err == nil {
		err = syscall.SetNonblock(int(f.Fd()), false)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// OpenDir opens the directory at path for listing. A name that is
// expected to be a directory — a skills directory, a session's work
// directory, a directory of the state tree — can be a FIFO instead, and
// a plain open of one waits for a writer: measured for
// <project>/.claude/skills, which held discovery, the trust probe and
// the pins, and for a work directory the write lane had replaced, which
// held the exit (ADR-0120). O_DIRECTORY makes the kernel answer "not a
// directory" at the open, without waiting, so there is nothing between
// a look and the open to swap; it is what os.ReadDir itself passes, and
// changes nothing about how a directory opens.
func OpenDir(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY, 0)
}

// OpenDirIn is OpenDir for rel under root.
func OpenDirIn(root *os.Root, rel string) (*os.File, error) {
	return root.OpenFile(rel, os.O_RDONLY|syscall.O_DIRECTORY, 0)
}

// OpenRoot is os.OpenRoot for a name that must be a directory already.
// os.OpenRoot opens plainly and looks afterwards, so a FIFO at the name
// holds it: the write lane can put one where the work directory, an
// added directory or the project directory was (measured, ADR-0120),
// and every loader that opened a root there by path waited. The kind is
// looked at first. os.OpenRoot takes no flags, so a name swapped for a
// FIFO between the look and the open still waits — the same residual
// OpenRegular has.
func OpenRoot(path string) (*os.Root, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.ENOTDIR}
	}
	return os.OpenRoot(path)
}

// WriteFile is os.WriteFile for a name that is a regular file or does
// not exist yet. Opened for writing, a FIFO waits for a reader, and
// writing a FIFO somebody reads is not writing the file: a memory, the
// policy file's temporary and the like are files this runtime writes
// and reads back (ADR-0120). The kind is looked at first, through a
// link as the write goes.
//
// The open cannot wait either, and the kind is looked at again on what
// was opened, as for a read: the first form of this looked and then
// called os.WriteFile, and a name swapped for a FIFO in between waited
// (ADR-0120 §9). Without waiting, a FIFO nobody reads does not open at
// all, and one somebody reads opens and is refused. The file is
// emptied after that look and not by the open, so nothing but a
// regular file is touched.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if st, err := os.Stat(path); err == nil && !st.Mode().IsRegular() {
		return &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	return writeFile(path, data, perm)
}

// writeFile is WriteFile after its look.
func writeFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NONBLOCK, perm)
	if errors.Is(err, syscall.ENXIO) {
		// What a FIFO with no reader, or a device that is not there,
		// answers an open that does not wait.
		return &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	if err != nil {
		return err
	}
	if f, err = regular(f, path); err != nil {
		return err
	}
	err = f.Truncate(0)
	if err == nil {
		_, err = f.Write(data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// RemoveAll is os.RemoveAll for a path whose parent may no longer be a
// directory. os.RemoveAll opens the parent plainly once the first
// removal fails, and a FIFO where the parent was held it: measured for
// a session's cache directory, whose parent is in the state tree
// (ADR-0120 §9). The parent is opened as a root, which is refused at
// once when it is not a directory, and the name is removed from it. A
// parent that is not there is nothing to remove, as for os.RemoveAll.
func RemoveAll(path string) error {
	path = filepath.Clean(path)
	root, err := OpenRoot(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(filepath.Base(path))
}

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
