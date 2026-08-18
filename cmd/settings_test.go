package cmd

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/tui"
)

func newStore(t *testing.T) *settingsStore {
	t.Helper()
	projectDir := t.TempDir()
	reg, err := tools.New(projectDir,
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/bash", "-c", command)
		}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Approval.Tools = map[string]string{}
	ag := agent.New(agent.Options{Registry: reg, MaxTurns: 1})
	s := &settingsStore{
		cfg: cfg, projectCfg: &config.ProjectConfig{},
		policyFile: &config.PolicyFile{Tools: map[string]string{}, Projects: map[string]config.ProjectPolicy{}},
		policyPath: filepath.Join(t.TempDir(), config.PolicyFileName),
		projectDir: projectDir, registry: reg, ag: ag,
	}
	if _, err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return s
}

func rowFor(d tui.SettingsData, label string) (tui.SettingRow, bool) {
	for _, r := range d.Rows {
		if r.Label == label {
			return r, true
		}
	}
	return tui.SettingRow{}, false
}

// The panel exists because four precedence layers were invisible.
func TestSettingsRowsCarryProvenance(t *testing.T) {
	s := newStore(t)
	s.cfg.Sources = map[string]string{"model.name": config.FromFlag}
	d := s.data()

	row, ok := rowFor(d, "model.name")
	if !ok || row.Source != config.FromFlag {
		t.Errorf("model.name row = %+v", row)
	}
	if len(row.Values) != 0 {
		t.Error("model.name must be read-only: changing it needs a new backend client")
	}
	if row.Detail == "" {
		t.Error("a read-only row must say why, or pressing Enter looks broken")
	}
	if sandbox, _ := rowFor(d, "sandbox.enabled"); len(sandbox.Values) != 0 {
		t.Error("the sandbox switch must not be a menu item")
	}
	if theme, _ := rowFor(d, "tui.theme"); len(theme.Values) == 0 {
		t.Error("tui.theme should be editable")
	}
}

