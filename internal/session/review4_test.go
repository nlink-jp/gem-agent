package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortWriter fails one write part-way, then works.
type shortWriter struct {
	strings.Builder
	failAt int // fail the first write, landing this many bytes
	failed bool
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if !w.failed {
		w.failed = true
		_, _ = w.Builder.Write(p[:w.failAt])
		return w.failAt, errors.New("disk full")
	}
	return w.Builder.Write(p)
}

// Review round 4: a write that failed part-way leaves a fragment
// without its newline; the next record must not glue onto it — that
// merged line was one invalid record that swallowed a whole message on
// resume. The logger repairs the tear itself, as Reopen does across
// processes.
func TestTornWriteIsRepairedInProcess(t *testing.T) {
	w := &shortWriter{failAt: 5}
	l := &Logger{w: w, id: "t"}
	if err := l.Log("usage", map[string]any{"n": 1}); err == nil {
		t.Fatal("the short write must be reported")
	}
	if err := l.Log(KindMessage, map[string]any{"role": "user", "content": "two"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(w.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want the fragment and the record on separate lines:\n%s", len(lines), w.String())
	}
	if !strings.Contains(lines[1], `"two"`) || strings.Contains(lines[0], `"two"`) {
		t.Fatalf("the record glued onto the fragment:\n%s", w.String())
	}
}

// A write that landed nothing leaves nothing to repair: no stray blank
// line precedes the next record.
func TestFailedWholeWriteNeedsNoRepair(t *testing.T) {
	w := &shortWriter{failAt: 0}
	l := &Logger{w: w, id: "t"}
	_ = l.Log("usage", nil)
	if err := l.Log(KindMessage, map[string]any{"content": "x"}); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(w.String(), "\n") {
		t.Fatalf("a blank line was inserted for a write that landed nothing:\n%q", w.String())
	}
}

// Review round 4: a legacy flat transcript (pre-ADR-0022) is resumed
// in place and holds its lock there; InUse must look where Reopen
// does, or a live legacy session reads as free and `workdirs clean`
// deletes its directory.
func TestInUseSeesALegacyFlatSession(t *testing.T) {
	dir, project := t.TempDir(), t.TempDir()
	const id = "20260819-150102"
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"session_start","data":{"schema":2}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lg, err := Reopen(dir, project, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lg.Close() }()
	if lg.Path() != path {
		t.Fatalf("resumed at %q, want the flat file %q", lg.Path(), path)
	}
	if !InUse(dir, project, id) {
		t.Fatal("a live legacy session read as not in use")
	}
}
