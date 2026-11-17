package statedir

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The marker is a file this runtime writes, and every per-project state
// directory is claimed through it. One that is a FIFO claims nothing
// and is replaced like a missing one; read through a plain open it held
// the start (ADR-0120).
func TestMarkerThatIsAFifoIsNotWaitedOn(t *testing.T) {
	const proj = "/some/project"
	dir := filepath.Join(t.TempDir(), "state")
	fifo := fifoAt(t, filepath.Join(dir, Marker))
	returnsAtOnce(t, fifo, func() {
		if ok, note := MarkerMatches(dir, proj); !ok {
			t.Errorf("MarkerMatches refused: %s", note)
		}
	})
	var err error
	returnsAtOnce(t, fifo, func() { err = EnsureProjectDir(dir, proj) })
	if err != nil {
		t.Fatalf("EnsureProjectDir: %v", err)
	}
	data, err := os.ReadFile(fifo)
	if err != nil || strings.TrimSpace(string(data)) != proj {
		t.Errorf("the marker now reads %q, %v — want the project's path", data, err)
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
