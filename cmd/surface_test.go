package cmd

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tui"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// Without a program every method writes what its branch in runREPL wrote
// to stderr — or nothing, for the ones only the TUI shows.
func TestSurfaceStreamRoute(t *testing.T) {
	var w bytes.Buffer
	s := &surface{w: &w, msgs: uitext.For(uitext.EN)}
	s.notice("n1")
	s.hookNotice("h1")
	s.toolCall("read_file", "path=a", "")
	s.toolCall("read_file", "path=b", "why")
	s.childToolCall("search_files", "q=x")
	s.toolDone("read_file")
	s.childToolDone("search_files")
	s.attached([]string{"attached file: a (3 bytes)"}, []string{"@b: missing"})
	s.autoApproved("write_file", "safe", "Allow")
	s.output([]string{"o1", "o2"})
	s.usage(llm.Usage{Prompt: 1})
	s.stream(llm.StreamEvent{})
	s.contextWindow(10, true)
	want := "[⚠ n1]\n" +
		"[⚠ h1]\n" +
		"\n[tool] read_file path=a\n" +
		"\n[tool] read_file path=b\n[tool] ↪ why\n" +
		"\n[tool ↳] search_files q=x\n" +
		"[📎 attached file: a (3 bytes)]\n[⚠ @b: missing]\n" +
		"[↳ auto-approved (Allow): write_file: safe]\n" +
		"o1\no2\n"
	if w.String() != want {
		t.Fatalf("stream output:\n%q\nwant:\n%q", w.String(), want)
	}
}

// Hook notices — and only they — are collected while a slash command
// runs the hooks, into the caller's slice, interleaved with its own.
func TestSurfaceCollectsHookNoticesOnly(t *testing.T) {
	var w bytes.Buffer
	s := &surface{w: &w}
	notes := []string{"own 1"}
	stop := s.collectHookNotices(&notes)
	s.hookNotice("hook 1")
	notes = append(notes, "own 2")
	s.notice("plain notice")
	s.hookNotice("hook 2")
	stop()
	s.hookNotice("after")
	if want := []string{"own 1", "hook 1", "own 2", "hook 2"}; !reflect.DeepEqual(notes, want) {
		t.Fatalf("collected %q, want %q", notes, want)
	}
	if want := "[⚠ plain notice]\n[⚠ after]\n"; w.String() != want {
		t.Fatalf("shown %q, want %q", w.String(), want)
	}
}

func TestSurfaceAskRoutes(t *testing.T) {
	one := &surface{oneShot: true}
	if _, err := one.ask(context.Background(), "q", []string{"a"}); err == nil || !strings.Contains(err.Error(), "one-shot") {
		t.Fatalf("one-shot ask: %v", err)
	}
	called := false
	plain := &surface{plainAsk: func(context.Context, string, []string) (int, error) { called = true; return 1, nil }}
	if idx, err := plain.ask(context.Background(), "q", []string{"a", "b"}); err != nil || idx != 1 || !called {
		t.Fatalf("plain ask: %d %v %v", idx, err, called)
	}
}

// recorder is a Bubble Tea model that keeps what it is sent.
type recorder struct {
	got chan tea.Msg
}

func (r recorder) Init() tea.Cmd { return nil }
func (r recorder) Update(m tea.Msg) (tea.Model, tea.Cmd) {
	switch v := m.(type) {
	case tui.AskRequest:
		v.Resp <- 1
	}
	r.got <- m
	return r, nil
}
func (r recorder) View() string { return "" }

