package bounded

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A name that has to be the file itself: a link is refused, to a
// regular file as well, and nothing that is not a regular file waits.
func TestOpenRegularPathNoFollowTakesOnlyTheFileItself(t *testing.T) {
	dir := t.TempDir()
	fifo := fifoAt(t, filepath.Join(dir, "pipe"))
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"to-file": "file", "to-pipe": "pipe", "to-zero": "/dev/zero"} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	f, err := OpenRegularPathNoFollow(filepath.Join(dir, "file"))
	if err != nil {
		t.Fatalf("a regular file: %v", err)
	}
	data, rerr := io.ReadAll(f)
	flags, ferr := fcntlGetFl(f)
	_ = f.Close()
	if rerr != nil || string(data) != "content" {
		t.Errorf("read %q, %v", data, rerr)
	}
	if ferr != nil || flags&syscall.O_NONBLOCK != 0 {
		t.Errorf("the descriptor is still non-blocking (flags %#x, %v)", flags, ferr)
	}
	for _, name := range []string{"pipe", "sub", "to-file", "to-pipe", "to-zero", "missing"} {
		var err error
		returnsAtOnce(t, fifo, func() {
			var f *os.File
			if f, err = OpenRegularPathNoFollow(filepath.Join(dir, name)); err == nil {
				_ = f.Close()
			}
		})
		if err == nil {
			t.Errorf("%s was opened", name)
		}
	}
	for _, name := range []string{"pipe", "sub", "to-file"} {
		if _, err := OpenRegularPathNoFollow(filepath.Join(dir, name)); !errors.Is(err, ErrNotRegular) {
			t.Errorf("%s: %v, want ErrNotRegular", name, err)
		}
	}
	// After the look, when the name is something else by now: a link is
	// not followed and a FIFO is refused, neither with a wait.
	for _, name := range []string{"pipe", "to-file", "to-pipe", "to-zero"} {
		var err error
		returnsAtOnce(t, fifo, func() {
			var f *os.File
			if f, err = openRegularNoFollow(filepath.Join(dir, name)); err == nil {
				_ = f.Close()
			}
		})
		if err == nil {
			t.Errorf("%s was opened after the look", name)
		}
	}
	if _, err := OpenRegularPathNoFollow(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing name: %v", err)
	}
}

func fcntlGetFl(f *os.File) (int, error) {
	r, _, e := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFL, 0)
	if e != 0 {
		return 0, e
	}
	return int(r), nil
}

// A FIFO is refused by the look, before it is opened: opening one,
// even without waiting, lets through whoever waits at its other end,
// and that is somebody else's pipe. Here somebody waits on each FIFO,
// and is still waiting after it was refused.
func TestAFifoIsRefusedWithoutBeingOpened(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	closing := func(f *os.File, err error) error {
		if err == nil {
			_ = f.Close()
		}
		return err
	}
	for how, c := range map[string]struct {
		waiter int // how the one at the other end opens it
		open   func(path string) error
	}{
		"under a root":       {os.O_WRONLY, func(p string) error { return closing(OpenRegular(root, filepath.Base(p))) }},
		"by path":            {os.O_WRONLY, func(p string) error { return closing(OpenRegularPath(p)) }},
		"by path, no link":   {os.O_WRONLY, func(p string) error { return closing(OpenRegularPathNoFollow(p)) }},
		"opened for writing": {os.O_RDONLY, func(p string) error { return WriteFile(p, []byte("x"), 0o644) }},
	} {
		t.Run(how, func(t *testing.T) {
			fifo := fifoAt(t, filepath.Join(dir, fmt.Sprintf("pipe-%d", len(how))))
			through := make(chan struct{})
			go func() {
				defer close(through)
				if f, err := os.OpenFile(fifo, c.waiter, 0); err == nil {
					_ = f.Close()
				}
			}()
			// Let the other end get as far as its open.
			time.Sleep(150 * time.Millisecond)
			if err := c.open(fifo); !errors.Is(err, ErrNotRegular) {
				t.Errorf("%v, want ErrNotRegular", err)
			}
			select {
			case <-through:
				t.Error("the FIFO was opened: the one waiting at its other end was let through")
			case <-time.After(300 * time.Millisecond):
			}
			// Let it through now, so nothing is left waiting.
			for released := false; !released; {
				if rw, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
					select {
					case <-through:
						released = true
					case <-time.After(50 * time.Millisecond):
					}
					_ = rw.Close()
				}
			}
		})
	}
}

