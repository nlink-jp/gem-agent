package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Scan is the read path for diagnostic records Load discards: /learn
// reads the operator's own gate decisions through it (ADR-0045 §2).
func TestScanSeesDiagnosticRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"ts":"2026-08-26T10:00:00Z","kind":"session","data":{"schema":2}}
{"ts":"2026-08-26T10:00:01Z","kind":"message","data":{"role":"user","content":"hi"}}
{"ts":"2026-08-26T10:00:02Z","kind":"gate_decision","data":{"name":"shell_exec","decision":"approved"}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var gateTime time.Time
	err := Scan(path, func(kind string, ts time.Time, data json.RawMessage) error {
		kinds = append(kinds, kind)
		if kind == "gate_decision" {
			gateTime = ts
			var rec struct{ Decision string }
			if err := json.Unmarshal(data, &rec); err != nil {
				t.Errorf("payload did not decode: %v", err)
			}
			if rec.Decision != "approved" {
				t.Errorf("decision = %q", rec.Decision)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 3 || kinds[2] != "gate_decision" {
		t.Errorf("kinds = %v — Scan must not filter records", kinds)
	}
	if gateTime.IsZero() {
		t.Error("the record's timestamp did not reach the callback")
	}
}

// A torn tail costs its own line, matching Load's tolerance.
func TestScanSkipsUnreadableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := "{not json\n" +
		`{"ts":"2026-08-26T10:00:02Z","kind":"notice","data":{"message":"x"}}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen int
	if err := Scan(path, func(string, time.Time, json.RawMessage) error {
		seen++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Errorf("visited %d records, want the one readable line", seen)
	}
}

// The callback stops the walk by returning an error, which Scan returns.
func TestScanPropagatesCallbackError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path,
		[]byte(`{"ts":"2026-08-26T10:00:00Z","kind":"session","data":{}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("stop")
	if err := Scan(path, func(string, time.Time, json.RawMessage) error {
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the callback's error", err)
	}
}
