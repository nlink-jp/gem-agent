package config

import (
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
