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
func TestSearchFilesStopsMidWalkOnCancel(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	for _, d := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fifo := filepath.Join(dir, "a", "stall.txt")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		p := filepath.Join(dir, "b", fmt.Sprintf("f%02d.go", i))
		if err := os.WriteFile(p, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool, _ := r.Get("search_files")
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := tool.Run(ctx, map[string]any{"pattern": "needle", "literal": true})
		done <- result{out, err}
	}()

	// Blocks until the walk has the FIFO open for reading. Bounded: if
	// a future walk skips FIFOs the reader never comes, and this test
	// must fail, not hang the package.
	type opened struct {
		w   *os.File
		err error
	}
	writer := make(chan opened, 1)
	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		writer <- opened{w, err}
	}()
	var w *os.File
	select {
	case o := <-writer:
		if o.err != nil {
			t.Fatal(o.err)
		}
		w = o.w
	case <-time.After(5 * time.Second):
		t.Fatal("the walk never opened the FIFO — does search_files still read non-regular files?")
	}
	cancel()
	_ = w.Close() // EOF: the stalled read returns, the walk sees the cancel

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatal(res.err)
		}
		if !strings.Contains(res.out, "[interrupted after 1 files scanned — results above are partial]") {
			t.Errorf("cut not named after the stalled file:\n%s", res.out)
		}
		if strings.Contains(res.out, "b/f00.go") {
			t.Errorf("walk continued past the cancel into b/:\n%s", res.out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the walk did not return after the cancel")
	}
}
