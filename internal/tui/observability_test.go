package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ADR-0033 regression tests: the running status distinguishes a
// thinking model, a stalled stream, and a backoff retry — the three
// states that used to render identically as "thinking…".

func runningModel(t *testing.T) (Model, *capture) {
	t.Helper()
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.ta.SetValue("go")
	m = press(m, enter())
	if m.phase != phaseRunning {
		t.Fatal("expected phaseRunning")
	}
	return m, c
}

func TestHeartbeatShowsElapsedAndChunks(t *testing.T) {
	m, _ := runningModel(t)
	for range 3 {
		next, _ := m.Update(StreamUpdate{Kind: "chunk"})
		m = next.(Model)
	}
	v := m.View()
	if !strings.Contains(v, "3 chunks") {
		t.Errorf("heartbeat missing chunk count:\n%s", v)
	}
	if !strings.Contains(v, "last 0s") {
		t.Errorf("heartbeat missing last-chunk age:\n%s", v)
	}
}

func TestStallWarningAfterSilence(t *testing.T) {
	m, _ := runningModel(t)
	// No chunk for stallSeconds: rewind the clock instead of sleeping.
	m.turnStart = time.Now().Add(-25 * time.Second)
	v := m.View()
	if !strings.Contains(v, "stalled") {
		t.Errorf("no stall warning after %ds silence:\n%s", stallSeconds, v)
	}
	// A chunk arriving clears the stall reading.
	next, _ := m.Update(StreamUpdate{Kind: "chunk"})
	m = next.(Model)
	if strings.Contains(m.View(), "stalled") {
		t.Error("stall warning survived fresh data")
	}
}

func TestRetryIsVisible(t *testing.T) {
	m, _ := runningModel(t)
	next, _ := m.Update(StreamUpdate{Kind: "retry", Attempt: 2, Max: 3, Cause: "429", DelayMS: 4000})
	m = next.(Model)
	v := m.View()
	if !strings.Contains(v, "retry 2/3 (429)") || !strings.Contains(v, "4s") {
		t.Errorf("retry not visible:\n%s", v)
	}
	// Data flowing again clears the retry line.
	next, _ = m.Update(StreamUpdate{Kind: "chunk"})
	m = next.(Model)
	if strings.Contains(m.View(), "retry 2/3") {
		t.Error("retry line survived fresh data")
	}
}

func TestThoughtTailDisplaysAndYieldsToAnswer(t *testing.T) {
	m, _ := runningModel(t)
	next, _ := m.Update(StreamUpdate{Kind: "thought", Thought: "Reading the config loader first"})
	m = next.(Model)
	if !strings.Contains(m.View(), "Reading the config loader first") {
		t.Errorf("thought not displayed:\n%s", m.View())
	}
	// The visible answer supersedes the thought tail.
	next, _ = m.Update(TextDelta("The answer is"))
	m = next.(Model)
	if strings.Contains(m.View(), "Reading the config loader") {
		t.Error("thought tail survived the start of the answer")
	}
	// A tool call also ends the round's thoughts.
	next, _ = m.Update(StreamUpdate{Kind: "thought", Thought: "checking files"})
	m = next.(Model)
	next, _ = m.Update(ToolCall{Name: "read_file", Detail: "x"})
	m = next.(Model)
	if m.thoughtTail != "" {
		t.Error("thought tail survived a tool call")
	}
	// TurnDone clears everything.
	next, _ = m.Update(StreamUpdate{Kind: "thought", Thought: "wrapping up"})
	m = next.(Model)
	next, _ = m.Update(TurnDone{})
	m = next.(Model)
	if m.thoughtTail != "" || !m.turnStart.IsZero() {
		t.Error("observability state survived TurnDone")
	}
}

// Thoughts are ephemeral display (ADR-0033 §3): nothing reaches the
// scrollback printer.
func TestThoughtsNeverReachScrollback(t *testing.T) {
	m, c := runningModel(t)
	next, _ := m.Update(StreamUpdate{Kind: "thought", Thought: "secret internal reasoning"})
	m = next.(Model)
	next, _ = m.Update(TurnDone{})
	_ = next.(Model)
	for _, line := range c.printed {
		if strings.Contains(line, "secret internal reasoning") {
			t.Fatalf("thought text reached the scrollback: %q", line)
		}
	}
}
