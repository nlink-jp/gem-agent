package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// CanonicalDir is the kernel's name for a directory: the volume's case,
// the stored Unicode form, links resolved. What is not a directory is
// refused without opening it for real — a FIFO opened plainly blocks
// until a writer comes (review of v0.94.0, #1).
func TestCanonicalDir(t *testing.T) {
	base, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "Notes")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := CanonicalDir(dir); err != nil || got != dir {
		t.Errorf("CanonicalDir(%s) = %q, %v", dir, got, err)
	}
	// Another case of it is the same directory on a case-folding volume,
	// and its name is the one on disk.
	if _, err := os.Stat(filepath.Join(base, "NOTES")); err == nil {
		if got, err := CanonicalDir(filepath.Join(base, "NOTES")); err != nil || got != dir {
			t.Errorf("case variant = %q, %v; want %q", got, err, dir)
		}
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if got, err := CanonicalDir(link); err != nil || got != dir {
		t.Errorf("link = %q, %v; want %q", got, err, dir)
	}

	fifo := filepath.Join(base, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := CanonicalDir(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO was accepted as a directory")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CanonicalDir blocked on a FIFO")
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalDir(file); err == nil {
		t.Error("a regular file was accepted as a directory")
	}
	if got, err := CanonicalDir(filepath.Join(base, "missing")); err == nil || got != "" {
		t.Errorf("missing = %q, %v; want an error and no path", got, err)
	}
}

// The name a directory is stored under is what the kernel gives back,
// whichever Unicode form was typed: APFS keeps the form it was created
// with and finds it under either (measured by the review of v0.94.0,
// #2). A Japanese name typed through the IME is composed; one created
// by another tool may be decomposed.
func TestCanonicalDirKeepsTheStoredForm(t *testing.T) {
	base, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	decomposed := "ド" + "キュメント" // ト + combining dakuten
	composed := "ドキュメント"
	stored := filepath.Join(base, decomposed)
	if err := os.Mkdir(stored, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, composed)); err != nil {
		t.Skip("this volume does not fold Unicode forms")
	}
	got, err := CanonicalDir(filepath.Join(base, composed))
	if err != nil || got != stored {
		t.Errorf("composed spelling = %q, %v; want the stored %q", got, err, stored)
	}
	if strings.Contains(got, composed) {
		t.Errorf("the typed form came back: %q", got)
	}
}

// Absorbed is §12's rule: roots at or inside the new one whose level is
// not higher.
func TestAbsorbed(t *testing.T) {
	roots := []AddedRoot{
		{Dir: "/r/a", Write: false},
		{Dir: "/r/a/w", Write: true},
		{Dir: "/r/ab", Write: false},
		{Dir: "/r/b", Write: true},
	}
	names := func(rs []AddedRoot) string {
		var s []string
		for _, r := range rs {
			s = append(s, r.Dir)
		}
		return strings.Join(s, ",")
	}
	for _, tc := range []struct {
		dir   string
		write bool
		want  string
	}{
		{"/r/a", true, "/r/a,/r/a/w"}, // write over read and write; /r/ab is a sibling
		{"/r/a", false, "/r/a"},       // read takes only read
		{"/r", false, "/r/a,/r/ab"},   // a read root never takes a write one
		{"/r/b", false, ""},           // the same directory at a lower level is not absorbed
		{"/r/c", true, ""},
	} {
		if got := names(Absorbed(tc.dir, tc.write, roots)); got != tc.want {
			t.Errorf("Absorbed(%s, %v) = %q, want %q", tc.dir, tc.write, got, tc.want)
		}
	}
}
