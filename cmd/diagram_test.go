package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/tools"
)

// The tool draws as a side effect and returns a status — never the art.
// Handing sixty lines of box drawing back to the model would double the
// tokens and invite it to reproduce them badly (ADR-0043 §2).
func TestRenderDiagramReturnsStatusNotArt(t *testing.T) {
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var shown []string
	if err := registerDiagramTool(reg, func(art string) { shown = append(shown, art) }); err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get("render_diagram")
	if !ok {
		t.Fatal("render_diagram not registered")
	}
	if tool.Mutating {
		t.Error("render_diagram is Mutating — it writes nothing and must not be gated")
	}
	out, err := tool.Run(context.Background(), map[string]any{
		"source": "flowchart TD\n  A[start] --> B[end]\n",
	})
	if err != nil {
		t.Fatalf("not drawn: %v", err)
	}
	if strings.Contains(out, "┌") {
		t.Errorf("the art came back to the model:\n%s", out)
	}
	if !strings.Contains(out, "drawn in the terminal") || !strings.Contains(out, "budget") {
		t.Errorf("status does not report the outcome and the budget: %q", out)
	}
	if len(shown) != 1 || !strings.Contains(shown[0], "┌") {
		t.Errorf("art was not shown to the terminal: %v", shown)
	}
}

// A refusal reaches the model as an error carrying the reason and the
// budget, so it can correct and call again before the user sees
// anything (ADR-0043 §1).
func TestRenderDiagramRefusalCarriesTheReason(t *testing.T) {
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerDiagramTool(reg, func(string) {}); err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Get("render_diagram")
	_, err = tool.Run(context.Background(), map[string]any{
		"source": "stateDiagram-v2\n  [*] --> A\n",
	})
	if err == nil {
		t.Fatal("an unsupported type was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"not drawn", "budget", "columns"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal is missing %q: %q", want, msg)
		}
	}
}

// A model that wraps the source in a fence is accepted rather than
// refused on punctuation — the fence is the habit this tool replaces.
func TestRenderDiagramAcceptsAFencedSource(t *testing.T) {
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	shown := 0
	if err := registerDiagramTool(reg, func(string) { shown++ }); err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Get("render_diagram")
	if _, err := tool.Run(context.Background(), map[string]any{
		"source": "```mermaid\nflowchart TD\n  A[a] --> B[b]\n```",
	}); err != nil {
		t.Fatalf("fenced source refused: %v", err)
	}
	if shown != 1 {
		t.Error("fenced source did not draw")
	}
}
