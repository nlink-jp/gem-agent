package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func slashModel(c *capture) Model {
	commands := []string{"/clear", "/compact", "/help", "/settings", "/skill", "/skills", "/usage"}
	skills := []string{"meeting-notes", "mcp-tactics"}
	return New(Options{
		StartTurn: func(ctx context.Context, input string) { c.turns = append(c.turns, input) },
		Slash:     slashStub,
		Printer:   c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
		CompleteSlash: func(prefix string) []string {
			if rest, ok := strings.CutPrefix(prefix, "/skill "); ok {
				var out []string
				for _, s := range skills {
					if strings.HasPrefix(s, rest) {
						out = append(out, "/skill "+s)
					}
				}
				return out
			}
			var out []string
			for _, cmd := range commands {
				if strings.HasPrefix(cmd, prefix) {
					out = append(out, cmd)
				}
			}
			return out
		},
	})
}

// Tab on a /-prefixed input completes commands the way @-references
// complete: unique match in place, common prefix else, candidates
// listed when Tab cannot advance.
func TestSlashTabCompletion(t *testing.T) {
	c := &capture{}

	// Unique match completes fully.
	m := slashModel(c)
	m.ta.SetValue("/us")
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ta.Value() != "/usage" {
		t.Errorf("unique completion = %q", m.ta.Value())
	}

	// Ambiguous: advance to the common prefix, then list.
	m = slashModel(c)
	m.ta.SetValue("/c")
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ta.Value() != "/c" {
		t.Errorf("common prefix = %q, want /c (clear vs compact)", m.ta.Value())
	}
	if !strings.Contains(c.all(), "/clear") || !strings.Contains(c.all(), "/compact") {
		t.Errorf("candidates not listed: %q", c.all())
	}

	// Second token: skill names complete after "/skill ".
	m = slashModel(c)
	m.ta.SetValue("/skill me")
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ta.Value() != "/skill meeting-notes" {
		t.Errorf("skill completion = %q", m.ta.Value())
	}

	// Plain text and multi-line input stay untouched.
	m = slashModel(c)
	m.ta.SetValue("普通の文章")
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ta.Value() != "普通の文章" {
		t.Errorf("plain text altered: %q", m.ta.Value())
	}
	m.ta.SetValue("/help\n続き")
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ta.Value() != "/help\n続き" {
		t.Errorf("multi-line altered: %q", m.ta.Value())
	}
}
