package bounded

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadAllReportsMore(t *testing.T) {
	data, more, err := ReadAll(strings.NewReader("abcdef"), 4)
	if err != nil || string(data) != "abcd" || !more {
		t.Errorf("got %q more=%v err=%v", data, more, err)
	}
	data, more, err = ReadAll(strings.NewReader("abcd"), 4)
	if err != nil || string(data) != "abcd" || more {
		t.Errorf("exact cap: got %q more=%v err=%v", data, more, err)
	}
}

func TestReadDirReportsMore(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	entries, more, err := ReadDir(d, 2)
	if err != nil || len(entries) != 2 || !more {
		t.Errorf("got %d entries more=%v err=%v", len(entries), more, err)
	}
}

func TestWriterCutsWholeRunes(t *testing.T) {
	w := NewWriter(4)
	_, _ = w.Write([]byte("あいう"))
	data, more := w.Bytes()
	if !more || !utf8.Valid(data) || string(data) != "あ" {
		t.Errorf("got %q more=%v", data, more)
	}
	if w.Total() != 9 {
		t.Errorf("total = %d", w.Total())
	}
	w = NewWriter(4)
	_, _ = w.Write([]byte("abcd"))
	if data, more := w.Bytes(); more || string(data) != "abcd" {
		t.Errorf("exact limit: %q more=%v", data, more)
	}
}

func TestCombinedOutputIsCapped(t *testing.T) {
	out, more, err := CombinedOutput(exec.Command("/bin/sh", "-c", "yes | head -c 5000"), 100)
	if err != nil || !more || len(out) != 100 {
		t.Errorf("len=%d more=%v err=%v", len(out), more, err)
	}
	s := Scanner(strings.NewReader(strings.Repeat("x", 200)+"\n"), 16, 64)
	if s.Scan() {
		t.Error("a line past max was accepted")
	}
}

func TestTrimIncompleteRune(t *testing.T) {
	data, more, _ := ReadAll(strings.NewReader("あいう"), 4)
	if !more || string(TrimIncompleteRune(data)) != "あ" {
		t.Errorf("got %q", TrimIncompleteRune(data))
	}
	if string(TrimIncompleteRune([]byte("abc"))) != "abc" {
		t.Error("complete text trimmed")
	}
}

// HeadTail's tail stays bounded however many small writes arrive.
func TestHeadTailTailIsBoundedOverManyWrites(t *testing.T) {
	b := NewHeadTail(40, 1<<20, nil)
	for i := range 100000 {
		_, _ = b.Write([]byte{byte('a' + i%26)})
	}
	if len(b.tail) > 2*b.tailLimit()+utf8.UTFMax {
		t.Errorf("tail grew to %d", len(b.tail))
	}
	v := b.View()
	if v.Total != 100000 || len(v.Head) != 30 || len(v.Tail) != 10 || v.TailStart != 99990 {
		t.Errorf("view = total %d head %d tail %d start %d", v.Total, len(v.Head), len(v.Tail), v.TailStart)
	}
}

// A tail of continuation bytes (binary output) is kept, not skipped away.
func TestHeadTailBinaryTailIsKept(t *testing.T) {
	b := NewHeadTail(40, 1<<20, nil) // tail limit 10
	_, _ = b.Write(make([]byte, 40))
	_, _ = b.Write([]byte(strings.Repeat("\x80", 20)))
	// At most UTFMax-1 leading continuation bytes are skipped.
	if v := b.View(); len(v.Tail) != 10-(utf8.UTFMax-1) || v.TailStart != 60-int64(len(v.Tail)) {
		t.Errorf("tail = %d bytes from %d", len(v.Tail), v.TailStart)
	}
}
