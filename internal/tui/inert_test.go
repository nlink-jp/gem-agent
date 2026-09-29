package tui

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nlink-jp/gem-agent/internal/termimg"
)

// hostile carries one of each control class ADR-0093 removes, around text
// that must survive. The escape comes first, the way a real payload opens.
const hostile = "\x1b]0;PWN\a\x1b]52;c;RVNDUFdO\a\x1b[2J\x1b[3;60HMARKER\rSPOOF\b\x9b\u009d0;x\u009c\u202eEND\x1b]0; tail"

// leaked lists what must never reach the screen from hostile. The runtime's
// own escapes are SGR and Bubble Tea's cursor control, none of which is
// here: the checks are for the hostile string's controls, not for ESC as
// such.
func leaked(out string) []string {
	var found []string
	for _, bad := range []string{"\x1b]", "\x1b[2J", "\x1b[3;60H", "\a", "\r", "\b", "\u009b", "\u009d", "\u009c", "\u202e", "\x9b"} {
		if strings.Contains(out, bad) {
			found = append(found, fmt.Sprintf("%q", bad))
		}
	}
	return found
}

// msgCase is one message type. shows says the message puts its text on the
// screen, so the text must be found there; keep names fields that select a
// branch and must not be overwritten (StreamUpdate.Kind picks "thought").
type msgCase struct {
	msg   tea.Msg
	shows bool
	keep  map[string]bool
}

// notMessages are declared in msgs.go but never reach Update: an answer
// travels back on a channel, and Gate and Screen are the senders.
var notMessages = map[string]bool{"ApprovalAnswer": true, "Gate": true, "Screen": true}

func messageCases() map[string]msgCase {
	return map[string]msgCase{
		"TextDelta":       {msg: TextDelta(""), shows: true},
		"AskRequest":      {msg: AskRequest{Resp: make(chan int, 1)}, shows: true},
		"StreamUpdate":    {msg: StreamUpdate{Kind: "thought"}, shows: true, keep: map[string]bool{"Kind": true}},
		"ToolCall":        {msg: ToolCall{}, shows: true},
		"ToolDone":        {msg: ToolDone{}},
		"TurnDone":        {msg: TurnDone{}, shows: true},
		"AutoApproved":    {msg: AutoApproved{}, shows: true},
		"Attached":        {msg: Attached{}, shows: true},
		"ShellDone":       {msg: ShellDone{}, shows: true},
		"Usage":           {msg: Usage{}},
		"ContextWindow":   {msg: ContextWindow{}},
		"Image":           {msg: Image{}},
		"Output":          {msg: Output{}, shows: true},
		"ApprovalRequest": {msg: ApprovalRequest{Resp: make(chan ApprovalAnswer, 1)}, shows: true},
	}
}

// declaredTypes reads the type names off a source file, so a message added
// there without a case fails here instead of passing unexamined.
func declaredTypes(t *testing.T, file string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.TYPE {
			continue
		}
		for _, s := range g.Specs {
			if ts := s.(*ast.TypeSpec); ts.Name.IsExported() {
				names = append(names, ts.Name.Name)
			}
		}
	}
	return names
}

// fill sets every string in v to hostile, every string slice to one hostile
// element and every error to one whose text is hostile.
func fill(v reflect.Value, keep map[string]bool) {
	if v.Type() == errorType {
		v.Set(reflect.ValueOf(errors.New(hostile)))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(hostile)
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Field(i); f.CanSet() && !keep[v.Type().Field(i).Name] {
				fill(f, nil)
			}
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.String {
			v.Set(reflect.ValueOf([]string{hostile}).Convert(v.Type()))
		}
	}
}

// inertModel is a running turn on the production renderer, dark theme — the
// styling that splits a sequence by accident, so the test cannot pass on
// that accident — with every print captured.
func inertModel(c *capture) Model {
	m := New(Options{Printer: c.printer, Theme: "dark", StartTurn: func(context.Context, string) {}})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	m.phase = phaseRunning
	return m
}

// Every message type in msgs.go, every string in it hostile, through
// Update, the flush and View: no control of the hostile string reaches the
// screen, and its text does — without the second check a message that
// shows nothing would pass without having passed anything through
// (ADR-0093 §5).
func TestEveryMessageReachesTheScreenInert(t *testing.T) {
	cases := messageCases()
	for _, name := range declaredTypes(t, "msgs.go") {
		if notMessages[name] {
			continue
		}
		tc, ok := cases[name]
		if !ok {
			t.Errorf("%s is declared in msgs.go and has no case here: say whether it shows text", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			v := reflect.New(reflect.TypeOf(tc.msg)).Elem()
			v.Set(reflect.ValueOf(tc.msg))
			fill(v, tc.keep)
			c := &capture{}
			m := inertModel(c)
			next, _ := m.Update(v.Interface())
			m = next.(Model)
			screen := m.View()
			if m.phase == phaseRunning {
				next, _ = m.Update(TurnDone{})
				m = next.(Model)
			}
			out := c.all() + "\n" + screen + "\n" + m.View()
			if bad := leaked(out); len(bad) > 0 {
				t.Errorf("%s put %s on the screen:\n%q", name, strings.Join(bad, " "), out)
			}
			if tc.shows && (!strings.Contains(out, "PWN") || !strings.Contains(out, "MARKER")) {
				t.Errorf("%s's text never reached the screen, so the check above saw nothing:\n%q", name, out)
			}
		})
	}
}

