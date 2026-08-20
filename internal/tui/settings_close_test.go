package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ADR-0028: the settings panel budgets itself to height-1, which is
// taller than the rows left below already-printed content — rendering
// it scrolls the terminal and moves the frame anchor up. The printed
// counter must follow (self-heal), or closing the panel leaves the
// input/footer floating mid-screen (operator report: ESC after
// /settings put the footer at about half height).
func TestSettingsCloseKeepsFooterAtBottom(t *testing.T) {
	c := &capture{}
	rows := settingsRows(30)
	m := settingsModel(t, c, rows) // already in phaseSettings
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	m.hold.printed = 6 // banner rows already on screen

	panel := frameLines(m)
	if panel < 20 {
		t.Fatalf("panel is %d lines — setup too small to overflow", panel)
	}
	if want := 40 - 1 - panel; m.hold.printed != want {
		t.Errorf("printed = %d after the overflow render, want self-healed %d", m.hold.printed, want)
	}

	// ESC closes the panel; the next frame must still END at the bottom
	// row: printed + frame = height-1.
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.phase != phaseInput {
		t.Fatalf("phase after ESC = %v", m.phase)
	}
	frame := frameLines(m)
	if m.hold.printed+frame != 39 {
		t.Errorf("after ESC: printed(%d) + frame(%d) = %d, want 39 — the footer floated off the bottom",
			m.hold.printed, frame, m.hold.printed+frame)
	}
}

// The same invariant holds when the panel fits (small terminal budget
// path): no self-heal needed, no drift introduced.
func TestSettingsCloseNoOverflowNoDrift(t *testing.T) {
	c := &capture{}
	rows := settingsRows(2)
	m := settingsModel(t, c, rows)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	m = next.(Model)
	m.hold.printed = 4

	panel := frameLines(m)
	if m.hold.printed != 4 && panel < 45 {
		t.Errorf("printed changed without overflow: %d (panel %d)", m.hold.printed, panel)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	frame := frameLines(m)
	if m.hold.printed+frame != 49 {
		t.Errorf("after ESC: printed(%d) + frame(%d) != 49", m.hold.printed, frame)
	}
}
