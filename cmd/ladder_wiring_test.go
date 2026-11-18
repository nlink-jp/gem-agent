package cmd

import (
	"regexp"
	"strings"
	"testing"
)

// Every turn outside the TUI must climb the ladder (ADR-0065 §3): a
// call site that used the bare runTurn would silently lose the second
// and third press. Pinned by scanning the wiring, the way the shared
// stdin reader is.
func TestEveryTurnOutsideTheTUIUsesTheLadder(t *testing.T) {
	names, src := cmdSources(t)
	// The session context is a field of the live session since ADR-0124.
	bare := regexp.MustCompile(`\brunTurn\((sess\.)?ctx`)
	with := regexp.MustCompile(`\brunTurnWith\((sess\.)?ctx`)
	calls := 0
	for _, name := range names {
		for i, line := range strings.Split(src[name], "\n") {
			if bare.MatchString(line) {
				t.Errorf("%s:%d uses the bare runTurn — the ladder is lost there", name, i+1)
			}
			if with.MatchString(line) {
				calls++
				if !strings.Contains(line, "ladder") {
					t.Errorf("%s:%d calls runTurnWith without the ladder", name, i+1)
				}
			}
		}
	}
	if calls == 0 {
		t.Fatal("no runTurnWith call sites found — did the wiring move?")
	}
}
