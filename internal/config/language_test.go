package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLanguageValidation pins [tui].language (ADR-0029): auto/ja/en
// load, anything else fails at startup with the key named, and the
// default is auto with provenance tracked when the file sets it.
func TestLanguageValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	base := "[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\n"
	os.WriteFile(path, []byte(base+"[tui]\nlanguage = \"fr\"\n"), 0o644)
	if _, err := LoadWithOverrides(path, Overrides{}); err == nil || !strings.Contains(err.Error(), "language") {
		t.Errorf("invalid language loaded without error: %v", err)
	}

	os.WriteFile(path, []byte(base+"[tui]\nlanguage = \"ja\"\n"), 0o644)
	cfg, err := LoadWithOverrides(path, Overrides{})
	if err != nil || cfg.TUI.Language != "ja" {
		t.Errorf("valid language: cfg=%v err=%v", cfg.TUI.Language, err)
	}
	if cfg.Source("tui.language") != FromFile {
		t.Errorf("tui.language source = %q, want %q", cfg.Source("tui.language"), FromFile)
	}

	os.WriteFile(path, []byte(base), 0o644)
	cfg, err = LoadWithOverrides(path, Overrides{})
	if err != nil || cfg.TUI.Language != "auto" {
		t.Errorf("default language: cfg=%v err=%v", cfg.TUI.Language, err)
	}
}
