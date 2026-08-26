package riskbook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/session"
)

// writeTranscript builds a synthetic transcript in the flat legacy
// layout, which session.List reads in place (ADR-0022 §3).
func writeTranscript(t *testing.T, dir, projectDir, id string, records ...map[string]any) {
	t.Helper()
	var b strings.Builder
	add := func(kind string, data any) {
		rec, err := json.Marshal(map[string]any{
			"ts": "2026-08-26T10:00:00Z", "kind": kind, "data": data,
		})
		if err != nil {
			t.Fatal(err)
		}
		b.Write(rec)
		b.WriteByte('\n')
	}
	add(session.KindHeader, map[string]any{
		"schema": session.SchemaVersion, "version": "test",
		"model": "m", "project": projectDir,
	})
	add(session.KindMessage, map[string]any{"role": "user", "content": "go"})
	for _, r := range records {
		kind, _ := r["kind"].(string)
		add(kind, r["data"])
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gate(name, decision, key, detail, source string) map[string]any {
	return map[string]any{"kind": "gate_decision", "data": map[string]any{
		"name": name, "decision": decision, "key": key,
		"detail": detail, "source": source,
	}}
}

func auto(name, key string, approved bool) map[string]any {
	return map[string]any{"kind": "auto_decision", "data": map[string]any{
		"name": name, "approved": approved, "key": key, "tier": "review",
	}}
}

func id(n int) string {
	return []string{"20260820-100000", "20260821-100000", "20260822-100000"}[n]
}

func scan(t *testing.T, dir, proj string) Report {
	t.Helper()
	rep, err := Scan(dir, proj)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// ADR-0048 §1, carried into the rulebook's learning tool: typed answers
// count individually, allowlist answers are one vote per session.
func TestCountingTypedVersusAllowlist(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("shell_exec", "approved", "go test", "go test ./...", "operator"),
		gate("shell_exec", "approved", "go test", "go test -v ./...", "operator"),
		gate("shell_exec", "approved", "go test", "go test -race ./...", "allowlist"),
		gate("shell_exec", "approved", "go test", "go test ./x", "allowlist"),
		gate("shell_exec", "denied", "make deploy", "make deploy", "operator"),
		auto("shell_exec", "go test", false))

	rep := scan(t, dir, proj)
	st := rep.Keys["go test"]
	if st.TypedApprovals != 2 {
		t.Errorf("TypedApprovals = %d, want 2 (allowlist answers are not typed decisions)", st.TypedApprovals)
	}
	if st.ApproveSessions != 1 {
		t.Errorf("ApproveSessions = %d, want 1", st.ApproveSessions)
	}
	if st.ModelEscalated != 1 {
		t.Errorf("ModelEscalated = %d, want 1", st.ModelEscalated)
	}
	if d := rep.Keys["make deploy"]; d.TypedDenials != 1 || d.DenySessions != 1 {
		t.Errorf("denial stats = %+v", d)
	}
	if !rep.HasDecisions() {
		t.Error("HasDecisions = false with five gate decisions on record")
	}
}

// Unsourced records (pre-ADR-0048 builds) read as allowlist — the
// conservative direction.
func TestUnsourcedRecordsAreNotTyped(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("shell_exec", "approved", "go test", "go test ./...", ""))
	rep := scan(t, dir, proj)
	if got := rep.Keys["go test"].TypedApprovals; got != 0 {
		t.Errorf("TypedApprovals = %d, want 0 for unsourced records", got)
	}
}

// Another project's transcripts are not this project's evidence.
func TestScanIsScopedToTheProject(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "/work/other", id(0),
		gate("shell_exec", "approved", "go test", "go test ./...", "operator"))
	rep := scan(t, dir, "/work/proj")
	if len(rep.Keys) != 0 || rep.HasDecisions() {
		t.Errorf("evidence leaked across projects: %+v", rep)
	}
}

// Unkeyable decisions (empty key) are skipped: no summary should
// generalize from what cannot be named.
func TestEmptyKeysAreSkipped(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("shell_exec", "approved", "", "cat x | sh", "operator"))
	rep := scan(t, dir, proj)
	if len(rep.Keys) != 0 {
		t.Errorf("keys = %v, want none", rep.Keys)
	}
}

