package cmd

// The ceiling and its watcher are two settings, so the flags resolve
// independently (ADR-0080 §1). --writable and --read-only are the two
// ends of one of them; --auto-read-only is the other, and composes.

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

func TestReadOnlyOverrideResolvesTheCeiling(t *testing.T) {
	cases := []struct {
		name                            string
		writable, readOnly, autoRO, one bool
		want                            string
		wantErr                         string
	}{
		{name: "no flag", want: ""},
		{name: "writable", writable: true, want: "off"},
		{name: "read-only", readOnly: true, want: "on"},
		// The watcher is a different setting: it names no ceiling.
		{name: "auto alone", autoRO: true, want: ""},
		// And it composes with either end of the ceiling.
		{name: "auto with read-only", autoRO: true, readOnly: true, want: "on"},
		{name: "auto with writable", autoRO: true, writable: true, want: "off"},
		{name: "read-only in one-shot", readOnly: true, one: true, want: "on"},
		// Two ends of one setting.
		{name: "both ends", writable: true, readOnly: true, wantErr: "at most one"},
		// The watcher watches a session; -p has one input, at launch.
		{name: "auto in one-shot", autoRO: true, one: true, wantErr: "needs a session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readOnlyOverride(tc.writable, tc.readOnly, tc.autoRO, tc.one)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
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
		if got := effectiveCeiling(tc.ro, tc.auto, tc.one); got != tc.want {
			t.Errorf("effectiveCeiling(%v, %v, oneShot=%v) = %+v, want %+v",
				tc.ro, tc.auto, tc.one, got, tc.want)
		}
	}
}
