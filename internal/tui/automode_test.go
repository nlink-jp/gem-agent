package tui

// `/auto on` reported ON and left the footer saying otherwise: the
// interception matched the whole line, so anything with an argument
// went to the shared slash handler, which flips the agent's flag and
// cannot see this model. Operator report, 2026-09-09.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// autoModel drives the real submit path, so an interception that misses
// shows up as the footer disagreeing with the agent.
func autoModel(t *testing.T, start bool) (Model, *bool, *capture) {
	t.Helper()
	agentOn := start
	c := &capture{}
	m := New(Options{Msgs: uitext.For(uitext.JA), Theme: "notty", ModelName: "m",
		Printer:    c.printer,
		AutoMode:   agentOn,
		AutoState:  func() bool { return agentOn },
		ToggleAuto: func() bool { agentOn = !agentOn; return agentOn },
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return next.(Model), &agentOn, c
}

func submit(t *testing.T, m Model, input string) Model {
	t.Helper()
	m.ta.SetValue(input)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(Model)
}

func footerSaysAuto(m Model) bool { return strings.Contains(m.View(), "⚡auto") }

func TestAutoCommandFormsKeepTheFooterTrue(t *testing.T) {
	for _, tc := range []struct {
		input string
		start bool
		want  bool
	}{
		{"/auto", false, true},
		{"/auto", true, false},
		// The forms that used to reach the shared handler.
		{"/auto on", false, true},
		{"/auto off", true, false},
		// Idempotent: asking for the state it is already in must not
		// flip it to the opposite of what was asked.
		{"/auto on", true, true},
		{"/auto off", false, false},
	} {
		m, agentOn, _ := autoModel(t, tc.start)
		m = submit(t, m, tc.input)
		if *agentOn != tc.want {
			t.Errorf("%q from %v: agent = %v, want %v", tc.input, tc.start, *agentOn, tc.want)
		}
		if got := footerSaysAuto(m); got != tc.want {
			t.Errorf("%q from %v: footer says auto = %v, agent says %v", tc.input, tc.start, got, *agentOn)
		}
	}
}

// An unknown argument changes nothing and says so, rather than being
// ignored — which is how `/auto on` came to toggle.
func TestAutoCommandRefusesAnUnknownArgument(t *testing.T) {
	for _, input := range []string{"/auto maybe", "/auto on off"} {
		m, agentOn, c := autoModel(t, false)
		m = submit(t, m, input)
		if *agentOn {
			t.Errorf("%q changed the mode", input)
		}
		if footerSaysAuto(m) {
			t.Errorf("%q: footer says auto:\n%s", input, m.View())
		}
		// "and says so" — the earlier version asserted only that
		// nothing changed, so a TUI that swallowed the line silently
		// would have passed (independent review, 2026-09-09). The line
		// goes to scrollback, not the view.
		if !strings.Contains(c.all(), "/auto on|off") {
			t.Errorf("%q: no usage line: %q", input, c.all())
		}
	}
}

// The footer reads the mode live, so a path that changes it without
// telling the TUI cannot leave the marker stale — the class of bug the
// interception miss belonged to.
func TestFooterFollowsTheAgentWithoutBeingTold(t *testing.T) {
	agentOn := false
	m := New(Options{Msgs: uitext.For(uitext.JA), Theme: "notty", ModelName: "m",
		AutoState: func() bool { return agentOn }})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	if footerSaysAuto(m) {
		t.Fatalf("footer says auto before anything:\n%s", m.View())
	}
	agentOn = true // changed behind the TUI's back
	if !footerSaysAuto(m) {
		t.Errorf("footer did not follow the agent:\n%s", m.View())
	}
}
