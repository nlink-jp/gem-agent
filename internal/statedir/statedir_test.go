package statedir

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestEnsureProjectDirParallelSameProject is the measured race: N
// simultaneous launches of ONE project must all succeed. Before the
// temp+rename fix, a reader between WriteFile's create and write saw
// an empty marker and refused its own project as a collision.
func TestEnsureProjectDirParallelSameProject(t *testing.T) {
	for round := 0; round < 20; round++ {
		dir := filepath.Join(t.TempDir(), "projects", "-p")
		const n = 16
		var wg sync.WaitGroup
		errs := make([]error, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = EnsureProjectDir(dir, "/p")
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d, opener %d: %v", round, i, err)
			}
		}
		data, err := os.ReadFile(filepath.Join(dir, Marker))
		if err != nil || strings.TrimSpace(string(data)) != "/p" {
			t.Fatalf("round %d: marker = %q, %v", round, data, err)
		}
		// No temp droppings left behind.
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), Marker+".tmp-") {
				t.Errorf("round %d: leftover temp file %s", round, e.Name())
			}
		}
	}
}

// TestEnsureProjectDirRefusesRealCollision: the check the race fix
// must not weaken — a dir owned by a DIFFERENT project still refuses.
func TestEnsureProjectDirRefusesRealCollision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "-p")
	if err := EnsureProjectDir(dir, "/a/p"); err != nil {
		t.Fatal(err)
	}
	err := EnsureProjectDir(dir, "/b/p")
	if err == nil || !strings.Contains(err.Error(), "path-escape collision") {
		t.Errorf("different project accepted: %v", err)
	}
}

// TestEnsureProjectDirRepairsEmptyMarker: an empty marker (crashed
// pre-fix writer, hand-made dir) is no one's claim — it must admit the
// caller and be rewritten with a real owner.
func TestEnsureProjectDirRepairsEmptyMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Marker), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureProjectDir(dir, "/p"); err != nil {
		t.Fatalf("empty marker refused: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, Marker))
	if strings.TrimSpace(string(data)) != "/p" {
		t.Errorf("empty marker not repaired: %q", data)
	}
}
