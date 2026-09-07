package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// groupRows is one server with two functions, plus an ungrouped row —
// the shape the panel gets from ADR-0077's two levels.
func groupRows() []SettingRow {
	return []SettingRow{
		{Section: "mcp tools", Label: "obsidian", Value: "on", Values: []string{"on", "off"},
			Exclude: "obsidian", Group: "mcp:obsidian", Collapsible: true, Source: "default"},
		{Section: "mcp tools", Label: "get_vault_file", Value: "on", Values: []string{"on", "off"},
			Exclude: "obsidian/get_vault_file", Group: "mcp:obsidian", Child: true, Source: "default"},
		{Section: "mcp tools", Label: "patch_vault_file", Value: "on", Values: []string{"on", "off"},
			Exclude: "obsidian/patch_vault_file", Group: "mcp:obsidian", Child: true, Source: "default"},
		{Section: "limits", Label: "agent.max_turns", Value: "50", Source: "default"},
	}
}

func groupModel(t *testing.T, applied *[]SettingChange) Model {
	t.Helper()
	rows := groupRows()
	m := New(Options{
		StartTurn: func(ctx context.Context, input string) {},
		Slash:     slashStub,
		Printer:   (&capture{}).printer,
		Settings:  &SettingsData{Rows: rows, ProjectDir: "~/work/p"},
		ApplySetting: func(ch SettingChange) (SettingsData, string) {
			*applied = append(*applied, ch)
			return SettingsData{Rows: rows, ProjectDir: "~/work/p"}, ""
		},
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
	})
	next, _ := m.openSettings()
	m = next.(Model)
	next, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return next.(Model)
}

// ADR-0077 §3: groups open closed. Flat, this list is hundreds of rows
// on a machine with a full server list, which is the state the two
// levels exist to end.
func TestGroupsOpenClosed(t *testing.T) {
	var applied []SettingChange
	m := groupModel(t, &applied)

	visible := m.visibleRows()
	if len(visible) != 2 {
		t.Fatalf("visible rows = %d, want the server and the ungrouped row", len(visible))
	}
	for _, r := range visible {
		if r.Child {
			t.Errorf("a child row is visible with its group closed: %+v", r)
		}
	}
	if !strings.Contains(m.settingsView(), "▸ obsidian") {
		t.Error("a closed group does not show it is closed")
	}
}

// Enter opens and closes a group, and the cursor stays on the row the
// operator was looking at.
func TestEnterOpensAndClosesAGroup(t *testing.T) {
	var applied []SettingChange
	m := groupModel(t, &applied)

	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.visibleRows()) != 4 {
		t.Fatalf("after Enter, visible = %d, want every row", len(m.visibleRows()))
	}
	if got := m.visibleRows()[m.settingsCursor].Label; got != "obsidian" {
		t.Errorf("cursor moved off the group parent to %q", got)
	}
	if !strings.Contains(m.settingsView(), "▾ obsidian") {
		t.Error("an open group does not show it is open")
	}
	if len(applied) != 0 {
		t.Errorf("opening a group edited something: %+v", applied)
	}

	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.visibleRows()) != 2 {
		t.Errorf("Enter did not close the group again: %d rows", len(m.visibleRows()))
	}
}

// Cycling a child row reports the entry it writes, not just its label:
// two servers can offer functions of the same name.
func TestCyclingAChildReportsItsEntry(t *testing.T) {
	var applied []SettingChange
	m := groupModel(t, &applied)
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter}) // open the group
	m = press(m, tea.KeyMsg{Type: tea.KeyDown})  // get_vault_file
	if got := m.visibleRows()[m.settingsCursor].Label; got != "get_vault_file" {
		t.Fatalf("cursor is on %q, not the first child", got)
	}
	press(m, tea.KeyMsg{Type: tea.KeyRight})

	if len(applied) != 1 {
		t.Fatalf("applied = %+v, want one change", applied)
	}
	if applied[0].Exclude != "obsidian/get_vault_file" {
		t.Errorf("change carried %q, want the full entry", applied[0].Exclude)
	}
	if applied[0].Value != "off" {
		t.Errorf("value = %q, want the next one in the cycle", applied[0].Value)
	}
}

// The server row is both a group parent and a setting: Enter opens it,
// ←→ turns it off. A row that did both on one key would make one of them
// unreachable.
func TestServerRowIsBothAGroupAndASetting(t *testing.T) {
	var applied []SettingChange
	m := groupModel(t, &applied)
	m = press(m, tea.KeyMsg{Type: tea.KeyRight})

	if len(applied) != 1 || applied[0].Exclude != "obsidian" {
		t.Fatalf("applied = %+v, want the server entry", applied)
	}
	if len(m.visibleRows()) != 2 {
		t.Error("changing the server's value also opened its group")
	}
}

// Children are indented, so two levels are visible as two levels.
func TestChildRowsAreIndented(t *testing.T) {
	var applied []SettingChange
	m := groupModel(t, &applied)
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	view := m.settingsView()
	if !strings.Contains(view, "    get_vault_file") {
		t.Errorf("child rows are not indented:\n%s", view)
	}
}
