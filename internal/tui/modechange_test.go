package tui

// The read-only lift is a mode change, not a tool approval (ADR-0080
// §4). Operator report, 2026-09-09: it rendered as "承認が必要です:
// write_file" with an English reason and the full option row, so what
// was being decided, and what would change, were both unclear — and 'a'
// on that row registers the tool in the session allowlist, granting
// exactly what the ceiling withholds.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

func modeChangeModel(t *testing.T, lang uitext.Lang) (Model, chan ApprovalAnswer) {
	t.Helper()
	m := New(Options{Msgs: uitext.For(lang), Theme: "notty"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	resp := make(chan ApprovalAnswer, 1)
	next, _ = m.Update(ApprovalRequest{Tool: "write_file", Detail: "path=test.txt",
		Reason: "このセッションは read レーンに抑えられています", ModeChange: true, Resp: resp})
	return next.(Model), resp
}

func TestModeChangeDialogSaysWhatIsBeingDecided(t *testing.T) {
	for _, tc := range []struct {
		lang        uitext.Lang
		title, cons string
	}{
		{uitext.JA, "read-only を解除しますか", "はい"},
		{uitext.EN, "Lift read-only?", "Yes turns read-only off"},
	} {
		m, _ := modeChangeModel(t, tc.lang)
		v := m.View()
		if !strings.Contains(v, tc.title) {
			t.Errorf("%v: the dialog does not say what is being decided:\n%s", tc.lang, v)
		}
		// The consequence, not just the question: the operator needs to
		// know the answer changes the session, not one call.
		if !strings.Contains(v, tc.cons) {
			t.Errorf("%v: the dialog does not say what changes:\n%s", tc.lang, v)
		}
		// It is not presented as an ordinary tool approval.
		if strings.Contains(v, "承認が必要です: write_file") || strings.Contains(v, "approval required: write_file") {
			t.Errorf("%v: the mode change reads as a tool approval:\n%s", tc.lang, v)
		}
		// The call stays visible — it is what raised the question.
		if !strings.Contains(v, "path=test.txt") {
			t.Errorf("%v: the triggering call is not shown:\n%s", tc.lang, v)
		}
	}
}

// The two answers a mode change does not have. They are answers about a
// tool, and 'a' would register one.
func TestModeChangeOffersNoAllowlistAnswers(t *testing.T) {
	m, _ := modeChangeModel(t, uitext.JA)
	v := m.View()
	for _, gone := range []string{"このセッション中は許可 (a)", "今後も許可 (p)"} {
		if strings.Contains(v, gone) {
			t.Errorf("the mode change offers %q:\n%s", gone, v)
		}
	}
	for _, kept := range []string{"許可 (y)", "拒否 (n)", "理由を添えて拒否 (N)"} {
		if !strings.Contains(v, kept) {
			t.Errorf("the mode change dropped %q:\n%s", kept, v)
		}
	}
}

// Typing them must not do what the dialog refuses to offer, and the
// selection must not be able to land on them either.
func TestModeChangeIgnoresTheAnswersItDoesNotOffer(t *testing.T) {
	for _, key := range []string{"a", "p"} {
		m, resp := modeChangeModel(t, uitext.JA)
		m.approvalAt = m.approvalAt.Add(-2 * approvalGrace)
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = next.(Model)
		select {
		case a := <-resp:
			t.Errorf("%q answered the mode change with %q", key, string(a.Key))
		default:
		}
		if m.approval == nil {
			t.Errorf("%q dismissed the mode-change dialog", key)
		}
	}

	// Tab wraps over three answers, never onto the two that are gone.
	m, _ := modeChangeModel(t, uitext.JA)
	m.approvalAt = m.approvalAt.Add(-2 * approvalGrace)
	for i := 0; i < 6; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(Model)
		if m.choice > 2 {
			t.Fatalf("selection reached index %d, past the three answers offered", m.choice)
		}
	}
}

// The footer carries the ceiling in force, continuously, for the same
// reason it carries auto mode: it changes what runs (ADR-0080 §1).
func TestFooterShowsTheCeilingInForce(t *testing.T) {
	state := sandbox.Ceiling{}
	m := New(Options{Msgs: uitext.For(uitext.JA), Theme: "notty",
		ReadOnlyState: func() sandbox.Ceiling { return state }})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)

	if strings.Contains(m.View(), "read-only") {
		t.Errorf("both off is the default and says nothing:\n%s", m.View())
	}
	// Armed and waiting: the badge says what will happen.
	state = sandbox.Ceiling{Auto: true}
	if !strings.Contains(m.View(), "🔒auto-read-only") {
		t.Errorf("an armed watcher is not shown:\n%s", m.View())
	}
	// In force: the ceiling's badge, and only it — a watcher is dormant
	// while the ceiling is up, so a second badge would say that one of
	// them does nothing.
	state = sandbox.Ceiling{ReadOnly: true, Auto: true}
	v := m.View()
	if !strings.Contains(v, "🔒read-only") || strings.Contains(v, "🔒auto-read-only") {
		t.Errorf("want the ceiling badge alone:\n%s", v)
	}
	// Not the approval ladder's word (ADR-0080 §1: two indicators, never
	// one blurred one).
	if strings.Contains(v, "🔒auto ") {
		t.Errorf("the ceiling badge borrowed auto mode's word:\n%s", v)
	}
	// It is read live, so a change from anywhere shows without the TUI
	// being told: /readonly, the watcher, and an approved lift all move
	// it, and two of the three are inside the agent. A lift leaves the
	// watcher armed, so the badge falls back to it rather than vanishing.
	state = sandbox.Ceiling{Auto: true}
	if v := m.View(); strings.Contains(v, "🔒read-only ") || !strings.Contains(v, "🔒auto-read-only") {
		t.Errorf("after a lift, want the watcher badge:\n%s", v)
	}
}
