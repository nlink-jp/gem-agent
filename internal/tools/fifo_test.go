package tools

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// The file tools reach each root through an os.Root opened by path, and
// os.OpenRoot opens plainly. The write lane can leave a FIFO where the
// work directory, an added directory or the project directory was
// (measured with the real profile, ADR-0120): the next open of that
// root — at /clear, at a resume, in the child that runs a read — waited
// for a writer. A FIFO is not a root, and is refused at once.
func TestRootsDoNotWaitOnAFifo(t *testing.T) {
	r := newRegistry(t)
	work, _ := filepath.EvalSymlinks(t.TempDir())
	if err := r.UseWorkDir(work); err != nil {
		t.Fatal(err)
	}
	fifo := fifoAt(t, filepath.Join(t.TempDir(), "pipe"))
	var err error

	returnsAtOnce(t, fifo, func() { err = r.UseWorkDir(fifo) })
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("UseWorkDir: %v, want not-a-directory", err)
	}
	if got := r.rootState().workDir; got != work {
		t.Errorf("the work directory became %q; the previous one stays when the new one cannot open", got)
	}

	returnsAtOnce(t, fifo, func() { err = r.AddDirs([]sandbox.AddedRoot{{Dir: fifo, Write: true}}) })
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("AddDirs: %v, want not-a-directory", err)
	}
	if n := len(r.AddedDirs()); n != 0 {
		t.Errorf("%d added directories, want none", n)
	}

	returnsAtOnce(t, fifo, func() { _, err = New(fifo, directExec, 0) })
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("New: %v, want not-a-directory", err)
	}
}

// fifoAt makes a FIFO at path, with its parent directories.
func fifoAt(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// returnsAtOnce runs fn and fails the test if it has not returned in
// two seconds. What waits on a FIFO — an open for reading or one for
// writing — is released by opening the FIFO at both ends, so the test
// binary can still exit.
func returnsAtOnce(t *testing.T, fifo string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(2 * time.Second):
	}
	for {
		if rw, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
			_ = rw.Close()
		}
		select {
		case <-done:
			t.Fatal("it waited on a FIFO")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
