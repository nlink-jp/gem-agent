package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Review round 3 regressions.

func maxLineWidth(s string) int {
	w := 0
	for _, l := range strings.Split(s, "\n") {
		if n := ansi.StringWidth(l); n > w {
			w = n
		}
	}
	return w
}

// Side-call thoughts (risk/progress review) are not the main model's
// and must not render as such while a tool runs.
func TestSideCallThoughtsIgnoredDuringTool(t *testing.T) {
	m, _ := runningModel(t)
	next, _ := m.Update(ToolCall{Name: "shell_exec", Detail: "make"})
	m = next.(Model)
	next, _ = m.Update(StreamUpdate{Kind: "thought", Thought: "evaluating the risk of make"})
	m = next.(Model)
	if m.thoughtTail != "" {
		t.Errorf("side-call thought rendered as the model's: %q", m.thoughtTail)
	}
	next, _ = m.Update(ToolDone{Name: "shell_exec"})
	m = next.(Model)
	next, _ = m.Update(StreamUpdate{Kind: "thought", Thought: "now answering"})
	m = next.(Model)
	if m.thoughtTail != "now answering" {
		t.Errorf("main thought after the tool returned not shown: %q", m.thoughtTail)
	}
}

// toolRunning never leaks across turns: an interrupted tool must not
// disarm the next turn's stall detector.
func TestToolRunningResetsOnTurnBoundary(t *testing.T) {
	m, _ := runningModel(t)
	next, _ := m.Update(ToolCall{Name: "shell_exec", Detail: "make"})
	m = next.(Model)
	next, _ = m.Update(TurnDone{})
	m = next.(Model)
	if m.toolRunning {
		t.Error("toolRunning survived TurnDone")
	}
	m.ta.SetValue("again")
	m = press(m, enter())
	m.lastChunk = time.Now().Add(-(stallSeconds + 5) * time.Second)
	if !strings.Contains(m.View(), "stalled") {
		t.Error("next turn's stall detector disarmed by the previous turn's tool")
	}
}

// A `!command` has no model stream: elapsed time yes, stall warning no.
func TestShellCommandNeverStallWarns(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.shell = func(context.Context, string) {}
	m.ta.SetValue("!sleep 60")
	m = press(m, enter())
	m.lastChunk = time.Now().Add(-(stallSeconds + 5) * time.Second)
	if strings.Contains(m.View(), "stalled") {
		t.Errorf("stall warning for a shell command with no connection:\n%s", m.View())
	}
}

// `/riskbook learn` drives dialogs on this event loop and produces no
// model stream while it waits for the human. The suppression flag used
// to be set BEFORE beginTurnStats, which resets it — so the pass
// warned about a connection it was not using (ADR-0056).
func TestRiskbookLearnNeverStallWarns(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.riskbook = func(context.Context) {}
	m.ta.SetValue("/riskbook learn")
	m = press(m, enter())
	if !m.toolRunning {
		t.Fatal("the learning pass did not suppress the stall detector")
	}
	m.lastChunk = time.Now().Add(-(stallSeconds + 5) * time.Second)
	if strings.Contains(m.View(), "stalled") {
		t.Errorf("stall warning while the learning pass waits on a human:\n%s", m.View())
	}
}

// The ask dialog wraps long questions and options to the width — the
// operator can read the whole question — and discloses hidden lines.
func TestAskViewWrapsAndDiscloses(t *testing.T) {
	m, _ := runningModel(t)
	q := strings.Repeat("長い質問文で幅を超える。", 12) // ~144 CJK runes = 288 cells
	resp := make(chan int, 1)
	next, _ := m.Update(AskRequest{Question: q, Options: []string{strings.Repeat("選択肢", 40), "b"}, Resp: resp})
	m = next.(Model)
	view := m.View()
	if w := maxLineWidth(view); w > m.width-1 {
		t.Errorf("ask view line width %d exceeds %d", w, m.width-1)
	}
	// Every rune of the question survives the wrap (nothing truncated).
	plain := ansi.Strip(view)
	for _, part := range []string{"長い質問文で幅を超える。", "選択肢選択肢"} {
		if !strings.Contains(plain, part) {
			t.Errorf("wrapped view lost %q", part)
		}
	}
	// The box pads and borders every line; strip all of that before
	// counting the question's repeats across the wrapped lines.
	joined := strings.Map(func(r rune) rune {
		if r == '\n' || r == ' ' || strings.ContainsRune("│─┃╭╮╰╯┌┐└┘", r) {
			return -1
		}
		return r
	}, plain)
	if strings.Count(joined, "長い質問文で幅を超える。") < 12 {
		t.Errorf("question body truncated: %d of 12 repeats visible", strings.Count(joined, "長い質問文で幅を超える。"))
	}
	// A question taller than the budget is disclosed, never dropped silently.
	tall := strings.Repeat("行。", 40*30)
	next, _ = m.Update(AskRequest{Question: tall, Options: []string{"a", "b"}, Resp: make(chan int, 1)})
	m = next.(Model)
	if !strings.Contains(ansi.Strip(m.View()), "hidden") && !strings.Contains(ansi.Strip(m.View()), "非表示") {
		t.Error("hidden question lines not disclosed")
	}
}

// thoughtView shows the FRESHEST text — the last words are visible.
func TestThoughtViewShowsFreshest(t *testing.T) {
	m, _ := runningModel(t)
	next, _ := m.Update(StreamUpdate{Kind: "thought", Thought: strings.Repeat("old words ", 60) + "FRESHEST-END"})
	m = next.(Model)
	if !strings.Contains(ansi.Strip(m.View()), "FRESHEST-END") {
		t.Errorf("newest thought text not visible:\n%s", ansi.Strip(m.View()))
	}
	if w := maxLineWidth(m.View()); w > m.width-1 {
		t.Errorf("thought line width %d exceeds %d", w, m.width-1)
	}
}

// The live region expands tabs before the width clip: a tab-indented
// code line must never soft-wrap the managed view.
func TestLiveViewExpandsTabs(t *testing.T) {
	m, _ := runningModel(t)
	next, _ := m.Update(TextDelta("\t\t\tfmt.Println(\"" + strings.Repeat("a", 150) + "\")\n"))
	m = next.(Model)
	view := m.View()
	if strings.Contains(view, "\t") {
		t.Error("raw tab reached the managed view")
	}
	if w := maxLineWidth(view); w > m.width-1 {
		t.Errorf("live line width %d exceeds %d", w, m.width-1)
	}
}

// releaseTurn never strands the approval gate either.
func TestReleaseTurnDrainsApproval(t *testing.T) {
	m, _ := runningModel(t)
	resp := make(chan byte, 1)
	next, _ := m.Update(ApprovalRequest{Tool: "shell_exec", Detail: "x", Resp: resp})
	m = next.(Model)
	next, _ = m.Update(TurnDone{})
	m = next.(Model)
	select {
	case b := <-resp:
		if b != 'n' {
			t.Errorf("drained approval answered %q, want n", b)
		}
	default:
		t.Error("approval gate stranded on TurnDone")
	}
}

// An AskRequest with no options cannot panic the UI.
func TestAskZeroOptionsNoPanic(t *testing.T) {
	m, _ := runningModel(t)
	resp := make(chan int, 1)
	next, _ := m.Update(AskRequest{Question: "q", Resp: resp})
	m = next.(Model)
	m.askAt = time.Time{} // skip the typed-ahead grace
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = next.(Model)
	select {
	case v := <-resp:
		if v != -1 {
			t.Errorf("answer = %d, want decline", v)
		}
	default:
		t.Error("zero-option ask not declined")
	}
}
