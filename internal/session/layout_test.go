package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// ADR-0022 §1: new sessions live under projects/<escaped>/ with the
// shared .project marker — a glob or cleanup in one project's directory
// cannot touch another project's transcripts.
func TestOpenCreatesPerProjectLayout(t *testing.T) {
	dir := t.TempDir()
	lg, err := Open(dir, "/proj/alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lg.Close() }()

	wantDir := filepath.Join(dir, "projects", statedir.EscapeProject("/proj/alpha"))
	if filepath.Dir(lg.Path()) != wantDir {
		t.Errorf("session at %s, want under %s", lg.Path(), wantDir)
	}
	marker, err := os.ReadFile(filepath.Join(wantDir, statedir.Marker))
	if err != nil || strings.TrimSpace(string(marker)) != "/proj/alpha" {
		t.Errorf("marker = %q err=%v", marker, err)
	}
}

// ADR-0022 §3: legacy flat files are read in place — listed, found, and
// resumed without ever being moved.
func TestLegacyFlatSessionStaysUsable(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Hand-place a legacy flat transcript, as pre-0.18 builds wrote it.
	legacy := filepath.Join(dir, "20260101-120000.jsonl")
	lines := []string{
		`{"ts":"2026-01-01T12:00:00Z","kind":"session","data":{"schema":1,"model":"m","project":"/proj"}}`,
		`{"ts":"2026-01-01T12:00:01Z","kind":"message","data":{"role":"user","content":"legacy question"}}`,
	}
	if err := os.WriteFile(legacy, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	metas, err := List(dir, "/proj")
	if err != nil || len(metas) != 1 || metas[0].ID != "20260101-120000" {
		t.Fatalf("List = %+v err=%v — the legacy session must appear", metas, err)
	}
	if _, err := Find(dir, "/proj", "20260101-120000"); err != nil {
		t.Errorf("Find on a legacy session: %v", err)
	}
	lg, err := Reopen(dir, "/proj", "20260101-120000")
	if err != nil {
		t.Fatalf("Reopen on a legacy session: %v", err)
	}
	if lg.Path() != legacy {
		t.Errorf("Reopen moved the file: %s", lg.Path())
	}
	_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "appended after upgrade"})
	_ = lg.Close()
	history, _, _, err := Load(legacy)
	if err != nil || len(history) != 2 {
		t.Errorf("legacy resume roundtrip: %d messages, err=%v", len(history), err)
	}
}

// New and legacy sessions of one project list together; another
// project's subdirectory sessions do not leak in.
func TestListMergesLayoutsAndSeparatesProjects(t *testing.T) {
	dir := t.TempDir()
	// New-layout session for /proj.
	lg, err := Open(dir, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	_ = lg.Log(KindHeader, Header{Schema: SchemaVersion, Model: "m", Project: "/proj"})
	_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "new layout"})
	_ = lg.Close()
	// New-layout session for another project.
	other, err := Open(dir, "/other")
	if err != nil {
		t.Fatal(err)
	}
	_ = other.Log(KindHeader, Header{Schema: SchemaVersion, Model: "m", Project: "/other"})
	_ = other.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "other project"})
	_ = other.Close()
	// Legacy flat session for /proj.
	legacy := filepath.Join(dir, "20260101-120000.jsonl")
	lines := []string{
		`{"kind":"session","data":{"schema":1,"model":"m","project":"/proj"}}`,
		`{"kind":"message","data":{"role":"user","content":"legacy"}}`,
	}
	if err := os.WriteFile(legacy, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	metas, err := List(dir, "/proj")
	if err != nil || len(metas) != 2 {
		t.Fatalf("List(/proj) = %d sessions (err=%v), want new + legacy", len(metas), err)
	}
	all, err := List(dir, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("List(all) = %d sessions (err=%v), want 3", len(all), err)
	}
}

// An escape collision (marker mismatch) refuses session creation loudly
// instead of mixing two projects' transcripts in one directory.
func TestOpenRefusesMarkerCollision(t *testing.T) {
	dir := t.TempDir()
	parent := t.TempDir()
	a := filepath.Join(parent, "x-y")
	b := filepath.Join(parent, "x", "y") // escapes to the same name
	if _, err := Open(dir, a); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, b); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Errorf("colliding project Open = %v, want a loud refusal", err)
	}
}

// ADR-0022 §4: GEMAGENT_STATE_DIR redirects the state root — the
// isolation that makes an E2E structurally unable to touch real state.
func TestStateDirEnvOverride(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv(statedir.EnvRoot, scratch)

	dir, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(scratch, "sessions") {
		t.Errorf("DefaultDir = %s, want under the override", dir)
	}
}
