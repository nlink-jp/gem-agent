package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The readings are what the table in ADR-0093 rests on, so each one is
// held to a screen that should say YES and one that should say no. A
// reading that cannot tell them apart would print a table of nothing.
func TestReadingsSeparateActedFromShown(t *testing.T) {
	screen := func(lines ...string) []string { return lines }
	for _, tc := range []struct {
		name       string
		acted, not reading
	}{
		{"OSC0", reading{title: "ESCPWN"}, reading{title: "host"}},
		{"OSC52", reading{buffers: "ESCPWN"}, reading{}},
		{"CSI-CUP",
			reading{screen: screen("", "", strings.Repeat(" ", 59)+"CUPMARK after")},
			reading{screen: screen("before [3;60HCUPMARK after")}},
		{"CSI-ED2", reading{screen: screen("before  after")}, reading{screen: screen(readyTag, "before [2J after")}},
		{"CR", reading{screen: screen("XY after")}, reading{screen: screen("before abcdefXY after")}},
		{"BS", reading{screen: screen("before abcXY after")}, reading{screen: screen("before abcdefXY after")}},
		{"OSC-open", reading{screen: screen("before ")}, reading{screen: screen("before ]0; ; rm -rf HIDDENMARK after")}},
		{"OSC1337", reading{screen: screen("before  after")}, reading{screen: screen("before ]1337;File=inline=1:QUFBQQ== after")}},
	} {
		c, ok := find(tc.name)
		if !ok {
			t.Fatalf("no case %s", tc.name)
		}
		if acted, what := c.effect(tc.acted); !acted {
			t.Errorf("%s: a screen where it acted read as not acted (%s)", tc.name, what)
		}
		if acted, what := c.effect(tc.not); acted {
			t.Errorf("%s: a screen where it was shown read as acted (%s)", tc.name, what)
		}
	}
}

// A case name is a flag value and a table row: two with one name would
// make one of them unreachable from -ui.
func TestCaseNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range cases() {
		if seen[c.name] {
			t.Errorf("case %s appears twice", c.name)
		}
		seen[c.name] = true
		// The raw 8-bit cases are not valid UTF-8, so they are found by
		// byte rather than by rune.
		if !strings.ContainsAny(c.payload, "\x07\x08\x0d\x1b\u009b\u009c\u009d") &&
			!strings.ContainsFunc(c.payload, func(r rune) bool { return r == utf8.RuneError }) {
			t.Errorf("case %s carries no control character: it measures nothing", c.name)
		}
	}
}
