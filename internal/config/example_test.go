package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExampleConfigLoads pins the shipped template against the loader.
// A template that drifted from the schema would fail only in a user's
// hands — and the strict decode makes any stale key a hard error, so
// this must be checked mechanically, not by reading.
func TestExampleConfigLoads(t *testing.T) {
	clearEnv(t)
	path := filepath.Join("..", "..", "config.example.toml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config.example.toml is missing: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("shipped template does not load: %v", err)
	}

	// The template must be complete enough to start from: every field a
	// user is expected to fill has a value, and the documented defaults
	// match the built-in ones.
	if cfg.GCP.Project == "" || cfg.Model.Name == "" {
		t.Errorf("template should carry placeholder project/model: %+v", cfg)
	}
	if cfg.GCP.Location != "global" {
		t.Errorf("template location = %q, want global (Gemini 3 is global-only)", cfg.GCP.Location)
	}
	def := defaults()
	if cfg.Sandbox.Enabled != def.Sandbox.Enabled ||
		cfg.Agent.MaxTurns != def.Agent.MaxTurns ||
		cfg.Agent.ShellTimeoutSec != def.Agent.ShellTimeoutSec ||
		cfg.Agent.AutoApprove != def.Agent.AutoApprove ||
		cfg.Agent.AutoCompact != def.Agent.AutoCompact ||
		cfg.Agent.CompactAtPct != def.Agent.CompactAtPct ||
		cfg.MCP.Enabled != def.MCP.Enabled ||
		cfg.MCP.CallTimeoutSec != def.MCP.CallTimeoutSec ||
		cfg.TUI.Theme != def.TUI.Theme {
		t.Errorf("template values drifted from the built-in defaults:\ntemplate %+v\ndefaults %+v", cfg, def)
	}
}

// TestExampleConfigHasNoRealProject guards the org rule that
// environment-specific values never land in a repository.
func TestExampleConfigHasNoRealProject(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := struct{ text string }{string(data)}
	for _, forbidden := range []string{"gemini-ai-", "news-collector-", "@", "AIza"} {
		if containsToken(cfg.text, forbidden) {
			t.Errorf("template contains an environment-specific value or credential-looking token: %q", forbidden)
		}
	}
}

func containsToken(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}

// TestExampleProjectConfigLoads pins the shipped project template the
// same way: strict decode makes a drifted template a hard error in a
// user's hands, so it is checked mechanically.
func TestExampleProjectConfigLoads(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join("..", "..", "gem-agent.example.project.toml"))
	if err != nil {
		t.Fatalf("project template is missing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ProjectFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("shipped project template does not load: %v", err)
	}
	if cfg.Approval.Tools["write_file"] != "always" {
		t.Errorf("template policy = %v", cfg.Approval.Tools)
	}
}
