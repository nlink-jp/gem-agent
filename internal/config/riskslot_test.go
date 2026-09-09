package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0082 §2: the slot exists as soon as either key is set, and its
// model is the named one or the main one.
func TestRiskSlotSemantics(t *testing.T) {
	cases := []struct {
		name, risk, thinking string
		slot                 bool
		model                string
	}{
		{"unset/unset rides the main backend", "", "", false, "main"},
		{"model only", "judge", "", true, "judge"},
		{"model and level", "judge", "low", true, "judge"},
		{"level only puts the main name on a slot", "", "low", true, "main"},
	}
	for _, c := range cases {
		m := ModelConfig{Name: "main", Risk: c.risk, RiskThinking: c.thinking}
		if got := m.RiskSlot(); got != c.slot {
			t.Errorf("%s: RiskSlot() = %v, want %v", c.name, got, c.slot)
		}
		if got := m.RiskModel(); got != c.model {
			t.Errorf("%s: RiskModel() = %q, want %q", c.name, got, c.model)
		}
	}
}

// The keys load from the file with provenance, and the level is
// validated with [model].thinking's vocabulary (ADR-0025 §1).
func TestRiskSlotLoadsAndValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("risk = \"judge\"\nrisk_thinking = \"low\"\n")
	cfg, err := LoadWithOverrides(path, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Risk != "judge" || cfg.Model.RiskThinking != "low" {
		t.Errorf("loaded risk=%q risk_thinking=%q", cfg.Model.Risk, cfg.Model.RiskThinking)
	}
	for _, key := range []string{"model.risk", "model.risk_thinking"} {
		if cfg.Source(key) != FromFile {
			t.Errorf("provenance of %s = %v, want file", key, cfg.Source(key))
		}
	}

	write("risk_thinking = \"turbo\"\n")
	if _, err := LoadWithOverrides(path, Overrides{}); err == nil || !strings.Contains(err.Error(), "risk_thinking") {
		t.Errorf("invalid risk_thinking loaded without a naming error: %v", err)
	}

	// Unset keys are unset — not the main model's values copied in —
	// so /settings can say "(main model)" and the agent can tell the
	// two cases apart.
	write("")
	cfg, err = LoadWithOverrides(path, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.RiskSlot() {
		t.Errorf("no keys set, but RiskSlot() = true (%+v)", cfg.Model)
	}
	if cfg.Source("model.risk") != FromDefault {
		t.Errorf("provenance of unset model.risk = %v", cfg.Source("model.risk"))
	}
}

// --model moves the main name, and an unset slot follows it (ADR-0082
// §2): the tier keeps judging on the model the operator chose for the
// run, without a second flag.
func TestRiskModelFollowsModelOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithOverrides(path, Overrides{Model: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Model.RiskModel(); got != "other" {
		t.Errorf("RiskModel() = %q after --model other", got)
	}
}
