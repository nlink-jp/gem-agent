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

// A shell line carries printf's "\n": kitty's send-text decodes escapes, so
// an undoubled backslash would press Enter in the middle of the command.
func TestKittyEscapeKeepsBackslashesAndSendsKeys(t *testing.T) {
	if got := kittyEscape(`printf 'a\n'` + "\r"); got != `printf 'a\\n'\x0d` {
		t.Errorf("kittyEscape = %q", got)
	}
	if got := kittyEscape("\x15"); got != `\x15` {
		t.Errorf("Ctrl+U = %q", got)
	}
}

func TestAsTextJoinsKeysAndStrings(t *testing.T) {
	if got := asText("ab\rc\"d"); got != `"ab" & (ASCII character 13) & "c\"d"` {
		t.Errorf("asText = %s", got)
	}
	if got := asText(""); got != `""` {
		t.Errorf("empty = %s", got)
	}
}

// The binary's echo of what was typed is not history.
func TestAnalyzeSkipsTheBinarysEcho(t *testing.T) {
	r := analyze([]string{
		"! echo 'PROBE-START arm=app-gem-agent proto=none'",
		"PROBE-START arm=app-gem-agent proto=none size=80x24",
		"! i=1; while [ $i -le 1 ]; do printf 'H%03d history\n' $i; done",
		"H001 history",
		"> /show pic.png",
		"! echo 'RESIZE-1: the driver narrows the window'",
		"RESIZE-1: the driver narrows the window",
		"AFTER-SHRINK",
		"RESIZE-2: widen",
		"AFTER-GROW",
		"PROBE-END printed H=1",
	})
	if r.Arm != "app-gem-agent" || len(r.Zones) != 3 || r.Zones[0].Stray != 0 || r.Zones[1].Stray != 0 {
		t.Errorf("echo lines were read as history: %+v", r.Zones)
	}
	// The binary puts one blank row before each echo; that is its layout.
	// A blank row anywhere else is still black space.
	b := analyze([]string{
		"PROBE-START arm=app-lagent proto=none",
		"H001 history",
		"",
		"! echo RESIZE-1",
		"RESIZE-1",
		"",
		"",
		"AFTER-SHRINK",
		"PROBE-END printed H=1",
	})
	if b.Zones[0].Blank != 0 || b.Zones[1].Blank != 2 {
		t.Errorf("separator vs black space: before %d (want 0), shrink %d (want 2)", b.Zones[0].Blank, b.Zones[1].Blank)
	}
}
