package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

func declaredNames(defs []llm.ToolDef) map[string]bool {
	out := map[string]bool{}
	for _, d := range defs {
		out[d.Name] = true
	}
	return out
}

// ADR-0083 §1: a tool the predicate refuses is declared to nobody and
// unreachable by name — the same predicate at declaration and dispatch.
func TestUnadvertisedToolIsNeitherDeclaredNorCallable(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "mcp__x__hidden", Args: map[string]any{}}}},
		{Content: "done"},
	}}
	_, reg := newAgent(t, mb, &approveAll{}, 5)
	ran := false
	for _, name := range []string{"mcp__x__hidden", "mcp__x__shown"} {
		n := name
		if err := reg.Register(&tools.Tool{Name: n, Description: "d", Parameters: map[string]any{},
			Run: func(ctx context.Context, args map[string]any) (string, error) { ran = true; return "ran " + n, nil },
		}); err != nil {
			t.Fatal(err)
		}
	}
	log := &capturingLog{}
	a := New(Options{Backend: mb, Registry: reg, Gate: &approveAll{}, System: "s", MaxTurns: 5, Log: log,
		Advertise: func(name string) bool { return name != "mcp__x__hidden" }})

	defs := declaredNames(a.toolDefs)
	if defs["mcp__x__hidden"] || !defs["mcp__x__shown"] {
		t.Errorf("declared = %v: hidden must be absent, shown present", defs)
	}
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("an unadvertised tool ran when called by name")
	}
	// The model is told what an unknown name is told; the record keeps
	// the distinction for the operator.
	result := mb.calls[1][len(mb.calls[1])-1].Content
	if !strings.Contains(result, "unknown tool") {
		t.Errorf("model was told %q, want the unknown-tool wording", result)
	}
	found := false
	for _, k := range log.kinds {
		if k == "tool_not_advertised" {
			found = true
		}
	}
	if !found {
		t.Errorf("no tool_not_advertised record; kinds = %v", log.kinds)
	}
}

// ADR-0083 §3: a tool stages a load under its call id; the loop's hook
// commits it and rebuilds the declarations before the next model call,
// so the round after the load sees the new tool. nil predicate and nil
// hook keep today's behaviour.
func TestAfterToolHookRebuildsDeclarationsForTheNextRound(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "load-1", Name: "loader", Args: map[string]any{}}}},
		{Content: "done"},
	}}
	_, reg := newAgent(t, mb, &approveAll{}, 5)
	loaded := map[string]bool{}
	staged := map[string]string{} // call id -> tool to load
	if err := reg.Register(&tools.Tool{Name: "loader", Description: "d", Parameters: map[string]any{},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			// The tool only stages: the call id comes from the loop.
			staged[tools.CallID(ctx)] = "mcp__x__late"
			return "staged", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&tools.Tool{Name: "mcp__x__late", Description: "d", Parameters: map[string]any{},
		Run: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}
	var hookCalls []string
	a := New(Options{Backend: mb, Registry: reg, Gate: &approveAll{}, System: "s", MaxTurns: 5,
		Advertise: func(name string) bool { return !strings.HasPrefix(name, "mcp__") || loaded[name] },
		AfterTool: func(tc llm.ToolCall, abandoned bool) bool {
			hookCalls = append(hookCalls, tc.ID)
			if abandoned {
				delete(staged, tc.ID)
				return false
			}
			if name, ok := staged[tc.ID]; ok {
				loaded[name] = true
				return true
			}
			return false
		}})
	if declaredNames(a.toolDefs)["mcp__x__late"] {
		t.Fatal("late tool declared before any load")
	}
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(hookCalls) != 1 || hookCalls[0] != "load-1" {
		t.Errorf("hook calls = %v, want [load-1]", hookCalls)
	}
	// The second model call — the round after the load — carried the
	// new declaration.
	if len(mb.toolDefs) < 2 || !declaredNames(mb.toolDefs[1])["mcp__x__late"] {
		t.Errorf("round 2 declarations lack the loaded tool: %v", declaredNames(mb.toolDefs[len(mb.toolDefs)-1]))
	}
	if declaredNames(mb.toolDefs[0])["mcp__x__late"] {
		t.Error("round 1 declared the tool before it was loaded")
	}
}
