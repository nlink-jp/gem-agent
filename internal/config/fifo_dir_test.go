package config

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/bounded"
)

// The policy file is written by this runtime and read at every start.
// One that is a FIFO is an error, at once: opened plainly it waited for
// a writer, and so did every start in every project (ADR-0120).
func TestLoadPolicyFileDoesNotWaitOnAFifo(t *testing.T) {
	fifo := fifoAt(t, filepath.Join(t.TempDir(), PolicyFileName))
	var err error
	returnsAtOnce(t, fifo, func() { _, err = LoadPolicyFile(fifo) })
	if !errors.Is(err, bounded.ErrNotRegular) {
		t.Errorf("err = %v, want one that is bounded.ErrNotRegular", err)
	}
}

// The policy is saved through a temporary of a fixed name. A FIFO of
// that name waited for a reader, and the answer the operator had just
// given was never recorded.
func TestPolicySaveDoesNotWaitOnAFifo(t *testing.T) {
	path := filepath.Join(t.TempDir(), PolicyFileName)
	fifo := fifoAt(t, path+".tmp")
	pf, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	returnsAtOnce(t, fifo, func() { err = pf.Save(path) })
	if !errors.Is(err, bounded.ErrNotRegular) {
		t.Errorf("err = %v, want one that is bounded.ErrNotRegular", err)
	}
}

// A policy file past what the decoder accepts is refused as it was
// when the decoder read the file itself.
func TestLoadPolicyFileRefusesAnOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), PolicyFileName)
	if err := os.WriteFile(path, append([]byte("# "), make([]byte, policyFileCap)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicyFile(path); err == nil {
		t.Error("a policy file over the cap was read")
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
