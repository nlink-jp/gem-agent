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

// A clean that fails on the cache leaves the work directory, the only
// thing listed, for a retry to find.
func TestRemoveDeletesTheCacheFirst(t *testing.T) {
	t.Setenv("GEMAGENT_STATE_DIR", t.TempDir())
	work, err := Ensure("/some/project", "sid")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := EnsureApprovedCache(work)
	if err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(cache, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if err := Remove("/some/project", "sid"); err == nil {
		t.Fatal("Remove reported success on a cache it could not delete")
	}
	if _, err := os.Stat(work); err != nil {
		t.Errorf("the work directory is gone, so nothing lists the cache: %v", err)
	}
}

// A link left where a session directory belongs is refused, not followed:
// the write lane can make one, and a resume would then write through it.
func TestSessionDirectoriesRefuseAPlantedLink(t *testing.T) {
	t.Setenv("GEMAGENT_STATE_DIR", t.TempDir())
	project := "/some/project"
	victim := t.TempDir()
	work, err := Ensure(project, "sid")
	if err != nil {
		t.Fatal(err)
	}
	cache, ok := ApprovedCachePath(work)
	if !ok {
		t.Fatal("no cache path")
	}
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, cache); err != nil {
		t.Fatal(err)
	}
	if dir, err := EnsureApprovedCache(work); err == nil {
		t.Errorf("the planted approved-cache link was accepted: %q", dir)
	}
	if err := os.Remove(work); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, work); err != nil {
		t.Fatal(err)
	}
	if dir, err := Ensure(project, "sid"); err == nil {
		t.Errorf("the planted work-directory link was accepted: %q", dir)
	}
}
