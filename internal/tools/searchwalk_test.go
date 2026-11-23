package tools

// ADR-0125 §2: the walk and the workers run beside the calling
// goroutine. These tests drive the search IN PROCESS (no file child);
// cmd.TestFileChildUnderTheRealProfile covers the shipped path.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func withSearchWorkers(t *testing.T, n int) {
	t.Helper()
	old := searchWorkerLimit
	searchWorkerLimit = n
	t.Cleanup(func() { searchWorkerLimit = old })
}

func withSearchHook(t *testing.T, hook func(event, path string)) {
	t.Helper()
	searchTestHook = hook
	t.Cleanup(func() { searchTestHook = nil })
}

// goroutinesSettleTo waits briefly for the goroutine count to fall back
// to n — the test framework's own goroutines come and go too.
func goroutinesSettleTo(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > n {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines: %d after the call, %d before\n%s", runtime.NumGoroutine(), n, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A capped search counts what one reader counts, however the timing of
// eight falls.
func TestSearchCapIsTheSameAtAnyTiming(t *testing.T) {
	r := judgeCapTree(t)
	withSearchWorkers(t, 1)
	want := judgeRun(t, r, map[string]any{"pattern": "hit", "literal": true})
	if !strings.Contains(want, "stopped at the 200-line cap") {
		t.Fatalf("the tree does not reach the cap:\n%s", want)
	}
	searchWorkerLimit = 8
	for i := 0; i < 50; i++ {
		if got := judgeRun(t, r, map[string]any{"pattern": "hit", "literal": true}); got != want {
			t.Fatalf("run %d at 8 workers differs from one worker:\n--- one\n%s\n--- eight\n%s", i, want, got)
		}
	}
}

// A cancel while a worker is reading ends the call with the partial
// footer, and no goroutine of the call is left (ADR-0065 §1).
func TestSearchCancelMidReadLeavesNothingRunning(t *testing.T) {
	withSearchWorkers(t, 8)
	r := judgeCapTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	held := make(chan struct{})
	var once sync.Once
	withSearchHook(t, func(event, path string) {
		if event == "file" && strings.HasSuffix(path, "f05.txt") {
			once.Do(func() { close(held) })
			<-ctx.Done()
		}
	})
	before := runtime.NumGoroutine()
	tool, _ := r.Get("search_files")
	done := make(chan string, 1)
	go func() {
		out, _ := tool.Run(ctx, map[string]any{"pattern": "hit", "literal": true, "mode": "files"})
		done <- out
	}()
	<-held
	cancel()
	select {
	case out := <-done:
		if !strings.Contains(out, "[interrupted after ") {
			t.Errorf("a cancelled search must name the cut:\n%s", out)
		}
		if strings.Contains(out, "f05.txt") || strings.Contains(out, "f06.txt") {
			t.Errorf("a file at or after the cut was counted:\n%s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled search did not return")
	}
	goroutinesSettleTo(t, before)
}

// A walk stopped by a cancel just before it lists a directory still
// ends with the footer: the stream carries why it ended.
func TestSearchCancelAtADirectoryKeepsTheFooter(t *testing.T) {
	withSearchWorkers(t, 8)
	r := judgeCapTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	withSearchHook(t, func(event, path string) {
		if event == "dir" && filepath.Base(path) == "b" {
			cancel()
		}
	})
	tool, _ := r.Get("search_files")
	out, err := tool.Run(ctx, map[string]any{"pattern": "nothing-matches-this", "literal": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[interrupted after ") {
		t.Errorf("the cut was silent:\n%s", out)
	}
}

func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// Every directory handle the walk opened is closed when the call
// returns — finished, capped or cancelled. The count may dip while the
// runtime closes a descriptor of its own; a leak only ever adds.
func TestSearchClosesEveryHandle(t *testing.T) {
	withSearchWorkers(t, 8)
	r := judgeMainTree(t)
	capTree := judgeCapTree(t)
	before := openDescriptors(t)
	for _, c := range []struct {
		r    *Registry
		args map[string]any
	}{
		{r, map[string]any{"pattern": "needle", "literal": true}},
		{r, map[string]any{"pattern": "needle", "include_ignored": true}},
		{capTree, map[string]any{"pattern": "hit", "literal": true}},
	} {
		if _, err := run(t, c.r, "search_files", c.args); err != nil {
			t.Fatal(err)
		}
		if after := openDescriptors(t); after > before {
			t.Fatalf("descriptors: %d before, %d after %v", before, after, c.args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	withSearchHook(t, func(event, path string) {
		if event == "dir" && filepath.Base(path) == "a" {
			cancel()
		}
	})
	tool, _ := capTree.Get("search_files")
	if _, err := tool.Run(ctx, map[string]any{"pattern": "hit"}); err != nil {
		t.Fatal(err)
	}
	if after := openDescriptors(t); after > before {
		t.Fatalf("descriptors: %d before, %d after a cancelled search", before, after)
	}
}

// A root rotated away during a walk (/clear moves the work directory)
// stays open until the walk is done with it, and the walk finishes on
// it.
func TestSearchFinishesOnTheRootItStarted(t *testing.T) {
	withSearchWorkers(t, 8)
	r := newRegistry(t)
	first, second := t.TempDir(), t.TempDir()
	if err := r.UseWorkDir(first); err != nil {
		t.Fatal(err)
	}
	work := r.WorkDir()
	for i := 0; i < 5; i++ {
		judgeWrite(t, work, fmt.Sprintf("d/f%d.txt", i), "needle\n")
	}
	old := r.rootState().workRoot
	var rotated sync.Once
	withSearchHook(t, func(event, path string) {
		if event == "dir" && filepath.Base(path) == "d" {
			rotated.Do(func() {
				if err := r.UseWorkDir(second); err != nil {
					t.Error(err)
				}
				if old.closed.Load() {
					t.Error("the old root closed while the walk still held it")
				}
			})
		}
	})
	out, err := run(t, r, "search_files", map[string]any{"pattern": "needle", "path": work})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "5 matches in 5 files") {
		t.Errorf("the walk did not finish on the root it started with:\n%s", out)
	}
	if !old.closed.Load() {
		t.Error("the old root was not closed once the walk let it go")
	}
}

// A directory swapped for a FIFO between its listing and its open is
// refused at once, not waited on: OpenRoot alone would hold the walk
// past any cancel (pre-release review of ADR-0125). Nothing is left
// running.
func TestSearchDoesNotWaitOnADirectorySwappedForAFIFO(t *testing.T) {
	withSearchWorkers(t, 8)
	r := newRegistry(t)
	dir := r.ProjectDir()
	judgeWrite(t, dir, "a/x.txt", "needle\n")
	judgeWrite(t, dir, "b/y.txt", "needle\n")
	judgeWrite(t, dir, "c/z.txt", "needle\n")
	withSearchHook(t, func(event, path string) {
		if event == "dir" && filepath.Base(path) == "a" {
			b := filepath.Join(dir, "b")
			if err := os.RemoveAll(b); err != nil {
				t.Error(err)
			}
			if err := syscall.Mkfifo(b, 0o600); err != nil {
				t.Error(err)
			}
		}
	})
	before := runtime.NumGoroutine()
	tool, _ := r.Get("search_files")
	done := make(chan string, 1)
	go func() {
		out, _ := tool.Run(context.Background(), map[string]any{"pattern": "needle", "literal": true})
		done <- out
	}()
	select {
	case out := <-done:
		if !strings.Contains(out, "2 matches in 2 files") || !strings.Contains(out, "[1 directory could not be listed]") {
			t.Errorf("the swapped directory was not counted as unlistable:\n%s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the walk waited on a FIFO where a directory had been listed")
	}
	goroutinesSettleTo(t, before)
}

// A worker keeps only the clipped lines it will print, copied out of
// the file's text: a result waiting for the calling goroutine does not
// hold the whole file (pre-release review of ADR-0125).
func TestSearchWorkerKeepsOnlyClippedCopies(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	long := "needle " + strings.Repeat("x", 3*searchLineClip)
	judgeWrite(t, dir, "f.txt", "  "+long+"\nneedle short\n")
	h, release, err := r.openWalkStart(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close(); release() }()
	jobs := make(chan searchJob, 1)
	res := make(chan fileResult, 1)
	d := newDirRef(h)
	d.acquire() // the job's hold; the test keeps the first
	jobs <- searchJob{dir: d, name: "f.txt", full: filepath.Join(dir, "f.txt"), res: res}
	close(jobs)
	searchWorker(context.Background(), make(chan struct{}), jobs, regexp.MustCompile("needle"), false)
	got := <-res
	if got.count != 2 || len(got.hits) != 2 || got.hits[0].text != clipRunes(long, searchLineClip) || got.hits[1].text != "needle short" {
		t.Fatalf("result = %+v", got)
	}
}

// At the cap the walk stops: the sequential loop opened nothing past
// it. A subtree that gives the walk nothing to emit — empty directories
// — must not be walked to its end after the cap (verification of the
// fixes, ADR-0125).
func TestSearchStopsWalkingAtTheCap(t *testing.T) {
	withSearchWorkers(t, 8)
	r := newRegistry(t)
	dir := r.ProjectDir()
	// a/ reaches the cap exactly at its last file (40 files of 5
	// lines); b/ comes next and has nothing to emit.
	for i := 0; i < 40; i++ {
		judgeWrite(t, dir, fmt.Sprintf("a/f%02d.txt", i), strings.Repeat("hit\n", 5))
	}
	for i := 0; i < 30; i++ {
		for j := 0; j < 30; j++ {
			if err := os.MkdirAll(filepath.Join(dir, "b", fmt.Sprintf("d%02d", i), fmt.Sprintf("e%02d", j)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	var listed atomic.Int32
	withSearchHook(t, func(event, path string) {
		switch {
		case event == "dir":
			listed.Add(1)
		case strings.HasSuffix(path, "f39.txt"):
			// The file that reaches the cap is slow to read: the walk
			// has all that time to run ahead.
			time.Sleep(200 * time.Millisecond)
		}
	})
	out, err := run(t, r, "search_files", map[string]any{"pattern": "hit", "literal": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stopped at the 200-line cap") {
		t.Fatalf("the tree does not reach the cap:\n%s", out)
	}
	// The root and a/, then at most the read-ahead: 2 x workers steps,
	// a directory counting as one (third verification of ADR-0125).
	if n, most := listed.Load(), int32(2+2*searchWorkers()); n > most {
		t.Errorf("the walk listed %d of the tree's 933 directories, more than %d; the cap was reached in a/", n, most)
	}
}
