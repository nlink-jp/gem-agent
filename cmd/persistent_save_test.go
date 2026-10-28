package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/trustpin"
)

// Two sessions of one project may save the persistent-file snapshot at
// once (exit of one, /clear of another). A shared temporary name lets
// one writer truncate or rename away the other's half-written file:
// every save must succeed and every load must see a whole snapshot.
func TestSavePersistentSnapshotConcurrently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persistent.json")
	big := func(tag string) map[string]string {
		m := map[string]string{}
		for i := 0; i < 2000; i++ {
			m[fmt.Sprintf("dir%04d/AGENTS.md", i)] = tag
		}
		return m
	}
	var wg sync.WaitGroup
	errs := make(chan error, 400)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(tag string) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := savePersistentSnapshot(path, big(tag)); err != nil {
					errs <- err
				}
				if loadPersistentSnapshot(path) == nil {
					errs <- fmt.Errorf("a load saw a torn or missing snapshot")
				}
			}
		}(fmt.Sprintf("w%d", w))
	}
	wg.Wait()
	close(errs)
	n := 0
	for err := range errs {
		if n < 3 {
			t.Error(err)
		}
		n++
	}
	if n > 0 {
		t.Errorf("%d failures in 200 concurrent saves", n)
	}
}

// A walk the session cut short must say so (review of ADR-0099 §10,
// #2, which found the project's report shares the gap): a command can
// fill the project past trustpin.WalkEntries and then plant a nested
// AGENTS.md the lexical walk never reaches. Measured on the real walk.
func TestPersistentReportSaysWhenTheWalkWasCut(t *testing.T) {
	if testing.Short() {
		t.Skip("creates more files than the walk's cap")
	}
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, cut := trustpin.Snapshot(proj)
	if cut {
		t.Fatal("the starting walk was cut")
	}
	fill := filepath.Join(proj, "aaa")
	if err := os.Mkdir(fill, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= trustpin.WalkEntries; i++ {
		f, err := os.Create(filepath.Join(fill, fmt.Sprintf("f%05d", i)))
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	if err := os.MkdirAll(filepath.Join(proj, "zzz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "zzz", "AGENTS.md"), []byte("planted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, a, c, r, gotCut := persistentReport(base, func() (map[string]string, bool) { return trustpin.Snapshot(proj) })
	if !gotCut {
		t.Errorf("a cut walk was not reported as cut (added %v changed %v removed %v)", a, c, r)
	}
	for _, name := range r {
		if name == "AGENTS.md" {
			t.Errorf("a file the cut walk may not have reached was reported removed: %v", r)
		}
	}
}
