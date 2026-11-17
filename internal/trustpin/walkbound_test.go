package trustpin

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
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
			entries, cut := manifestRoot(root, max, 100)
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
			w := &manifestWalk{root: root, maxFiles: maxFiles, left: maxWalk}
			w.dir(".")
			if w.cut != c.cut {
				t.Errorf("cut = %v, want %v", w.cut, c.cut)
			}
			// Names listed: the budget, plus the one extra a directory
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

// Both bounds are properties of the tree, not of the order it is read
// in: a tree is cut exactly when it holds more files and links than the
// file cap, or more entries of every kind than the walk's budget. And
// the digest is the one the old walk took wherever the two agree on
// what the tree is — which is every tree a pin on disk can still be
// compared with.
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
		maxFiles, maxWalk := 1+rng.Intn(12), 1+rng.Intn(35)
		dir, root := buildTree(t, files, links, dirs)

		covered, total := 0, 0
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || p == dir {
				return nil
			}
			total++
			if d.Type().IsRegular() || d.Type()&fs.ModeSymlink != 0 {
				covered++
			}
			return nil
		})
		entries, cut := manifestRoot(root, maxFiles, maxWalk)
		if want := covered > maxFiles || total > maxWalk; cut != want {
			t.Fatalf("seed %d: cut = %v with %d files and links (cap %d) and %d entries (budget %d)", seed, cut, covered, maxFiles, total, maxWalk)
		}
		got := digestEntries(entries, cut)
		switch {
		case !cut:
			// Covered whole: the digest of everything, which the old
			// walk gives when no cap is in its way.
			if want, _ := pathDirDigest(dir, 1<<30); got != want {
				t.Fatalf("seed %d: an uncut tree digests as %s, in full %s", seed, got, want)
			}
		case total <= maxWalk:
			// Cut by the file cap alone: the files the old walk
			// stopped at, so a pin taken cut is still equal.
			if want, wantCut := pathDirDigest(dir, maxFiles); got != want || !wantCut {
				t.Fatalf("seed %d: cut at the file cap as %s, the old walk %s (cut %v)", seed, got, want, wantCut)
			}
		}
	}
}
