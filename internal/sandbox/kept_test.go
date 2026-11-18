package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The lanes are anchored on real paths, and a lane can replace a
// directory it writes by a link. A name resolved again after that led
// wherever the link did (ADR-0122 §2): the first answer is kept, a name
// that resolves elsewhere is reported with it, and a name that is
// simply gone keeps it.
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
	if got, err := ResolveKept(dir); err != nil || got != dir {
		t.Errorf("asked again with nothing changed: %q, %v", got, err)
	}

	// Gone: the answer stays, and it is no error.
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveKept(dir); err != nil || got != dir {
		t.Errorf("a name that is gone: %q, %v — want the kept answer and no error", got, err)
	}

	// Replaced by a link: the kept answer, and the error that says so.
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveKept(dir)
	var replaced *ReplacedError
	if !errors.As(err, &replaced) || got != dir || replaced.Was != dir {
		t.Errorf("a name that became a link: %q, %v — want the kept answer and a ReplacedError", got, err)
	}

	// A link to nowhere is a replacement too, not a name that is gone.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "nowhere"), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveKept(dir); !errors.As(err, &replaced) {
		t.Errorf("a name that became a link to nowhere: %v, want a ReplacedError", err)
	}
}

// The file tools and the MCP intake name the work directory by its real
// path, the lanes by the name the session was given. Both are kept: the
// real path asked about for the first time after the replacement would
// otherwise take the link's target as its first answer.
func TestTheRealPathIsKeptWithTheName(t *testing.T) {
	base, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real, other, name := filepath.Join(base, "real"), filepath.Join(base, "other"), filepath.Join(base, "name")
	for _, d := range []string{real, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(real, name); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveKept(name); err != nil || got != real {
		t.Fatalf("the name: %q, %v", got, err)
	}
	if err := os.Rename(real, real+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, real); err != nil {
		t.Fatal(err)
	}
	var replaced *ReplacedError
	if _, err := ResolveKept(real); !errors.As(err, &replaced) {
		t.Errorf("the real path, asked about first after it became a link: %v, want a ReplacedError", err)
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

// An added write directory is one a lane can replace by a link, and a
// rebuild makes its probes in it by its name: one that no longer leads
// where it did fails its check before anything is made where the link
// leads.
func TestAnAddedDirectoryThatBecameALinkGetsNoProbes(t *testing.T) {
	base, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, other := filepath.Join(base, "added"), filepath.Join(base, "other")
	for _, d := range []string{dir, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// As the check that added it did.
	if _, err := ResolveKept(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	_, _, err = verifyAddedRoot(nil, AddedRoot{Dir: dir, Write: true}, true)
	var replaced *ReplacedError
	if !errors.As(err, &replaced) {
		t.Errorf("the check of an added directory that became a link: %v, want a ReplacedError", err)
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Errorf("a probe was made where the link leads: %d entries", len(entries))
	}
}
