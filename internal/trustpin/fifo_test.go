package trustpin

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The pins are taken at startup and again after a tool writes. A
// <project>/.claude/skills that is a FIFO pins no skill; opened plainly
// to be listed it held both (ADR-0120).
func TestPinsDoNotWaitOnASkillsDirectoryThatIsAFifo(t *testing.T) {
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fifo := fifoAt(t, filepath.Join(proj, SkillsDir))
	var pins Pins
	returnsAtOnce(t, fifo, func() { pins, _ = Compute(proj) })
	if len(pins) != 1 || pins["AGENTS.md"] == "" {
		t.Errorf("pins = %v, want AGENTS.md alone", pins)
	}
	var name string
	returnsAtOnce(t, fifo, func() { name = PinName(proj, SkillsDir+"/x/SKILL.md") })
	if name != SkillsDir+"/x" {
		t.Errorf("PinName = %q", name)
	}
}

// The write lane can move the project directory aside and leave a FIFO
// under its name (measured, ADR-0120). Nothing is pinned there; the
// root that reads the instruction and configuration files was opened by
// that path, plainly, and waited.
func TestPinsDoNotWaitOnAProjectDirectoryThatIsAFifo(t *testing.T) {
	fifo := fifoAt(t, filepath.Join(t.TempDir(), "project"))
	var pins Pins
	returnsAtOnce(t, fifo, func() { pins, _ = Compute(fifo) })
	if len(pins) != 0 {
		t.Errorf("pins = %v, want none", pins)
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
