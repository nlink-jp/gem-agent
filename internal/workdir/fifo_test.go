package workdir

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// The write lane writes the session's work directory, and that includes
// removing it and leaving a FIFO under its name (measured with the real
// profile, ADR-0120). The tidy-up at exit and at /clear opened the
// name plainly to see whether it was empty, and never came back.
func TestRemoveIfEmptyDoesNotWaitOnAFifo(t *testing.T) {
	fifo := fifoAt(t, filepath.Join(t.TempDir(), "work", "session"))
	returnsAtOnce(t, fifo, func() { RemoveIfEmpty(fifo) })
	if st, err := os.Lstat(fifo); err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("what was not an empty directory was touched: %v, %v", st, err)
	}
	// An empty directory still goes.
	dir := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	RemoveIfEmpty(dir)
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("an empty directory was kept: %v", err)
	}
}

// The listing of a project's work directories is read at every start.
// A FIFO where the directory that holds them should be is an error, at
// once.
func TestListDoesNotWaitOnAFifo(t *testing.T) {
	const proj = "/some/project"
	root := t.TempDir()
	t.Setenv(statedir.EnvRoot, root)
	fifo := fifoAt(t, filepath.Join(root, statedir.EscapeProject(proj), "work"))
	var err error
	returnsAtOnce(t, fifo, func() { _, _, err = List(proj, "") })
	if err == nil {
		t.Error("a FIFO was listed as the work directories")
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