// The fill must reach the message: a registry case whose strings stayed
// empty would show nothing hostile and pass. Checked once, apart from the
// screen, so a failure names the fill rather than the TUI.
func TestFillReachesEveryString(t *testing.T) {
	for name, tc := range messageCases() {
		v := reflect.New(reflect.TypeOf(tc.msg)).Elem()
		v.Set(reflect.ValueOf(tc.msg))
		fill(v, tc.keep)
		if tc.shows && !strings.Contains(fmt.Sprintf("%v", v.Interface()), "PWN") {
			t.Errorf("%s: fill left no hostile text in %#v", name, v.Interface())
		}
	}
}

// The rewrite works on a copy: a sender that kept its slice still has what
// it sent, and a cancelled turn is still a cancelled turn.
func TestInertMsgCopiesAndKeepsErrorIdentity(t *testing.T) {
	lines := []string{"a\x1b]0;x\a"}
	got := inertMsg(Output{Lines: lines}).(Output)
	if got.Lines[0] != "a]0;x" || lines[0] != "a\x1b]0;x\a" {
		t.Fatalf("rewritten %q, sender's slice now %q", got.Lines[0], lines[0])
	}
	cause := fmt.Errorf("stopped \x1b[2J: %w", context.Canceled)
	done := inertMsg(TurnDone{Err: cause}).(TurnDone)
	if !errors.Is(done.Err, context.Canceled) || strings.Contains(done.Err.Error(), "\x1b") {
		t.Fatalf("error lost its identity or kept its escape: %v", done.Err)
	}
	img := inertMsg(Image{Data: []byte("\x1b_G"), MIME: "image/png"}).(Image)
	if string(img.Data) != "\x1b_G" {
		t.Fatal("an image's bytes are a picture, not text, and were rewritten")
	}
	if k := inertMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\x1b")}).(tea.KeyMsg); string(k.Runes) != "\x1b" {
		t.Fatal("Bubble Tea's own message was rewritten")
	}
}

// The callbacks that hand text back: the slash handler (/memory quotes what
// save_memory wrote), the skill expander's error, and the banner.
func TestCallbackTextReachesTheScreenInert(t *testing.T) {
	c := &capture{}
	m := New(Options{
		Printer: c.printer,
		Theme:   "dark",
		Banner:  []string{"banner " + hostile},
		Slash:   func(string) (string, bool, bool) { return "slash " + hostile, false, false },
		ExpandInput: func(input string) (string, bool, string) {
			if strings.HasPrefix(input, "/skill") {
				return "", true, "skill " + hostile
			}
			return "", false, ""
		},
	})
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	for cmd != nil {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				if c != nil {
					_, _ = m.Update(c())
				}
			}
			break
		}
		next, cmd = m.Update(msg)
		m = next.(Model)
	}
	for _, line := range []string{"/memory", "/skill x"} {
		m.ta.SetValue(line)
		next, _ = m.submit()
		m = next.(Model)
	}
	out := c.all() + m.View()
	if bad := leaked(out); len(bad) > 0 {
		t.Errorf("callback text put %s on the screen:\n%q", strings.Join(bad, " "), out)
	}
	for _, want := range []string{"banner ]0;PWN", "slash ]0;PWN", "skill ]0;PWN"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q never reached the screen:\n%q", want, out)
		}
	}
}

// The diagram note carries a renderer's error, and a renderer's error can
// quote the source. The source is inert before Split sees it, so the note
// is too — without noteSafe learning anything about terminals.
func TestDiagramNoteIsInert(t *testing.T) {
	quoting := func(src string) (image.Image, string, bool) {
		return nil, "cannot read " + src, true
	}
	for _, opts := range []Options{{}, {Images: termimg.ITerm2, Picture: quoting}} {
		c := &capture{}
		opts.Printer, opts.Theme = c.printer, "dark"
		m := New(opts)
		next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		m = next.(Model)
		m.phase = phaseRunning
		next, _ = m.Update(TextDelta("```mermaid\nsequenceDiagram\n  A->>B: " + hostile + "\n```\n"))
		m = next.(Model)
		_, _ = m.Update(TurnDone{})
		out := c.all()
		if bad := leaked(out); len(bad) > 0 {
			t.Errorf("images=%v: the fence or its note put %s on the screen:\n%q", opts.Images, strings.Join(bad, " "), out)
		}
		// Without images the fence is drawn as box art, labels and all;
		// with the quoting renderer it refuses, and the note carries the
		// source back.
		if !strings.Contains(out, "MARKER") {
			t.Errorf("images=%v: the fence's text never reached the screen:\n%q", opts.Images, out)
		}
		if opts.Picture != nil && !strings.Contains(out, "diagram shown as source: cannot read") {
			t.Errorf("images=%v: the refusal's note is missing:\n%q", opts.Images, out)
		}
	}
}
