package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/bounded"
)

const fifoSessionID = "11111111-1111-4111-8111-111111111111"

// A transcript is a file this runtime writes. One that is a FIFO is not
// a transcript: every reader opened it plainly and waited — the
// listing, and so --continue, among them (ADR-0120).
func TestReadersDoNotWaitOnATranscriptThatIsAFifo(t *testing.T) {
	const proj = "/some/project"
	dir := t.TempDir()
	fifo := fifoAt(t, filepath.Join(projectSubdir(dir, proj), fifoSessionID+".jsonl"))
	returnsAtOnce(t, fifo, func() {
		if _, _, _, err := Load(fifo); !errors.Is(err, bounded.ErrNotRegular) {
			t.Errorf("Load: %v", err)
		}
	})
	returnsAtOnce(t, fifo, func() {
		err := Scan(fifo, func(string, time.Time, json.RawMessage) error { return nil })
		if !errors.Is(err, bounded.ErrNotRegular) {
			t.Errorf("Scan: %v", err)
		}
	})
	returnsAtOnce(t, fifo, func() {
		metas, _, err := List(dir, proj)
		if err != nil || len(metas) != 0 {
			t.Errorf("List: %v, %v — want no session and no error", metas, err)
		}
	})
	returnsAtOnce(t, fifo, func() {
		if InUse(dir, proj, fifoSessionID) {
			t.Error("InUse: a FIFO is not a session in use")
		}
	})
}

// The directories the listing reads are this runtime's too. A FIFO
// where one should be lists nothing.
func TestListDoesNotWaitOnADirectoryThatIsAFifo(t *testing.T) {
	const proj = "/some/project"
	t.Run("the project's sessions", func(t *testing.T) {
		dir := t.TempDir()
		fifo := fifoAt(t, projectSubdir(dir, proj))
		returnsAtOnce(t, fifo, func() {
			if metas, _, _ := List(dir, proj); len(metas) != 0 {
				t.Errorf("List: %v", metas)
			}
		})
	})
	t.Run("every project's sessions", func(t *testing.T) {
		dir := t.TempDir()
		fifo := fifoAt(t, filepath.Join(dir, "projects"))
		returnsAtOnce(t, fifo, func() {
			if metas, _, _ := List(dir, ""); len(metas) != 0 {
				t.Errorf("List: %v", metas)
			}
		})
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
