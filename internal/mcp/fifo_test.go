package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/bounded"
)

// An MCP configuration that is a FIFO is an error, at once: a plain
// open waited for a writer, and a project holding a .mcp.json like that
// stopped the runtime at startup.
func TestLoadConfigDoesNotWaitOnAFileThatIsAFifo(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), ".mcp.json")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	var err error
	notWaiting(t, fifo, func() { _, _, err = LoadConfig(fifo) })
	if !errors.Is(err, bounded.ErrNotRegular) {
		t.Errorf("err = %v, want one that is bounded.ErrNotRegular", err)
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
