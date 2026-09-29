package tui

import (
	"reflect"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nlink-jp/gem-agent/internal/inert"
)

// This file is the TUI's ingress for text from outside it (ADR-0093). Every
// string a message carries, and every string a callback hands back, is made
// inert here — once — before any branch of Update or any renderer reads it.
// Nothing downstream calls internal/inert: glamour's styling, lipgloss's
// colours and termimg's payloads are this runtime's own escapes, written
// after this point, and a renderer that sanitized as well would be a second
// mechanism hiding the absence of the first.

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