// WriteFile after its look: the open cannot wait, and what was opened
// is looked at before anything is written. A FIFO nobody reads does not
// open; one somebody reads opens, is refused, and gets nothing.
func TestWriteFileOpensWithoutWaitingAndLooksAgain(t *testing.T) {
	dir := t.TempDir()
	unread := fifoAt(t, filepath.Join(dir, "unread"))
	var err error
	returnsAtOnce(t, unread, func() { err = writeFile(unread, []byte("new"), 0o644) })
	if !errors.Is(err, ErrNotRegular) {
		t.Errorf("a FIFO nobody reads: %v, want ErrNotRegular", err)
	}

	read := fifoAt(t, filepath.Join(dir, "read"))
	r, err := os.OpenFile(read, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	returnsAtOnce(t, read, func() { err = writeFile(read, []byte("new"), 0o644) })
	if !errors.Is(err, ErrNotRegular) {
		t.Errorf("a FIFO somebody reads: %v, want ErrNotRegular", err)
	}
	buf := make([]byte, 8)
	if n, _ := syscall.Read(int(r.Fd()), buf); n > 0 {
		t.Errorf("%q was written into the FIFO", buf[:n])
	}

	// A regular file is replaced whole, shorter content too; a new one
	// is made with the mode asked for.
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("something much longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(file, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "new" {
		t.Errorf("the file holds %q, %v", data, err)
	}
	fresh := filepath.Join(dir, "fresh")
	if err := writeFile(fresh, []byte("made"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, serr := os.Stat(fresh)
	if data, err := os.ReadFile(fresh); err != nil || string(data) != "made" || serr != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the new file holds %q (%v), mode %v (%v)", data, err, st, serr)
	}
}

// The name is swapped back and forth between a regular file and a FIFO
// while it is written again and again: every write comes back, done or
// refused, and none waits. A look and then os.WriteFile waited here.
func TestWriteFileDoesNotWaitOnANameSwappedForAFifo(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "name")
	if err := os.WriteFile(name, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	swapped := make(chan struct{})
	go func() {
		defer close(swapped)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			tmp := filepath.Join(dir, fmt.Sprintf("next-%d", i))
			if i%2 == 0 {
				if syscall.Mkfifo(tmp, 0o644) != nil {
					return
				}
			} else if os.WriteFile(tmp, []byte("content"), 0o644) != nil {
				return
			}
			if os.Rename(tmp, name) != nil {
				return
			}
		}
	}()
	var written, refused atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		start := time.Now()
		for time.Since(start) < 1500*time.Millisecond ||
			((written.Load() == 0 || refused.Load() == 0) && time.Since(start) < 8*time.Second) {
			err := WriteFile(name, []byte("written"), 0o644)
			switch {
			case err == nil:
				written.Add(1)
			case errors.Is(err, ErrNotRegular):
				refused.Add(1)
			default:
				t.Errorf("a write failed with %v", err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		close(stop)
		// As in the open's swap test: the FIFO that holds the write has
		// no name left to release it by.
		t.Fatalf("a write waited on a FIFO swapped in under the name (after %d written, %d refused)", written.Load(), refused.Load())
	}
	close(stop)
	<-swapped
	if written.Load() == 0 || refused.Load() == 0 {
		t.Errorf("%d written, %d refused: the swap was not exercised both ways", written.Load(), refused.Load())
	}
	t.Logf("%d written, %d refused, none waited", written.Load(), refused.Load())
}

// RemoveAll removes what os.RemoveAll removes, and does not wait when
// the parent is no longer a directory.
func TestRemoveAllDoesNotWaitOnAParentThatIsAFifo(t *testing.T) {
	dir := t.TempDir()
	fifo := fifoAt(t, filepath.Join(dir, "parent"))
	var err error
	returnsAtOnce(t, fifo, func() { err = RemoveAll(filepath.Join(fifo, "child")) })
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("under a FIFO: %v, want not a directory", err)
	}
	if st, err := os.Lstat(fifo); err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the FIFO was touched: %v, %v", st, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAll(filepath.Join(dir, "file", "child")); !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("under a file: %v, want not a directory", err)
	}
	for _, gone := range []string{filepath.Join(dir, "missing"), filepath.Join(dir, "missing", "child")} {
		if err := RemoveAll(gone); err != nil {
			t.Errorf("%s: %v, want nothing to remove", gone, err)
		}
	}

	// A tree with one of everything in it goes whole, and what a link
	// in it leads to stays.
	outside := filepath.Join(dir, "outside")
	tree := filepath.Join(dir, "tree")
	for _, d := range []string{outside, filepath.Join(tree, "a", "b"), filepath.Join(tree, "empty")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(outside, "kept"), filepath.Join(tree, "f"), filepath.Join(tree, "a", "b", "g")} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inner := fifoAt(t, filepath.Join(tree, "a", "pipe"))
	if err := os.Symlink(outside, filepath.Join(tree, "a", "out")); err != nil {
		t.Fatal(err)
	}
	returnsAtOnce(t, inner, func() { err = RemoveAll(tree) })
	if err != nil {
		t.Fatalf("a tree: %v", err)
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Errorf("the tree is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "kept")); err != nil {
		t.Errorf("what a link led to was removed: %v", err)
	}
	// A name that is a FIFO itself, and one that is a link, go as names.
	alone := fifoAt(t, filepath.Join(dir, "alone"))
	returnsAtOnce(t, alone, func() { err = RemoveAll(alone) })
	if _, lerr := os.Lstat(alone); err != nil || !os.IsNotExist(lerr) {
		t.Errorf("a FIFO by its own name: %v, %v", err, lerr)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAll(link); err != nil {
		t.Errorf("a link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "kept")); err != nil {
		t.Errorf("removing a link removed what it led to: %v", err)
	}
}
