package bounded

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// withinTime runs open and fails the test if it has not returned in two
// seconds. A blocked open of a FIFO is released by opening its other
// end, so the test binary can still exit.
func withinTime(t *testing.T, fifo string, open func() (*os.File, error)) (*os.File, error) {
	t.Helper()
	type result struct {
		f   *os.File
		err error
	}
	done := make(chan result, 1)
	go func() {
		f, err := open()
		done <- result{f, err}
	}()
	select {
	case r := <-done:
		return r.f, r.err
	case <-time.After(2 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("the open waited: a FIFO with no writer held it")
		return nil, nil
	}
}

// A plain open of a FIFO with no writer waits until one arrives. The
// name is one a project chooses — a skill's SKILL.md — so that open
// held skill discovery, and startup with it, for as long as nobody
// wrote. OpenRegular does not wait, and takes only a regular file.
func TestOpenRegularDoesNotWaitAndTakesOnlyARegularFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pipe", filepath.Join(dir, "link-to-pipe")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	for name, regular := range map[string]bool{"file": true, "link": true, "pipe": false, "link-to-pipe": false, "sub": false} {
		for how, open := range map[string]func() (*os.File, error){
			"under a root": func() (*os.File, error) { return OpenRegular(root, name) },
			"by path":      func() (*os.File, error) { return OpenRegularPath(filepath.Join(dir, name)) },
		} {
			f, err := withinTime(t, fifo, open)
			if !regular {
				if !errors.Is(err, ErrNotRegular) || f != nil {
					t.Errorf("%s, %s: f=%v err=%v, want ErrNotRegular", name, how, f, err)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s, %s: %v", name, how, err)
				continue
			}
			data, err := io.ReadAll(io.LimitReader(f, 64))
			_ = f.Close()
			if err != nil || string(data) != "content" {
				t.Errorf("%s, %s: read %q, %v", name, how, data, err)
			}
		}
	}
	if _, err := OpenRegular(root, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
}
