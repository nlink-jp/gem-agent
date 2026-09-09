package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// reopenSettings closes and reopens the panel so settingsPlan sees the
// test's terminal size and printed counter (settingsModel opens it
// before either is set).
func reopenSettings(t *testing.T, m Model) Model {
	t.Helper()
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	next, _ := m.openSettings()
	return next.(Model)
}

// Operator report 2026-09-10 (v0.75.0): after ESC from /settings one
// stray line of earlier output sat at the top of an otherwise empty
// screen. A frame of N rows drawn from row printed+1 scrolls
// printed+N-height rows (measured in tmux), so a frame one short of
// the height leaves the last printed row above it, while the counter's
// convention reads as if nothing were there — and that row survived
// the close. When the panel needs more rows than remain, its frame
// must be the terminal's full height, so every printed row scrolls
// out, the counter lands at zero, and the close is clean.
func TestSettingsFrameFillsTheScreenWhenItMustScroll(t *testing.T) {
	c := &capture{}
	m := settingsModel(t, c, settingsRows(30))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	m.hold.printed = 30 // most of the screen is conversation
	m = reopenSettings(t, m)

	if frame := frameLines(m); frame != 40 {
		t.Fatalf("settings frame is %d lines, want the full height 40", frame)
	}
	if m.hold.printed != 0 {
		t.Errorf("printed = %d after rendering the panel, want 0 — a row above the panel would survive the close", m.hold.printed)
	}
	// The renderer paints on its own tick, so the frame that reaches
	// the terminal may be a later View than the one that healed the
	// counter. Every View while the panel is open must therefore be
	// the same full height — a plan re-derived from printed = 0 would
	// shrink to height-1 here, and that shorter frame is what was
	// painted (raw bytes under script, 2026-09-10).
	if frame := frameLines(m); frame != 40 {
		t.Errorf("second View is %d lines, want 40 — the layout must not move once the panel is open", frame)
	}

	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.hold.printed != 0 {
		t.Errorf("printed = %d after ESC, want 0", m.hold.printed)
	}
	if frame := frameLines(m); frame != 39 {
		t.Errorf("frame after ESC is %d lines, want 39 (input at the bottom of a clean screen)", frame)
	}
}

// The operator's diagnosis: the panel was bottom-aligned like every
// other frame, with a band of blank rows between the conversation and
// the title. A panel that fits sits directly below the printed rows,
// the footer stays on the bottom row, and opening it scrolls nothing —
// so closing it gives the screen back exactly as it was.
func TestSettingsPanelSitsBelowTheConversationWhenItFits(t *testing.T) {
	c := &capture{}
	m := settingsModel(t, c, settingsRows(3))
	// 12 base rows + 3, five section headers and the chrome: about 25
	// lines, which fit under 8 printed rows on a 40-row terminal.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	m.hold.printed = 8
	m = reopenSettings(t, m)

	view := m.View()
	lines := strings.Split(view, "\n")
	if got, want := len(lines), 40-1-8; got != want {
		t.Fatalf("frame is %d lines, want the %d rows below the printed content", got, want)
	}
	// One blank row separates the conversation from the title
	// (operator report, v0.75.1: without it the two ran together).
	if strings.TrimSpace(lines[0]) != "" {
		t.Errorf("first frame row is %q, want a blank separator under the conversation", lines[0])
	}
	if !strings.Contains(lines[1], m.msgs.SettingsTitle) {
		t.Errorf("second frame row is %q, want the panel title", lines[1])
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "" || strings.TrimSpace(lines[len(lines)-2]) == "" {
		t.Errorf("footer is not on the frame's last content row:\n%s", view)
	}
	if m.hold.printed != 8 {
		t.Errorf("printed = %d after rendering a panel that fits, want 8 (nothing scrolled)", m.hold.printed)
	}

	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.hold.printed != 8 {
		t.Errorf("printed = %d after ESC, want 8: the conversation must still be on screen", m.hold.printed)
	}
	if frame := frameLines(m); 8+frame != 39 {
		t.Errorf("after ESC: printed(8) + frame(%d) != 39", frame)
	}
}

// A terminal too short for the panel still gets the short-terminal
// notice rather than a padded frame.
func TestSettingsFrameShortTerminalSaysSo(t *testing.T) {
	c := &capture{}
	m := settingsModel(t, c, settingsRows(3))
	m.width, m.height = 100, minSettingsHeight-1
	short := m.viewContent()
	if strings.Count(short, "\n")+1 >= m.height {
		t.Errorf("short terminal: view has %d lines for height %d — padded instead of saying it is too short", strings.Count(short, "\n")+1, m.height)
	}
	if !strings.Contains(short, m.msgs.SettingsTooShort) {
		t.Error("short terminal notice missing")
	}
}
