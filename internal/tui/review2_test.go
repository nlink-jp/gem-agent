package tui

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// Review round 2 regression tests. Each pins one measured or
// code-confirmed defect from the second whole-code review.

// LCP trimmed bytes, not runes: 資料/説明 share the first UTF-8 lead
// byte of their kanji, and the completer wrote a lone 0xE8 into the
// input box (measured).
func TestLongestCommonPrefixIsRuneSafe(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"資料.md", "説明.md"}, ""},
		{[]string{"/skill 議事録", "/skill 調査"}, "/skill "},
		{[]string{"報告書A.md", "報告書B.md"}, "報告書"},
		{[]string{"abc", "abd"}, "ab"},
	}
	for _, c := range cases {
		got := longestCommonPrefix(c.in)
		if got != c.want {
			t.Errorf("LCP(%q) = %q, want %q", c.in, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("LCP(%q) = %q is not valid UTF-8", c.in, got)
		}
	}
}

// ceil(cells/width) assumed perfect packing; a double-width rune that
// straddles the wrap column wraps whole and wastes a cell, so the
// printed-row count under-counted and the pin drifted.
func TestPhysicalRowsModelsWideRuneStraddle(t *testing.T) {
	// width 21: "> " (2 cells) + 30 kanji (60 cells) = 62 cells.
	// Perfect packing says ceil(62/21) = 3; a real terminal fits
	// 2+9 kanji hits 20 cells, the 10th kanji straddles → wraps: the
	// line takes 4 rows.
	line := "> " + strings.Repeat("あ", 30)
	if got := physicalRows(line, 21); got != 4 {
		t.Errorf("physicalRows(wide straddle, 21) = %d, want 4", got)
	}
	// Pure ASCII keeps the old arithmetic.
	if got := physicalRows(strings.Repeat("x", 62), 21); got != 3 {
		t.Errorf("physicalRows(ascii, 21) = %d, want 3", got)
	}
	if got := physicalRows("", 21); got != 1 {
		t.Errorf("physicalRows(empty) = %d, want 1", got)
	}
}

// ADR-0007 promises the operator sees what they type during a run; the
// keys were routed but the box was never rendered.
func TestRunningViewShowsTheInputBox(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.ta.SetValue("run something")
	m = press(m, enter())
	if m.phase != phaseRunning {
		t.Fatal("expected phaseRunning")
	}
	m.ta.SetValue("typed while running")
	if !strings.Contains(m.View(), "typed while running") {
		t.Error("text typed during a turn is not visible in the running view (ADR-0007 §1)")
	}
}

// After Ctrl+C, a gate request already in flight must not open a
// dialog for the dead turn — it is auto-denied.
func TestApprovalAfterInterruptIsAutoDenied(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.ta.SetValue("go")
	m = press(m, enter())
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC}) // interrupt the turn

	resp := make(chan byte, 1)
	next, _ = m.Update(ApprovalRequest{Tool: "write_file", Detail: "x", Resp: resp})
	m = next.(Model)
	if m.phase == phaseApproval {
		t.Fatal("dialog opened for an interrupted turn")
	}
	select {
	case got := <-resp:
		if got != 'n' {
			t.Errorf("auto-answer = %q, want 'n'", got)
		}
	default:
		t.Fatal("gate left blocking after interrupt")
	}

	// The flag clears with the turn: the NEXT turn prompts normally.
	next, _ = m.Update(TurnDone{})
	m = next.(Model)
	m.ta.SetValue("again")
	m = press(m, enter())
	resp2 := make(chan byte, 1)
	next, _ = m.Update(ApprovalRequest{Tool: "write_file", Detail: "x", Resp: resp2})
	m = next.(Model)
	if m.phase != phaseApproval {
		t.Error("next turn's approval must prompt again")
	}
	m = dialogSeen(m)
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	<-resp2
}

// Clean turn completion must cancel the per-turn context — it was only
// ever cancelled on Ctrl+C and leaked one child per turn.
func TestTurnContextReleasedOnCompletion(t *testing.T) {
	c := &capture{}
	var turnCtx context.Context
	m := New(Options{
		Printer: c.printer,
		StartTurn: func(ctx context.Context, input string) {
			turnCtx = ctx
		},
		Theme: "notty",
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.ta.SetValue("go")
	m = press(m, enter())
	if turnCtx == nil {
		t.Fatal("StartTurn not called")
	}
	if turnCtx.Err() != nil {
		t.Fatal("context dead before the turn finished")
	}
	next, _ = m.Update(TurnDone{})
	_ = next.(Model)
	if turnCtx.Err() == nil {
		t.Error("per-turn context not cancelled on clean completion (leak)")
	}
}

// On a short terminal the fixed detail budget overflowed the frame and
// the View clamp cut the TITLE from the top with no disclosure. The
// budget now adapts: title and options always visible, the hidden
// count honest.
func TestShortTerminalApprovalKeepsTitleAndDiscloses(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 14})
	m = next.(Model)
	detail := strings.TrimRight(strings.Repeat("line\n", 20), "\n") // 20 lines
	resp := make(chan byte, 1)
	next, _ = m.Update(ApprovalRequest{Tool: "shell_exec", Detail: detail, Resp: resp})
	m = next.(Model)
	v := m.View()
	if !strings.Contains(v, "shell_exec") {
		t.Errorf("title (the tool being approved) missing at height 14:\n%s", v)
	}
	if !strings.Contains(v, "+18") { // budget = 14-12 = 2 shown, 18 hidden
		t.Errorf("hidden count wrong (want +18):\n%s", v)
	}
	if !strings.Contains(v, m.approvalLabels()[0]) {
		t.Errorf("options line missing:\n%s", v)
	}
}

// clipDetail counted lines AFTER the 600-rune clip, under-reporting
// the hidden count for long details.
func TestClipDetailCountsBeforeRuneClip(t *testing.T) {
	detail := strings.TrimRight(strings.Repeat("0123456789\n", 200), "\n") // 200 lines
	_, hidden := clipDetail(detail, 8)
	if hidden != 192 {
		t.Errorf("hidden = %d, want 192", hidden)
	}
}
