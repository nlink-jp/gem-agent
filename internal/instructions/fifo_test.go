package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// An instruction file that is a FIFO is skipped with a note. The loader
// opened it plainly and waited for a writer, so a project holding one
// stopped the runtime at startup.
func TestLoadDoesNotWaitOnAnInstructionFileThatIsAFifo(t *testing.T) {
	proj, home := t.TempDir(), t.TempDir()
	fifo := filepath.Join(proj, "AGENTS.md")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "CLAUDE.md"), []byte("a real instruction file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var files []File
	var notes []string
	notWaiting(t, fifo, func() { files, notes = Load(proj, home, "", DefaultLimits()) })
	for _, f := range files {
		if strings.HasSuffix(f.Path, "AGENTS.md") {
			t.Errorf("the FIFO was loaded: %+v", f)
		}
	}
	if len(files) != 1 || !strings.Contains(files[0].Content, "a real instruction file") {
		t.Errorf("files = %+v", files)
	}
	if joined := strings.Join(notes, " | "); !strings.Contains(joined, "AGENTS.md") || !strings.Contains(joined, "not a regular file") {
		t.Errorf("notes = %q", joined)
	}
}

// notWaiting runs load and fails the test if it has not returned in two
// seconds. A FIFO with no writer holds a plain open; the blocked open
// is released by opening the other end, so the test binary can exit.
func notWaiting(t *testing.T, fifo string, load func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		load()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		<-done
		t.Fatal("the load waited on a FIFO")
	}
}
