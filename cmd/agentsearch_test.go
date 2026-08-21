package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/telemetry"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// scriptBackend replays responses in order, recording what each round
// was offered. The last response repeats when the script runs out, so
// a "model that never answers" is one scripted tool call.
type scriptBackend struct {
	responses []*llm.Response
	calls     int
	systems   []string
	toolDefs  [][]llm.ToolDef
}

func (s *scriptBackend) ChatStream(ctx context.Context, system string, messages []llm.Message, defs []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	s.systems = append(s.systems, system)
	s.toolDefs = append(s.toolDefs, defs)
	i := s.calls
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	s.calls++
	return s.responses[i], nil
}

func searchSetup(t *testing.T, backend llm.Backend) (*tools.Registry, *usageTally, *telemetry.Recording, *[]string) {
	t.Helper()
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "pond.txt"),
		[]byte("a heron stands in the pond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := registerSummarizeTool(reg, &fakeBackend{resp: &llm.Response{Content: "s"}}, "light-model", nil, newUsageTally()); err != nil {
		t.Fatal(err)
	}
	tally := newUsageTally()
	sink, rec := telemetry.NewRecording()
	var seen []string
	if err := registerAgenticSearch(reg, agenticSearchOptions{
		backend:    backend,
		modelName:  "main-model",
		tally:      tally,
		sink:       sink,
		onToolCall: func(tc llm.ToolCall) { seen = append(seen, tc.Name) },
	}); err != nil {
		t.Fatal(err)
	}
	return reg, tally, rec, &seen
}

func runSearch(t *testing.T, reg *tools.Registry, args map[string]any) (string, error) {
	t.Helper()
	tool, ok := reg.Get("agentic_file_search")
	if !ok {
		t.Fatal("agentic_file_search not registered")
	}
	return tool.Run(context.Background(), args)
}

// ADR-0037 §1: the allowlist is read-only by construction, and the
// tools whose exclusion the ADR names stay excluded — recursion above
// all.
func TestSearchAgentAllowlistIsReadOnly(t *testing.T) {
	reg, _, _, _ := searchSetup(t, &scriptBackend{responses: []*llm.Response{{Content: "x"}}})
	for _, name := range searchAgentTools {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("allowlisted tool %q missing from the registry", name)
		}
		if tool.Mutating {
			t.Errorf("allowlisted tool %q is mutating", name)
		}
	}
	for _, banned := range []string{
		"agentic_file_search", "shell_exec", "write_file", "edit_file",
		"web_search", "web_fetch", "ask_user", "load_skill",
		"save_memory", "delete_memory", "view_image", "datetime",
	} {
		for _, name := range searchAgentTools {
			if name == banned {
				t.Errorf("%q must not be in the child allowlist", banned)
			}
		}
	}
	if tool, _ := reg.Get("agentic_file_search"); tool.Mutating {
		t.Error("agentic_file_search must be read-only")
	}
}

// The child loop runs to a report: tools execute, the report comes back
// under a provenance header, usage lands in the tally, and every child
// audit event is labeled (ADR-0037 §4).
func TestSearchAgentRunsChildLoop(t *testing.T) {
	sb := &scriptBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "search_files", Args: map[string]any{"pattern": "heron"}}},
			PromptTokens: 100, OutputTokens: 10},
		{Content: "pond.txt:1 に heron がいます。見つからなかったもの: なし。",
			PromptTokens: 120, OutputTokens: 30},
	}}
	reg, tally, rec, seen := searchSetup(t, sb)

	out, err := runSearch(t, reg, map[string]any{"question": "heron はどこ？"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Report from the file-search agent (main-model, 2 rounds") ||
		!strings.Contains(out, "pond.txt:1") {
		t.Errorf("out = %q", out)
	}
	if len(*seen) != 1 || (*seen)[0] != "search_files" {
		t.Errorf("child tool calls seen = %v", *seen)
	}
	// The child speaks the search contract, not the main system prompt.
	if len(sb.systems) == 0 || sb.systems[0] != searchAgentPrompt {
		t.Error("child loop did not run on searchAgentPrompt")
	}
	// The API surface offered to the child pins the allowlist: no
	// recursion, nothing mutating.
	var offered []string
	for _, d := range sb.toolDefs[0] {
		offered = append(offered, d.Name)
	}
	names := strings.Join(offered, ",")
	if !strings.Contains(names, "search_files") ||
		strings.Contains(names, "agentic_file_search") || strings.Contains(names, "shell_exec") {
		t.Errorf("child was offered: %s", names)
	}

	tally.mu.Lock()
	e := tally.entries["agentic_file_search"]
	tally.mu.Unlock()
	if e == nil || e.calls != 1 || e.prompt != 220 || e.output != 40 || e.model != "main-model" {
		t.Errorf("tally = %+v", e)
	}

	var labeledTool, labeledUsage, labeledTurn bool
	for _, ev := range rec.Events() {
		if ev.Attrs["agent"] != "agentic_file_search" {
			continue
		}
		switch ev.Name {
		case "tool.call":
			labeledTool = ev.Attrs["tool"] == "search_files"
		case "model.usage":
			labeledUsage = true
		case "turn.end":
			labeledTurn = true
		}
	}
	if !labeledTool || !labeledUsage || !labeledTurn {
		t.Errorf("labeled audit events missing: tool=%v usage=%v turn=%v (events: %v)",
			labeledTool, labeledUsage, labeledTurn, rec.Events())
	}
}

func TestSearchAgentValidation(t *testing.T) {
	reg, _, _, _ := searchSetup(t, &scriptBackend{responses: []*llm.Response{{Content: "x"}}})
	if _, err := runSearch(t, reg, map[string]any{}); err == nil {
		t.Error("missing question accepted")
	}
	long := strings.Repeat("あ", searchQuestionCap+1)
	if _, err := runSearch(t, reg, map[string]any{"question": long}); err == nil ||
		!strings.Contains(err.Error(), "limit") {
		t.Errorf("over-cap question: %v", err)
	}
}

// A child that never answers fails at its round cap with the limit
// named — and the spend is tallied anyway: it happened.
func TestSearchAgentMaxTurnsSurfaces(t *testing.T) {
	sb := &scriptBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "list_files", Args: map[string]any{}}},
			PromptTokens: 10, OutputTokens: 1},
	}}
	reg, tally, _, _ := searchSetup(t, sb)
	_, err := runSearch(t, reg, map[string]any{"question": "q"})
	if err == nil || !strings.Contains(err.Error(), "file-search agent") ||
		!strings.Contains(err.Error(), "max turns") {
		t.Errorf("err = %v", err)
	}
	tally.mu.Lock()
	e := tally.entries["agentic_file_search"]
	tally.mu.Unlock()
	if e == nil || e.prompt != 10*searchAgentMaxTurns {
		t.Errorf("failed run not tallied: %+v", e)
	}
}

// A whitespace-only answer is an empty report, reported as such — not
// returned as a blank result that reads like success.
func TestSearchAgentEmptyReport(t *testing.T) {
	reg, _, _, _ := searchSetup(t, &scriptBackend{responses: []*llm.Response{{Content: " "}}})
	if _, err := runSearch(t, reg, map[string]any{"question": "q"}); err == nil ||
		!strings.Contains(err.Error(), "empty report") {
		t.Errorf("err = %v", err)
	}
}
