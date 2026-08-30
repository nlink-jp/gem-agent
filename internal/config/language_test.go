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
	_ = os.WriteFile(path, []byte(base+"[tui]\nlanguage = \"fr\"\n"), 0o644)
	if _, err := LoadWithOverrides(path, Overrides{}); err == nil || !strings.Contains(err.Error(), "language") {
		t.Errorf("invalid language loaded without error: %v", err)
	}

	_ = os.WriteFile(path, []byte(base+"[tui]\nlanguage = \"ja\"\n"), 0o644)
	cfg, err := LoadWithOverrides(path, Overrides{})
	if err != nil || cfg.TUI.Language != "ja" {
		t.Errorf("valid language: cfg=%v err=%v", cfg.TUI.Language, err)
	}
	if cfg.Source("tui.language") != FromFile {
		t.Errorf("tui.language source = %q, want %q", cfg.Source("tui.language"), FromFile)
	}

	_ = os.WriteFile(path, []byte(base), 0o644)
	cfg, err = LoadWithOverrides(path, Overrides{})
	if err != nil || cfg.TUI.Language != "auto" {
		t.Errorf("default language: cfg=%v err=%v", cfg.TUI.Language, err)
	}
}

// [telemetry] validation (ADR-0035): the gcp default needs nothing,
// otlp backends need an endpoint, unknown backends fail.
func TestTelemetryValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	base := "[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\n"

	_ = os.WriteFile(path, []byte(base+"[telemetry]\nenabled = true\n"), 0o644)
	if cfg, err := LoadWithOverrides(path, Overrides{}); err != nil || cfg.Telemetry.Backend != "gcp" {
		t.Errorf("gcp default: %+v err=%v", cfg.Telemetry, err)
	}
	_ = os.WriteFile(path, []byte(base+"[telemetry]\nenabled = true\nbackend = \"otlp-grpc\"\nendpoint = \"\"\n"), 0o644)
	if _, err := LoadWithOverrides(path, Overrides{}); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("otlp without endpoint accepted: %v", err)
	}
	_ = os.WriteFile(path, []byte(base+"[telemetry]\nenabled = true\nbackend = \"udp\"\n"), 0o644)
	if _, err := LoadWithOverrides(path, Overrides{}); err == nil || !strings.Contains(err.Error(), "backend") {
		t.Errorf("bad backend accepted: %v", err)
	}
	_ = os.WriteFile(path, []byte(base+"[telemetry]\nenabled = true\nheaders_file = \"/x.json\"\n"), 0o644)
	if _, err := LoadWithOverrides(path, Overrides{}); err == nil || !strings.Contains(err.Error(), "headers_file") {
		t.Errorf("headers_file with gcp backend accepted: %v", err)
	}
	_ = os.WriteFile(path, []byte(base), 0o644)
	cfg, err := LoadWithOverrides(path, Overrides{})
	if err != nil || cfg.Telemetry.Enabled {
		t.Errorf("defaults: %+v err=%v", cfg.Telemetry, err)
	}
}
