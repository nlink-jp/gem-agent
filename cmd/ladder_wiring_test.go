package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every turn outside the TUI must climb the ladder (ADR-0065 §3): a
// call site that used the bare runTurn would silently lose the second
// and third press. Pinned by scanning the wiring, the way the shared
// stdin reader is.
func TestEveryTurnOutsideTheTUIUsesTheLadder(t *testing.T) {
	src, err := os.ReadFile("root.go")
	if err != nil {
		t.Fatal(err)
	}
	bare := regexp.MustCompile(`\brunTurn\(ctx`)
	with := regexp.MustCompile(`\brunTurnWith\(ctx`)
	calls := 0
	for i, line := range strings.Split(string(src), "\n") {
		if bare.MatchString(line) {
			t.Errorf("root.go:%d uses the bare runTurn — the ladder is lost there", i+1)
		}
		if with.MatchString(line) {
			calls++
			if !strings.Contains(line, "ladder") {
				t.Errorf("root.go:%d calls runTurnWith without the ladder", i+1)
			}
		}
	}
	if calls == 0 {
		t.Fatal("no runTurnWith call sites found — did the wiring move?")
	}
}
