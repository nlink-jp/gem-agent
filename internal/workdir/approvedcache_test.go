package workdir

import (
	"os"
	"path/filepath"
	"testing"
)

// ADR-0109: the approved cache lies beside work/, keyed by the session,
// and is listed and removed with the work directory.
func TestApprovedCacheLayoutAndRetention(t *testing.T) {
	t.Setenv("GEMAGENT_STATE_DIR", t.TempDir())
	project := "/some/project"
	work, err := Ensure(project, "sid")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := EnsureApprovedCache(work)
	if err != nil || cache == "" {
		t.Fatalf("EnsureApprovedCache: %q, %v", cache, err)
	}
	if rel, err := filepath.Rel(work, cache); err != nil || rel == "." || filepath.IsLocal(rel) {
		t.Errorf("the approved cache %q is inside the work directory %q", cache, work)
	}
	if filepath.Base(cache) != "sid" || filepath.Dir(filepath.Dir(cache)) != filepath.Dir(filepath.Dir(work)) {
		t.Errorf("unexpected layout: %q beside %q", cache, work)
	}
	if err := os.WriteFile(filepath.Join(cache, "entry"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := Stat(project, "sid")
	if err != nil || info.Bytes != 5 {
		t.Errorf("the listing does not count the cache: %+v, %v", info, err)
	}
	if err := Remove(project, "sid"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("the cache survived Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(cache)); !os.IsNotExist(err) {
		t.Errorf("the empty approved-cache parent was left: %v", err)
	}
}

func TestApprovedCachePathNeedsTheLayout(t *testing.T) {
	for _, dir := range []string{"", "/tmp/x/001", "/work"} {
		if p, ok := ApprovedCachePath(dir); ok {
			t.Errorf("ApprovedCachePath(%q) = %q, want none", dir, p)
		}
	}
	if got, ok := ApprovedCachePath("/s/p/work/sid"); !ok || got != "/s/p/approved-cache/sid" {
		t.Errorf("got %q, %v", got, ok)
	}
}
