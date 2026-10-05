package tools

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --- shell_exec output: head, tail and the whole saved (ADR-0096 §3) ---

func registryWithWorkDir(t *testing.T) (*Registry, string) {
	t.Helper()
	r := newRegistry(t)
	work := t.TempDir()
	if err := r.UseWorkDir(work); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	return r, real
}

var savedPath = regexp.MustCompile(`the whole output is saved: (\S+)\]$`)

// The case the record exists for: a script whose closing totals come
// last. They reach the model, and every byte is in the saved file.
func TestShellExecKeepsTheTailAndSavesTheWhole(t *testing.T) {
	r, work := registryWithWorkDir(t)
	out, err := run(t, r, "shell_exec", map[string]any{"command": "seq 1 20000; echo TOTAL=20000"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\nTOTAL=20000\n\n[output: ") {
		t.Errorf("the closing line is not in front of the model: %q", out[len(out)-300:])
	}
	if !strings.HasPrefix(out, "1\n2\n3\n") {
		t.Errorf("the head is missing: %q", out[:20])
	}
	m := savedPath.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no saved path in the note: %q", out[len(out)-300:])
	}
	if filepath.Dir(m[1]) != work {
		t.Errorf("saved outside the work directory: %s", m[1])
	}
	saved, err := os.ReadFile(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(saved), "1\n2\n") || !strings.HasSuffix(string(saved), "\n20000\nTOTAL=20000\n") {
		t.Errorf("saved file is not the whole output (%d bytes)", len(saved))
	}
	if len(out) > OutputCap+400 {
		t.Errorf("the inline result is %d bytes", len(out))
	}
}

// Output that fits leaves no file and no note.
func TestShellExecSmallOutputIsUntouched(t *testing.T) {
	r, work := registryWithWorkDir(t)
	out, err := run(t, r, "shell_exec", map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello\n" {
		t.Errorf("out = %q", out)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Errorf("a small output left %d file(s)", len(entries))
	}
}

// The operator lane may read credentials; its output is not copied to a
// file read_file could open without approval, and the note says so.
func TestShellOperatorLaneIsNotSaved(t *testing.T) {
	r, work := registryWithWorkDir(t)
	open, noSave, release := r.shellSpoolOpener(true)
	defer release()
	if open != nil {
		t.Fatal("the operator lane got a spool")
	}
	o := newShellOutput(8, shellSpoolCap, open, noSave)
	_, _ = o.Write([]byte(strings.Repeat("s", 20)))
	o.Close()
	if s := o.String(); !strings.HasSuffix(s, "operator-lane output is not written to disk)]") {
		t.Errorf("String = %q", s)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Errorf("operator-lane output reached the work directory")
	}
}

func TestShellOutputWithoutAWorkDirSaysTheMiddleIsLost(t *testing.T) {
	r := newRegistry(t)
	out, err := run(t, r, "shell_exec", map[string]any{"command": "yes | head -c 30000"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "the middle is lost (no session work directory)]") {
		t.Errorf("out tail = %q", out[len(out)-120:])
	}
}

type failingWriter struct{ after int }

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("no space left on device")
	}
	n := min(len(p), f.after)
	f.after -= n
	if n < len(p) {
		return n, errors.New("no space left on device")
	}
	return n, nil
}
func (f *failingWriter) Close() error { return nil }

// A save that fails mid-stream says how much was saved before it did.
func TestShellOutputStatesAFailedSave(t *testing.T) {
	o := newShellOutput(8, shellSpoolCap, func() (io.WriteCloser, string, error) {
		return &failingWriter{after: 12}, "/w/shell.txt", nil
	}, "")
	_, _ = o.Write([]byte(strings.Repeat("a", 10)))
	_, _ = o.Write([]byte(strings.Repeat("b", 10)))
	o.Close()
	if s := o.String(); !strings.HasSuffix(s, "the first 12 bytes are saved (saving stopped: no space left on device): /w/shell.txt]") {
		t.Errorf("String = %q", s)
	}
	o = newShellOutput(8, shellSpoolCap, func() (io.WriteCloser, string, error) {
		return nil, "", errors.New("permission denied")
	}, "")
	_, _ = o.Write([]byte(strings.Repeat("a", 20)))
	if s := o.String(); !strings.HasSuffix(s, "the middle could not be saved (permission denied), so it is lost]") {
		t.Errorf("String = %q", s)
	}
}

type sink struct{ strings.Builder }

func (s *sink) Close() error { return nil }

// Past the spool cap the stream is counted, not written.
func TestShellOutputSpoolCap(t *testing.T) {
	var got sink
	o := newShellOutput(8, 15, func() (io.WriteCloser, string, error) {
		return &got, "/w/shell.txt", nil
	}, "")
	for range 5 {
		_, _ = o.Write([]byte("0123456789"))
	}
	o.Close()
	if got.Len() != 15 {
		t.Errorf("spooled %d bytes, cap 15", got.Len())
	}
	if s := o.String(); !strings.HasSuffix(s, "[output: 50 bytes; shown: bytes 0–6 and 48–50; the first 15 bytes are saved (the save cap): /w/shell.txt]") {
		t.Errorf("String = %q", s)
	}
}

// Many small writes keep the tail bounded and exact.
func TestShellOutputTailOverManyWrites(t *testing.T) {
	o := newShellOutput(40, shellSpoolCap, nil, "")
	for i := range 1000 {
		_, _ = o.Write([]byte{byte('a' + i%26)})
	}
	s := o.String()
	want := ""
	for i := 990; i < 1000; i++ {
		want += string(rune('a' + i%26))
	}
	if !strings.Contains(s, "\n"+want+"\n[output: 1000 bytes; shown: bytes 0–30 and 990–1000;") {
		t.Errorf("String = %q, want tail %q", s, want)
	}
}
