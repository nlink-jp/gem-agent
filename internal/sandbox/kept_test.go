package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// The lanes are anchored on real paths, and a command in the write lane
// can replace a directory it writes by a link. A name resolved again
// after that led wherever the link did (ADR-0122 §2): the first answer
// a name gave is the one every later call returns.
func TestTheFirstAnswerANameGaveIsKept(t *testing.T) {
	base, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, other := filepath.Join(base, "dir"), filepath.Join(base, "other")
	for _, d := range []string{dir, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := ResolveKept(dir); err != nil || got != dir {
		t.Fatalf("the first answer: %q, %v", got, err)
	}
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveKept(dir); err != nil || got != dir {
		t.Errorf("a name that is gone: %q, %v — want the kept answer", got, err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveKept(dir); err != nil || got != dir {
		t.Errorf("a name that became a link: %q, %v — want the kept answer, not %s", got, err, other)
	}
	// A name asked about for the first time is resolved as it is.
	if got, err := ResolveKept(filepath.Join(base, "nothing")); err == nil {
		t.Errorf("a name that never existed resolved to %q", got)
	}
}

// The temporary directory is one the write lane writes, and the
// profiles and the rule tier both read ScratchDirs: a link put under
// its name does not become a scratch directory.
func TestTheScratchDirectoriesKeepTheTemporaryDirectoryTheyFirstHad(t *testing.T) {
	base, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tmp, other := filepath.Join(base, "tmp"), filepath.Join(base, "other")
	for _, d := range []string{tmp, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", tmp)
	holds := func(want string) bool {
		for _, d := range ScratchDirs() {
			if d == want {
				return true
			}
		}
		return false
	}
	if !holds(tmp) {
		t.Fatalf("the scratch directories do not hold the temporary directory: %v", ScratchDirs())
	}
	if err := os.Rename(tmp, tmp+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, tmp); err != nil {
		t.Fatal(err)
	}
	if !holds(tmp) || holds(other) {
		t.Errorf("after the temporary directory became a link: %v — want %s still, and not %s", ScratchDirs(), tmp, other)
	}
}
