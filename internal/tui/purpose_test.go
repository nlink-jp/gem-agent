package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

const why = "staging the report so the next call can upload it to Slack"

// The dialog answers the third question (ADR-0047): what runs, why the
// operator is being asked, and why the agent wants it.
func TestApprovalDialogShowsDeclaredPurpose(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	resp := make(chan byte, 1)
	next, _ = m.Update(ApprovalRequest{
		Tool: "shell_exec", Detail: "cp report.csv /tmp/x/", Purpose: why, Resp: resp,
	})
	m = next.(Model)
	v := m.View()
	if !strings.Contains(v, why) {
		t.Errorf("declared purpose missing from the approval dialog:\n%s", v)
	}
	if !strings.Contains(v, "cp report.csv") {
		t.Errorf("arguments missing from the approval dialog:\n%s", v)
	}
}

// "It did not say" and "there is nothing to say" must not look the
// same: a blank line reads as a rendering bug, so the absence is named.
func TestApprovalDialogNamesAMissingPurpose(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	resp := make(chan byte, 1)
	next, _ = m.Update(ApprovalRequest{Tool: "shell_exec", Detail: "cp a b", Resp: resp})
	m = next.(Model)
	if v := m.View(); !strings.Contains(v, m.msgs.PurposeNone) {
		t.Errorf("an undeclared purpose must be shown as such:\n%s", v)
	}
}

// An auto-approved or allowlisted call never opens the dialog, so the
// event line is the only place its purpose can appear — and it must
// still be one write, with the text that streamed before it.
func TestToolCallLineCarriesPurpose(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	next, _ = m.Update(TextDelta("text before the call"))
	m = next.(Model)

	before := len(c.printedCalls())
	m.Update(ToolCall{Name: "shell_exec", Detail: "cp report.csv /tmp/x/", Purpose: why})
	if got := printCalls(c, before); got != 1 {
		t.Errorf("tool event produced %d writes, want 1", got)
	}
	joined := c.all()
	if !strings.Contains(joined, why) {
		t.Errorf("event line lost the declared purpose: %q", joined)
	}
	if !strings.Contains(joined, "text before the call") {
		t.Errorf("streamed text was not flushed with the event: %q", joined)
	}
}

// Read-only tools carry no purpose, and their event lines must not grow
// a second line for a field that will always be empty.
func TestToolCallLineWithoutPurposeStaysOneLine(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	next, _ = m.Update(ToolCall{Name: "read_file", Detail: "path=x.go"})
	_ = next.(Model)
	for _, line := range c.printedCalls() {
		if strings.Contains(line, "↪") {
			t.Errorf("purpose marker rendered for a tool that has none: %q", line)
		}
	}
}
