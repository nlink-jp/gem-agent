package trustpin

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTree makes a directory holding the given files (any content),
// links (name → target) and empty directories, and opens it.
func buildTree(t *testing.T, files []string, links map[string]string, dirs []string) (string, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(f)), []byte(f))
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return dir, root
}

func names(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%sf%05d", prefix, i)
	}
	return out
}

// The file cap is looked at where a file is counted. It used to be
// looked at on every entry the walk came to, so a skill of exactly the
// cap was refused for a directory that happened to sort after its last
// file — "more than 2,000 files" said of 2,000. A tree at the cap is
// covered whole, whatever directories it holds; one file or one link
// more is past it.
func TestTheFileCapIsLookedAtWhereAFileIsCounted(t *testing.T) {
	const max = 5
	for _, c := range []struct {
		name    string
		files   []string
		links   map[string]string
		dirs    []string
		cut     bool
		wasEdge bool // refused before this change, covered now
	}{
		{name: "under the cap", files: names("", 4)},
		{name: "at the cap, nothing after", files: names("", 5)},
		{name: "at the cap, an empty directory after", files: names("", 5), dirs: []string{"zzz"}, wasEdge: true},
		{name: "at the cap, an empty directory before", files: names("", 5), dirs: []string{"aaa"}},
		{name: "at the cap in a directory, an empty one after", files: names("a/", 5), dirs: []string{"b"}, wasEdge: true},
		{name: "at the cap, nested empty directories after", files: names("", 5), dirs: []string{"zzz/y/x"}, wasEdge: true},
		{name: "one file over", files: names("", 6), cut: true},
		{name: "at the cap, then a directory holding a file", files: append(names("", 5), "zzz/x"), cut: true},
		{name: "at the cap, then a link", files: names("", 5), links: map[string]string{"zzz": "f00000"}, cut: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, root := buildTree(t, c.files, c.links, c.dirs)
			entries, cut := manifestRoot(root, max, 100, 100)
			if cut != c.cut {
				t.Fatalf("cut = %v, want %v (%d entries)", cut, c.cut, len(entries))
			}
			if !cut && len(entries) != len(c.files)+len(c.links) {
				t.Errorf("covered %d entries of %d", len(entries), len(c.files)+len(c.links))
			}
			// Against the walk every pin on disk was taken with: the
			// same answer and the same digest, except in the one case
			// this change is about.
			old, oldCut := pathDirDigest(dir, max)
			if c.wasEdge {
				if !oldCut {
					t.Error("the old walk did not refuse this tree: the case is not the one it claims to be")
				}
				return
			}
			if got := digestEntries(entries, cut); got != old || cut != oldCut {
				t.Errorf("digest %s (cut %v), the old walk %s (cut %v)", got, cut, old, oldCut)
			}
		})
	}
	t.Run("the real cap", func(t *testing.T) {
		_, root := buildTree(t, names("", SkillFiles), nil, []string{"zzz"})
		_, files, cut := SkillManifest(root)
		if cut || len(files) != SkillFiles {
			t.Errorf("cut = %v with %d files covered: a skill of exactly %d files is covered", cut, len(files), SkillFiles)
		}
	})
}

// The walk is bounded in what it looks at. A directory is not part of a
// digest, so empty ones change none — and a walk bounded only by the
// file cap went through every one of them, at every use.
func TestTheWalkLooksAtNoMoreThanItsBudget(t *testing.T) {
	const maxFiles, maxWalk = 1000, 20
	dirs := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("d%03d", i)
		}
		return out
	}
	chain := "c"
	for i := 0; i < 30; i++ {
		chain += "/c"
	}
	for _, c := range []struct {
		name  string
		files []string
		dirs  []string
		cut   bool
	}{
		{name: "at the budget", files: names("", 10), dirs: dirs(10)},
		{name: "one directory over", files: names("", 10), dirs: dirs(11), cut: true},
		{name: "only empty directories", dirs: dirs(21), cut: true},
		{name: "one directory holding far more than the budget", files: names("big/", 3000), cut: true},
		{name: "a deep chain", dirs: []string{chain}, cut: true},
		// a, its 6 files, 8 directories, a/b, a/b/c, a/b/d, e, e/f: 20.
		{name: "wide and nested, at the budget", files: names("a/", 6), dirs: append(dirs(8), "a/b/c", "a/b/d", "e/f")},
		{name: "wide and nested, one over", files: names("a/", 6), dirs: append(dirs(8), "a/b/c", "a/b/d", "e/f", "e/g"), cut: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, root := buildTree(t, c.files, nil, c.dirs)
			w := &manifestWalk{maxFiles: maxFiles, left: maxWalk, maxDepth: 100}
			w.dir(root, "", 0)
			if w.cut != c.cut {
				t.Errorf("cut = %v, want %v", w.cut, c.cut)
			}
			// Names listed: the budget, and the one extra a directory
			// is read by to see that it does not fit. A directory of
			// 3,000 files costs 21 names, not 3,000.
			if w.listed > maxWalk+1 {
				t.Errorf("listed %d names with a budget of %d", w.listed, maxWalk)
			}
		})
	}
	t.Run("the real budget", func(t *testing.T) {
		_, root := buildTree(t, []string{"SKILL.md"}, nil, func() []string {
			out := make([]string, SkillWalk)
			for i := range out {
				out[i] = fmt.Sprintf("d%05d", i)
			}
			return out
		}())
		if _, _, cut := SkillManifest(root); !cut {
			t.Errorf("a skill of one file and %d directories is past the walk's bound", SkillWalk)
		}
	})
}

