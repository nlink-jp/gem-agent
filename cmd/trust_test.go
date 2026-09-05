package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

func TestBroadRoot(t *testing.T) {
	home := "/Users/op"
	// broadRoot returns stable keys; uitext localizes them (ADR-0029).
	broad := map[string]string{
		"/":         "root",
		"/Users/op": "home",
		"/Users":    "home-ancestor",
	}
	for dir, wantSub := range broad {
		if got := broadRoot(dir, home); !strings.Contains(got, wantSub) {
			t.Errorf("broadRoot(%q) = %q, want %q", dir, got, wantSub)
		}
	}
	for _, dir := range []string{"/Users/op/works/proj", "/private/tmp/x", "/Users/opera"} {
		if got := broadRoot(dir, home); got != "" {
			t.Errorf("broadRoot(%q) = %q, want ordinary", dir, got)
		}
	}
}

func TestConfirmBroadRootNonInteractiveRefuses(t *testing.T) {
	msgs := uitext.For(uitext.EN)
	err := confirmBroadRoot("home", "/Users/op", false, strings.NewReader(""), &strings.Builder{}, msgs)
	if err == nil || !strings.Contains(err.Error(), "interactively") {
		t.Errorf("non-interactive broad root = %v, want a refusal naming the interactive path", err)
	}
	if err := confirmBroadRoot("root", "/", true, strings.NewReader("y\n"), &strings.Builder{}, msgs); err != nil {
		t.Errorf("confirmed start refused: %v", err)
	}
	if err := confirmBroadRoot("root", "/", true, strings.NewReader("\n"), &strings.Builder{}, msgs); err == nil {
		t.Error("bare Enter must default to NO")
	}
}

