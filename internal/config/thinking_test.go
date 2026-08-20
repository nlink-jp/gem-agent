package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0025: [model].thinking accepts the four Gemini 3 levels or empty;
// an unknown value is a startup error, never a silent fallback.
func TestThinkingValidation(t *testing.T) {
	for _, ok := range []string{"", "minimal", "low", "medium", "high"} {
		if !ValidThinking(ok) {
			t.Errorf("ValidThinking(%q) = false", ok)
		}
	}
	if ValidThinking("ultra") {
		t.Error("unknown thinking level accepted")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\nthinking = \"turbo\"\n"), 0o644)
	if _, err := LoadWithOverrides(path, Overrides{}); err == nil || !strings.Contains(err.Error(), "thinking") {
		t.Errorf("invalid thinking loaded without error: %v", err)
	}
	os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\nthinking = \"high\"\n"), 0o644)
	cfg, err := LoadWithOverrides(path, Overrides{})
	if err != nil || cfg.Model.Thinking != "high" {
		t.Errorf("valid thinking: cfg=%v err=%v", cfg.Model.Thinking, err)
	}
	if cfg.Source("model.thinking") != FromFile {
		t.Errorf("provenance = %v, want file", cfg.Source("model.thinking"))
	}
}
