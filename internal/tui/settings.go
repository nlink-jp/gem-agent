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
//
// Every line of the managed region has to fit on screen. A view taller
// than the terminal scrolls it, and the inline renderer's accounting
// then drifts by exactly the overflow — which is how closing this panel
// left the input line one row higher than it started (measured: the
// panel ran 2-3 lines over on a 40-line terminal). So the row list is
// budgeted against the real chrome rather than a guessed margin.
func (m Model) settingsView() string {
	rows := m.settings.Rows
	// Below this there is no honest layout: the header, one row, the
	// scope line, the footer and the trailing newline already exceed the
	// screen. Say so rather than overflowing it.
	if m.height > 0 && m.height < minSettingsHeight {
		return m.st.user.Render("settings") + "\n" +
			m.st.hint.Render("  terminal too short — resize, or edit the config file directly")
	}
	// Chrome outside the row list: this function's header and scope
	// lines, the footer viewContent appends, its trailing newline, and
	// one spare row so the panel never sits flush against the bottom.
	budget := m.height - 5
	if budget < 1 {
		budget = 1
	}
	// Reserve the two "… more" markers up front. Over-reserving costs an
	// invisible blank row; under-reserving scrolls the terminal.
	start, end := m.settingsWindow(budget - 2)
	if start == 0 && end == len(rows) {
		start, end = m.settingsWindow(budget)
	}

	var b strings.Builder
	b.WriteString(m.st.user.Render("settings") + m.st.hint.Render(m.msgs.SettingsHint))
	if start > 0 {
		b.WriteString("\n" + m.st.hint.Render(fmt.Sprintf("  … %d more above", start)))
	}
	section := ""
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
		fmt.Fprintf(&b, "\n%s%s%s %s %s", marker, label, pad, value,
			m.st.hint.Render("("+row.Source+")"))
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

// settingsWindow picks the slice of rows to show, keeping the cursor
// inside it and never exceeding budget rendered lines.
//
// The cost of a window is counted exactly rather than approximated,
// because the approximation was wrong in the one direction that matters:
// the first rendered row always prints its section heading, even when it
// shares a section with the row above it, and that single uncounted line
// was enough to push the panel past the bottom of the screen.
func (m Model) settingsWindow(budget int) (int, int) {
	rows := m.settings.Rows
	if len(rows) == 0 {
		return 0, 0
	}
	if budget < 1 {
		budget = 1
	}
	// lines reports how many terminal lines rows[s:e] actually renders.
	lines := func(s, e int) int {
		n := e - s
		for i := s; i < e; i++ {
			if i == s || rows[i].Section != rows[i-1].Section {
				n++ // this row opens a section heading
			}
		}
		return n
	}

	cursor := wrapCursor(m.settingsCursor, len(rows))
	start, end := cursor, cursor+1
	for {
		grew := false
		if end < len(rows) && lines(start, end+1) <= budget {
			end++
			grew = true
		}
		if start > 0 && lines(start-1, end) <= budget {
			start--
			grew = true
		}
		if !grew {
			return start, end
		}
	}
}

// minSettingsHeight is the shortest terminal the panel can render on
// without overflowing: header + one row WITH its section heading (2
// lines — the window can never be smaller) + both "… more" markers +
// scope + footer + trailing newline + the row the pinning reserves.
// At 8 the marker-reserved budget (1) could not hold the 2-line
// minimum window and the header scrolled off (review round 2).
const minSettingsHeight = 9

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
