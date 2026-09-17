package termimg

import (
	"strings"
	"testing"
)

// TestFromEnvDecidesWhatItCan: the environment settles the common cases
// without a query, and a multiplexer settles to None — passthrough is the
// multiplexer's configuration, and the one measured rendering a payload
// stranded a frame for every image.
func TestFromEnvDecidesWhatItCan(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		want    Protocol
		decided bool
	}{
		{"tmux wins over everything", map[string]string{"TMUX": "/tmp/s", "KITTY_WINDOW_ID": "1", "TERM_PROGRAM": "iTerm.app"}, None, true},
		{"screen wins too", map[string]string{"TERM": "screen-256color", "TERM_PROGRAM": "iTerm.app"}, None, true},
		{"kitty by window id", map[string]string{"KITTY_WINDOW_ID": "3"}, Kitty, true},
		{"kitty by TERM", map[string]string{"TERM": "xterm-kitty"}, Kitty, true},
		{"ghostty", map[string]string{"GHOSTTY_RESOURCES_DIR": "/opt/ghostty"}, Kitty, true},
		{"iTerm2", map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM": "xterm-256color"}, ITerm2, true},
		{"Terminal.app is not decided here", map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, None, false},
		{"bare xterm must be asked", map[string]string{"TERM": "xterm-256color"}, None, false},
		{"empty environment must be asked", map[string]string{}, None, false},
	} {
		got, decided := fromEnv(func(k string) string { return tc.env[k] })
		if got != tc.want || decided != tc.decided {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", tc.name, got, decided, tc.want, tc.decided)
		}
	}
}

// TestDetectWithoutATTYDrawsNothing: every failure path answers None,
// because not drawing is always safe and the fallback is what the runtime
// does today.
func TestDetectWithoutATTYDrawsNothing(t *testing.T) {
	env := func(k string) string { return map[string]string{"TERM": "xterm-256color"}[k] }
	if got := Detect(nil, env, 0); got != None {
		t.Errorf("no tty: got %v, want None", got)
	}
	// A decided environment still answers without touching the terminal.
	iterm := func(k string) string { return map[string]string{"TERM_PROGRAM": "iTerm.app"}[k] }
	if got := Detect(nil, iterm, 0); got != ITerm2 {
		t.Errorf("iTerm2 by env with no tty: got %v, want ITerm2", got)
	}
}

// TestParseKittyReply: silence and a foreign escape both mean "no". Only a
// complete _G reply carrying OK is an acceptance — and "done" must not be
// reported early, or the caller stops reading a reply still arriving.
func TestParseKittyReply(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in       string
		wantDone bool
		wantOK   bool
	}{
		{"empty", "", false, false},
		{"partial prefix", "\x1b_", false, false},
		{"complete OK", "\x1b_Gi=31;OK\x1b\\", true, true},
		{"complete failure", "\x1b_Gi=31;ENOTSUPPORTED:not supported\x1b\\", true, false},
		{"OK still arriving", "\x1b_Gi=31;OK", false, false},
		{"a foreign escape is not our reply", "\x1b[0m", true, false},
		{"plain text is not our reply", "abc", true, false},
		// The DA1 answer to the second half of the query. A terminal
		// that ignores the graphics question still answers this one,
		// so its arrival is the definitive no that the timeout used
		// to have to stand in for.
		{"a DA1 reply is a definitive no", "\x1b[?62;4c", true, false},
		{"a DA1 reply still arriving", "\x1b[", false, false},
	} {
		done, ok := parseKittyReply([]byte(tc.in))
		if done != tc.wantDone || ok != tc.wantOK {
			t.Errorf("%s: got (done=%v, ok=%v), want (done=%v, ok=%v)", tc.name, done, ok, tc.wantDone, tc.wantOK)
		}
	}
}

// TestResolveHonoursTheSetting: "auto" is the only value that asks the
// terminal, a named protocol is the escape hatch for a probe that is wrong,
// and anything else draws nothing — including a value that should have been
// rejected by config validation, because reaching here unvalidated is not a
// reason to start emitting escapes.
func TestResolveHonoursTheSetting(t *testing.T) {
	kitty := func(k string) string { return map[string]string{"KITTY_WINDOW_ID": "1"}[k] }
	terminalApp := func(k string) string { return map[string]string{"TERM_PROGRAM": "Apple_Terminal"}[k] }
	for _, tc := range []struct {
		setting string
		env     func(string) string
		want    Protocol
	}{
		{"auto", kitty, Kitty},
		{"auto", terminalApp, None}, // no tty to ask, so nothing draws
		{"off", kitty, None},        // off beats a capable terminal
		{"iterm", terminalApp, ITerm2},
		{"kitty", terminalApp, Kitty},
		{"", kitty, None},
		{"sixel", kitty, None}, // never a protocol here, whatever a file says
	} {
		if got := Resolve(tc.setting, nil, tc.env, 0); got != tc.want {
			t.Errorf("Resolve(%q) = %v, want %v", tc.setting, got, tc.want)
		}
	}
}

// TestTheQueryIsAnswerableByEveryTerminal is the property the timeout used
// to stand in for. The graphics question alone has no negative answer — a
// terminal that does not know the protocol says nothing, and silence cannot
// be told from slowness, so every such terminal paid the whole budget:
// measured at 2.001 s on Apple Terminal, at every start, with TERM_PROGRAM
// set and fromEnv unable to classify it. Appending a device-attributes
// request gives the negative answer a carrier, because every VT-compatible
// terminal replies to DA1.
func TestTheQueryIsAnswerableByEveryTerminal(t *testing.T) {
	if !strings.HasSuffix(kittyQuery, "\x1b[c") {
		t.Fatalf("the query must end with a DA1 request; got %q", kittyQuery)
	}
	if !strings.HasPrefix(kittyQuery, "\x1b_G") {
		t.Errorf("the graphics question must come first, so a kitty reply arrives before DA1: %q", kittyQuery)
	}
	// And the parser must read that carrier as a no rather than waiting.
	done, ok := parseKittyReply([]byte("\x1b[?62;4c"))
	if !done || ok {
		t.Errorf("a DA1 reply must end the wait with a no; got (done=%v, ok=%v)", done, ok)
	}
}
