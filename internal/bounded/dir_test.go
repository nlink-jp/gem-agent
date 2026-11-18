package bounded

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// dirFixture is a directory holding one of each thing a walk meets,
// opened as a Dir.
func dirFixture(t *testing.T) (string, *os.Root, *Dir) {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"sub", "listed-only"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"file": "content", "sub/inner": "inner", "listed-only/inner": "inner"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"to-sub": "sub", "to-file": "file", "long": strings.Repeat("t", 300), "sub/link": "inner"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Skipf("no FIFO here: %v", err)
	}
	// Listed and not searched: its names can be read, nothing under
	// them can.
	if err := os.Chmod(filepath.Join(dir, "listed-only"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "listed-only"), 0o755) })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	d, err := DirOf(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return dir, root, d
}

// atOnce runs fn and fails if it is still running after two seconds —
// which is what an open that waits on the fixture's FIFO does. The FIFO
// is then given a writer, so the open ends and the test with it.
func atOnce(t *testing.T, dir string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		w, err := os.OpenFile(filepath.Join(dir, "fifo"), os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err == nil {
			defer func() { _ = w.Close() }()
		}
		<-done
		t.Fatal("it waited on a FIFO")
	}
}

// rootListing is the listing an os.Root gives of rel: the names in
// directory order with their kinds, and what was listed when it
// stopped at an error. It is the listing the skill walk was made of
// when the pins now on disk were taken.
func rootListing(t *testing.T, root *os.Root, rel string, n int) ([]string, error) {
	t.Helper()
	sub := root
	if rel != "." {
		var err error
		if sub, err = root.OpenRoot(rel); err != nil {
			return nil, err
		}
		defer func() { _ = sub.Close() }()
	}
	f, err := sub.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	list, err := f.ReadDir(n)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	var out []string
	for _, e := range list {
		out = append(out, fmt.Sprintf("%s %v", e.Name(), e.Type()))
	}
	return out, err
}

func dirListing(d *Dir, n int) ([]string, error) {
	list, err := d.List(n)
	var out []string
	for _, e := range list {
		out = append(out, fmt.Sprintf("%s %v", e.Name, e.Type))
	}
	return out, err
}

// A sub-directory is opened as a directory, by its one name, and
// nothing else is: not a FIFO (which a plain open waits on), not a
// file, not a link to a directory, not a path.
func TestDirOpensASubDirectoryOnlyByItsNameAndAsADirectory(t *testing.T) {
	dir, _, d := dirFixture(t)
	sub, err := d.OpenDir("sub")
	if err != nil {
		t.Fatalf("a sub-directory: %v", err)
	}
	list, err := dirListing(sub, 10)
	_ = sub.Close()
	sort.Strings(list)
	if err != nil || strings.Join(list, ", ") != "inner ----------, link L---------" {
		t.Errorf("listed %v, %v: want the file and the link in it", list, err)
	}
	for _, name := range []string{"fifo", "file", "to-sub", "nothing", "..", ".", "", "sub/..", "sub/.", "/", "sub\x00"} {
		atOnce(t, dir, func() {
			if sub, err := d.OpenDir(name); err == nil {
				_ = sub.Close()
				t.Errorf("%q was opened as a sub-directory", name)
			}
		})
	}
}

// A file is opened only if it is a regular file, by its one name, and
// without waiting; what comes back reads like any open file.
func TestDirOpensAFileOnlyByItsNameAndIfItIsRegular(t *testing.T) {
	dir, _, d := dirFixture(t)
	f, err := d.OpenRegular("file")
	if err != nil {
		t.Fatalf("a regular file: %v", err)
	}
	data, rerr := io.ReadAll(f)
	flags, ferr := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
	fdflags, fderr := unix.FcntlInt(f.Fd(), unix.F_GETFD, 0)
	_ = f.Close()
	if rerr != nil || string(data) != "content" {
		t.Errorf("read %q, %v", data, rerr)
	}
	if ferr != nil || flags&unix.O_NONBLOCK != 0 {
		t.Errorf("the descriptor is still non-blocking (flags %#x, %v)", flags, ferr)
	}
	if fderr != nil || fdflags&unix.FD_CLOEXEC == 0 {
		t.Errorf("the descriptor would be handed to a command this process runs (flags %#x, %v)", fdflags, fderr)
	}
	for _, name := range []string{"fifo", "sub"} {
		atOnce(t, dir, func() {
			f, err := d.OpenRegular(name)
			if err == nil {
				_ = f.Close()
			}
			if !errors.Is(err, ErrNotRegular) {
				t.Errorf("%q: %v, want ErrNotRegular", name, err)
			}
		})
	}
	for _, name := range []string{"to-file", "nothing", "sub/inner", "../x", "..", ".", ""} {
		if f, err := d.OpenRegular(name); err == nil {
			_ = f.Close()
			t.Errorf("%q was opened as a file", name)
		}
	}
}

