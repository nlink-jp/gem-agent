package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// SettingRow is one line of the settings panel (ADR-0009). Rows the
// operator can change carry Values; the rest are shown with their
// provenance and nothing else, because a menu that offers to change
// something it cannot change is worse than a read-only row.
type SettingRow struct {
	// Section groups rows under a heading.
	Section string
	// Label is the setting's name as the operator knows it — a config
	// key, or a tool name for a policy row.
	Label string
	// Value is the effective value.
	Value string
	// Source says where Value came from: flag, env:VAR, config.toml,
	// policy.toml, the project file, or default.
	Source string
	// Values, when non-empty, are the choices this row cycles through.
	// Empty means the row is read-only.
	Values []string
	// Tool marks a row as an approval-policy row for the named tool.
	Tool string
	// Detail is an optional dim note (why a row is read-only, say).
	Detail string
}

// SettingsData is the panel's content, rebuilt by the caller whenever it
// changes. The TUI holds no configuration of its own: it renders what it
// is given and reports what the operator chose.
type SettingsData struct {
	Rows []SettingRow
	// ProjectDir is shown in the scope indicator.
	ProjectDir string
}

// SettingChange reports one edit. Tool is set for approval-policy rows;
// otherwise Label names the setting. Scope is "global" or "project" and
// only applies to policy rows.
type SettingChange struct {
	Label string
	Tool  string
	Value string
	Scope string
}

// SettingsApplier receives an edit and returns the refreshed panel plus a
// line to print into scrollback (empty for none). Returning the data
// keeps the panel honest: it shows what the caller actually stored, not
// what the keypress asked for.
type SettingsApplier func(SettingChange) (SettingsData, string)

// Scope values for SettingChange.
const (
	ScopeGlobal  = "global"
	ScopeProject = "project"
)

// openSettings enters the panel phase.
func (m Model) openSettings() (tea.Model, tea.Cmd) {
	if m.settingsData == nil {
		return m, m.emit(m.st.errS.Render("✗ settings are unavailable in this mode"))
	}
	data := *m.settingsData
	m.settings = &data
	m.settingsCursor = 0
	m.settingsScope = ScopeGlobal
	m.phase = phaseSettings
	return m, nil
}

// updateSettings handles keys while the panel is open.
func (m Model) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.settings.Rows
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.phase = phaseInput
		m.settings = nil
		m.ta.Focus()
		return m, textarea.Blink
	case tea.KeyUp:
		m.settingsCursor = wrapCursor(m.settingsCursor-1, len(rows))
		return m, nil
	case tea.KeyDown, tea.KeyTab:
		m.settingsCursor = wrapCursor(m.settingsCursor+1, len(rows))
		return m, nil
	case tea.KeyLeft:
		return m.cycleSetting(-1)
	case tea.KeyRight, tea.KeyEnter:
		return m.cycleSetting(1)
	}
	switch strings.ToLower(msg.String()) {
	case "s":
		// Scope applies to policy rows: where the change is written.
		if m.settingsScope == ScopeGlobal {
			m.settingsScope = ScopeProject
		} else {
			m.settingsScope = ScopeGlobal
		}
		return m, nil
	case "q":
		m.phase = phaseInput
		m.settings = nil
		m.ta.Focus()
		return m, textarea.Blink
	}
	return m, nil
}

// cycleSetting moves the highlighted row's value by delta and reports it.
func (m Model) cycleSetting(delta int) (tea.Model, tea.Cmd) {
	if m.settingsCursor >= len(m.settings.Rows) {
		return m, nil
	}
	row := m.settings.Rows[m.settingsCursor]
	if len(row.Values) == 0 {
		// Read-only: say why rather than doing nothing silently.
		detail := row.Detail
		if detail == "" {
			detail = "this setting cannot change mid-session — edit the config file and restart"
		}
		return m, m.emit(m.st.hint.Render("  " + row.Label + ": " + detail))
	}
	next := row.Values[wrapCursor(indexOf(row.Values, row.Value)+delta, len(row.Values))]
	scope := ScopeGlobal
	if row.Tool != "" {
		scope = m.settingsScope
	}
	data, line := m.applySetting(SettingChange{
		Label: row.Label, Tool: row.Tool, Value: next, Scope: scope,
	})
	m.settings = &data
	if line == "" {
		return m, nil
	}
	return m, m.emit(m.st.tool.Render("  " + line))
}

// settingsView renders the panel.
func (m Model) settingsView() string {
	var b strings.Builder
	b.WriteString(m.st.user.Render("settings") + m.st.hint.Render("  ↑↓ 選択 · ←→/Enter 変更 · s 保存先 · Esc 閉じる"))

	section := ""
	// Show a window around the cursor: the panel is inline, so it must
	// not be taller than the terminal (ADR-0003's accounting).
	rows := m.settings.Rows
	height := m.height - 8
	if height < 6 {
		height = 6
	}
	start, end := windowAround(m.settingsCursor, len(rows), height)
	if start > 0 {
		b.WriteString("\n" + m.st.hint.Render(fmt.Sprintf("  … %d more above", start)))
	}
	for i := start; i < end; i++ {
		row := rows[i]
		if row.Section != section {
			section = row.Section
			b.WriteString("\n" + m.st.status.Render(section))
		}
		marker := "  "
		label := row.Label
		if len([]rune(label)) > labelWidth {
			label = string([]rune(label)[:labelWidth-1]) + "…"
		}
		// Pad on the plain text before styling: a styled label is full
		// of escapes, and %-Ns counts bytes.
		pad := strings.Repeat(" ", max(0, labelWidth-len([]rune(label))))
		if i == m.settingsCursor {
			marker = m.st.selected.Render("▶ ")
			label = m.st.selected.Render(label)
		}
		value := row.Value
		if len(row.Values) == 0 {
			value = m.st.hint.Render(value)
		}
		// MCP tool names run past 35 characters, which pushed the value
		// column out of alignment. Pad on the plain text, then style:
		// %-Ns counts bytes, and a styled label is full of escapes.
		b.WriteString(fmt.Sprintf("\n%s%s%s %s %s", marker, label, pad, value,
			m.st.hint.Render("("+row.Source+")")))
	}
	if end < len(rows) {
		b.WriteString("\n" + m.st.hint.Render(fmt.Sprintf("  … %d more below", len(rows)-end)))
	}

	scope := "global (~/.config/gem-agent/policy.toml)"
	if m.settingsScope == ScopeProject {
		scope = "this project only — " + m.settings.ProjectDir
	}
	b.WriteString("\n" + m.st.hint.Render("  policy changes are saved to: ") + m.st.tool.Render(scope))
	return b.String()
}

// labelWidth is the value column's start. Long MCP tool names
// (mcp__urlscan-lookup__get_screenshot) set the floor.
const labelWidth = 40

func wrapCursor(i, n int) int {
	if n == 0 {
		return 0
	}
	return ((i % n) + n) % n
}

func indexOf(values []string, v string) int {
	for i, candidate := range values {
		if candidate == v {
			return i
		}
	}
	return 0
}

// windowAround returns the slice bounds of a scrolling window that keeps
// the cursor visible.
func windowAround(cursor, total, height int) (int, int) {
	if total <= height {
		return 0, total
	}
	start := cursor - height/2
	if start < 0 {
		start = 0
	}
	if start+height > total {
		start = total - height
	}
	return start, start + height
}
