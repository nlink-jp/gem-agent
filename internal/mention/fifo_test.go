package mention

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// An @ reference inside a root is opened through an os.Root at that
// root, by path. The write lane can leave a FIFO where the work
// directory or an added directory was (ADR-0120), and a reference to
// it waited on the plain open of the root. It is a problem, at once.
func TestExpandDoesNotWaitOnARootThatIsAFifo(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	base, _ := filepath.EvalSymlinks(t.TempDir())
	for name, expand := range map[string]func(fifo string) ([]Attachment, []Problem){
		"the work directory": func(fifo string) ([]Attachment, []Problem) {
			return Expand(context.Background(), "@"+fifo, project, fifo, DefaultLimits())
		},
		"an added directory": func(fifo string) ([]Attachment, []Problem) {
			return Expand(context.Background(), "@"+fifo, project, "", DefaultLimits(), fifo)
		},
	} {
		t.Run(name, func(t *testing.T) {
			// No space in the path: a reference ends at one.
			fifo := fifoAt(t, filepath.Join(base, strings.ReplaceAll(name, " ", "-")))
			var atts []Attachment
			var problems []Problem
			returnsAtOnce(t, fifo, func() { atts, problems = expand(fifo) })
			if len(atts) != 0 || len(problems) != 1 {
				t.Errorf("attachments = %+v, problems = %+v — want the reference refused", atts, problems)
			}
		})
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