// Editing a policy writes the machine-owned file and takes effect at
// once — the panel and the running agent must not disagree.
func TestSettingsPolicyEditPersistsAndApplies(t *testing.T) {
	s := newStore(t)
	d, line := s.Apply(tui.SettingChange{Tool: "write_file", Value: "never", Scope: tui.ScopeGlobal})
	if !strings.Contains(line, "never") || !strings.Contains(line, config.PolicyFileName) {
		t.Errorf("line = %q, want it to name the value and the file", line)
	}
	if row, _ := rowFor(d, "write_file"); row.Value != "never" || row.Source != config.PolicyFileName {
		t.Errorf("row = %+v", row)
	}

	back, err := config.LoadPolicyFile(s.policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if back.Tools["write_file"] != "never" {
		t.Errorf("policy file = %+v", back)
	}
	if s.current.For("write_file") != policy.NeverAsk {
		t.Error("the edit did not reach the resolved policy")
	}
}

// Project scope is expressed inside the machine-owned file, so nothing
// is written into the operator's repository (ADR-0009 §4).
func TestSettingsProjectScopeStaysOutOfTheRepository(t *testing.T) {
	s := newStore(t)
	if _, line := s.Apply(tui.SettingChange{Tool: "edit_file", Value: "never", Scope: tui.ScopeProject}); !strings.Contains(line, "in ") {
		t.Errorf("line = %q, want it to name the project scope", line)
	}
	back, err := config.LoadPolicyFile(s.policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if back.Projects[s.projectDir].Tools["edit_file"] != "never" {
		t.Errorf("project policy = %+v", back.Projects)
	}
	if len(back.Tools) != 0 {
		t.Errorf("a project-scoped edit leaked into the global table: %v", back.Tools)
	}
	// And nothing was written into the project itself.
	if _, err := config.LoadProject(s.projectDir); err != nil {
		t.Fatal(err)
	}
	if pc, _ := config.LoadProject(s.projectDir); len(pc.Approval.Tools) != 0 {
		t.Errorf("the settings panel wrote into the repository: %v", pc.Approval.Tools)
	}
}

// "default" removes the entry rather than recording a third state.
func TestSettingsDefaultRemovesTheEntry(t *testing.T) {
	s := newStore(t)
	s.Apply(tui.SettingChange{Tool: "write_file", Value: "never", Scope: tui.ScopeGlobal})
	d, line := s.Apply(tui.SettingChange{Tool: "write_file", Value: "default", Scope: tui.ScopeGlobal})
	if !strings.Contains(line, "default") {
		t.Errorf("line = %q", line)
	}
	back, _ := config.LoadPolicyFile(s.policyPath)
	if len(back.Tools) != 0 {
		t.Errorf("the entry survived: %v", back.Tools)
	}
	if row, _ := rowFor(d, "write_file"); row.Value != "default" {
		t.Errorf("row = %+v", row)
	}
}

// A hand-written entry that the UI overrode must be visible as such,
// not mysterious.
func TestSettingsShowsWhichFileDecided(t *testing.T) {
	s := newStore(t)
	s.cfg.Approval.Tools["shell_exec"] = "always"
	if _, err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if row, _ := rowFor(s.data(), "shell_exec"); row.Source != config.FromFile || row.Value != "always" {
		t.Errorf("row = %+v, want it attributed to config.toml", row)
	}
	s.Apply(tui.SettingChange{Tool: "shell_exec", Value: "never", Scope: tui.ScopeGlobal})
	row, _ := rowFor(s.data(), "shell_exec")
	if row.Value != "never" || row.Source != config.PolicyFileName {
		t.Errorf("row = %+v, want the UI edit to win and say so", row)
	}
}

func TestSettingsSessionTogglesTakeEffectImmediately(t *testing.T) {
	s := newStore(t)
	if s.ag.AutoApprove() {
		t.Fatal("precondition")
	}
	if _, line := s.Apply(tui.SettingChange{Label: "agent.auto_approve", Value: "true"}); !strings.Contains(line, "this session") {
		t.Errorf("line = %q, want it to say the change is not persisted", line)
	}
	if !s.ag.AutoApprove() {
		t.Error("the toggle did not reach the agent")
	}
	s.Apply(tui.SettingChange{Label: "agent.auto_compact", Value: "true"})
	if !s.ag.AutoCompact() {
		t.Error("auto-compact toggle did not reach the agent")
	}
}

func TestWriteSettingsTableGroupsBySection(t *testing.T) {
	s := newStore(t)
	var b strings.Builder
	writeSettingsTable(&b, s.data())
	out := b.String()
	for _, want := range []string{"[backend]", "[approval policy]", "model.name", "write_file", "editable in the TUI"} {
		if !strings.Contains(out, want) {
			t.Errorf("table is missing %q:\n%s", want, out)
		}
	}
}

// An untrusted project's "never" is dropped (ADR-0008). Crediting it as
// the deciding source would tell the operator the opposite of the truth.
func TestSettingsMarksAnIgnoredProjectEntry(t *testing.T) {
	s := newStore(t)
	s.projectCfg.Approval.Tools = map[string]string{"read_file": "never"}
	if _, err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}
	row, _ := rowFor(s.data(), "read_file")
	if row.Value != "default" {
		t.Fatalf("an untrusted project loosened a gate: %+v", row)
	}
	if !strings.Contains(row.Source, "ignored") {
		t.Errorf("source = %q, want it to say the entry was ignored", row.Source)
	}

	// Trusted, the same entry decides — and is credited.
	s.cfg.Approval.TrustedProjects = []string{s.projectDir}
	if _, err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}
	row, _ = rowFor(s.data(), "read_file")
	if row.Value != "never" || row.Source != config.ProjectFileName {
		t.Errorf("trusted row = %+v", row)
	}
}
