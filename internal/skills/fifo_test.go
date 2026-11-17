package skills

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A skill whose SKILL.md is a FIFO is skipped with a note. Discovery
// used to open it and wait for a writer: a project that held one
// stopped the runtime at startup, before anything was asked.
func TestDiscoverDoesNotWaitOnASkillFileThatIsAFifo(t *testing.T) {
	proj := t.TempDir()
	bad := filepath.Join(proj, ".claude", "skills", "stuck")
	good := filepath.Join(proj, ".claude", "skills", "fine")
	for _, d := range []string{bad, good} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fifo := filepath.Join(bad, "SKILL.md")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "SKILL.md"), []byte("---\nname: fine\ndescription: a skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	type result struct {
		list  []Skill
		notes []string
	}
	done := make(chan result, 1)
	go func() {
		list, notes := Discover("", proj, DefaultLimits())
		done <- result{list, notes}
	}()
	select {
	case r := <-done:
		defer CloseAll(r.list)
		if len(r.list) != 1 || r.list[0].Name != "fine" {
			t.Errorf("listed %d skills: %v", len(r.list), r.list)
		}
		if joined := strings.Join(r.notes, " | "); !strings.Contains(joined, "stuck") || !strings.Contains(joined, "not a regular file") {
			t.Errorf("notes = %q", joined)
		}
	case <-time.After(2 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("discovery waited on a FIFO")
	}
}
