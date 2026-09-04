package session

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// New ids are UUID v4 (ADR-0071 §1); the timestamp form stays valid so
// every existing transcript still lists and resumes; anything else is
// refused, as before, so an id can never be a path.
func TestIDFormats(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := NewID()
		if !re.MatchString(id) || !ValidID(id) || seen[id] {
			t.Fatalf("bad or repeated id %q", id)
		}
		seen[id] = true
		if Short(id) != id[:8] {
			t.Errorf("Short(%q) = %q", id, Short(id))
		}
	}
	for _, legacy := range []string{"20260819-150102", "20260819-150102-2"} {
		if !ValidID(legacy) || Short(legacy) != legacy {
			t.Errorf("legacy id %q rejected or shortened", legacy)
		}
	}
	for _, bad := range []string{"", "../x", "20260819", "abc", "ABCDEF01-0000-4000-8000-000000000000", "12345678-1234-1234-1234-123456789012"} {
		if ValidID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A prefix resolves when it names exactly one session, in this project
// first; an ambiguous one says so and lists the candidates.
func TestFindByPrefix(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 2; i++ {
		lg, err := Open(dir, proj)
		if err != nil {
			t.Fatal(err)
		}
		if err := lg.Log(KindHeader, Header{Schema: SchemaVersion, Project: proj}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, lg.ID())
		_ = lg.Close()
	}
	if ids[0] == ids[1] {
		t.Fatal("two sessions share an id")
	}
	if m, err := FindByPrefix(dir, proj, ids[0][:8]); err != nil || m.ID != ids[0] {
		t.Errorf("prefix: %+v %v", m, err)
	}
	if m, err := FindByPrefix(dir, proj, ids[1]); err != nil || m.ID != ids[1] {
		t.Errorf("full id: %+v %v", m, err)
	}
	// A prefix both ids share (they are random, so build one) is ambiguous.
	common := ""
	for i := 0; i < 36 && i < len(ids[0]) && ids[0][i] == ids[1][i]; i++ {
		common += string(ids[0][i])
	}
	if len(common) >= 4 {
		if _, err := FindByPrefix(dir, proj, common); err == nil || !strings.Contains(err.Error(), "matches 2") {
			t.Errorf("ambiguous prefix: %v", err)
		}
	}
	for _, bad := range []string{"zz", "../x", "0000"} {
		if _, err := FindByPrefix(dir, proj, bad); err == nil {
			t.Errorf("%q resolved", bad)
		}
	}
}
