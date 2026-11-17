package memory

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/bounded"
)

// Memories are files this runtime writes. A FIFO where one of them, a
// scope's directory or its marker should be is skipped; each was opened
// plainly, and held the load — every start — until somebody wrote to it
// (ADR-0120).
func TestLoadDoesNotWaitOnAFifo(t *testing.T) {
	const proj = "/some/project"
	lim := DefaultLimits()
	seed := func(t *testing.T) string {
		t.Helper()
		base := t.TempDir()
		if _, _, err := Save(base, proj, ScopeGlobal, "kept", "a fact", lim); err != nil {
			t.Fatal(err)
		}
		return base
	}
	projectDir := func(t *testing.T, base string) string {
		t.Helper()
		dir, err := Dir(base, proj, ScopeProject)
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}
	for name, place := range map[string]func(t *testing.T, base string) string{
		"a memory file":         func(t *testing.T, base string) string { return filepath.Join(base, "global", "stuck.md") },
		"the project directory": projectDir,
		"the project's marker": func(t *testing.T, base string) string {
			return filepath.Join(projectDir(t, base), ".project")
		},
	} {
		t.Run(name, func(t *testing.T) {
			base := seed(t)
			fifo := fifoAt(t, place(t, base))
			var mems []Memory
			returnsAtOnce(t, fifo, func() { mems, _ = Load(base, proj, lim) })
			if len(mems) != 1 || mems[0].Name != "kept" {
				t.Errorf("memories = %+v, want the one that is a file", mems)
			}
		})
	}
	t.Run("the global directory", func(t *testing.T) {
		base := t.TempDir()
		fifo := fifoAt(t, filepath.Join(base, "global"))
		var mems []Memory
		returnsAtOnce(t, fifo, func() { mems, _ = Load(base, proj, lim) })
		if len(mems) != 0 {
			t.Errorf("memories = %+v, want none", mems)
		}
	})
}

// A memory is saved over a file, never into a FIFO: opened for writing,
// one waits for a reader, and the save never came back.
func TestSaveDoesNotWaitOnAFifo(t *testing.T) {
	base := t.TempDir()
	fifo := fifoAt(t, filepath.Join(base, "global", "stuck.md"))
	var err error
	returnsAtOnce(t, fifo, func() { _, _, err = Save(base, "/some/project", ScopeGlobal, "stuck", "a fact", DefaultLimits()) })
	if !errors.Is(err, bounded.ErrNotRegular) {
		t.Errorf("err = %v, want one that is bounded.ErrNotRegular", err)
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
