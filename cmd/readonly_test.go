package cmd

// The lane ceiling's three states resolve from three flags and one
// config key (ADR-0080 §1-2). What is pinned here is the shape of the
// axis: the flags contradict rather than layer, the auto state has no
// meaning without a session to watch, and a configured "auto" is read
// the strict way in one-shot.

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
)

func TestReadOnlyOverrideResolvesOneState(t *testing.T) {
	cases := []struct {
		name                            string
		writable, readOnly, autoRO, one bool
		want                            string
		wantErr                         string
	}{
		{name: "no flag", want: ""},
		{name: "writable", writable: true, want: config.ReadOnlyOff},
		{name: "read-only", readOnly: true, want: config.ReadOnlyOn},
		{name: "auto", autoRO: true, want: config.ReadOnlyAuto},
		{name: "read-only in one-shot", readOnly: true, one: true, want: config.ReadOnlyOn},
		{name: "writable in one-shot", writable: true, one: true, want: config.ReadOnlyOff},
		// Three states of one axis: two flags is a contradiction, not a
		// precedence question to answer quietly.
		{name: "two flags", writable: true, readOnly: true, wantErr: "at most one"},
		{name: "all three", writable: true, readOnly: true, autoRO: true, wantErr: "at most one"},
		// The auto state watches what the operator types; -p has one
		// input, given at invocation.
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

func TestEffectiveReadOnly(t *testing.T) {
	cases := []struct {
		cfg  string
		one  bool
		want string
	}{
		{"", false, config.ReadOnlyOff},
		{"", true, config.ReadOnlyOff},
		{config.ReadOnlyOff, true, config.ReadOnlyOff},
		{config.ReadOnlyOn, false, config.ReadOnlyOn},
		// A restriction only restricts, so unlike auto_approve it needs
		// no invocation-visibility argument and is honoured in -p.
		{config.ReadOnlyOn, true, config.ReadOnlyOn},
		{config.ReadOnlyAuto, false, config.ReadOnlyAuto},
		// The strict reading: dropping it would lose a restriction
		// silently where nobody is watching.
		{config.ReadOnlyAuto, true, config.ReadOnlyOn},
	}
	for _, tc := range cases {
		if got := effectiveReadOnly(tc.cfg, tc.one); got != tc.want {
			t.Errorf("effectiveReadOnly(%q, oneShot=%v) = %q, want %q", tc.cfg, tc.one, got, tc.want)
		}
	}
}