// With a program every method sends the message its branch sent, and
// nothing reaches the stream.
func TestSurfaceProgramRoute(t *testing.T) {
	rec := recorder{got: make(chan tea.Msg, 32)}
	p := tea.NewProgram(rec, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	done := make(chan struct{})
	go func() { _, _ = p.Run(); close(done) }()
	defer func() { p.Quit(); <-done }()

	var w bytes.Buffer
	s := &surface{w: &w, msgs: uitext.For(uitext.EN)}
	s.setProgram(p)
	s.notice("n")
	s.toolCall("read_file", "d", "p")
	s.childToolCall("search_files", "q")
	s.toolDone("read_file")
	s.childToolDone("search_files")
	s.attached([]string{"l"}, []string{"n"})
	s.autoApproved("write_file", "r", "Allow")
	s.output([]string{"o"})
	s.usage(llm.Usage{Prompt: 1, Output: 2, Cached: 3})
	s.stream(llm.StreamEvent{Kind: "heartbeat"})
	s.contextWindow(10, true)
	idx, err := s.ask(context.Background(), "q", []string{"a", "b"})
	if err != nil || idx != 1 {
		t.Fatalf("ask: %d %v", idx, err)
	}
	want := []tea.Msg{
		tui.Attached{Notes: []string{"n"}},
		tui.ToolCall{Name: "read_file", Detail: "d", Purpose: "p"},
		tui.ToolCall{Name: "↳ search_files", Detail: "q"},
		tui.ToolDone{Name: "read_file"},
		tui.ToolDone{Name: "↳ search_files"},
		tui.Attached{Lines: []string{"l"}, Notes: []string{"n"}},
		tui.AutoApproved{Tool: "write_file", Reason: "r", Tier: "Allow"},
		tui.Output{Lines: []string{"o"}},
		tui.Usage{Prompt: 1, Output: 2, Cached: 3},
		tui.StreamUpdate{Kind: "heartbeat"},
		tui.ContextWindow{Tokens: 10, Assumed: true},
	}
	var got []tea.Msg
	timeout := time.After(5 * time.Second)
	for len(got) < len(want)+1 { // + the ask request
		select {
		case m := <-rec.got:
			switch m.(type) {
			case tui.AskRequest:
				got = append(got, "ask")
			case tea.WindowSizeMsg:
			default:
				got = append(got, m)
			}
		case <-timeout:
			t.Fatalf("got %d of %d messages: %#v", len(got), len(want)+1, got)
		}
	}
	if !reflect.DeepEqual(got[:len(want)], want) || got[len(want)] != "ask" {
		t.Fatalf("messages:\n%#v\nwant:\n%#v then ask", got, want)
	}
	if w.Len() != 0 {
		t.Fatalf("the stream got %q while a program ran", w.String())
	}
}

// After the TUI has ended only the exit's notices reach the stream: a
// turn still running (a wedged tool past the third Ctrl+C) or a late
// window lookup reports nothing after the exit summary, and nothing
// reads stdin for an answer.
func TestSurfaceAfterTheTUIEnds(t *testing.T) {
	var w, term bytes.Buffer
	s := &surface{w: &w, term: &term, msgs: uitext.For(uitext.EN), plainAsk: func(context.Context, string, []string) (int, error) {
		t.Error("an ended session asked on stdin")
		return 0, nil
	}}
	s.endTUI()
	s.notice("late lookup")
	s.toolCall("shell_exec", "cmd=sleep", "why")
	s.childToolCall("search_files", "q")
	s.toolDone("shell_exec")
	s.attached([]string{"a"}, []string{"b"})
	s.autoApproved("write_file", "safe", "Allow")
	s.output([]string{"o"})
	s.usage(llm.Usage{Prompt: 1})
	s.stream(llm.StreamEvent{})
	s.contextWindow(10, true)
	if _, err := s.ask(context.Background(), "q", []string{"a", "b"}); err != errAskDeclined {
		t.Errorf("ask after the end = %v, want declined", err)
	}
	s.hookNotice("a wedged turn's pre_tool_use hook timed out")
	s.sayingExit(func() { s.hookNotice("session_end hook failed") })
	s.backgroundWarning("telemetry export failing (reported once): refused")
	if got, want := w.String(), "[⚠ session_end hook failed]\n"; got != want {
		t.Fatalf("stream after the end:\n%q\nwant:\n%q", got, want)
	}
	if got, want := term.String(), "warning: telemetry export failing (reported once): refused\n"; got != want {
		t.Fatalf("terminal after the end:\n%q\nwant:\n%q", got, want)
	}
}

// A background warning raised before the TUI runs is written to the
// terminal — which the TUI's first clear wipes — and replayed as a note
// when it starts; without a TUI coming, nothing is kept. One the program
// never takes is written when the TUI ends (ADR-0124 addendum, Found 6).
func TestSurfaceBackgroundWarningAcrossTheTUI(t *testing.T) {
	var term bytes.Buffer
	s := &surface{w: io.Discard, term: &term, msgs: uitext.For(uitext.EN)}
	s.backgroundWarning("early")
	if term.String() != "warning: early\n" {
		t.Fatalf("before the TUI: %q", term.String())
	}

	rec := recorder{got: make(chan tea.Msg, 8)}
	p := tea.NewProgram(rec, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	done := make(chan struct{})
	go func() { _, _ = p.Run(); close(done) }()
	s.setProgram(p)
	select {
	case m := <-rec.got:
		note, ok := m.(tui.BackgroundNote)
		if !ok || note.Text != "early" {
			t.Fatalf("replayed %#v, want the early warning as a note", m)
		}
		// The recorder takes it without acknowledging: as a program
		// that ended before reaching it.
	case <-time.After(5 * time.Second):
		t.Fatal("the early warning was not replayed")
	}
	p.Quit()
	<-done
	term.Reset()
	s.endTUI()
	if term.String() != "warning: early\n" {
		t.Fatalf("a note the TUI never took is not written at its end: %q", term.String())
	}

	none := &surface{w: io.Discard, term: io.Discard, msgs: uitext.For(uitext.EN)}
	none.withoutTUI()
	none.backgroundWarning("plain")
	if len(none.pending) != 0 {
		t.Fatalf("kept %q for a TUI that will not run", none.pending)
	}
}

// A note the TUI takes is not written again at its end.
func TestSurfaceBackgroundWarningTakenIsNotRepeated(t *testing.T) {
	var term bytes.Buffer
	s := &surface{w: io.Discard, term: &term, msgs: uitext.For(uitext.EN)}
	rec := recorder{got: make(chan tea.Msg, 8)}
	p := tea.NewProgram(rec, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	done := make(chan struct{})
	go func() { _, _ = p.Run(); close(done) }()
	s.setProgram(p)
	s.backgroundWarning("mid-session")
	select {
	case m := <-rec.got:
		m.(tui.BackgroundNote).Shown()
	case <-time.After(5 * time.Second):
		t.Fatal("the warning was not sent")
	}
	p.Quit()
	<-done
	s.endTUI()
	if term.Len() != 0 {
		t.Fatalf("a note the TUI took was written again: %q", term.String())
	}
}
