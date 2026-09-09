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

	"github.com/charmbracelet/x/ansi"

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
		{uitext.JA, "read-only を解除しますか", "このセッションの残りで"},
		{uitext.EN, "Lift read-only?", "rest of this session"},
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
		// And every line stays inside the box: a line rendered straight
		// from the catalog set the box's width and pushed the border off
		// the screen (operator report).
		for _, row := range strings.Split(v, "\n") {
			if w := ansi.StringWidth(row); w > 120 {
				t.Errorf("%v: a dialog row is %d columns wide, past the terminal:\n%s", tc.lang, w, row)
			}
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

// The footer carries both settings in one badge (ADR-0080 §1): the
// padlock is the ceiling right now, the word is the mode. A watcher
// armed mid-session had no lasting surface before this, and an armed
// watcher that had not fired hid the ceiling it was about to move.
func TestFooterBadgeCarriesBothSettings(t *testing.T) {
	state := sandbox.Ceiling{}
	m := New(Options{Msgs: uitext.For(uitext.JA), Theme: "notty",
		ReadOnlyState: func() sandbox.Ceiling { return state }})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)

	for _, tc := range []struct {
		state sandbox.Ceiling
		want  string // "" = no badge at all
	}{
		// Nothing armed, nothing in force: the default says nothing.
		{sandbox.Ceiling{}, ""},
		// The operator moves the ceiling themselves here, so only the
		// state that constrains shows.
		{sandbox.Ceiling{ReadOnly: true}, "🔒read-only"},
		// Armed: the ceiling moves without them, so its current value is
		// on screen either way — the word alone when nothing is in
		// force. The padlock's presence is the signal, because 🔓 and 🔒
		// read as the same glyph in a terminal.
		{sandbox.Ceiling{Auto: true}, "auto-read-only"},
		{sandbox.Ceiling{Auto: true, ReadOnly: true}, "🔒auto-read-only"},
	} {
		state = tc.state
		v := m.View()
		if tc.want == "" {
			if strings.Contains(v, "read-only") {
				t.Errorf("%+v: the default printed a badge:\n%s", tc.state, v)
			}
			continue
		}
		if !strings.Contains(v, tc.want) {
			t.Errorf("%+v: want %q in the footer:\n%s", tc.state, tc.want, v)
		}
		// One badge, not two.
		if strings.Count(v, "read-only") != 1 {
			t.Errorf("%+v: want exactly one badge:\n%s", tc.state, v)
		}
	}

	// Not the approval ladder's word (ADR-0080 §1: two indicators, never
	// one blurred one).
	state = sandbox.Ceiling{Auto: true, ReadOnly: true}
	if strings.Contains(m.View(), "🔒auto ") {
		t.Errorf("the badge borrowed auto mode's word:\n%s", m.View())
	}
	// The badge's style is not asserted here and cannot be: the test
	// themes render without colour. It is structural instead — the
	// footer has one Render call for the badge, with no branch, so
	// there is no dim variant to drift back to (independent review,
	// 2026-09-09: the earlier assertion here re-checked the text and
	// called it a styling test).

	// Read live: /readonly, the watcher and an approved lift all move
	// the state, and two of the three are inside the agent. After a lift
	// the padlock is gone — its presence is what says "in force".
	state = sandbox.Ceiling{Auto: true}
	v := m.View()
	if !strings.Contains(v, "auto-read-only") {
		t.Errorf("the footer did not follow a lift:\n%s", v)
	}
	if strings.Contains(v, "🔒") || strings.Contains(v, "🔓") {
		t.Errorf("a padlock survived the lift:\n%s", v)
	}
}
