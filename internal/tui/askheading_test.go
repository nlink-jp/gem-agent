package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func trustAsk(resp chan int) AskRequest {
	return AskRequest{
		Heading:  "project trust — a skill has changed",
		Question: "The skill deploy has changed since you trusted it.\nLook at its files before you answer.",
		Options:  []string{"Trust and load", "Disable for this session", "Not now"},
		Default:  2,
		Resp:     resp,
	}
}

// The runtime's own question (ADR-0119) opens under its own heading and
// not as "question:", which is how the model's questions open; and it
// opens on the option that changes nothing, so a key pressed too soon
// neither trusts nor disables.
func TestAskHeadingAndDefault(t *testing.T) {
	m, _ := runningModel(t)
	resp := make(chan int, 1)
	next, _ := m.Update(trustAsk(resp))
	m = next.(Model)
	v := m.View()
	for _, want := range []string{"project trust — a skill has changed", "The skill deploy has changed", "Look at its files", "  Trust and load", "▶ Not now"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "question:") || strings.Contains(v, "1) ") {
		t.Errorf("the runtime's question reads as the model's, or numbers its options:\n%s", v)
	}
	if strings.Index(v, "project trust") > strings.Index(v, "The skill deploy") {
		t.Errorf("the heading does not open the dialog:\n%s", v)
	}
	// Enter on the opening choice is "not now".
	m.askAt = time.Time{}
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := <-resp; got != 2 {
		t.Errorf("Enter on the opening choice = %d, want 2 (not now)", got)
	}
}

// Every way of not answering gives -1, never a choice: Esc, Ctrl+C, a
// question that arrives after the turn was interrupted, and a turn that
// ends with the dialog still up.
func TestAskHeadedQuestionUnanswered(t *testing.T) {
	for name, key := range map[string]tea.KeyType{"Esc": tea.KeyEsc, "Ctrl+C": tea.KeyCtrlC} {
		t.Run(name, func(t *testing.T) {
			m, _ := runningModel(t)
			resp := make(chan int, 1)
			next, _ := m.Update(trustAsk(resp))
			m = next.(Model)
			m.askAt = time.Time{}
			next, cmd := m.Update(tea.KeyMsg{Type: key})
			if got := <-resp; got != -1 {
				t.Errorf("%s = %d, want -1", name, got)
			}
			// Nothing is printed for it: "denied" would say the operator
			// decided something. The asker's own notice follows.
			if cmd != nil || next.(Model).phase != phaseRunning {
				t.Errorf("%s: a verdict line was emitted for an unanswered question (phase %v)", name, next.(Model).phase)
			}
		})
	}
	t.Run("after an interrupt", func(t *testing.T) {
		m, _ := runningModel(t)
		m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC})
		resp := make(chan int, 1)
		next, _ := m.Update(trustAsk(resp))
		if next.(Model).phase == phaseAsk {
			t.Error("the dialog opened for an interrupted turn")
		}
		if got := <-resp; got != -1 {
			t.Errorf("interrupted = %d, want -1", got)
		}
	})
	t.Run("the turn ends under it", func(t *testing.T) {
		m, _ := runningModel(t)
		resp := make(chan int, 1)
		next, _ := m.Update(trustAsk(resp))
		m = next.(Model)
		m.Update(TurnDone{})
		if got := <-resp; got != -1 {
			t.Errorf("turn done = %d, want -1", got)
		}
	})
}

// A key typed just before the dialog appeared is aimed at the input box,
// not at the question: within the grace nothing answers it.
func TestAskHeadedQuestionIgnoresATypedAheadKey(t *testing.T) {
	m, _ := runningModel(t)
	resp := make(chan int, 1)
	next, _ := m.Update(trustAsk(resp))
	m = next.(Model)
	m = press(m, runeMsg("1"))
	select {
	case got := <-resp:
		t.Fatalf("a typed-ahead key answered the trust question: %d", got)
	default:
	}
	if m.phase != phaseAsk {
		t.Error("the dialog closed without an answer")
	}
}

// A default out of range opens on the first option rather than on none.
func TestAskDefaultOutOfRange(t *testing.T) {
	m, _ := runningModel(t)
	req := trustAsk(make(chan int, 1))
	req.Default = 9
	next, _ := m.Update(req)
	if next.(Model).askChoice != 0 {
		t.Errorf("askChoice = %d", next.(Model).askChoice)
	}
}

