package config

// The ceiling and its watcher are two settings, not one tri-state
// (ADR-0080 §1), so they load, default and override independently.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const cfgBase = "[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\n"

func TestReadOnlyConfigDefaultsOff(t *testing.T) {
	cfg, err := LoadWithOverrides(writeCfg(t, cfgBase), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.ReadOnly || cfg.Agent.ReadOnlyAuto {
		t.Errorf("default = %v/%v, want both off", cfg.Agent.ReadOnly, cfg.Agent.ReadOnlyAuto)
	}
}

// All four combinations load, including the one a tri-state could not
// express: watching while already read-only.
func TestReadOnlyConfigLoadsBothIndependently(t *testing.T) {
	for _, tc := range []struct{ ro, auto bool }{
		{false, false}, {true, false}, {false, true}, {true, true},
	} {
		body := cfgBase + "[agent]\n"
		body += "read_only = " + boolLit(tc.ro) + "\nread_only_auto = " + boolLit(tc.auto) + "\n"
		cfg, err := LoadWithOverrides(writeCfg(t, body), Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Agent.ReadOnly != tc.ro || cfg.Agent.ReadOnlyAuto != tc.auto {
			t.Errorf("read_only=%v auto=%v → %v/%v", tc.ro, tc.auto,
				cfg.Agent.ReadOnly, cfg.Agent.ReadOnlyAuto)
		}
		for _, key := range []string{"agent.read_only", "agent.read_only_auto"} {
			if cfg.Sources[key] != FromFile {
				t.Errorf("%s provenance = %q, want %q", key, cfg.Sources[key], FromFile)
			}
		}
	}
}

func boolLit(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// The ceiling override runs both ways — --writable is how a run steps
// out of a configured ceiling, which is what makes the loosening
// visible on the invocation — and it does not touch the watcher.
func TestReadOnlyOverridesAreIndependent(t *testing.T) {
	body := cfgBase + "[agent]\nread_only = true\nread_only_auto = true\n"
	// Either bit can be moved alone at this layer; which flags move
	// which is readOnlyOverride's job, tested in cmd.
	cfg, err := LoadWithOverrides(writeCfg(t, body), Overrides{ReadOnly: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.ReadOnly {
		t.Error("the ceiling override did not lower the ceiling")
	}
	if !cfg.Agent.ReadOnlyAuto {
		t.Error("a ceiling override moved the watcher, which is a different setting")
	}
	if cfg.Sources["agent.read_only"] != FromFlag {
		t.Errorf("ceiling provenance = %q", cfg.Sources["agent.read_only"])
	}
	if cfg.Sources["agent.read_only_auto"] != FromFile {
		t.Errorf("watcher provenance = %q, want it untouched", cfg.Sources["agent.read_only_auto"])
	}

	// And arming the watcher does not raise the ceiling.
	cfg, err = LoadWithOverrides(writeCfg(t, cfgBase), Overrides{ReadOnlyAuto: "on"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.ReadOnly || !cfg.Agent.ReadOnlyAuto {
		t.Errorf("--auto-read-only → %v/%v, want watcher only", cfg.Agent.ReadOnly, cfg.Agent.ReadOnlyAuto)
	}
}

// Both overrides refuse a value they do not understand, and each says
// which one it was: the watcher's branch had no test, so a typo there
// could have silently armed nothing (independent review, pass 2).
func TestReadOnlyRejectsAnUnknownOverride(t *testing.T) {
	for _, tc := range []struct {
		name string
		ov   Overrides
		want string
	}{
		{"ceiling", Overrides{ReadOnly: "maybe"}, "read-only override"},
		{"watcher", Overrides{ReadOnlyAuto: "maybe"}, "read-only-auto override"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadWithOverrides(writeCfg(t, cfgBase), tc.ov)
			if err == nil {
				t.Fatalf("accepted an unknown override: %+v", cfg.Agent)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not name the setting: %v", err)
			}
			if !strings.Contains(err.Error(), `"maybe"`) {
				t.Errorf("error does not quote what was passed: %v", err)
			}
		})
	}
}
