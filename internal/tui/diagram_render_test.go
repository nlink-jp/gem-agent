package tui

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/diagram"
)

// The Markdown renderer draws mermaid fences in place (ADR-0063): the
// reply shows box art where the model wrote a fence, and the fence
// path is the ONLY diagram path — there is no tool.
func TestGlamourRendererDrawsMermaidFence(t *testing.T) {
	render := newGlamourRenderer(80, "notty")
	out := render("intro\n\n```mermaid\nflowchart TD\n  A[alpha] --> B[beta]\n```\n\noutro\n")
	if strings.Contains(out, "flowchart TD") {
		t.Fatalf("mermaid source shown instead of art:\n%s", out)
	}
	for _, want := range []string{"intro", "outro", "alpha", "beta", "┌"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in rendered reply:\n%s", want, out)
		}
	}
}

// Wide art bypasses glamour and reaches the output verbatim — glamour
// word-wraps code-block lines at spaces, which would shear a drawing
// wider than the wrap width into interleaved fragments (the ADR-0063
// independent-review case). The terminal's own wrap is the accepted
// overflow behavior, and it needs the intact lines.
func TestGlamourRendererKeepsWideArtIntact(t *testing.T) {
	md := "```mermaid\ngraph LR\n  A[Parse config] --> B[Resolve project] --> C[Connect MCP] --> D[Discover skills] --> E[Build prompt] --> F[Start TUI]\n```\n"
	var art string
	for _, seg := range diagram.Split(md) {
		if seg.Art {
			art = seg.Text
		}
	}
	if art == "" {
		t.Fatal("no art segment for the wide chain")
	}
	out := newGlamourRenderer(80, "notty")(md)
	if !strings.Contains(out, art) {
		t.Fatalf("wide art did not pass through verbatim at width 80:\n%s", out)
	}
}

// A supported diagram that cannot be drawn keeps its source and shows
// the reader-facing note; the model is never told (the reader closes
// the loop, ADR-0063 §4).
func TestGlamourRendererKeepsSourceWithNote(t *testing.T) {
	render := newGlamourRenderer(80, "notty")
	out := render("```mermaid\nsequenceDiagram\n  participant U as 操作者\n  U->>A: 質問\n```\n")
	if !strings.Contains(out, "sequenceDiagram") || !strings.Contains(out, "操作者") {
		t.Fatalf("source lost:\n%s", out)
	}
	if !strings.Contains(out, "diagram shown as source") {
		t.Errorf("note missing:\n%s", out)
	}
}
