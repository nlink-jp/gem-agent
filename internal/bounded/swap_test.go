package bounded

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A look and then a plain open leaves a moment between the two, and a
// name swapped for a FIFO in that moment held the open as any FIFO does
// (ADR-0120, the residual of its first form). Here the name is swapped
// back and forth between a regular file and a FIFO as fast as the disk
// allows while it is opened again and again: every open comes back,
// with the file or with a refusal, and none waits.
func TestOpenRegularDoesNotWaitOnANameSwappedForAFifo(t *testing.T) {
	for how, open := range map[string]func(root *os.Root, dir, name string) (*os.File, error){
		"under a root": func(root *os.Root, _, name string) (*os.File, error) { return OpenRegular(root, name) },
		"by path":      func(_ *os.Root, dir, name string) (*os.File, error) { return OpenRegularPath(filepath.Join(dir, name)) },
		"by path, no link followed": func(_ *os.Root, dir, name string) (*os.File, error) {
			return OpenRegularPathNoFollow(filepath.Join(dir, name))
		},
	} {
		t.Run(how, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "name")
			if err := os.WriteFile(name, []byte("content"), 0o644); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()

			// The swapper: a new FIFO renamed over the name, then a new
			// file renamed over it, without end. The name always
			// exists; what it is changes.
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
					tmp := filepath.Join(dir, fmt.Sprintf("next-%d", i))
					if i%2 == 0 {
						if syscall.Mkfifo(tmp, 0o644) != nil {
							return
						}
					} else if os.WriteFile(tmp, []byte("content"), 0o644) != nil {
						return
					}
					if os.Rename(tmp, name) != nil {
						return
					}
				}
			}()

			var opened, refused atomic.Int64
			done := make(chan struct{})
			go func() {
				defer close(done)
				// A second and a half as a rule. On a machine too busy
				// to get an open of each kind through in that time, for
				// as long as it takes, within reason: a run that saw
				// one kind only has shown nothing.
				start := time.Now()
				for time.Since(start) < 1500*time.Millisecond ||
					((opened.Load() == 0 || refused.Load() == 0) && time.Since(start) < 8*time.Second) {
					f, err := open(root, dir, "name")
					if err != nil {
						if !errors.Is(err, ErrNotRegular) && !errors.Is(err, os.ErrNotExist) {
							t.Errorf("an open failed with %v", err)
							return
						}
						refused.Add(1)
						continue
					}
					data, rerr := io.ReadAll(io.LimitReader(f, 64))
					_ = f.Close()
					if rerr != nil || string(data) != "content" {
						t.Errorf("read %q, %v", data, rerr)
						return
					}
					opened.Add(1)
				}
			}()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				close(stop)
				// The FIFO that holds the open has been renamed over and
				// has no name left to release it by; the goroutine is
				// left behind and the test binary exits without it.
				t.Fatalf("an open waited on a FIFO swapped in under the name (after %d opened, %d refused)", opened.Load(), refused.Load())
			}
			close(stop)
			<-swapped
			if opened.Load() == 0 || refused.Load() == 0 {
				t.Errorf("%d opened, %d refused: the swap was not exercised both ways", opened.Load(), refused.Load())
			}
			t.Logf("%d opened, %d refused, none waited", opened.Load(), refused.Load())
		})
	}
}

// What OpenRegular hands back reads like any open file: the flag that
// kept the open from waiting is cleared once the kind is known.
func TestOpenRegularReturnsABlockingDescriptor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for how, open := range map[string]func() (*os.File, error){
		"under a root": func() (*os.File, error) { return OpenRegular(root, "file") },
		"by path":      func() (*os.File, error) { return OpenRegularPath(filepath.Join(dir, "file")) },
	} {
		f, err := open()
		if err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
		_ = f.Close()
		if err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		if flags&unix.O_NONBLOCK != 0 {
			t.Errorf("%s: the descriptor is still non-blocking", how)
		}
	}
}
