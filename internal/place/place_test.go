package place

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func fixture(t *testing.T) (base, dir, other string) {
	t.Helper()
	base = t.TempDir()
	dir, other = filepath.Join(base, "dir"), filepath.Join(base, "other")
	for _, d := range []string{dir, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(other, "keep"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return base, dir, other
}

func take(t *testing.T, path string) *Place {
	t.Helper()
	p, err := Take(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Release)
	return p
}

// A place is taken once, and answers to the name it was taken by and
// to its real path. A name nothing was taken under is a fault, not a
// place to take now: taken late, it would be whatever a lane had put
// there.
func TestAPlaceAnswersToTheNamesItWasTakenUnder(t *testing.T) {
	_, dir, other := fixture(t)
	p := take(t, dir)
	real, _ := filepath.EvalSymlinks(dir)
	if p.Real() != real {
		t.Errorf("real path %s, want %s", p.Real(), real)
	}
	for _, name := range []string{dir, real} {
		if got, err := Of(name); err != nil || got != p {
			t.Errorf("Of(%s) = %v, %v", name, got, err)
		}
	}
	if again, err := Take(dir); err != nil || again != p {
		t.Errorf("taken again: %v, %v, want the place already held", again, err)
	}
	if _, err := Of(other); !errors.Is(err, ErrNotTaken) {
		t.Errorf("a directory nobody took: %v, want ErrNotTaken", err)
	}
}

// What a lane writes it can replace. Whatever the name holds afterwards
// — a link to another directory, another directory, a FIFO, nothing —
// it is not the place, and nothing is taken in its stead.
func TestANameThatLeadsElsewhereIsNotThePlace(t *testing.T) {
	for what, replace := range map[string]func(t *testing.T, dir, other string){
		"a link to another directory": func(t *testing.T, dir, other string) {
			must(t, os.Remove(dir))
			must(t, os.Symlink(other, dir))
		},
		"another directory": func(t *testing.T, dir, other string) {
			must(t, os.Remove(dir))
			must(t, os.Mkdir(dir, 0o700))
		},
		"a directory moved over it": func(t *testing.T, dir, other string) {
			must(t, syscall.Rename(other, dir))
		},
		"a FIFO": func(t *testing.T, dir, other string) {
			must(t, os.Remove(dir))
			must(t, syscall.Mkfifo(dir, 0o600))
		},
		"nothing": func(t *testing.T, dir, other string) {
			must(t, os.Remove(dir))
		},
	} {
		t.Run(what, func(t *testing.T) {
			_, dir, other := fixture(t)
			p := take(t, dir)
			if err := p.InPlace(); err != nil {
				t.Fatalf("a place nobody touched: %v", err)
			}
			replace(t, dir, other)
			if err := p.InPlace(); !errors.Is(err, ErrReplaced) {
				t.Errorf("InPlace: %v, want ErrReplaced", err)
			}
			for _, name := range []string{dir, p.Real()} {
				if got, err := Of(name); !errors.Is(err, ErrReplaced) || got != nil {
					t.Errorf("Of(%s) = %v, %v: want ErrReplaced and no place", name, got, err)
				}
			}
			if got, err := Take(dir); !errors.Is(err, ErrReplaced) || got != nil {
				t.Errorf("Take again = %v, %v: what replaced the place was taken in its stead", got, err)
			}
			if root, err := p.OpenRoot(); err == nil {
				_ = root.Close()
				t.Error("a root was opened on what replaced the place")
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A directory inside a place is taken from the directory held, by its
// one name and not through a link: a link a lane left under the name is
// refused, and nothing is made where it leads.
func TestASubPlaceIsTakenFromThePlaceAndNotThroughALink(t *testing.T) {
	_, dir, other := fixture(t)
	p := take(t, dir)
	sub, err := p.Sub("scratch", 0o700)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Release)
	if st, err := os.Lstat(filepath.Join(dir, "scratch")); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Errorf("made %v (%v)", st, err)
	}
	if sub.Real() != filepath.Join(p.Real(), "scratch") {
		t.Errorf("real path %s", sub.Real())
	}
	if again, err := p.Sub("scratch", 0o700); err != nil || again != sub {
		t.Errorf("taken again: %v, %v", again, err)
	}
	if got, err := Of(filepath.Join(dir, "scratch")); err != nil || got != sub {
		t.Errorf("Of by the typed name: %v, %v", got, err)
	}
	// One that was there is taken as it is.
	must(t, os.Mkdir(filepath.Join(dir, "resumed"), 0o755))
	resumed, err := p.Sub("resumed", 0o755)
	if err != nil {
		t.Fatalf("a directory already there: %v", err)
	}
	t.Cleanup(resumed.Release)
	// A link, a FIFO and a file under the name are refused.
	must(t, os.Symlink(other, filepath.Join(dir, "linked")))
	must(t, syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o600))
	for _, name := range []string{"linked", "pipe", "file", "a/b", ".."} {
		if got, err := p.Sub(name, 0o700); err == nil {
			got.Release()
			t.Errorf("%q was taken as a place", name)
		}
	}
	if entries, _ := os.ReadDir(other); len(entries) != 1 {
		t.Errorf("something was made where the link led: %v", entries)
	}
	// And one replaced after it was taken is not the place any more.
	must(t, os.Remove(filepath.Join(dir, "scratch")))
	must(t, os.Symlink(other, filepath.Join(dir, "scratch")))
	if _, err := p.Sub("scratch", 0o700); !errors.Is(err, ErrReplaced) {
		t.Errorf("a sub-place replaced by a link: %v, want ErrReplaced", err)
	}
}

// The root the file tools get is the directory held, and a released
// place's name can be taken again.
func TestOpenRootAndRelease(t *testing.T) {
	_, dir, _ := fixture(t)
	p := take(t, dir)
	must(t, os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600))
	root, err := p.OpenRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat("f"); err != nil {
		t.Errorf("the root is not the place: %v", err)
	}
	_ = root.Close()
	p.Release()
	if _, err := Of(dir); !errors.Is(err, ErrNotTaken) {
		t.Errorf("a released place: %v, want ErrNotTaken", err)
	}
	again, err := Take(dir)
	if err != nil || again == p {
		t.Fatalf("taken after its release: %v, %v", again, err)
	}
	again.Release()
}
