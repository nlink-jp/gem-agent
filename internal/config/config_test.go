package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GEMAGENT_PROJECT", "GEMAGENT_LOCATION", "GEMAGENT_MODEL",
		"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestLoadFileWithDefaults(t *testing.T) {
	clearEnv(t)
	path := writeConfig(t, `
[gcp]
project = "example-project"

[model]
name = "example-model"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GCP.Project != "example-project" {
		t.Errorf("project = %q", cfg.GCP.Project)
	}
	if cfg.GCP.Location != "global" {
		t.Errorf("default location = %q, want global (Gemini 3 family is global-endpoint-only)", cfg.GCP.Location)
	}
	if !cfg.Sandbox.Enabled {
		t.Error("sandbox should default to enabled")
	}
	if cfg.Agent.MaxTurns != 50 || cfg.Agent.ShellTimeoutSec != 120 {
		t.Errorf("agent defaults = %+v", cfg.Agent)
	}
	if !cfg.MCP.Enabled || cfg.MCP.CallTimeoutSec != 60 {
		t.Errorf("mcp defaults = %+v", cfg.MCP)
	}
	if cfg.TUI.Theme != "auto" {
		t.Errorf("tui theme default = %q, want auto", cfg.TUI.Theme)
	}
}

func TestInvalidThemeRejected(t *testing.T) {
	clearEnv(t)
	path := writeConfig(t, `
[gcp]
project = "p"

[model]
name = "m"

[tui]
theme = "solarized"
`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "[tui].theme") {
		t.Fatalf("invalid theme should be rejected, got %v", err)
	}
}

func TestEnvPrecedence(t *testing.T) {
	clearEnv(t)
	path := writeConfig(t, `
[gcp]
project = "file-project"

[model]
name = "file-model"
`)
	// GOOGLE_CLOUD_* beats file.
	t.Setenv("GOOGLE_CLOUD_PROJECT", "gcloud-project")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GCP.Project != "gcloud-project" {
		t.Errorf("GOOGLE_CLOUD_PROJECT should beat file: got %q", cfg.GCP.Project)
	}

	// GEMAGENT_* beats GOOGLE_CLOUD_*.
	t.Setenv("GEMAGENT_PROJECT", "tool-project")
	t.Setenv("GEMAGENT_MODEL", "tool-model")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GCP.Project != "tool-project" {
		t.Errorf("GEMAGENT_PROJECT should beat GOOGLE_CLOUD_PROJECT: got %q", cfg.GCP.Project)
	}
	if cfg.Model.Name != "tool-model" {
		t.Errorf("GEMAGENT_MODEL should beat file: got %q", cfg.Model.Name)
	}
}

func TestMissingFileEnvOnly(t *testing.T) {
	clearEnv(t)
	t.Setenv("GEMAGENT_PROJECT", "env-project")
	t.Setenv("GEMAGENT_MODEL", "env-model")
	cfg, err := Load(filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GCP.Project != "env-project" || cfg.Model.Name != "env-model" {
		t.Errorf("env-only config = %+v", cfg)
	}
}

func TestFlagOverrideBeatsEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("GEMAGENT_PROJECT", "env-project")
	t.Setenv("GEMAGENT_MODEL", "env-model")
	cfg, err := LoadWithOverrides(filepath.Join(t.TempDir(), "nonexistent.toml"), Overrides{Model: "flag-model"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Name != "flag-model" {
		t.Errorf("flag should beat env: got %q", cfg.Model.Name)
	}
}

// ADR-0039 §5: --mcp on|off overrides [mcp].enabled at the top of the
// precedence, with flag provenance; anything else is a loud error.
func TestMCPFlagOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("GEMAGENT_PROJECT", "p")
	t.Setenv("GEMAGENT_MODEL", "m")
	path := filepath.Join(t.TempDir(), "nonexistent.toml")

	cfg, err := LoadWithOverrides(path, Overrides{MCP: "off"})
	if err != nil || cfg.MCP.Enabled {
		t.Errorf("--mcp off: enabled=%v err=%v", cfg != nil && cfg.MCP.Enabled, err)
	}
	if cfg.Source("mcp.enabled") != FromFlag {
		t.Errorf("provenance = %v, want flag", cfg.Source("mcp.enabled"))
	}
	cfg, err = LoadWithOverrides(path, Overrides{MCP: "on"})
	if err != nil || !cfg.MCP.Enabled {
		t.Errorf("--mcp on: err=%v", err)
	}
	if _, err = LoadWithOverrides(path, Overrides{MCP: "maybe"}); err == nil ||
		!strings.Contains(err.Error(), "--mcp") {
		t.Errorf("invalid --mcp value: %v", err)
	}
}

func TestUnknownKeyIsError(t *testing.T) {
	clearEnv(t)
	path := writeConfig(t, `
[gcp]
project = "p"
locaton = "typo"

[model]
name = "m"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("unknown key should be a strict-decode error, got: %v", err)
	}
}

