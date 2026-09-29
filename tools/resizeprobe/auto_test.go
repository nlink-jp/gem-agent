package main

import (
	"strings"
	"testing"
)

// The command iTerm2 runs passes through AppleScript and then a shell; a
// quote in either layer that is not escaped runs something else.
func TestQuotingSurvivesBothLayers(t *testing.T) {
	if got := shQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shQuote = %s", got)
	}
	if got := asQuote(`say "hi" \ bye`); got != `"say \"hi\" \\ bye"` {
		t.Errorf("asQuote = %s", got)
	}
}

func TestWidestMarkIsWhatTheProbePrints(t *testing.T) {
	line := "RESIZE-1: NARROW the window now, to about two-thirds of its width, and wait (widest frame line 158)"
	m := widestMark.FindStringSubmatch(line)
	if m == nil || m[1] != "158" {
		t.Errorf("widestMark on %q = %v", line, m)
	}
}

func TestOnlyTheTwoTerminalsAreDriven(t *testing.T) {
	for _, ok := range []string{"kitty", "iterm2"} {
		if _, err := newTerminal(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	if _, err := newTerminal("wezterm"); err == nil || !strings.Contains(err.Error(), "kitty, iterm2") {
		t.Errorf("an unknown terminal must be refused by name: %v", err)
	}
}
