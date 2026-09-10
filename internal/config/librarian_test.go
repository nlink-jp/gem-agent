package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLibCfg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ADR-0083 §1: the feature is an opt-in. Nothing set means every tool
// is advertised, exactly as before the record.
func TestAdvertiseDefaultsToAll(t *testing.T) {
	cfg, err := LoadWithOverrides(writeLibCfg(t, ""), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCP.Advertise != "all" || cfg.MCP.OnRequest() {
		t.Errorf("default advertise = %q, OnRequest = %v; want all / false", cfg.MCP.Advertise, cfg.MCP.OnRequest())
	}
	if cfg.Source("mcp.advertise") != FromDefault {
		t.Errorf("provenance = %v, want default", cfg.Source("mcp.advertise"))
	}
}

func TestAdvertiseOnRequestAndPreloadLoad(t *testing.T) {
	cfg, err := LoadWithOverrides(writeLibCfg(t, "[mcp]\nadvertise = \"on-request\"\npreload = [\"tor-exit-lookup\", \"asn-lookup\"]\n"), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MCP.OnRequest() {
		t.Error("on-request not recognised")
	}
	if len(cfg.MCP.Preload) != 2 || cfg.MCP.Preload[0] != "tor-exit-lookup" {
		t.Errorf("preload = %v", cfg.MCP.Preload)
	}
	for _, key := range []string{"mcp.advertise", "mcp.preload"} {
		if cfg.Source(key) != FromFile {
			t.Errorf("provenance of %s = %v, want file", key, cfg.Source(key))
		}
	}
}

// Strict config: an unknown value names the key and the accepted
// values, rather than silently falling back to "all".
func TestAdvertiseRejectsUnknownValue(t *testing.T) {
	_, err := LoadWithOverrides(writeLibCfg(t, "[mcp]\nadvertise = \"lazy\"\n"), Overrides{})
	if err == nil || !strings.Contains(err.Error(), "advertise") || !strings.Contains(err.Error(), "on-request") {
		t.Errorf("unknown advertise value loaded without a naming error: %v", err)
	}
}

// ADR-0083 §2: the librarian slot follows ADR-0082's table.
func TestLibrarianSlotSemantics(t *testing.T) {
	cases := []struct {
		name, lib, thinking string
		slot                bool
		model               string
	}{
		{"unset/unset rides the tier", "", "", false, "main"},
		{"model only", "judge", "", true, "judge"},
		{"model and level", "judge", "low", true, "judge"},
		{"level only puts the main name on a slot", "", "low", true, "main"},
	}
	for _, c := range cases {
		m := ModelConfig{Name: "main", Librarian: c.lib, LibrarianThinking: c.thinking}
		if got := m.LibrarianSlot(); got != c.slot {
			t.Errorf("%s: LibrarianSlot() = %v, want %v", c.name, got, c.slot)
		}
		if got := m.LibrarianModel(); got != c.model {
			t.Errorf("%s: LibrarianModel() = %q, want %q", c.name, got, c.model)
		}
	}
}

func TestLibrarianKeysLoadAndValidate(t *testing.T) {
	cfg, err := LoadWithOverrides(writeLibCfg(t, "librarian = \"judge\"\nlibrarian_thinking = \"low\"\n"), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Librarian != "judge" || cfg.Model.LibrarianThinking != "low" {
		t.Errorf("loaded librarian=%q librarian_thinking=%q", cfg.Model.Librarian, cfg.Model.LibrarianThinking)
	}
	for _, key := range []string{"model.librarian", "model.librarian_thinking"} {
		if cfg.Source(key) != FromFile {
			t.Errorf("provenance of %s = %v, want file", key, cfg.Source(key))
		}
	}
	if _, err := LoadWithOverrides(writeLibCfg(t, "librarian_thinking = \"turbo\"\n"), Overrides{}); err == nil || !strings.Contains(err.Error(), "librarian_thinking") {
		t.Errorf("invalid librarian_thinking loaded without a naming error: %v", err)
	}
}
