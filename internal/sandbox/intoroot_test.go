package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntoRoot covers ADR-0108 §1: a path typed through a link outside
// every root becomes the real path inside the root it leads to, with
// its last element kept as typed; everything else is left alone.
func TestIntoRoot(t *testing.T) {
	real := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	proj, wroot, rroot := real(t.TempDir()), real(t.TempDir()), real(t.TempDir())
	outside, escape := real(t.TempDir()), real(t.TempDir())
	roots := []string{proj, wroot, rroot}
	link := func(target, name string) string {
		l := filepath.Join(outside, name)
		if err := os.Symlink(target, l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	wlink, rlink, plink, elink := link(wroot, "wlink"), link(rroot, "rlink"), link(proj, "plink"), link(escape, "elink")
	broken := link(filepath.Join(outside, "gone"), "broken")
	// A link at the last element, inside the root, stays a link.
	if err := os.WriteFile(filepath.Join(wroot, "AGENTS.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(wroot, "AGENTS.md"), filepath.Join(wroot, "notes.md")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, in, want string
		ok             bool
	}{
		{"link to a write root, new file", filepath.Join(wlink, "new.txt"), filepath.Join(wroot, "new.txt"), true},
		{"link to a write root, new directories", filepath.Join(wlink, "a/b/new.txt"), filepath.Join(wroot, "a/b/new.txt"), true},
		{"link to a read root", filepath.Join(rlink, "x.txt"), filepath.Join(rroot, "x.txt"), true},
		{"link to the project", filepath.Join(plink, "AGENTS.md"), filepath.Join(proj, "AGENTS.md"), true},
		{"the link names the root itself", wlink, wroot, true},
		{"a link into a mount tree", filepath.Join(link("/Volumes/gem-agent-no-such-share", "nas"), "x.txt"), "", false},
		{"under the automounted /home", "/home/someone/x.txt", "", false},
		{"the last element is a link inside the root", filepath.Join(wlink, "notes.md"), filepath.Join(wroot, "notes.md"), true},
		{"link leading out of every root", filepath.Join(elink, "x.txt"), "", false},
		{"broken link", filepath.Join(broken, "x.txt"), "", false},
		{"credential spelling", filepath.Join(wlink, ".env"), "", false},
		{"under a mount tree", "/Volumes/share/x.txt", "", false},
		{"relative", "wlink/x.txt", "", false},
		{"already inside a root", filepath.Join(wroot, "x.txt"), "", false},
	} {
		got, ok := IntoRoot(c.in, roots)
		want := c.want
		if !c.ok {
			want = c.in // left as typed
		}
		if ok != c.ok || got != want {
			t.Errorf("%s: IntoRoot(%q) = %q, %v; want %q, %v", c.name, c.in, got, ok, want, c.ok)
		}
	}
	// The root itself, reached through the link to its parent.
	parentLink := link(filepath.Dir(wroot), "parent")
	if got, ok := IntoRoot(filepath.Join(parentLink, filepath.Base(wroot)), roots); !ok || got != wroot {
		t.Errorf("the root through a link to its parent: %q, %v", got, ok)
	}
	if fi, err := os.Lstat(filepath.Join(wroot, "notes.md")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the last element was resolved through")
	}
}

// walkReal judges each real prefix against the mount trees before it
// reads into it, so no spelling reaches a share: a chain of links, a
// relative link behind another link, the data volume's spelling and a
// case variant (pre-release review of the first cut, which judged the
// typed spelling and one link's own target). IntoRoot's result does not
// show this — a path into /Volumes leads out of every root either way —
// so the walk is tested on its own.
func TestWalkRealStopsAtEveryMountSpelling(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Repeat("../", strings.Count(dir, string(filepath.Separator)))
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"abs":     "/Volumes/share",
		"rel":     up + "Volumes/share",
		"net":     "/net/host",
		"upper":   "/VOLUMES/share",
		"data":    dataVolume + "/home/someone",
		"chain2":  "/Volumes/share",
		"chain1":  filepath.Join(dir, "chain2"),
		"subrel":  "sub",
		"plain":   dir,
		"selfish": "selfish",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	// A relative link inside sub, reached through the link subrel: its
	// target is taken from sub's real directory, one level deeper than
	// the spelling subrel/ suggests.
	if err := os.Symlink("../"+up+"Volumes/share", filepath.Join(sub, "deep")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		ok   bool
	}{
		{filepath.Join(dir, "abs", "x"), false},
		{filepath.Join(dir, "rel", "x"), false},
		{filepath.Join(dir, "net", "x"), false},
		{filepath.Join(dir, "upper", "x"), false},
		{filepath.Join(dir, "data", "x"), false},
		{filepath.Join(dir, "chain1", "x"), false},
		{filepath.Join(dir, "subrel", "deep", "x"), false},
		{filepath.Join(dir, "selfish", "x"), false}, // a loop does not resolve
		{"/volumes/share/x", false},
		{dataVolume + "/home/x", false},
		{filepath.Join(dir, "plain", "new", "x"), true},
	} {
		got, ok := walkReal(c.path)
		if ok != c.ok {
			t.Errorf("walkReal(%s) = %q, %v; want ok %v", c.path, got, ok, c.ok)
		}
	}
	if got, ok := walkReal(filepath.Join(dir, "plain", "new", "x")); got != filepath.Join(dir, "new", "x") || !ok {
		t.Errorf("walkReal through a plain link = %q, %v", got, ok)
	}
	if got, ok := walkReal(filepath.Join(dir, "subrel", "..", "sub")); got != sub || !ok {
		t.Errorf("`..` after a link is taken from the real directory: %q, %v", got, ok)
	}
}
