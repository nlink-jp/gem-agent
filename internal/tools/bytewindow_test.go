package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// --- byte windows (ADR-0096 §2) ---

func writeProjectFile(t *testing.T, r *Registry, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The case the ADR exists for: a single line past readCap whose last
// bytes carry the metadata. A line window cannot reach them; a negative
// offset does.
func TestReadFileByteWindowReachesTheTailOfOneLongLine(t *testing.T) {
	r := newRegistry(t)
	tail := `], "truncated": true, "total_rows": 200}`
	body := `{"results":[` + strings.Repeat(`{"_raw":"x"},`, readCap/13+100) + `{"_raw":"y"}` + tail
	writeProjectFile(t, r, "spill.json", body)

	whole, err := run(t, r, "read_file", map[string]any{"path": "spill.json"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(whole, `"truncated": true`) {
		t.Fatal("precondition: a plain read should not reach the tail")
	}
	if !strings.Contains(whole, "offset=") {
		t.Errorf("the truncation note should name the route on: %q", whole[len(whole)-120:])
	}

	out, err := run(t, r, "read_file", map[string]any{"path": "spill.json", "offset": float64(-len(tail))})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, tail) {
		t.Errorf("tail window = %q", out)
	}
	want := "[bytes " + itoa(len(body)-len(tail)) + "–" + itoa(len(body)) + " of " + itoa(len(body)) + "]"
	if !strings.HasSuffix(out, want) {
		t.Errorf("note = %q, want suffix %q", out, want)
	}
}

func TestReadFileByteWindowDefaultsAndCaps(t *testing.T) {
	r := newRegistry(t)
	body := strings.Repeat("a", readCap*2)
	writeProjectFile(t, r, "a.txt", body)

	// length absent: the inline limit, not readCap.
	out, err := run(t, r, "read_file", map[string]any{"path": "a.txt", "offset": float64(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "[bytes 0–"+itoa(OutputCap)+" of "+itoa(len(body))+"]") {
		t.Errorf("default length note = %q", out[len(out)-60:])
	}
	// length over readCap is held to readCap.
	out, err = run(t, r, "read_file", map[string]any{"path": "a.txt", "offset": float64(10), "length": float64(readCap * 3)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "[bytes 10–"+itoa(10+readCap)+" of "+itoa(len(body))+"]") {
		t.Errorf("capped length note = %q", out[len(out)-60:])
	}
	// length alone reads from the start.
	out, err = run(t, r, "read_file", map[string]any{"path": "a.txt", "length": float64(5)})
	if err != nil {
		t.Fatal(err)
	}
	if out != "aaaaa\n[bytes 0–5 of "+itoa(len(body))+"]" {
		t.Errorf("length-only = %q", out)
	}
}

func TestReadFileByteWindowClampsAndSaysSo(t *testing.T) {
	r := newRegistry(t)
	writeProjectFile(t, r, "s.txt", "0123456789")
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"offset": float64(-100)}, "0123456789\n[bytes 0–10 of 10 (offset -100 is before the start; read from 0)]"},
		{map[string]any{"offset": float64(50)}, "\n[bytes 10–10 of 10 (offset 50 is past the end)]"},
		{map[string]any{"offset": float64(8), "length": float64(100)}, "89\n[bytes 8–10 of 10]"},
		{map[string]any{"offset": float64(-3), "length": float64(2)}, "78\n[bytes 7–9 of 10]"},
	}
	for _, c := range cases {
		c.args["path"] = "s.txt"
		out, err := run(t, r, "read_file", c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if out != c.want {
			t.Errorf("%v = %q, want %q", c.args, out, c.want)
		}
	}
}

func TestReadFileByteWindowRefusals(t *testing.T) {
	r := newRegistry(t)
	writeProjectFile(t, r, "s.txt", "0123456789")
	for _, args := range []map[string]any{
		{"offset": float64(0), "start_line": float64(1)},
		{"length": float64(4), "end_line": float64(2)},
		{"length": float64(0)},
		{"length": float64(-1)},
		{"offset": 1.5},
		{"offset": "10"},
	} {
		args["path"] = "s.txt"
		if _, err := run(t, r, "read_file", args); err == nil {
			t.Errorf("%v: want a refusal", args)
		}
	}
}

// A window edge inside a UTF-8 sequence moves to a rune boundary, and
// the note reports the moved bounds — the ones a next call continues
// from.
func TestReadFileByteWindowKeepsRunesWhole(t *testing.T) {
	r := newRegistry(t)
	writeProjectFile(t, r, "j.txt", "あいうえお") // 3 bytes each
	out, err := run(t, r, "read_file", map[string]any{"path": "j.txt", "offset": float64(1), "length": float64(7)})
	if err != nil {
		t.Fatal(err)
	}
	// [1,8) → start advances to 3, end retreats to 6.
	if out != "い\n[bytes 3–6 of 15]" {
		t.Errorf("rune-safe window = %q", out)
	}
	if !utf8.ValidString(out) {
		t.Error("window split a rune")
	}
}

// A window shorter than the rune it starts in widens to that rune, so a
// continuation from its end advances (pre-release review).
func TestReadFileByteWindowNeverReturnsNothingMidText(t *testing.T) {
	r := newRegistry(t)
	writeProjectFile(t, r, "j.txt", "あいう")
	out, err := run(t, r, "read_file", map[string]any{"path": "j.txt", "offset": float64(0), "length": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if out != "あ\n[bytes 0–3 of 9]" {
		t.Errorf("out = %q", out)
	}
}

// start_line: 0 is "absent" to the line window, so it does not conflict.
func TestReadFileByteWindowAcceptsZeroLineArgs(t *testing.T) {
	r := newRegistry(t)
	writeProjectFile(t, r, "s.txt", "0123456789")
	out, err := run(t, r, "read_file", map[string]any{"path": "s.txt", "offset": float64(2), "length": float64(3), "start_line": float64(0)})
	if err != nil || out != "234\n[bytes 2–5 of 10]" {
		t.Errorf("out = %q err = %v", out, err)
	}
}

// offset=N names a byte of the file only on the whole-file path.
func TestReadFileWindowedCutNamesNoOffset(t *testing.T) {
	r := newRegistry(t)
	writeProjectFile(t, r, "w.txt", "head\n"+strings.Repeat("x", readCap+100)+"\n")
	out, err := run(t, r, "read_file", map[string]any{"path": "w.txt", "start_line": float64(1), "end_line": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "offset=") {
		t.Errorf("a line window named a file offset: %q", out[len(out)-200:])
	}
}
