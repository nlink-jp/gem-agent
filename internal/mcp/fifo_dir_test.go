package mcp

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// .mcp.json is read through a root at its directory, opened by path. A
// FIFO where the project directory was is an error, at once; the plain
// open of it waited (ADR-0120).
func TestLoadConfigDoesNotWaitOnADirectoryThatIsAFifo(t *testing.T) {
	fifo := fifoAt(t, filepath.Join(t.TempDir(), "project"))
	var err error
	returnsAtOnce(t, fifo, func() { _, _, err = LoadConfig(filepath.Join(fifo, ".mcp.json")) })
	if err == nil {
		t.Error("a FIFO was read as the directory of .mcp.json")
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
