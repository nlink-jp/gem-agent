package trustpin

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The walk lists a directory, sees that a name in it is a directory,
// and then opens that name. A name swapped for a FIFO between the two
// held the open as any FIFO does (ADR-0120, the residual its first form
// left for the sub-directories of a skill). Here a sub-directory's name
// is swapped back and forth between a directory and a FIFO as fast as
// the disk allows while the skill is digested again and again: every
// walk comes back, with the sub-directory's file or without it, and
// none waits.
func TestTheWalkDoesNotWaitOnASubDirectorySwappedForAFifo(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "SKILL.md"), []byte("skill"))
	mustWrite(t, filepath.Join(dir, "sub", "inner.md"), []byte("inner"))
	// What the name is swapped between is parked beside the skill, not
	// in it: the walk never lists the parked one.
	park := t.TempDir()
	sub := filepath.Join(dir, "sub")
	parkedDir, parkedFifo := filepath.Join(park, "dir"), filepath.Join(park, "fifo")
	if err := syscall.Mkfifo(parkedFifo, 0o644); err != nil {
		t.Skipf("no FIFO here: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	const (
		pace  = 100 * time.Microsecond
		burst = 31
		rest  = 10 * time.Millisecond
		// What a run must have seen to have shown anything.
		enoughSwaps, enoughWalks = 100, 20
	)
	var swaps atomic.Int64
	stop := make(chan struct{})
	swapped := make(chan struct{})
	go func() {
		defer close(swapped)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			out, in := parkedDir, parkedFifo
			if i%2 == 1 {
				out, in = parkedFifo, parkedDir
			}
			if os.Rename(sub, out) != nil || os.Rename(in, sub) != nil {
				return
			}
			swaps.Add(1)
			// Paced two ways. Mostly the swaps come close together,
			// which is when a walk is caught between its listing and
			// its open; renames that close keep the directory so busy
			// that a walk rarely gets through a whole sub-directory, so
			// now and then the name is left alone for a while — an odd
			// number of swaps apart, so it is left as a directory one
			// time and as a FIFO the next.
			if i%burst == burst-1 {
				time.Sleep(rest)
			} else {
				time.Sleep(pace)
			}
		}
	}()

	var with, without atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Two and a half seconds as a rule. On a machine too busy to
		// get enough walks and swaps through in that time, for as long
		// as it takes, within reason: a few walks under a few swaps
		// have shown nothing.
		start := time.Now()
		for time.Since(start) < 2500*time.Millisecond ||
			((swaps.Load() < enoughSwaps || with.Load()+without.Load() < enoughWalks) && time.Since(start) < 10*time.Second) {
			_, files, cut := SkillManifest(root)
			if cut {
				t.Errorf("a skill of two files was cut")
				return
			}
			if _, ok := files["SKILL.md"]; !ok {
				t.Errorf("the walk lost SKILL.md: %v", files)
				return
			}
			if _, ok := files["sub/inner.md"]; ok {
				with.Add(1)
			} else {
				without.Add(1)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		close(stop)
		<-swapped
		// Let the held open go, wherever the FIFO now is.
		for _, p := range []string{sub, parkedFifo} {
			if f, err := os.OpenFile(p, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
				defer func() { _ = f.Close() }()
			}
		}
		t.Fatalf("a walk waited on a FIFO swapped in under a sub-directory's name (after %d with the sub-directory, %d without)", with.Load(), without.Load())
	}
	close(stop)
	<-swapped
	// A walk that gets through a whole sub-directory between two swaps
	// is the rare one, so it is reported and not required: what is
	// required is that the name was swapped many times under walks.
	if swaps.Load() < enoughSwaps || with.Load()+without.Load() < enoughWalks {
		t.Errorf("%d swaps under %d walks: the swap was not exercised", swaps.Load(), with.Load()+without.Load())
	}
	t.Logf("%d swaps, %d walks with the sub-directory, %d without, none waited", swaps.Load(), with.Load(), without.Load())
}
