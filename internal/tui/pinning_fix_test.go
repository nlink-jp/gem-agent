package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// ADR-0021: tabs expand to 8-column stops before counting and printing,
// so the pin arithmetic matches what the terminal draws.
func TestExpandTabs(t *testing.T) {
	cases := map[string]string{
		"a\tb":        "a       b", // col 1 → next stop at 8
		"\tx":         "        x",
		"12345678\ty": "12345678        y", // at a stop: full 8 advance
		"no tabs":     "no tabs",
		"日本\tz":       "日本    z", // CJK width 4 → 4 spaces to stop 8
	}
	for in, want := range cases {
		if got := expandTabs(in); got != want {
			t.Errorf("expandTabs(%q) = %q, want %q", in, got, want)
		}
	}
	if got := expandTabs("a\tb\nc\td"); got != "a       b\nc       d" {
		t.Errorf("multi-line expansion = %q", got)
	}
}

// emit's count must equal the physical lines of what it prints — with
// tabs, the pre-fix counter undercounted and the pin drifted.
func TestEmitCountsTabExpandedWidth(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 20, Height: 40})
	m = next.(Model)
	before := m.hold.printed

	// 3 tabs → 24 printed cells minimum: wraps on a 20-col terminal.
	m.emit("\t\t\tend")
	printed := c.printed[len(c.printed)-1]
	wantPhys := (ansi.StringWidth(printed) + 19) / 20
	if got := m.hold.printed - before; got != wantPhys {
		t.Errorf("counted %d physical lines, printed string occupies %d", got, wantPhys)
	}
	if strings.Contains(printed, "\t") {
		t.Error("tabs must not reach the terminal — count and drawing would diverge")
	}
}

// ADR-0021: the managed view never exceeds height-1 lines; the bottom
// (input + footer) survives, the top is dropped.
func TestViewClampedToTerminalHeight(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	m = next.(Model)
	m.ta.SetValue("go")
	m = press(m, enter())
	// A big live stream would make the running view exceed 7 lines.
	next, _ = m.Update(TextDelta(strings.Repeat("stream line\n", 30)))
	m = next.(Model)

	view := m.View()
	if got := len(strings.Split(view, "\n")); got > 7 {
		t.Errorf("view is %d lines on an 8-line terminal — overflow scrolls and desyncs the pin", got)
	}
	if !strings.Contains(view, "ctx") && !strings.Contains(view, "Ctrl+C") {
		t.Errorf("clamp dropped the bottom chrome:\n%s", view)
	}
}

// ADR-0021: a multi-line approval detail is budgeted with an explicit
// hidden-line count — never silently, and the options stay visible.
func TestApprovalDetailBudgeted(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)

	script := "cat <<EOF > f\n" + strings.TrimRight(strings.Repeat("line\n", 20), "\n") + "\nEOF"
	resp := make(chan byte, 1)
	next, _ = m.Update(ApprovalRequest{Tool: "shell_exec", Detail: script, Resp: resp})
	m = next.(Model)

	view := m.View()
	if !strings.Contains(view, "省略") {
		t.Errorf("hidden detail lines not disclosed:\n%s", view)
	}
	if !strings.Contains(view, "許可") {
		t.Errorf("options line missing from the budgeted dialog:\n%s", view)
	}
}
