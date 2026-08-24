package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/diagram"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"golang.org/x/term"
)

// terminalWidth reports the current usable width. It is an ioctl on the
// file descriptor, not an escape-sequence query — the OSC rule that
// TestResizeNeverQueriesTerminal pins is about renderers writing query
// sequences into the session, which would come back as phantom
// keystrokes. This writes nothing.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

// registerDiagramTool adds render_diagram (ADR-0043). Registered under
// the TUI only: the plain REPL and one-shot mode have nowhere to draw,
// and a surface must never advertise what it cannot do.
//
// show delivers the finished art to the terminal as a side effect. The
// model gets a status line, never the art — handing back sixty lines of
// box drawing would double the tokens and invite the model to reproduce
// it badly (ADR-0043 §2).
func registerDiagramTool(registry *tools.Registry, show func(string)) error {
	return registry.Register(&tools.Tool{
		Name: "render_diagram",
		Description: "Draw one mermaid diagram in the user's terminal. Renders " +
			strings.Join(diagram.Supported(), "; ") +
			". The drawing appears in the terminal; you receive only whether it worked. " +
			"If it is refused, the reason says what to change — fix the source and call " +
			"again, and the user sees only the diagram that worked. Never put a mermaid " +
			"fence in a reply instead: a fence is shown as raw source and nothing draws it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{
					"type":        "string",
					"description": "the mermaid source, without the ``` fence",
				},
			},
			"required": []string{"source"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			src, _ := args["source"].(string)
			if strings.TrimSpace(src) == "" {
				return "", fmt.Errorf("source is empty")
			}
			// A fenced block is the mistake this tool exists to replace;
			// accept it rather than refusing on punctuation.
			src = strings.TrimSpace(src)
			if strings.HasPrefix(src, "```") {
				if i := strings.IndexByte(src, '\n'); i >= 0 {
					src = src[i+1:]
				}
				src = strings.TrimSuffix(strings.TrimSpace(src), "```")
			}
			width := terminalWidth()
			cols, rows := diagram.Budget(width)
			art, why, ok := diagram.Render(src, width)
			if !ok {
				return "", fmt.Errorf("not drawn: %s (budget: %d columns x %d lines)", why, cols, rows)
			}
			show(art)
			lines := strings.Split(art, "\n")
			return fmt.Sprintf("drawn in the terminal: %d lines, widest %d columns "+
				"(budget %d x %d). The user can see it; do not repeat it in your reply.",
				len(lines), widestLine(art), cols, rows), nil
		},
	})
}

// widestLine measures the art the way the width guard does.
func widestLine(art string) int {
	w := 0
	for _, l := range strings.Split(art, "\n") {
		if n := len([]rune(l)); n > w {
			w = n
		}
	}
	return w
}

// diagramCols and diagramRows report render_diagram's budget for
// agent_info. Zero outside the TUI, where the tool is not registered.
func diagramCols(useTUI bool) int {
	if !useTUI {
		return 0
	}
	c, _ := diagram.Budget(terminalWidth())
	return c
}

func diagramRows(useTUI bool) int {
	if !useTUI {
		return 0
	}
	_, r := diagram.Budget(terminalWidth())
	return r
}
