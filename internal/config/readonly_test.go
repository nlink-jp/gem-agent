package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// [agent].read_only is validated, defaulted and overridable, and the
// override is recorded as flag provenance so /settings can say where
// the ceiling came from (ADR-0080 §1).
func TestReadOnlyConfig(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const base = "[gcp]\nproject = \"p\"\n[model]\nname = \"m\"\n"

	t.Run("default is off", func(t *testing.T) {
		cfg, err := LoadWithOverrides(write(t, base), Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Agent.ReadOnly != ReadOnlyOff {
			t.Errorf("read_only = %q, want %q", cfg.Agent.ReadOnly, ReadOnlyOff)
		}
	})

	t.Run("configured value is kept", func(t *testing.T) {
		cfg, err := LoadWithOverrides(write(t, base+"[agent]\nread_only = \"auto\"\n"), Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Agent.ReadOnly != ReadOnlyAuto {
			t.Errorf("read_only = %q", cfg.Agent.ReadOnly)
		}
		if cfg.Sources["agent.read_only"] != FromFile {
			t.Errorf("provenance = %q, want %q", cfg.Sources["agent.read_only"], FromFile)
		}
	})

	t.Run("an unknown state is refused", func(t *testing.T) {
		_, err := LoadWithOverrides(write(t, base+"[agent]\nread_only = \"maybe\"\n"), Overrides{})
		if err == nil || !strings.Contains(err.Error(), "read_only") {
			t.Fatalf("err = %v, want one naming read_only", err)
		}
	})

	// The override runs both ways: --writable steps out of a configured
	// "on", which is what makes the loosening visible on the invocation.
	t.Run("the flag overrides in both directions", func(t *testing.T) {
		for _, tc := range []struct{ file, flag, want string }{
			{"", ReadOnlyOn, ReadOnlyOn},
			{"auto", ReadOnlyOff, ReadOnlyOff},
			{"on", ReadOnlyOff, ReadOnlyOff},
		} {
			body := base
			if tc.file != "" {
				body += "[agent]\nread_only = \"" + tc.file + "\"\n"
			}
			cfg, err := LoadWithOverrides(write(t, body), Overrides{ReadOnly: tc.flag})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Agent.ReadOnly != tc.want {
				t.Errorf("file=%q flag=%q → %q, want %q", tc.file, tc.flag, cfg.Agent.ReadOnly, tc.want)
			}
			if cfg.Sources["agent.read_only"] != FromFlag {
				t.Errorf("provenance = %q, want %q", cfg.Sources["agent.read_only"], FromFlag)
			}
		}
	})

	t.Run("an unknown override is refused", func(t *testing.T) {
		if _, err := LoadWithOverrides(write(t, base), Overrides{ReadOnly: "maybe"}); err == nil {
			t.Fatal("accepted an unknown read-only state")
		}
	})
}
