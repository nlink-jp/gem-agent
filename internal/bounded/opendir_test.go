package bounded

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A directory is opened as a directory (ADR-0120). A plain open of a
// FIFO waits for a writer, and a name that should be a directory can be
// a FIFO: <project>/.claude/skills held discovery, the trust probe and
// the pins that way. OpenDir answers at once, for the name and for a
// link to it, and takes nothing but a directory.
func TestOpenDirDoesNotWaitAndTakesOnlyADirectory(t *testing.T) {
	dir := t.TempDir()
	fifo := fifoAt(t, filepath.Join(dir, "pipe"))
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub", "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"link-to-sub": "sub", "link-to-pipe": "pipe"} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	for name, isDir := range map[string]bool{"sub": true, "link-to-sub": true, "pipe": false, "link-to-pipe": false, "file": false} {
		for how, open := range map[string]func() (*os.File, error){
			"by path":      func() (*os.File, error) { return OpenDir(filepath.Join(dir, name)) },
			"under a root": func() (*os.File, error) { return OpenDirIn(root, name) },
		} {
			var d *os.File
			var err error
			returnsAtOnce(t, fifo, func() { d, err = open() })
			if !isDir {
				if err == nil {
					_ = d.Close()
					t.Errorf("%s, %s: opened; want an error", name, how)
				} else if name != "file" && !errors.Is(err, syscall.ENOTDIR) {
					t.Errorf("%s, %s: %v, want not-a-directory", name, how, err)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s, %s: %v", name, how, err)
				continue
			}
			entries, more, err := ReadDir(d, 10)
			_ = d.Close()
			if err != nil || more || len(entries) != 1 || entries[0].Name() != "inner" {
				t.Errorf("%s, %s: listed %v more=%v err=%v", name, how, entries, more, err)
			}
		}
	}
	if _, err := OpenDir(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing directory: %v", err)
	}
}

// os.OpenRoot opens plainly and looks afterwards, so a FIFO where a
// root is expected held it — the write lane can put one where the work
// directory, an added directory or the project directory was
// (ADR-0120). OpenRoot looks first.
func TestOpenRootDoesNotWaitOnAFifo(t *testing.T) {
	dir := t.TempDir()
	fifo := fifoAt(t, filepath.Join(dir, "pipe"))
	if err := os.Symlink("pipe", filepath.Join(dir, "link-to-pipe")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pipe", "link-to-pipe", "file"} {
		var root *os.Root
		var err error
		returnsAtOnce(t, fifo, func() { root, err = OpenRoot(filepath.Join(dir, name)) })
		if err == nil {
			_ = root.Close()
			t.Errorf("%s: opened as a root", name)
		} else if !errors.Is(err, syscall.ENOTDIR) {
			t.Errorf("%s: %v, want not-a-directory", name, err)
		}
	}
	root, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat("file"); err != nil {
		t.Errorf("the root does not read its directory: %v", err)
	}
	_ = root.Close()
	if _, err := OpenRoot(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing directory: %v", err)
	}
}

// Opened for writing, a FIFO waits for a reader. WriteFile writes a
// regular file or a new one, through a link as os.WriteFile does, and
// refuses anything else without waiting.
func TestWriteFileDoesNotWaitOnAFifo(t *testing.T) {
	dir := t.TempDir()
	fifo := fifoAt(t, filepath.Join(dir, "pipe"))
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"link-to-file": "file", "link-to-pipe": "pipe"} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"pipe", "link-to-pipe"} {
		var err error
		returnsAtOnce(t, fifo, func() { err = WriteFile(filepath.Join(dir, name), []byte("new"), 0o644) })
		if !errors.Is(err, ErrNotRegular) {
			t.Errorf("%s: %v, want ErrNotRegular", name, err)
		}
	}
	for name, reads := range map[string]string{"file": "file", "link-to-file": "file", "fresh": "fresh"} {
		if err := WriteFile(filepath.Join(dir, name), []byte("new "+name), 0o644); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if data, err := os.ReadFile(filepath.Join(dir, reads)); err != nil || string(data) != "new "+name {
			t.Errorf("%s: wrote %q, %v", name, data, err)
		}
	}
}

// fifoAt makes a FIFO at path, with its parent directories.
func fifoAt(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// returnsAtOnce runs fn and fails the test if it has not returned in
// two seconds. What waits on a FIFO — an open for reading or one for
// writing — is released by opening the FIFO at both ends, so the test
// binary can still exit.
func returnsAtOnce(t *testing.T, fifo string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(2 * time.Second):
	}
	for {
		if rw, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
			_ = rw.Close()
		}
		select {
		case <-done:
			t.Fatal("it waited on a FIFO")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
