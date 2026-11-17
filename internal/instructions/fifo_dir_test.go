package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// An instruction file is read through a root at its directory, opened
// by path. A FIFO where that directory should be — the project's, once
// the write lane has moved it aside, or the operator's own — held the
// load. Nothing is loaded from it, and the other directory still is
// (ADR-0120).
func TestLoadDoesNotWaitOnADirectoryThatIsAFifo(t *testing.T) {
	t.Run("the project directory", func(t *testing.T) {
		home, global := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(global, "AGENTS.md"), []byte("the operator's rules\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fifo := fifoAt(t, filepath.Join(home, "project"))
		var files []File
		returnsAtOnce(t, fifo, func() { files, _ = Load(fifo, home, global, DefaultLimits()) })
		if len(files) != 1 || !strings.Contains(files[0].Content, "the operator's rules") {
			t.Errorf("files = %+v, want the global file alone", files)
		}
	})
	t.Run("the global directory", func(t *testing.T) {
		home := t.TempDir()
		proj := filepath.Join(home, "project")
		if err := os.Mkdir(proj, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("the project's rules\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fifo := fifoAt(t, filepath.Join(t.TempDir(), "global"))
		var files []File
		returnsAtOnce(t, fifo, func() { files, _ = Load(proj, home, fifo, DefaultLimits()) })
		if len(files) != 1 || !strings.Contains(files[0].Content, "the project's rules") {
			t.Errorf("files = %+v, want the project file alone", files)
		}
	})
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