// How deep directories nest is bounded too. A directory is opened from
// its parent's handle — one component — and not by a path resolved
// again from the root: resolved from the root, a chain of d directories
// cost d*d/2 opens, and 800 empty ones, inside every other bound, took
// half a minute at each use. One handle is held per level, so the depth
// is what bounds the handles.
func TestTheWalkGoesNoDeeperThanItsBound(t *testing.T) {
	chain := func(n int) string {
		s := "c"
		for i := 1; i < n; i++ {
			s += "/c"
		}
		return s
	}
	for _, c := range []struct {
		name  string
		files []string
		dirs  []string
		cut   bool
	}{
		{name: "at the depth", dirs: []string{chain(3)}},
		{name: "a file at the depth", files: []string{chain(3) + "/f"}},
		{name: "one level deeper", dirs: []string{chain(4)}, cut: true},
		{name: "one level deeper, beside a shallow file", files: []string{"a"}, dirs: []string{chain(4)}, cut: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, root := buildTree(t, c.files, nil, c.dirs)
			entries, cut := manifestRoot(root, 100, 100, 3)
			if cut != c.cut {
				t.Errorf("cut = %v, want %v", cut, c.cut)
			}
			if !cut && len(entries) != len(c.files) {
				t.Errorf("covered %d of %d files", len(entries), len(c.files))
			}
		})
	}
	t.Run("the real depth", func(t *testing.T) {
		_, root := buildTree(t, []string{"SKILL.md", chain(SkillDepth) + "/deep.md"}, nil, nil)
		if _, files, cut := SkillManifest(root); cut || len(files) != 2 {
			t.Errorf("cut = %v with %d files: %d levels are within the bound", cut, len(files), SkillDepth)
		}
		_, root = buildTree(t, []string{"SKILL.md"}, nil, []string{chain(SkillDepth + 1)})
		if _, _, cut := SkillManifest(root); !cut {
			t.Errorf("%d levels are past the bound", SkillDepth+1)
		}
	})
	// The numbers the notices and the manual name.
	if SkillFiles != 2000 || SkillWalk != 4000 || SkillDepth != 32 {
		t.Errorf("the bounds are %d files, %d entries, %d levels: the notices and the manual say 2,000, 4,000 and 32", SkillFiles, SkillWalk, SkillDepth)
	}
}

// A file that cannot be digested is not an entry, wherever it sorts.
// Counted at the cap and not below it, it made the answer depend on
// its name: two readable files and one unreadable were covered when
// the unreadable one sorted first, and refused when it sorted last.
func TestAFileThatCannotBeDigestedIsNotCountedWhereverItSorts(t *testing.T) {
	for _, unreadable := range []string{"a", "b", "c"} {
		t.Run(unreadable, func(t *testing.T) {
			dir, root := buildTree(t, []string{"a", "b", "c"}, nil, nil)
			if err := os.Chmod(filepath.Join(dir, unreadable), 0); err != nil {
				t.Fatal(err)
			}
			entries, cut := manifestRoot(root, 2, 100, 100)
			if cut || len(entries) != 2 {
				t.Errorf("cut = %v with %d entries: two files can be digested, and the cap is two", cut, len(entries))
			}
		})
	}
}

// A directory whose name is not valid UTF-8 was never entered, and is
// not entered now: what is under it is in no pin taken so far.
func TestTheWalkDoesNotEnterANameThatWasNeverEntered(t *testing.T) {
	for name, want := range map[string]bool{"references": true, "日本語": true, ".hidden": true, "a\xff": false, "\xc3\x28": false} {
		if got := descends(name); got != want {
			t.Errorf("descends(%q) = %v, want %v", name, got, want)
		}
	}
}

