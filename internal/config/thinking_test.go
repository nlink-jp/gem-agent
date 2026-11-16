package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0025, ADR-0118: [model].thinking accepts the five levels or empty;
// an unknown value is a startup error, never a silent fallback.
func TestThinkingValidation(t *testing.T) {
	for _, ok := range []string{"", "minimal", "low", "medium", "high", "xhigh"} {
		if !ValidThinking(ok) {
			t.Errorf("ValidThinking(%q) = false", ok)
		}
	}
	if ValidThinking("ultra") {
		t.Error("unknown thinking level accepted")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\nthinking = \"turbo\"\n"), 0o644)
	if _, err := LoadWithOverrides(path, Overrides{}); err == nil || !strings.Contains(err.Error(), "thinking") {
		t.Errorf("invalid thinking loaded without error: %v", err)
	}
	_ = os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\nthinking = \"high\"\n"), 0o644)
	cfg, err := LoadWithOverrides(path, Overrides{})
	if err != nil || cfg.Model.Thinking != "high" {
		t.Errorf("valid thinking: cfg=%v err=%v", cfg.Model.Thinking, err)
	}
	if cfg.Source("model.thinking") != FromFile {
		t.Errorf("provenance = %v, want file", cfg.Source("model.thinking"))
	}
}

// --thinking flag (operator request): overrides the file, and the
// literal "default" clears a configured level — the empty string
// already means "flag not given".
func TestThinkingFlagOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\nthinking = \"low\"\n"), 0o644)

	cfg, err := LoadWithOverrides(path, Overrides{Thinking: "high"})
	if err != nil || cfg.Model.Thinking != "high" {
		t.Fatalf("flag override: thinking=%q err=%v", cfg.Model.Thinking, err)
	}
	if cfg.Source("model.thinking") != FromFlag {
		t.Errorf("provenance = %q, want %q", cfg.Source("model.thinking"), FromFlag)
	}

	cfg, err = LoadWithOverrides(path, Overrides{Thinking: "default"})
	if err != nil || cfg.Model.Thinking != "" {
		t.Fatalf("default sentinel: thinking=%q err=%v", cfg.Model.Thinking, err)
	}

	if _, err := LoadWithOverrides(path, Overrides{Thinking: "turbo"}); err == nil ||
		!strings.Contains(err.Error(), "thinking") {
		t.Errorf("invalid flag value must fail naming the knob: %v", err)
	}

	// Flag absent: the file value stands.
	cfg, err = LoadWithOverrides(path, Overrides{})
	if err != nil || cfg.Model.Thinking != "low" {
		t.Errorf("no flag: thinking=%q err=%v", cfg.Model.Thinking, err)
	}
}

// ADR-0118 §1: xhigh is one vocabulary — every key that names a level
// and the flag take it, and the error for an unknown word lists it.
func TestXHighIsAcceptedWhereverALevelIsNamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\nthinking = \"xhigh\"\nrisk_thinking = \"xhigh\"\nlibrarian_thinking = \"xhigh\"\n"), 0o644)
	cfg, err := LoadWithOverrides(path, Overrides{})
	if err != nil {
		t.Fatalf("xhigh in the file: %v", err)
	}
	if cfg.Model.Thinking != "xhigh" || cfg.Model.RiskThinking != "xhigh" || cfg.Model.LibrarianThinking != "xhigh" {
		t.Errorf("loaded thinking=%q risk_thinking=%q librarian_thinking=%q", cfg.Model.Thinking, cfg.Model.RiskThinking, cfg.Model.LibrarianThinking)
	}

	_ = os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\n"), 0o644)
	cfg, err = LoadWithOverrides(path, Overrides{Thinking: "xhigh"})
	if err != nil || cfg.Model.Thinking != "xhigh" {
		t.Errorf("--thinking xhigh: thinking=%q err=%v", cfg.Model.Thinking, err)
	}

	for _, key := range []string{"thinking", "risk_thinking", "librarian_thinking"} {
		_ = os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\n"+key+" = \"turbo\"\n"), 0o644)
		if _, err := LoadWithOverrides(path, Overrides{}); err == nil || !strings.Contains(err.Error(), "xhigh") {
			t.Errorf("%s = turbo: the error does not list xhigh: %v", key, err)
		}
	}
}
