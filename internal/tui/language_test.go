package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// TestChromeFollowsLanguage pins ADR-0029 on the TUI side: the model
// renders the catalog it was built with, and nil defaults to English
// (plain REPL fallback, tests).
func TestChromeFollowsLanguage(t *testing.T) {
	ja := New(Options{Msgs: uitext.For(uitext.JA), Theme: "notty"})
	if ja.ta.Placeholder != uitext.For(uitext.JA).Placeholder {
		t.Errorf("ja placeholder = %q", ja.ta.Placeholder)
	}
	if got := ja.approvalLabels()[0]; got != "許可 (y)" {
		t.Errorf("ja first approval label = %q", got)
	}

	def := New(Options{Theme: "notty"})
	if def.msgs == nil || def.approvalLabels()[0] != "allow (y)" {
		t.Errorf("nil Msgs must default to English, got %v", def.approvalLabels())
	}
}

// TestApprovalDialogRendersInJapanese drives a real approval through
// Update and checks the dialog chrome is Japanese end to end.
func TestApprovalDialogRendersInJapanese(t *testing.T) {
	m := New(Options{Msgs: uitext.For(uitext.JA), Theme: "notty"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	resp := make(chan ApprovalAnswer, 1)
	next, _ = m.Update(ApprovalRequest{Tool: "write_file", Detail: "x", Resp: resp})
	m = next.(Model)
	v := m.View()
	for _, want := range []string{"承認が必要です: write_file", "許可 (y)", "拒否 (n)", "Esc 拒否"} {
		if !strings.Contains(v, want) {
			t.Errorf("ja dialog missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "approval required") {
		t.Errorf("ja dialog leaks English chrome:\n%s", v)
	}
}
