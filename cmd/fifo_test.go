package cmd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The trust question is asked after a look at what the project offers,
// and the look lists <project>/.claude/skills. A FIFO of that name
// offers no skill; listed through a plain open it held the start before
// anything had been asked (ADR-0120).
func TestProbeProjectDoesNotWaitOnASkillsDirectoryThatIsAFifo(t *testing.T) {
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fifo := fifoAt(t, filepath.Join(proj, ".claude", "skills"))
	var o projectOffering
	returnsAtOnce(t, fifo, func() { o = probeProject(proj) })
	if o.Skills != 0 || len(o.Instructions) != 1 {
		t.Errorf("offering = %+v, want the instruction file and no skill", o)
	}
}

// The snapshots of the last session are files this runtime wrote. One
// that is a FIFO is no previous state, like one that is missing; a
// plain open of it waited (ADR-0120).
func TestSnapshotsDoNotWaitOnAFifo(t *testing.T) {
	fifo := fifoAt(t, filepath.Join(t.TempDir(), "persistent.json"))
	returnsAtOnce(t, fifo, func() {
		if snap := loadPersistentSnapshot(fifo); snap != nil {
			t.Errorf("persistent snapshot = %v, want none", snap)
		}
	})
	returnsAtOnce(t, fifo, func() {
		if snaps := loadAddedSnapshots(fifo); snaps != nil {
			t.Errorf("added snapshots = %v, want none", snaps)
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