func TestMissingRequiredFields(t *testing.T) {
	clearEnv(t)
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil {
		t.Fatal("missing project/model should be an error")
	}
	if !strings.Contains(err.Error(), "[gcp].project") || !strings.Contains(err.Error(), "[model].name") {
		t.Errorf("error should name both missing fields: %v", err)
	}
}

func TestCompactThresholdIsRangeChecked(t *testing.T) {
	clearEnv(t)
	for _, pct := range []int{0, 5, 100, 200, -1} {
		path := writeConfig(t, `
[gcp]
project = "p"
[model]
name = "m"
[agent]
compact_at_pct = `+itoa(pct)+`
`)
		if _, err := Load(path); err == nil {
			t.Errorf("compact_at_pct = %d accepted; it must be a usable share of the window", pct)
		}
	}
	// 100 would fire only after the request that already failed; 80 is
	// the default and must load.
	path := writeConfig(t, `
[gcp]
project = "p"
[model]
name = "m"
[agent]
compact_at_pct = 90
auto_compact = false
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.CompactAtPct != 90 || cfg.Agent.AutoCompact {
		t.Errorf("agent config = %+v", cfg.Agent)
	}
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func TestLoadProjectReadsApprovalPolicyOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProjectFileName), []byte(`
[approval.tools]
"write_file" = "always"
"mcp__tor-exit-lookup__*" = "never"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Approval.Tools["write_file"] != "always" ||
		cfg.Approval.Tools["mcp__tor-exit-lookup__*"] != "never" {
		t.Errorf("tools = %v", cfg.Approval.Tools)
	}
}

// A project file must not be able to reach settings that belong to the
// operator — the model, credentials, the sandbox switch.
func TestProjectFileRejectsAnythingButPolicy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProjectFileName), []byte(`
[sandbox]
enabled = false

[approval.tools]
"write_file" = "always"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProject(dir)
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("a project file disabled the sandbox: err = %v", err)
	}
}

func TestLoadProjectMissingFileIsNotAnError(t *testing.T) {
	cfg, err := LoadProject(t.TempDir())
	if err != nil || len(cfg.Approval.Tools) != 0 {
		t.Errorf("LoadProject on a project with no file: %+v %v", cfg, err)
	}
}

func TestTrustedProjectsMatchesExactPaths(t *testing.T) {
	clearEnv(t)
	path := writeConfig(t, `
[gcp]
project = "p"
[model]
name = "m"
[approval]
trusted_projects = ["/work/mine"]
[approval.tools]
"shell_exec" = "always"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TrustsProject("/work/mine") {
		t.Error("listed project not trusted")
	}
	// No prefix matching: /work/mine-evil must not inherit the trust of
	// /work/mine.
	for _, other := range []string{"/work/mine-evil", "/work/mine/sub", "/work", ""} {
		if cfg.TrustsProject(other) {
			t.Errorf("TrustsProject(%q) = true", other)
		}
	}
	if cfg.Approval.Tools["shell_exec"] != "always" {
		t.Errorf("approval tools = %v", cfg.Approval.Tools)
	}
}

// Four precedence layers with nothing on screen assumes the operator
// remembers them. /settings shows this instead (ADR-0009).
func TestConfigRecordsWhereEachValueCameFrom(t *testing.T) {
	clearEnv(t)
	path := writeConfig(t, `
[gcp]
project = "file-project"
[model]
name = "file-model"
[agent]
max_turns = 7
`)
	t.Setenv("GEMAGENT_PROJECT", "env-project")
	cfg, err := LoadWithOverrides(path, Overrides{Model: "flag-model"})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"model.name":              FromFlag,
		"gcp.project":             FromEnv + ":GEMAGENT_PROJECT",
		"agent.max_turns":         FromFile,
		"agent.shell_timeout_sec": FromDefault,
		"tui.theme":               FromDefault,
	} {
		if got := cfg.Source(key); got != want {
			t.Errorf("Source(%q) = %q, want %q", key, got, want)
		}
	}
}
