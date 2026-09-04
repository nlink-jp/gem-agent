package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// boxText flattens a rendered dialog for content checks: newlines,
// padding and box borders removed, so a value the wrap split across
// rows still reads as one string.
func boxText(view string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == ' ' || strings.ContainsRune("│─┃╭╮╰╯┌┐└┘", r) {
			return -1
		}
		return r
	}, ansi.Strip(view))
}

// Review round 4: the approval detail wraps to the box. edit_file's
// one-line detail put `path=` past the terminal edge, where clipLines
// cut it with no marker — the operator approved an edit whose target
// they could not see.
func TestApprovalDetailWrapsSoThePathIsVisible(t *testing.T) {
	m, _ := runningModel(t)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = sized.(Model)
	detail := "new_string=" + strings.Repeat("n", 90) + " old_string=" + strings.Repeat("o", 90) + " path=src/main.go"
	resp := make(chan ApprovalAnswer, 1)
	next, _ := m.Update(ApprovalRequest{Tool: "edit_file", Detail: detail, Purpose: strings.Repeat("why ", 40), Reason: strings.Repeat("because ", 20), Resp: resp})
	m = next.(Model)
	view := m.View()
	if w := maxLineWidth(view); w > m.width-1 {
		t.Errorf("approval view line width %d exceeds %d", w, m.width-1)
	}
	flat := boxText(view)
	if !strings.Contains(flat, "path=src/main.go") {
		t.Errorf("the edit's path is not visible:\n%s", ansi.Strip(view))
	}
	if !strings.Contains(flat, strings.Repeat("o", 90)) {
		t.Errorf("the detail's tail was cut:\n%s", ansi.Strip(view))
	}
	if lines := strings.Count(view, "\n"); m.height > 0 && lines > m.height-1 {
		t.Errorf("approval view is %d lines, taller than %d", lines, m.height-1)
	}
}

// Review round 4: long options are budgeted like the question — the
// title stays visible and what does not fit is disclosed, never
// dropped from the top by the frame clamp.
func TestAskOptionsAreBudgeted(t *testing.T) {
	m, _ := runningModel(t)
	opts := make([]string, 8)
	for i := range opts {
		opts[i] = strings.Repeat("選択肢", 33) // ~100 runes, 3 rows at 80 columns
	}
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = sized.(Model)
	resp := make(chan int, 1)
	next, _ := m.Update(AskRequest{Question: "QUESTION-TITLE?", Options: opts, Resp: resp})
	m = next.(Model)
	view := m.View()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "QUESTION-TITLE?") {
		t.Errorf("the question was pushed off the frame:\n%s", plain)
	}
	if !strings.Contains(plain, "hidden") && !strings.Contains(plain, "非表示") {
		t.Errorf("option rows that do not fit are not disclosed:\n%s", plain)
	}
	if w := maxLineWidth(view); w > m.width-1 {
		t.Errorf("ask view line width %d exceeds %d", w, m.width-1)
	}
}

// Review round 4: the settings panel body reads from the catalog — a
// ja operator got an English panel around a Japanese hint line.
func TestSettingsPanelSpeaksTheCatalogLanguage(t *testing.T) {
	c := &capture{}
	rows := settingsRows(3)
	m := New(Options{
		StartTurn: func(ctx context.Context, input string) {},
		Slash:     slashStub,
		Printer:   c.printer,
		Settings:  &SettingsData{Rows: rows, ProjectDir: "~/work/p"},
		ApplySetting: func(SettingChange) (SettingsData, string) {
			return SettingsData{Rows: rows, ProjectDir: "~/work/p"}, ""
		},
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
		Msgs: uitext.For(uitext.JA),
	})
	next, _ := m.openSettings()
	m = next.(Model)
	m.height, m.width = 24, 120
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "設定") || strings.Contains(plain, "policy changes are saved to") {
		t.Errorf("settings panel is not in the catalog language:\n%s", plain)
	}
}
