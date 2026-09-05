package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ADR-0065 §1: the walks consult the context. A cancelled context
// ends the walk within one syscall, and the cut is named in the
// result — a partial result never poses as a whole one.

func TestSearchFilesCancelledBeforeStartReportsPartial(t *testing.T) {
	r := navProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool, _ := r.Get("search_files")
	out, err := tool.Run(ctx, map[string]any{"pattern": "maxRetries"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[interrupted after 0 files scanned — results above are partial]") {
		t.Errorf("cancelled search must name the cut:\n%s", out)
	}
}

func TestListTreeCancelledBeforeStartReportsPartial(t *testing.T) {
	r := navProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool, _ := r.Get("list_tree")
	out, err := tool.Run(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[interrupted — the tree above is partial]") {
		t.Errorf("cancelled tree must name the cut:\n%s", out)
	}
	if strings.Contains(out, "(empty directory)") {
		t.Errorf("an interrupted walk must not claim the directory is empty:\n%s", out)
	}
}

// A live run carries no interruption note — the footer is the cut,
// not decoration.
func TestNavUninterruptedRunsCarryNoFooter(t *testing.T) {
	r := navProject(t)
	for name, args := range map[string]map[string]any{
		"search_files": {"pattern": "maxRetries"},
		"list_tree":    {},
	} {
		out, err := run(t, r, name, args)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "interrupted") {
			t.Errorf("%s: uninterrupted run mentions interruption:\n%s", name, out)
		}
	}
}

// The slow-filesystem shape, made deterministic: a FIFO in the first
// directory stalls the walk inside ReadFile (open blocks until a
// writer appears) exactly like a hung mount would. The test opens the
// writer — which itself blocks until the walk is inside the read, so
// it doubles as the synchronisation point — cancels, then releases
// the read with EOF. The walk must come back at its very next check
// without touching the directory that sorts after the stall.
// A FIFO in the tree is not opened for reading at all (ADR-0072 §4.8):
// with no writer, a plain open blocked past any cancel. The walk skips
// it as "not a regular file" and finishes on its own — the old form of
// this test used the FIFO to stall the walk and cancel mid-file, which
// the refusal makes impossible to stage; the cancel checks before every
// directory read and file read stay pinned by the tests above.
func TestSearchFilesSkipsAFIFOWithoutBlocking(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	for _, d := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "a", "stall.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		p := filepath.Join(dir, "b", fmt.Sprintf("f%02d.go", i))
		if err := os.WriteFile(p, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool, _ := r.Get("search_files")
	done := make(chan string, 1)
	go func() {
		out, _ := tool.Run(context.Background(), map[string]any{"pattern": "needle", "literal": true})
		done <- out
	}()
	select {
	case out := <-done:
		if !strings.Contains(out, "20 matches in 20 files") {
			t.Errorf("the regular files were not all searched:\n%s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the walk blocked on the FIFO")
	}
	// read_file refuses it too, promptly.
	rf, _ := r.Get("read_file")
	errc := make(chan error, 1)
	go func() { _, err := rf.Run(context.Background(), map[string]any{"path": "a/stall.txt"}); errc <- err }()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("read_file on a FIFO: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read_file blocked on the FIFO")
	}
}
