package tui

import (
	"reflect"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nlink-jp/gem-agent/internal/inert"
)

// This file is the TUI's ingress for text from outside it (ADR-0093). Every
// string a message carries, and every string a callback hands back, is made
// inert here — once — before any branch of Update or any renderer reads it.
//
// One thing downstream can make a control out of text that has none: a
// transform over it. goldmark decodes the character reference &#27; into a
// real ESC, so the Markdown renderer's output is held to the escapes the
// renderer itself writes (SGR), and the box-art renderer's to none. Both are
// wrapped here too, where the renderer is built, never where its output is
// printed. lipgloss's colours and termimg's payloads are this runtime's own
// and pass untouched.

// ownPkg is this package's import path: only its message types are
// rewritten. Bubble Tea's own messages carry the operator's keys and the
// terminal's reports, not outside text.
var ownPkg = reflect.TypeOf(TextDelta("")).PkgPath()

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// inertMsg returns msg with every string it carries made inert: fields,
// slices, nested structs, and an error's text. It works by reflection so a
// field added to a message later is covered the day it is added. A byte
// slice is not text — an Image's bytes are a picture the view layer encodes
// — and is left alone. msg itself is never modified: the sender may still
// hold the slices it sent.
func inertMsg(msg tea.Msg) tea.Msg {
	if _, ok := msg.(initialSubmit); ok {
		// The operator's argv, the keyboard's trust (ADR-0064), and the
		// text of a turn: rewriting it would change what the model is
		// sent, which is not the display's to change.
		return msg
	}
	v := reflect.ValueOf(msg)
	if !v.IsValid() || v.Type().PkgPath() != ownPkg {
		return msg
	}
	out := reflect.New(v.Type()).Elem()
	out.Set(v)
	inertValue(out)
	return out.Interface()
}

func inertValue(v reflect.Value) {
	if !v.CanSet() {
		return
	}
	if v.Type() == errorType {
		if v.IsNil() {
			return
		}
		err := v.Interface().(error)
		if clean := inert.String(err.Error()); clean != err.Error() {
			v.Set(reflect.ValueOf(error(inertError{err: err, text: clean})))
		}
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(inert.String(v.String()))
	case reflect.Struct:
		for i := range v.NumField() {
			inertValue(v.Field(i))
		}
	case reflect.Slice:
		if v.IsNil() || v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		cp := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(cp, v)
		for i := range cp.Len() {
			inertValue(cp.Index(i))
		}
		v.Set(cp)
	}
}

// inertError shows an error's text made inert and still unwraps to the
// error it came from, so errors.Is(err, context.Canceled) keeps working.
type inertError struct {
	err  error
	text string
}

func (e inertError) Error() string { return e.text }
func (e inertError) Unwrap() error { return e.err }

// walkKinds are the kinds inertValue rewrites or deliberately passes: a
// message field of any other kind (a pointer, a map, an interface other than
// error) would be skipped silently, so the test holds every message type to
// this list rather than trusting the walker to grow with them.
var walkKinds = map[reflect.Kind]bool{
	reflect.String: true, reflect.Struct: true, reflect.Slice: true,
	reflect.Bool: true, reflect.Int: true, reflect.Int64: true, reflect.Uint8: true,
	reflect.Float64: true, reflect.Chan: true,
}

// inertRenderer holds every renderer the factory builds to the escapes that
// renderer writes: SGR for the dark and light styles, nothing at all for the
// plain one (measured, ADR-0093 §2). It is applied once, to the factory, so
// the renders at resize and the note on a refused picture are covered
// without a call at any of them.
//
// An SGR in the output is closed at its end. glamour's plain style prints an
// HTML block as its decoded text with no reset after it, and nothing below
// resets either, so a decoded SGR 8 (conceal) stayed open over the event
// line and the approval dialog that followed — the command included
// (independent review, reproduced). The reset bounds any style to the reply
// it came in.
func inertRenderer(mk func(int) func(string) string, plain bool) func(int) func(string) string {
	return func(width int) func(string) string {
		render := mk(width)
		if plain {
			return func(s string) string { return inert.String(render(s)) }
		}
		return func(s string) string {
			out := inert.Styled(render(s))
			if strings.Contains(out, "\x1b[") {
				out += "\x1b[0m"
			}
			return out
		}
	}
}

// inertArt holds box art to no escapes at all: the art renderer writes
// none, so any it produced came from the text it drew.
func inertArt(s string) string { return inert.String(s) }

// inertStrings wraps a callback that returns candidates: completion names
// files and skills the model or a project can have created.
func inertStrings(f func(string) []string) func(string) []string {
	if f == nil {
		return nil
	}
	return func(prefix string) []string { return inertLines(f(prefix)) }
}

// inertSettings makes the panel's content inert. Tool, Exclude and Group
// are not shown — they key the edit the row sends back — and keep their
// bytes: rewritten, an exclusion of a function whose raw name held a control
// character named a function that does not exist and silently took no hold
// (independent review, reproduced). Label is what the panel shows for them.
func inertSettings(d SettingsData) SettingsData {
	raw := d.Rows
	v := reflect.ValueOf(&d).Elem()
	inertValue(v)
	for i := range d.Rows {
		d.Rows[i].Tool, d.Rows[i].Exclude, d.Rows[i].Group = raw[i].Tool, raw[i].Exclude, raw[i].Group
	}
	return d
}

func inertSettingsPtr(d *SettingsData) *SettingsData {
	if d == nil {
		return nil
	}
	c := inertSettings(*d)
	return &c
}

func inertRefresh(f func() SettingsData) func() SettingsData {
	if f == nil {
		return nil
	}
	return func() SettingsData { return inertSettings(f()) }
}

// inertApply wraps an edit: the refreshed content, and the line it prints,
// which can quote an MCP server's reconnect error.
func inertApply(f SettingsApplier) SettingsApplier {
	if f == nil {
		return nil
	}
	return func(c SettingChange) (SettingsData, string) {
		d, line := f(c)
		return inertSettings(d), inert.String(line)
	}
}

// inertSlash wraps the slash handler: its output quotes state the model can
// have written — /memory lists what save_memory saved.
func inertSlash(h SlashHandler) SlashHandler {
	if h == nil {
		return nil
	}
	return func(cmd string) (string, bool, bool) {
		out, isErr, quit := h(cmd)
		return inert.String(out), isErr, quit
	}
}

// inertExpand wraps the skill expander's error text. The expanded turn is
// not shown — it is sent to the model — and stays as written.
func inertExpand(f func(string) (string, bool, string)) func(string) (string, bool, string) {
	if f == nil {
		return nil
	}
	return func(input string) (string, bool, string) {
		turn, handled, errMsg := f(input)
		return turn, handled, inert.String(errMsg)
	}
}

// inertLine makes one Options string inert: the footer prints the project
// directory's name on every repaint, and a directory is named by whoever
// created it — an archive, or the model.
func inertLine(s string) string { return inert.String(s) }

// inertLines makes each banner line inert: startup notes quote MCP server
// output and paths.
func inertLines(lines []string) []string {
	if lines == nil {
		return nil
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = inert.String(l)
	}
	return out
}
