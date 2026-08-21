package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// ADR-0039 §3: after a reload changes the registry, RefreshTools
// re-caches the declarations, and SetSystem swaps the system prompt —
// both visible to the model on the very next round.
func TestRefreshToolsAndSetSystem(t *testing.T) {
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := &autoBackend{responses: []*llm.Response{{Content: "one"}, {Content: "two"}}}
	var defsSeen [][]llm.ToolDef
	var systemsSeen []string
	backend := backendFunc(func(ctx context.Context, system string, msgs []llm.Message, defs []llm.ToolDef, onText func(string)) (*llm.Response, error) {
		defsSeen = append(defsSeen, defs)
		systemsSeen = append(systemsSeen, system)
		return b.ChatStream(ctx, system, msgs, defs, onText)
	})
	a := New(Options{Backend: backend, Registry: reg, Gate: &recordingGate{}, System: "sys-old", MaxTurns: 3})

	if _, err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}

	// A "reload" registers a new tool and swaps the prompt.
	if err := reg.Register(&tools.Tool{Name: "mcp__srv__fresh", Description: "d",
		Parameters: map[string]any{"type": "object"}, Mutating: true,
		Run: func(context.Context, map[string]any) (string, error) { return "", nil }}); err != nil {
		t.Fatal(err)
	}
	a.RefreshTools()
	a.SetSystem("sys-new")

	if _, err := a.Run(context.Background(), "again", nil); err != nil {
		t.Fatal(err)
	}
	last := defsSeen[len(defsSeen)-1]
	var names []string
	for _, d := range last {
		names = append(names, d.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "mcp__srv__fresh") {
		t.Errorf("refreshed declarations missing the new tool: %v", names)
	}
	if systemsSeen[len(systemsSeen)-1] != "sys-new" {
		t.Errorf("system prompt not swapped: %q", systemsSeen[len(systemsSeen)-1])
	}
	if systemsSeen[0] != "sys-old" {
		t.Errorf("first turn system = %q", systemsSeen[0])
	}
}

// backendFunc adapts a function to llm.Backend.
type backendFunc func(context.Context, string, []llm.Message, []llm.ToolDef, func(string)) (*llm.Response, error)

func (f backendFunc) ChatStream(ctx context.Context, system string, msgs []llm.Message, defs []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	return f(ctx, system, msgs, defs, onText)
}