// A link's target is read whole, however long, and only a link has
// one — a link named by a path is not read either.
func TestDirReadsALinkByItsName(t *testing.T) {
	_, _, d := dirFixture(t)
	for name, want := range map[string]string{"to-sub": "sub", "to-file": "file", "long": strings.Repeat("t", 300)} {
		if got, err := d.Readlink(name); err != nil || got != want {
			t.Errorf("%q: %d bytes, %v; want the %d of its target", name, len(got), err, len(want))
		}
	}
	for _, name := range []string{"file", "sub", "nothing", "sub/link", "sub/inner", "..", ""} {
		if got, err := d.Readlink(name); err == nil {
			t.Errorf("%q: read %q as a link's target", name, got)
		}
	}
}

// A listing has a number — there is no "all of them" — and starts at
// the start each time.
func TestDirListsNoMoreThanItIsAskedFor(t *testing.T) {
	_, _, d := dirFixture(t)
	for _, n := range []int{0, -1} {
		if list, err := d.List(n); len(list) != 0 || err != nil {
			t.Errorf("asked for %d: %d names, %v", n, len(list), err)
		}
	}
	three, err := dirListing(d, 3)
	if err != nil || len(three) != 3 {
		t.Fatalf("asked for three: %v, %v", three, err)
	}
	all, err := dirListing(d, 100)
	if err != nil || len(all) != 7 || strings.Join(all[:3], ", ") != strings.Join(three, ", ") {
		t.Fatalf("asked for a hundred after three: %v, %v", all, err)
	}
	sort.Strings(all)
	want := "fifo p---------, file ----------, listed-only d---------, long L---------, sub d---------, to-file L---------, to-sub L---------"
	if got := strings.Join(all, ", "); got != want {
		t.Errorf("listed %q, want %q", got, want)
	}
}

// A name that is gone between the listing and the look at it is left
// out, and the listing goes on: a skill being written while it is
// walked is not an error.
func TestDirLeavesOutANameThatIsGone(t *testing.T) {
	_, _, d := dirFixture(t)
	list, err := d.kinds([]string{"file", "gone", "sub", "gone-too", "fifo"})
	var names []string
	for _, e := range list {
		names = append(names, e.Name)
	}
	if err != nil || strings.Join(names, ", ") != "file, sub, fifo" {
		t.Errorf("listed %v, %v: want the three that are there", names, err)
	}
}

// A listing is the one an os.Root gives: the same names in the same
// order with the same kinds, and stopped at the same name when one
// cannot be looked at. The pins on disk were taken from that listing,
// so a name more or a name less is another digest for an unchanged
// skill. The directory's own record of a kind is not asked: with it, a
// name an ACL denies a look at is listed, and so is everything after it.
func TestDirListsWhatARootLists(t *testing.T) {
	dir, root, d := dirFixture(t)
	closed := filepath.Join(dir, "sub", "denied")
	for _, name := range []string{"a", "b", "c", "denied", "e", "f", "g", "h"} {
		if err := os.WriteFile(filepath.Join(dir, "sub", name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	denied := exec.Command("/bin/chmod", "+a", "everyone deny readattr", closed).Run() == nil
	for _, c := range []struct {
		rel string
		n   int
	}{{".", 100}, {".", 2}, {"sub", 100}, {"sub", 4}, {"sub", 1}, {"listed-only", 100}} {
		if c.rel == "listed-only" && os.Geteuid() == 0 {
			continue // nothing is closed to root
		}
		want, wantErr := rootListing(t, root, c.rel, c.n)
		at := d
		if c.rel != "." {
			sub, err := d.OpenDir(c.rel)
			if err != nil {
				t.Errorf("%s: %v", c.rel, err)
				continue
			}
			defer func() { _ = sub.Close() }()
			at = sub
		}
		got, gotErr := dirListing(at, c.n)
		if strings.Join(got, ", ") != strings.Join(want, ", ") || (gotErr == nil) != (wantErr == nil) {
			t.Errorf("%s, %d names:\n  listed  %v (%v)\n  a root  %v (%v)", c.rel, c.n, got, gotErr, want, wantErr)
		}
		if c.rel == "sub" && c.n == 100 {
			switch {
			case !denied:
				t.Log("no ACL could be set here: the listing that stops at a name was not exercised")
			case os.Geteuid() == 0:
			case gotErr == nil || len(got) >= 10:
				t.Errorf("a name that cannot be looked at did not end the listing: %d names, %v", len(got), gotErr)
			}
		}
	}
}

// A Dir holds one descriptor, however much of it is listed: a walk
// holds one for every level it is in.
func TestDirHoldsOneDescriptorHoweverMuchIsListed(t *testing.T) {
	_, _, d := dirFixture(t)
	open := func() int {
		list, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Skipf("no /dev/fd here: %v", err)
		}
		return len(list)
	}
	before := open()
	for range 5 {
		if list, err := d.List(3); err != nil || len(list) != 3 {
			t.Fatalf("listed %d names, %v", len(list), err)
		}
	}
	if after := open(); after != before {
		t.Errorf("%d descriptors open before five listings, %d after", before, after)
	}
}
