package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// No pins yet and nobody at a prompt: nothing is recorded (recording
// trust nobody confirmed is what ADR-0023 §5 refuses), the files load
// as before and the note says so. Interactive, the note names the
// pinned files.
func TestCheckPinsWithoutPinsIsInteractiveOnly(t *testing.T) {
	cfg, pf, policyPath, proj := pinFixture(t)
	msgs := uitext.For(uitext.EN)
	var out bytes.Buffer
	ex, notes := checkPins(cfg, pf, policyPath, proj, true, false, nil, &out, msgs)
	if ex != nil || len(notes) != 1 || !strings.Contains(notes[0], "recorded yet") {
		t.Fatalf("non-interactive first use: excluded=%v notes=%v", ex, notes)
	}
	if pf.HasPins(proj) {
		t.Fatal("non-interactive run recorded pins")
	}
	_, notes = checkPins(cfg, pf, policyPath, proj, true, true, strings.NewReader(""), &out, msgs)
	if len(notes) != 1 || !strings.Contains(notes[0], "AGENTS.md") || !strings.Contains(notes[0], ".mcp.json") {
		t.Fatalf("interactive first use should name the pinned files: %v", notes)
	}
	if !pf.HasPins(proj) {
		t.Fatal("interactive run did not record pins")
	}
}

