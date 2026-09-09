package cmd

// The ceiling and its watcher are two settings, so the flags resolve
// independently (ADR-0080 §1). --writable and --read-only are the two
// ends of one of them; --auto-read-only is the other, and composes.

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

func TestReadOnlyOverrideResolvesBothSettings(t *testing.T) {
	cases := []struct {
		name                            string
		writable, readOnly, autoRO, one bool
		ceiling, auto                   string
		wantErr                         string
	}{
		{name: "no flag"},
		// --writable means "this run is writable", which has to include
		// "and is not watched": otherwise a configured watcher
		// re-tightens what the flag just released, and in -p there is
		// then no command-line escape at all.
		{name: "writable", writable: true, ceiling: "off", auto: "off"},
		{name: "read-only", readOnly: true, ceiling: "on"},
		{name: "auto alone", autoRO: true, auto: "on"},
		{name: "auto with read-only", autoRO: true, readOnly: true, ceiling: "on", auto: "on"},
		// Explicit beats implied: --auto-read-only asks for the watcher,
		// --writable only implies it is not wanted.
		{name: "auto with writable", autoRO: true, writable: true, ceiling: "off", auto: "on"},
		{name: "writable in one-shot", writable: true, one: true, ceiling: "off", auto: "off"},
		{name: "both ends", writable: true, readOnly: true, wantErr: "at most one"},
		{name: "auto in one-shot", autoRO: true, one: true, wantErr: "needs a session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ceiling, auto, err := readOnlyOverride(tc.writable, tc.readOnly, tc.autoRO, tc.one)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ceiling != tc.ceiling || auto != tc.auto {
				t.Errorf("got (%q, %q), want (%q, %q)", ceiling, auto, tc.ceiling, tc.auto)
			}
		})
	}
}

// The composition the two halves are resolved through: a flag has to
// survive all the way to the ceiling the run starts with. Testing each
// function alone let `-p --writable` with a configured watcher start
// read-only with no escape (independent review, 2026-09-09).
func TestReadOnlyFlagsComposeWithConfig(t *testing.T) {
	cases := []struct {
		name                     string
		cfgRO, cfgAuto           bool
		writable, readOnly, auto bool
		one                      bool
		want                     sandbox.Ceiling
	}{
		{name: "-p, configured watcher, --writable escapes",
			cfgAuto: true, writable: true, one: true, want: sandbox.Ceiling{}},
		{name: "-p, configured ceiling, --writable escapes",
			cfgRO: true, writable: true, one: true, want: sandbox.Ceiling{}},
		{name: "-p, configured watcher, no flag is read-only",
			cfgAuto: true, one: true, want: sandbox.Ceiling{ReadOnly: true}},
		{name: "session, configured watcher, --writable releases both",
			cfgAuto: true, writable: true, want: sandbox.Ceiling{}},
		{name: "session, configured ceiling, --auto-read-only keeps it and arms",
			cfgRO: true, auto: true, want: sandbox.Ceiling{ReadOnly: true, Auto: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ceiling, auto, err := readOnlyOverride(tc.writable, tc.readOnly, tc.auto, tc.one)
			if err != nil {
				t.Fatal(err)
			}
			ro, roAuto := tc.cfgRO, tc.cfgAuto
			if ceiling != "" {
				ro = ceiling == "on"
			}
			if auto != "" {
				roAuto = auto == "on"
			}
			if got := effectiveCeiling(config.AgentConfig{ReadOnly: ro, ReadOnlyAuto: roAuto}, tc.one); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEffectiveCeiling(t *testing.T) {
	cases := []struct {
		ro, auto, one bool
		want          sandbox.Ceiling
	}{
		{false, false, false, sandbox.Ceiling{}},
		{true, false, false, sandbox.Ceiling{ReadOnly: true}},
		{false, true, false, sandbox.Ceiling{Auto: true}},
		// The combination a tri-state could not express.
		{true, true, false, sandbox.Ceiling{ReadOnly: true, Auto: true}},
		// One-shot has no session to watch. A restriction only
		// restricts, so a configured ceiling is honoured; a configured
		// watcher becomes the ceiling rather than being dropped, which
		// would lose it silently where nobody is looking.
		{false, false, true, sandbox.Ceiling{}},
		{true, false, true, sandbox.Ceiling{ReadOnly: true}},
		{false, true, true, sandbox.Ceiling{ReadOnly: true}},
		{true, true, true, sandbox.Ceiling{ReadOnly: true}},
	}
	for _, tc := range cases {
		if got := effectiveCeiling(config.AgentConfig{ReadOnly: tc.ro, ReadOnlyAuto: tc.auto}, tc.one); got != tc.want {
			t.Errorf("effectiveCeiling(%v, %v, oneShot=%v) = %+v, want %+v",
				tc.ro, tc.auto, tc.one, got, tc.want)
		}
	}
}