// The bounds are properties of the tree, not of the order it is read
// in: a tree is cut exactly when it holds more files and links that can
// be digested than the file cap, more entries of every kind than the
// walk's budget, or a directory below the depth. And the digest is the
// one the old walk took wherever the two agree on what the tree is —
// which is every tree a pin on disk can still be compared with.
func TestBothBoundsArePropertiesOfTheTree(t *testing.T) {
	for seed := int64(0); seed < 400; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var files, dirs []string
		links := map[string]string{}
		pool := []string{""}
		for i, n := 0, rng.Intn(30); i < n; i++ {
			parent := pool[rng.Intn(len(pool))]
			name := fmt.Sprintf("%s%c%02d", parent, "abz"[rng.Intn(3)], i)
			switch rng.Intn(4) {
			case 0:
				dirs = append(dirs, name)
				pool = append(pool, name+"/")
			case 1:
				links[name] = []string{"nowhere", "/etc/hosts", "."}[rng.Intn(3)]
			default:
				files = append(files, name)
			}
		}
		maxFiles, maxWalk, maxDepth := 1+rng.Intn(12), 1+rng.Intn(35), 1+rng.Intn(4)
		dir, root := buildTree(t, files, links, dirs)
		// Some files cannot be read: listed, and not digested.
		unreadable := map[string]bool{}
		for _, f := range files {
			if rng.Intn(6) == 0 {
				unreadable[filepath.Join(dir, filepath.FromSlash(f))] = true
				if err := os.Chmod(filepath.Join(dir, filepath.FromSlash(f)), 0); err != nil {
					t.Fatal(err)
				}
			}
		}

		covered, total, deepest := 0, 0, 0
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || p == dir {
				return nil
			}
			total++
			switch {
			case d.IsDir():
				rel, _ := filepath.Rel(dir, p)
				if level := strings.Count(filepath.ToSlash(rel), "/") + 1; level > deepest {
					deepest = level
				}
			case d.Type()&fs.ModeSymlink != 0, d.Type().IsRegular() && !unreadable[p]:
				covered++
			}
			return nil
		})
		entries, cut := manifestRoot(root, maxFiles, maxWalk, maxDepth)
		if want := covered > maxFiles || total > maxWalk || deepest > maxDepth; cut != want {
			t.Fatalf("seed %d: cut = %v with %d files and links (cap %d), %d entries (budget %d), directories %d deep (bound %d)", seed, cut, covered, maxFiles, total, maxWalk, deepest, maxDepth)
		}
		got := digestEntries(entries, cut)
		switch {
		case !cut:
			// Covered whole: the digest of everything, which the old
			// walk gives when no cap is in its way.
			if want, _ := pathDirDigest(dir, 1<<30); got != want {
				t.Fatalf("seed %d: an uncut tree digests as %s, in full %s", seed, got, want)
			}
		case total <= maxWalk && deepest <= maxDepth:
			// Cut by the file cap alone: the files the old walk
			// stopped at, so a pin taken cut is still equal.
			if want, wantCut := pathDirDigest(dir, maxFiles); got != want || !wantCut {
				t.Fatalf("seed %d: cut at the file cap as %s, the old walk %s (cut %v)", seed, got, want, wantCut)
			}
		}
	}
}

// A directory that can be listed and not searched is not walked: its
// names can be read and nothing under them can. The walk never went
// into one — it could not open it to list — and one that did would
// record its links with no target, which is another digest for a tree
// a pin was already taken of.
func TestTheWalkDoesNotEnterADirectoryItCannotSearch(t *testing.T) {
	dir, root := buildTree(t, []string{"SKILL.md", "closed/f", "closed/deeper/g"}, map[string]string{"closed/l": "f"}, nil)
	closed := filepath.Join(dir, "closed")
	if err := os.Chmod(closed, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("nothing is closed to root")
	}
	_, files, cut := SkillManifest(root)
	if cut || len(files) != 1 || files["SKILL.md"] == "" {
		t.Errorf("cut = %v, covered %v: want SKILL.md alone", cut, files)
	}
}

// A level is opened from the one above it, so no path the system is
// given is ever as long as the tree is deep: names as long as a name
// can be, at the real depth, are walked like any others. (The system
// takes no path that long — the tree is made here through a root,
// which resolves a path one name at a time.)
func TestTheWalkIsNotBoundedByTheLengthOfAPath(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	long := strings.Repeat("n", 255)
	rel := ""
	for i := 0; i < SkillDepth; i++ {
		rel += long
		if err := root.Mkdir(rel, 0o755); err != nil {
			t.Skipf("level %d: %v", i, err)
		}
		rel += "/"
	}
	if err := root.WriteFile(rel+long, []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("target", rel+"link"); err != nil {
		t.Fatal(err)
	}
	_, files, cut := SkillManifest(root)
	if cut || len(files) != 2 || files[rel+long] != ContentLine([]byte("deep")) || files[rel+"link"] != "link:target" {
		t.Errorf("cut = %v with %d entries: want the file and the link %d levels down", cut, len(files), SkillDepth)
	}
}