// A project with no agent-facing files at all is pinned as "nothing",
// once: the marker survives the round trip and a later start compares
// against the empty set instead of re-pinning whatever appeared.
func TestCheckPinsEmptySetIsRecorded(t *testing.T) {
	cfg, pf, policyPath, proj := pinFixture(t)
	for _, n := range []string{"AGENTS.md", ".mcp.json"} {
		if err := os.Remove(filepath.Join(proj, n)); err != nil {
			t.Fatal(err)
		}
	}
	msgs := uitext.For(uitext.EN)
	var out bytes.Buffer
	if _, notes := checkPins(cfg, pf, policyPath, proj, true, true, strings.NewReader(""), &out, msgs); len(notes) != 1 || !strings.Contains(notes[0], "0 file(s) recorded") {
		t.Fatalf("empty project first use: %v", notes)
	}
	disk, err := config.LoadPolicyFile(policyPath)
	if err != nil || !disk.HasPins(proj) {
		t.Fatalf("empty pin set not recorded on disk: %v %v", err, disk.Projects[proj])
	}
	// AGENTS.md appears afterwards: it is new content, not trusted.
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("planted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex, notes := checkPins(cfg, disk, policyPath, proj, true, false, nil, &out, msgs)
	if !ex["AGENTS.md"] || len(notes) != 1 || !strings.Contains(notes[0], "AGENTS.md added") {
		t.Errorf("planted file after an empty pin set: excluded=%v notes=%v", ex, notes)
	}
}

// A pinned file that disappeared is nothing to load, so it is not
// excluded and not asked about — but its pin stays: content that comes
// back is "changed", never "new and unpinned".
func TestCheckPinsKeepsThePinOfARemovedFile(t *testing.T) {
	cfg, pf, policyPath, proj := pinFixture(t)
	msgs := uitext.For(uitext.EN)
	var out bytes.Buffer
	checkPins(cfg, pf, policyPath, proj, true, true, strings.NewReader(""), &out, msgs)
	if err := os.Remove(filepath.Join(proj, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	ex, notes := checkPins(cfg, pf, policyPath, proj, true, true, strings.NewReader(""), &out, msgs)
	if ex != nil || len(notes) != 1 || !strings.Contains(notes[0], ".mcp.json was removed") {
		t.Fatalf("removed file: excluded=%v notes=%v", ex, notes)
	}
	if _, ok := pf.PinsFor(proj)[".mcp.json"]; !ok {
		t.Fatal("the pin of the removed file was dropped")
	}
	if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ex, _ = checkPins(cfg, pf, policyPath, proj, true, false, nil, &out, msgs)
	if !ex[".mcp.json"] {
		t.Error("content that came back under the old name passed unasked")
	}
}

// An operator-approved write re-pins the one file the operator saw
// written — never a file that changed on its own, never one this
// session excluded, never anything after a command (which shows its
// text, not its effect).
func TestRepinIsScopedToTheApprovedWrite(t *testing.T) {
	cfg, pf, policyPath, proj := pinFixture(t)
	msgs := uitext.For(uitext.EN)
	var out bytes.Buffer
	checkPins(cfg, pf, policyPath, proj, true, true, strings.NewReader(""), &out, msgs)
	before := pf.PinsFor(proj)
	// Both files change outside gem-agent; the operator approves a
	// write to AGENTS.md only.
	for n, body := range map[string]string{"AGENTS.md": "v2\n", ".mcp.json": "{\"x\":1}\n"} {
		if err := os.WriteFile(filepath.Join(proj, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	name := pinNameForWrite(proj, llm.ToolCall{Name: "write_file", Args: map[string]any{"path": "AGENTS.md"}})
	if name != "AGENTS.md" {
		t.Fatalf("pinNameForWrite = %q", name)
	}
	if err := repinName(policyPath, proj, name, pf, nil); err != nil {
		t.Fatal(err)
	}
	after := pf.PinsFor(proj)
	if after["AGENTS.md"] == before["AGENTS.md"] {
		t.Error("the approved write was not re-pinned")
	}
	if after[".mcp.json"] != before[".mcp.json"] {
		t.Error("a file the operator did not see written was re-pinned")
	}
	// An excluded name is not re-pinned even when written: the operator
	// never saw the content the write replaced.
	if err := repinName(policyPath, proj, ".mcp.json", pf, map[string]bool{".mcp.json": true}); err != nil {
		t.Fatal(err)
	}
	if pf.PinsFor(proj)[".mcp.json"] != before[".mcp.json"] {
		t.Error("an excluded file was re-pinned")
	}
	// After a command: nothing moves, the difference is named.
	note := pinChangesNote(cfg, pf, proj, true, msgs)
	if !strings.Contains(note, ".mcp.json changed") || strings.Contains(note, "AGENTS.md") {
		t.Errorf("pending note = %q", note)
	}
	if pf.PinsFor(proj)[".mcp.json"] != before[".mcp.json"] {
		t.Error("a command re-pinned")
	}
}

// pinNameForWrite maps tool calls to pins: only file writes, only
// pinned names, a skill's inner file to the skill's pin, nothing
// outside the project.
func TestPinNameForWrite(t *testing.T) {
	proj := t.TempDir()
	cases := []struct {
		tool, path, want string
	}{
		{"write_file", "AGENTS.md", "AGENTS.md"},
		{"edit_file", filepath.Join(proj, ".mcp.json"), ".mcp.json"},
		{"write_file", ".claude/skills/deploy/SKILL.md", ".claude/skills/deploy"},
		{"write_file", ".claude/skills/deploy/references/x.md", ".claude/skills/deploy"},
		{"write_file", "README.md", ""},
		{"write_file", "sub/AGENTS.md", ""},
		{"write_file", "../AGENTS.md", ""},
		{"shell_exec", "AGENTS.md", ""},
	}
	for _, c := range cases {
		got := pinNameForWrite(proj, llm.ToolCall{Name: c.tool, Args: map[string]any{"path": c.path}})
		if got != c.want {
			t.Errorf("%s %s: %q, want %q", c.tool, c.path, got, c.want)
		}
	}
}

// The skill grant is keyed by directory entry, as the pin is — not by
// the name the skill's frontmatter declares for itself.
func TestGrantKeysSkillsByDirectoryEntry(t *testing.T) {
	g := projectGrant{trusted: true, excluded: map[string]bool{".claude/skills/dir-name": true}}
	if g.skill("dir-name") || !g.skill("frontmatter-name") {
		t.Error("skill grant keyed wrong")
	}
	if !g.config() {
		t.Error("config grant refused an unchanged .gem-agent.toml")
	}
	if (projectGrant{trusted: true, excluded: map[string]bool{".gem-agent.toml": true}}).config() {
		t.Error("config grant accepted a changed .gem-agent.toml")
	}
}

// The trust command: untrusted with --accept is an error, not a silent
// exit 0; without pins it says the files load as before; with pins it
// lists them and their state.
func TestTrustReport(t *testing.T) {
	_, pf, policyPath, proj := pinFixture(t)
	cfgPath := filepath.Join(filepath.Dir(policyPath), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[gcp]\nproject = \"p\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untrusted := t.TempDir()
	var out bytes.Buffer
	if err := trustReport(untrusted, cfgPath, true, &out); err == nil {
		t.Error("--accept on an untrusted project exited 0")
	}
	out.Reset()
	if err := trustReport(proj, cfgPath, false, &out); err != nil || !strings.Contains(out.String(), "none recorded yet") {
		t.Errorf("no pins: err=%v out=%q", err, out.String())
	}
	out.Reset()
	if err := trustReport(proj, cfgPath, true, &out); err != nil || !strings.Contains(out.String(), "2 file(s) recorded as trusted: .mcp.json, AGENTS.md") {
		t.Errorf("--accept: err=%v out=%q", err, out.String())
	}
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := trustReport(proj, cfgPath, false, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "trusted files: 2 recorded (") || !strings.Contains(s, "AGENTS.md") || !strings.Contains(s, "changed") || !strings.Contains(s, "not loaded until re-trusted") {
		t.Errorf("report = %q", s)
	}
	_ = pf
}