// A corrupt trailing line costs its own record, not the file.
func TestCorruptLinesAreSkipped(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("shell_exec", "approved", "go test", "go test ./...", "operator"))
	path := filepath.Join(dir, id(0)+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("{not json\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := scan(t, dir, proj)
	if rep.Keys["go test"] == nil || rep.Keys["go test"].TypedApprovals != 1 {
		t.Errorf("a corrupt line cost the whole file: %+v", rep.Keys)
	}
}

// The enumeration is what the summarizer reads: verdicts beside
// answers, with the examples the operator chose to include.
func TestRenderEnumeration(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("mcp__asn__lookup_ip", "approved", "mcp__asn__lookup_ip", "lookup_ip 203.0.113.7", "operator"),
		auto("mcp__asn__lookup_ip", "mcp__asn__lookup_ip", false))
	out := RenderEnumeration(scan(t, dir, proj))
	for _, want := range []string{
		"pattern: mcp__asn__lookup_ip",
		"approved 0, escalated 1",
		"typed approvals 1",
		"example: lookup_ip 203.0.113.7",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("enumeration missing %q:\n%s", want, out)
		}
	}
}

// ---- storage ----

func TestLoadComposeRoundTrip(t *testing.T) {
	t.Setenv("GEMAGENT_STATE_DIR", t.TempDir())
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.toml")
	proj := "/work/proj"

	// No layers yet: not in force, empty compose.
	b, err := Load(cfgPath, proj)
	if err != nil {
		t.Fatal(err)
	}
	if b.InForce() || b.Compose() != "" {
		t.Errorf("empty book: %+v", b)
	}

	// Base is the operator's hand-written file.
	if err := os.WriteFile(BasePath(cfgPath), []byte("network installs always need eyes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(proj, "writes under /data are routine here"); err != nil {
		t.Fatal(err)
	}
	b, err = Load(cfgPath, proj)
	if err != nil {
		t.Fatal(err)
	}
	out := b.Compose()
	if !strings.Contains(out, "base rules (hand-written by the operator)") ||
		!strings.Contains(out, "network installs always need eyes") {
		t.Errorf("compose missing the base layer:\n%s", out)
	}
	if !strings.Contains(out, "project rules (this project)") ||
		!strings.Contains(out, "/data are routine") {
		t.Errorf("compose missing the project layer:\n%s", out)
	}
	// Base must precede project: the addendum calls project the more
	// specific statement, and readers meet the general rule first.
	if strings.Index(out, "base rules") > strings.Index(out, "project rules") {
		t.Error("layers out of order")
	}

	// The project layer is scoped: another project sees nothing.
	other, err := Load(cfgPath, "/work/other")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(other.Compose(), "/data are routine") {
		t.Error("a project layer leaked into another project")
	}

	if err := DeleteProject(proj); err != nil {
		t.Fatal(err)
	}
	b, err = Load(cfgPath, proj)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.Compose(), "/data are routine") {
		t.Error("deleted project layer still loads")
	}
	// Deleting again is success: removal only tightens.
	if err := DeleteProject(proj); err != nil {
		t.Errorf("second delete errored: %v", err)
	}
}

// A layer over budget is clipped with the cut disclosed — a partial
// rulebook must not masquerade as the whole one.
func TestComposeClipsWithDisclosure(t *testing.T) {
	long := strings.Repeat("あ", layerBudget+300)
	b := Book{Base: long}
	out := b.Compose()
	if strings.Count(out, "あ") != layerBudget {
		t.Errorf("clip kept %d runes, want %d", strings.Count(out, "あ"), layerBudget)
	}
	if !strings.Contains(out, "300 more runes not shown") {
		t.Errorf("clip not disclosed:\n%s", out[len(out)-120:])
	}
}

// SaveProject replaces atomically and survives reload.
func TestSaveProjectAtomicReplace(t *testing.T) {
	t.Setenv("GEMAGENT_STATE_DIR", t.TempDir())
	proj := "/work/proj"
	if err := SaveProject(proj, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(proj, "v2"); err != nil {
		t.Fatal(err)
	}
	path, err := ProjectPath(proj)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v2" {
		t.Errorf("content = %q, want the replacement", data)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file left behind")
	}
}
