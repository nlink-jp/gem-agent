package riskbook

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/bounded"
	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// The rulebook's two layers are files of fixed names: one beside the
// configuration, one in the state directory, written by /learn. A FIFO
// at either is refused; read through a plain open it held the start
// (ADR-0120).
func TestLoadDoesNotWaitOnAFifo(t *testing.T) {
	const proj = "/some/project"
	for name, place := range map[string]func(t *testing.T, cfg string) string{
		"the base layer": func(t *testing.T, cfg string) string { return BasePath(cfg) },
		"the project layer": func(t *testing.T, cfg string) string {
			p, err := ProjectPath(proj)
			if err != nil {
				t.Fatal(err)
			}
			return p
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(statedir.EnvRoot, t.TempDir())
			cfg := filepath.Join(t.TempDir(), "config.toml")
			fifo := fifoAt(t, place(t, cfg))
			var err error
			returnsAtOnce(t, fifo, func() { _, err = Load(cfg, proj) })
			if !errors.Is(err, bounded.ErrNotRegular) {
				t.Errorf("err = %v, want one that is bounded.ErrNotRegular", err)
			}
		})
	}
}

// The project layer is saved through a temporary of a fixed name. A
// FIFO of that name waited for a reader.
func TestSaveProjectDoesNotWaitOnAFifo(t *testing.T) {
	const proj = "/some/project"
	t.Setenv(statedir.EnvRoot, t.TempDir())
	p, err := ProjectPath(proj)
	if err != nil {
		t.Fatal(err)
	}
	fifo := fifoAt(t, p+".tmp")
	returnsAtOnce(t, fifo, func() { err = SaveProject(proj, "a rule") })
	if !errors.Is(err, bounded.ErrNotRegular) {
		t.Errorf("err = %v, want one that is bounded.ErrNotRegular", err)
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
