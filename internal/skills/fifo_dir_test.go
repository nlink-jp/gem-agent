package skills

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A skills directory that is a FIFO holds no skills, and says so at
// once (ADR-0120). It was opened plainly to be listed, and a FIFO
// waits for a writer: the project's held discovery, and startup with
// it. The operator's own is read the same way.
func TestDiscoverDoesNotWaitOnASkillsDirectoryThatIsAFifo(t *testing.T) {
	good := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "fine"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fine", "SKILL.md"), []byte("---\nname: fine\ndescription: a skill\n---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("the project's", func(t *testing.T) {
		proj, global := t.TempDir(), filepath.Join(t.TempDir(), "skills")
		good(global)
		fifo := fifoAt(t, filepath.Join(proj, ".claude", "skills"))
		var list []Skill
		returnsAtOnce(t, fifo, func() { list, _ = Discover(global, proj, DefaultLimits()) })
		defer CloseAll(list)
		if len(list) != 1 || list[0].Name != "fine" || list[0].Scope != "global" {
			t.Errorf("listed %v, want the global skill alone", list)
		}
	})
	t.Run("the operator's", func(t *testing.T) {
		proj := t.TempDir()
		good(filepath.Join(proj, ".claude", "skills"))
		fifo := fifoAt(t, filepath.Join(t.TempDir(), "skills"))
		var list []Skill
		returnsAtOnce(t, fifo, func() { list, _ = Discover(fifo, proj, DefaultLimits()) })
		defer CloseAll(list)
		if len(list) != 1 || list[0].Name != "fine" || list[0].Scope != "project" {
			t.Errorf("listed %v, want the project skill alone", list)
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