// The dialog's text is made inert at the ingress like every message: a
// directory the project named cannot bring an escape sequence with it.
func TestAskHeadingIsInert(t *testing.T) {
	req := trustAsk(nil)
	req.Heading = "trust\x1b[2J"
	req.Question = "dir \x1b]0;title\x07 end"
	got := inertMsg(req).(AskRequest)
	if strings.ContainsAny(got.Heading+got.Question, "\x1b\x07") {
		t.Errorf("not inert: %q / %q", got.Heading, got.Question)
	}
}

// One answer to the runtime's question records persistent trust, so it
// takes keys nobody types by accident. A digit does nothing — not even
// followed by Enter, which is "1" sent as a message. The selection does
// not wrap: Tab and Enter, from the opening choice, stay on it. Only the
// arrows up to "trust" and then Enter give that answer. The model's own
// question keeps its one-press digits and its wrap (TestAskDialog).
func TestAskHeadedQuestionTakesDeliberateKeys(t *testing.T) {
	open := func(t *testing.T) (Model, chan int) {
		m, _ := runningModel(t)
		resp := make(chan int, 1)
		req := trustAsk(resp)
		req.Hint = "arrows select, Enter confirms"
		next, _ := m.Update(req)
		m = next.(Model)
		m.askAt = time.Time{} // past the typed-ahead grace
		return m, resp
	}
	for name, keys := range map[string][]tea.Msg{
		"a digit and Enter":      {runeMsg("1"), tea.KeyMsg{Type: tea.KeyEnter}},
		"Tab and Enter":          {tea.KeyMsg{Type: tea.KeyTab}, tea.KeyMsg{Type: tea.KeyEnter}},
		"Down and Enter":         {tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter}},
		"Right, Right and Enter": {tea.KeyMsg{Type: tea.KeyRight}, tea.KeyMsg{Type: tea.KeyRight}, tea.KeyMsg{Type: tea.KeyEnter}},
		"Enter alone":            {tea.KeyMsg{Type: tea.KeyEnter}},
		"digits, then Enter":     {runeMsg("2"), runeMsg("1"), runeMsg("9"), tea.KeyMsg{Type: tea.KeyEnter}},
	} {
		t.Run(name, func(t *testing.T) {
			m, resp := open(t)
			for _, k := range keys {
				next, _ := m.Update(k)
				m = next.(Model)
			}
			if got := <-resp; got != 2 {
				t.Errorf("%s answered %d, want 2 (not now)", name, got)
			}
		})
	}
	t.Run("a digit alone answers nothing", func(t *testing.T) {
		m, resp := open(t)
		m = press(m, runeMsg("1"))
		select {
		case got := <-resp:
			t.Fatalf("a digit answered the trust question: %d", got)
		default:
		}
		if m.askChoice != 2 || m.phase != phaseAsk {
			t.Errorf("after the digit: choice=%d phase=%v", m.askChoice, m.phase)
		}
		if v := m.View(); !strings.Contains(v, "arrows select, Enter confirms") || strings.Contains(v, "pick directly") {
			t.Errorf("the hint does not say how this question is answered:\n%s", v)
		}
	})
	t.Run("up twice and Enter trusts", func(t *testing.T) {
		m, resp := open(t)
		m = press(m, tea.KeyMsg{Type: tea.KeyUp})
		m = press(m, tea.KeyMsg{Type: tea.KeyUp})
		m = press(m, tea.KeyMsg{Type: tea.KeyUp}) // already at the top: stays
		press(m, tea.KeyMsg{Type: tea.KeyEnter})
		if got := <-resp; got != 0 {
			t.Errorf("up, up, Enter = %d, want 0", got)
		}
	})
	t.Run("up once and Enter disables", func(t *testing.T) {
		m, resp := open(t)
		m = press(m, tea.KeyMsg{Type: tea.KeyShiftTab})
		press(m, tea.KeyMsg{Type: tea.KeyEnter})
		if got := <-resp; got != 1 {
			t.Errorf("up, Enter = %d, want 1", got)
		}
	})
}