func TestProbeProject(t *testing.T) {
	dir := t.TempDir()
	if !probeProject(dir).empty() {
		t.Error("empty project reported an offering")
	}
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("do things"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"a":{"command":"/bin/true"},"b":{"command":"/bin/true"}}}`), 0o644)
	// Only entries that look like skills count (review round 2): a
	// real skill dir, plus decoys — a stray file and a dir without
	// SKILL.md — that must NOT inflate the trust prompt's number.
	_ = os.MkdirAll(filepath.Join(dir, ".claude", "skills", "s1"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".claude", "skills", "s1", "SKILL.md"), []byte("# s1"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".claude", "skills", ".DS_Store"), []byte{0}, 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".claude", "skills", "not-a-skill"), 0o755)
	o := probeProject(dir)
	if len(o.Instructions) != 1 || o.Instructions[0] != "AGENTS.md" {
		t.Errorf("instructions = %v", o.Instructions)
	}
	if !o.HasMCP || o.MCPServers != 2 {
		t.Errorf("mcp = %v/%d", o.HasMCP, o.MCPServers)
	}
	if o.Skills != 1 {
		t.Errorf("skills = %d", o.Skills)
	}
	if len(o.describe(uitext.For(uitext.EN))) != 3 {
		t.Errorf("describe = %v", o.describe(uitext.For(uitext.EN)))
	}
}

func trustFixture(t *testing.T) (*config.Config, *config.PolicyFile, string, string) {
	t.Helper()
	cfg := &config.Config{}
	pf := &config.PolicyFile{Tools: map[string]string{}, Projects: map[string]config.ProjectPolicy{}}
	policyPath := filepath.Join(t.TempDir(), "policy.toml")
	project := t.TempDir()
	_ = os.WriteFile(filepath.Join(project, "CLAUDE.md"), []byte("clone instructions"), 0o644)
	return cfg, pf, policyPath, project
}

// ADR-0023 §3: the first interactive answer persists; later runs do not
// ask again.
func TestResolveProjectTrustPersists(t *testing.T) {
	cfg, pf, policyPath, project := trustFixture(t)

	var out strings.Builder
	trusted, note := resolveProjectTrust(cfg, pf, policyPath, project, true, strings.NewReader("y\n"), &out, uitext.For(uitext.EN))
	if !trusted || note != "" {
		t.Fatalf("granted run: trusted=%v note=%q", trusted, note)
	}
	if !strings.Contains(out.String(), "CLAUDE.md") || !strings.Contains(out.String(), "trust this project?") {
		t.Errorf("prompt did not list the offering: %q", out.String())
	}

	// Reload from disk: the decision must have been persisted.
	pf2, err := config.LoadPolicyFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if pf2.TrustFor(project) != config.TrustGranted {
		t.Fatalf("persisted trust = %q", pf2.TrustFor(project))
	}
	trusted, _ = resolveProjectTrust(cfg, pf2, policyPath, project, true,
		strings.NewReader(""), &strings.Builder{}, uitext.For(uitext.EN)) // no input available: must not prompt
	if !trusted {
		t.Error("second run re-asked or forgot the decision")
	}
}

// Declining starts bare and says so; bare Enter means decline.
func TestResolveProjectTrustDecline(t *testing.T) {
	cfg, pf, policyPath, project := trustFixture(t)
	trusted, note := resolveProjectTrust(cfg, pf, policyPath, project, true, strings.NewReader("\n"), &strings.Builder{}, uitext.For(uitext.EN))
	if trusted {
		t.Fatal("bare Enter granted trust — the default must be NO")
	}
	if !strings.Contains(note, "declined") || !strings.Contains(note, policyPath) {
		t.Errorf("note = %q — must say declined and name the file to edit", note)
	}
	pf2, _ := config.LoadPolicyFile(policyPath)
	if pf2.TrustFor(project) != config.TrustDeclined {
		t.Errorf("persisted = %q", pf2.TrustFor(project))
	}
}

// ADR-0023 §5: non-interactive + undecided = bare run, nothing recorded.
func TestResolveProjectTrustNonInteractiveBare(t *testing.T) {
	cfg, pf, policyPath, project := trustFixture(t)
	trusted, note := resolveProjectTrust(cfg, pf, policyPath, project, false, strings.NewReader(""), &strings.Builder{}, uitext.For(uitext.EN))
	if trusted {
		t.Fatal("undecided non-interactive run loaded the project's files")
	}
	if !strings.Contains(note, "undecided") {
		t.Errorf("note = %q", note)
	}
	if _, err := os.Stat(policyPath); !os.IsNotExist(err) {
		t.Error("a non-answer was persisted")
	}
}

// ADR-0023 §4: hand-declared trusted_projects skips the question; a
// project offering nothing asks nothing.
func TestResolveProjectTrustShortcuts(t *testing.T) {
	cfg, pf, policyPath, project := trustFixture(t)
	cfg.Approval.TrustedProjects = []string{project}
	trusted, _ := resolveProjectTrust(cfg, pf, policyPath, project, true, strings.NewReader(""), &strings.Builder{}, uitext.For(uitext.EN))
	if !trusted {
		t.Error("trusted_projects entry did not auto-trust")
	}

	cfg2 := &config.Config{}
	emptyProject := t.TempDir()
	var out strings.Builder
	trusted, note := resolveProjectTrust(cfg2, pf, policyPath, emptyProject, true, strings.NewReader(""), &out, uitext.For(uitext.EN))
	if !trusted || note != "" || out.Len() != 0 {
		t.Errorf("empty project: trusted=%v note=%q prompt=%q — nothing to ask", trusted, note, out.String())
	}
}

// Untrusted projects contribute no instruction files; ancestors stay.
func TestLoadInstructionsExcludesUntrustedProject(t *testing.T) {
	parent := t.TempDir()
	_ = os.WriteFile(filepath.Join(parent, "AGENTS.md"), []byte("workspace rules"), 0o644)
	project := filepath.Join(parent, "clone")
	_ = os.MkdirAll(project, 0o755)
	_ = os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("CLONE INSTRUCTIONS"), 0o644)

	// home = parent so the ancestor walk includes it.
	t.Setenv("HOME", parent)
	section, _, _ := loadInstructions(project, projectGrant{})
	if strings.Contains(section, "CLONE INSTRUCTIONS") {
		t.Error("untrusted project's own instructions were injected")
	}
	if !strings.Contains(section, "workspace rules") {
		t.Error("ancestor instructions lost — the gate must only cover the project's own files")
	}
	section, _, _ = loadInstructions(project, projectGrant{trusted: true})
	if !strings.Contains(section, "CLONE INSTRUCTIONS") {
		t.Error("trusted project's instructions missing")
	}
}
