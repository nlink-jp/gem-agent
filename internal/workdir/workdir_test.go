package workdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// isolate points the state root at a scratch tree, so a test can never
// see — and therefore never delete — the operator's real state.
func isolate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(statedir.EnvRoot, root)
	return root
}

func TestEnsureIsUnderTheStateRootAndKeyedBySession(t *testing.T) {
	root := isolate(t)
	project := t.TempDir()

	dir, err := Ensure(project, "sess-1")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !strings.HasPrefix(dir, root) {
		t.Errorf("work dir %q is outside the isolated state root %q", dir, root)
	}
	if filepath.Base(dir) != "sess-1" {
		t.Errorf("work dir %q is not keyed by the session id", dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("Ensure did not create the directory: %v", err)
	}
	// The project marker guards the lossy path escape, exactly as it
	// does for transcripts and memory.
	marker := filepath.Join(filepath.Dir(filepath.Dir(dir)), statedir.Marker)
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("project marker missing: %v", err)
	}
}

// Resume has to land in the same directory, or a resumed session cannot
// reach what its earlier self produced.
func TestEnsureIsStableAcrossCallsForOneSession(t *testing.T) {
	isolate(t)
	project := t.TempDir()

	first, err := Ensure(project, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "report.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(project, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("resume got %q, want the original %q", second, first)
	}
	if _, err := os.Stat(filepath.Join(second, "report.md")); err != nil {
		t.Errorf("the earlier session's file is not reachable: %v", err)
	}
}

func TestDifferentSessionsAndProjectsDoNotShare(t *testing.T) {
	isolate(t)
	projectA, projectB := t.TempDir(), t.TempDir()

	a1, _ := Ensure(projectA, "sess-1")
	a2, _ := Ensure(projectA, "sess-2")
	b1, _ := Ensure(projectB, "sess-1")
	if a1 == a2 {
		t.Error("two sessions of one project share a work directory")
	}
	if a1 == b1 {
		t.Error("two projects share a work directory for the same session id")
	}
}

// A session id is a directory name. One carrying a separator would put
// the session's files outside the tree meant to hold them.
func TestPathRefusesASessionIDThatIsNotOneSegment(t *testing.T) {
	isolate(t)
	for _, id := range []string{"", "..", ".", "a/b", "../escape", "/abs"} {
		if _, err := Path(t.TempDir(), id); err == nil {
			t.Errorf("session id %q was accepted", id)
		}
	}
}

func TestSweepCountsOtherSessionsAndSkipsTheCurrentOne(t *testing.T) {
	isolate(t)
	project := t.TempDir()

	old1, _ := Ensure(project, "old-1")
	old2, _ := Ensure(project, "old-2")
	current, _ := Ensure(project, "current")
	if err := os.WriteFile(filepath.Join(old1, "a.bin"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old2, "b.bin"), make([]byte, 50), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "c.bin"), make([]byte, 999), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs, bytes, err := Sweep(project, "current")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if dirs != 2 {
		t.Errorf("dirs = %d, want the 2 earlier sessions", dirs)
	}
	if bytes != 150 {
		t.Errorf("bytes = %d, want 150 (the current session must not be counted)", bytes)
	}
	// Reporting must never be deletion: everything is still there.
	for _, d := range []string{old1, old2, current} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("Sweep removed %q: %v", d, err)
		}
	}
}

func TestSweepIsQuietWhenNothingWasEverWritten(t *testing.T) {
	isolate(t)
	dirs, bytes, err := Sweep(t.TempDir(), "sess-1")
	if err != nil || dirs != 0 || bytes != 0 {
		t.Errorf("Sweep on a fresh project = %d/%d/%v, want 0/0/nil", dirs, bytes, err)
	}
}

func TestRemoveIfEmptyLeavesADirectoryThatHoldsAnything(t *testing.T) {
	isolate(t)
	project := t.TempDir()

	empty, _ := Ensure(project, "empty")
	RemoveIfEmpty(empty)
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Errorf("an unused work directory should leave no trace: %v", err)
	}

	used, _ := Ensure(project, "used")
	if err := os.WriteFile(filepath.Join(used, "report.md"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	RemoveIfEmpty(used)
	if _, err := os.Stat(filepath.Join(used, "report.md")); err != nil {
		t.Fatalf("RemoveIfEmpty destroyed the session's output: %v", err)
	}
}

// List is the ledger the note and the cleanup both read: complete,
// sized, and read-only.
func TestListReportsEverySessionWithSizes(t *testing.T) {
	isolate(t)
	project := t.TempDir()
	a, _ := Ensure(project, "sess-a")
	if err := os.WriteFile(filepath.Join(a, "x.bin"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := Ensure(project, "sess-b")
	if err := os.WriteFile(filepath.Join(b, "y.bin"), make([]byte, 40), 0o644); err != nil {
		t.Fatal(err)
	}

	infos, err := List(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("listed %d, want 2", len(infos))
	}
	sizes := map[string]int64{}
	for _, in := range infos {
		sizes[in.ID] = in.Bytes
	}
	if sizes["sess-a"] != 100 || sizes["sess-b"] != 40 {
		t.Errorf("sizes = %v", sizes)
	}
	if got, _, _ := Sweep(project, "sess-b"); got != 1 {
		t.Errorf("Sweep must exclude the asking session: dirs = %d, want 1", got)
	}
}

// Remove deletes exactly the named session and folds up an emptied
// parent; a malformed id never reaches the filesystem.
func TestRemoveDeletesOnlyTheNamedSession(t *testing.T) {
	isolate(t)
	project := t.TempDir()
	a, _ := Ensure(project, "sess-a")
	if err := os.WriteFile(filepath.Join(a, "keep-me-not.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bDir, _ := Ensure(project, "sess-b")
	if err := os.WriteFile(filepath.Join(bDir, "survivor.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Remove(project, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Error("sess-a should be gone")
	}
	if _, err := os.Stat(filepath.Join(bDir, "survivor.txt")); err != nil {
		t.Errorf("sess-b was touched: %v", err)
	}
	for _, bad := range []string{"", "..", "a/b", "../escape"} {
		if err := Remove(project, bad); err == nil {
			t.Errorf("id %q was accepted for deletion", bad)
		}
	}
	// Removing the last one folds the empty parent up.
	if err := Remove(project, "sess-b"); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(bDir)
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Errorf("emptied work/ parent should be folded up: %v", err)
	}
}
