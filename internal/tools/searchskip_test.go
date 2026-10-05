package tools

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// --- search_files counts every file it did not search (ADR-0096 §4) ---

func TestSearchFilesCountsEverySkip(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	files := map[string]string{
		"a.txt":       "needle\n",
		"big/one.log": strings.Repeat("needle\n", searchFileCap/7+1),
		"big/two.log": strings.Repeat("x", searchFileCap+1),
		"blob.bin":    "needle\x00\x01",
		"shot.png":    "needle",
		"utf16.log":   "n\x00e\x00e\x00d\x00l\x00e\x00",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, r, "search_files", map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	want := "[not searched: 2 over 2 MB (big/one.log, big/two.log), 2 binary, 1 image]"
	if !strings.HasSuffix(out, want) {
		t.Errorf("closing line:\n%s\nwant suffix %q", out, want)
	}
	if !strings.Contains(out, "a.txt:1: needle") {
		t.Errorf("the searchable file was not searched:\n%s", out)
	}
}

// "No matches" over a tree whose files were all skipped says so.
func TestSearchFilesNoMatchStillSaysWhatWasNotSearched(t *testing.T) {
	r := newRegistry(t)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "huge.json"), []byte(strings.Repeat("y", searchFileCap+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "search_files", map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "no matches (0 files scanned)") || !strings.Contains(out, "[not searched: 1 over 2 MB (huge.json)]") {
		t.Errorf("out = %q", out)
	}
}

// A directory the walk cannot list is counted.
func TestSearchFilesCountsUnlistableDirectories(t *testing.T) {
	r := newRegistry(t)
	locked := filepath.Join(r.ProjectDir(), "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	out, err := run(t, r, "search_files", map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[1 directory could not be listed]") {
		t.Errorf("out = %q", out)
	}
}

// Names stop at the per-file cap; the count does not.
func TestSearchSkipNamesAreCappedCountsAreNot(t *testing.T) {
	s := &searchSkips{}
	for i := range searchPerFileCap + 3 {
		s.add(skipSize, "f"+strconv.Itoa(i))
	}
	got := s.summary()
	if !strings.HasPrefix(got, "[not searched: "+strconv.Itoa(searchPerFileCap+3)+" over 2 MB (f0, f1, f2, f3, f4 and 3 more)") {
		t.Errorf("summary = %q", got)
	}
	if (&searchSkips{}).summary() != "" {
		t.Error("an empty tally must add no line")
	}
}

// Refusals past the named five are counted, not dropped.
func TestSearchFilesCountsRefusalsPastTheNamedOnes(t *testing.T) {
	r := newRegistry(t)
	for i := range searchPerFileCap + 2 {
		d := filepath.Join(r.ProjectDir(), "s"+strconv.Itoa(i))
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, ".env"), []byte("needle=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, r, "search_files", map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, " and 2 more — reading one needs the operator's approval]") {
		t.Errorf("out = %q", out)
	}
}
