package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

const excludedName = "mcp__obsidian__patch_vault_file"

// newExcludeAgent builds an agent whose registry has one name recorded
// as removed by the MCP filter, and nothing registered under it.
func newExcludeAgent(t *testing.T, gate Approver, log SessionLog, hook func(context.Context, string, map[string]any) (bool, string)) *Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/true")
		}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reg.NoteExcluded(excludedName)
	return New(Options{Registry: reg, Gate: gate, Log: log, PreToolHook: hook, MaxTurns: 5,
		Backend: &mockBackend{responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: excludedName, Args: map[string]any{}}}},
			{Content: "ok"},
		}}})
}

// ADR-0077 §5: an excluded tool is not registered, so the executor
// refuses it exactly as it refuses any name it cannot resolve. The model
// is told what it is told for a server that was never configured —
// giving the exclusion its own wording would be "blocked by policy"
// under another name, the state the ADR exists to avoid.
func TestExcludedToolGetsTheUnresolvedNameText(t *testing.T) {
	gate := &denyAll{}
	a := newExcludeAgent(t, gate, nil, nil)
	if _, err := a.Run(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	mb := a.backend.(*mockBackend)
	got := mb.calls[1][2].Content
	if !strings.Contains(got, "unknown tool") || !strings.Contains(got, excludedName) {
		t.Errorf("excluded tool result = %q, want the unresolved-name text naming it", got)
	}
	if strings.Contains(strings.ToLower(got), "polic") || strings.Contains(strings.ToLower(got), "exclud") {
		t.Errorf("the model was told a policy removed the tool: %q", got)
	}
}

// The refusal is the executor's, before the hook and before the gate:
// nothing downstream is consulted about a tool that is not there.
func TestExcludedToolReachesNoHookAndNoGate(t *testing.T) {
	gate := &denyAll{}
	hookCalls := 0
	hook := func(context.Context, string, map[string]any) (bool, string) {
		hookCalls++
		return false, ""
	}
	a := newExcludeAgent(t, gate, nil, hook)
	if _, err := a.Run(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	if hookCalls != 0 {
		t.Errorf("the pre-tool hook ran for an excluded tool (%d calls)", hookCalls)
	}
	if len(gate.asked) != 0 {
		t.Errorf("the gate was asked about an excluded tool: %v", gate.asked)
	}
}

// The distinction the model is not given is kept for the operator.
func TestExcludedToolCallIsRecorded(t *testing.T) {
	log := &recordingLog{}
	a := newExcludeAgent(t, &approveAll{}, log, nil)
	if _, err := a.Run(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range log.kinds {
		if k == "tool_excluded" {
			found = true
		}
	}
	if !found {
		t.Errorf("no tool_excluded record: %v", log.kinds)
	}
}

// A name nobody ever registered is not the operator's doing, so it
// leaves no exclusion record — only the same refusal.
func TestUnrelatedUnknownNameIsNotRecordedAsExcluded(t *testing.T) {
	log := &recordingLog{}
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/true")
		}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a := New(Options{Registry: reg, Gate: &approveAll{}, Log: log, MaxTurns: 5,
		Backend: &mockBackend{responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "no_such_tool", Args: map[string]any{}}}},
			{Content: "ok"},
		}}})
	if _, err := a.Run(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	for _, k := range log.kinds {
		if k == "tool_excluded" {
			t.Errorf("an unregistered name was recorded as excluded: %v", log.kinds)
		}
	}
}

// A Subset shares its parent's exclusions: a delegated child must not
// reach what the operator removed from the session (ADR-0037 + ADR-0077).
func TestSubsetInheritsExclusions(t *testing.T) {
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/true")
		}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reg.NoteExcluded(excludedName)
	sub, err := reg.Subset("list_files")
	if err != nil {
		t.Fatal(err)
	}
	if !sub.Excluded(excludedName) {
		t.Error("a Subset did not inherit the parent's exclusions")
	}
}

// A server excluded whole is never started, so there are no function
// names to record one by one — the prefix carries it, or a call naming
// one of its tools reads in the transcript as a tool that never existed
// rather than as the operator's own doing (pre-release review).
func TestWholeServerExclusionIsRecordedByPrefix(t *testing.T) {
	log := &recordingLog{}
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/true")
		}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reg.NoteExcludedPrefix("mcp__chrome-pilot__")
	a := New(Options{Registry: reg, Gate: &approveAll{}, Log: log, MaxTurns: 5,
		Backend: &mockBackend{responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "mcp__chrome-pilot__navigate", Args: map[string]any{}}}},
			{Content: "ok"},
		}}})
	if _, err := a.Run(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range log.kinds {
		if k == "tool_excluded" {
			found = true
		}
	}
	if !found {
		t.Errorf("no tool_excluded record for an excluded server's tool: %v", log.kinds)
	}
	if reg.Excluded("mcp__other__navigate") {
		t.Error("the prefix matched a different server")
	}
}

// Resuming with a changed set: the history holds calls to tools that are
// no longer declared. The turn must run, and a fresh call to the removed
// name must be refused and recorded like any other (ADR-0077's
// Consequences promised this test).
func TestResumeWithAChangedSet(t *testing.T) {
	log := &recordingLog{}
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/true")
		}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reg.NoteExcluded(excludedName)
	a := New(Options{Registry: reg, Gate: &approveAll{}, Log: log, MaxTurns: 5,
		Backend: &mockBackend{responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{{ID: "c2", Name: excludedName, Args: map[string]any{}}}},
			{Content: "ok"},
		}}})
	// What a resume restores: a completed call to a tool that is gone now.
	a.history = []llm.Message{
		{Role: llm.RoleUser, Content: "earlier"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: excludedName}}},
		{Role: llm.RoleTool, ToolCallID: "c1", ToolName: excludedName, Content: "earlier result"},
	}

	if _, err := a.Run(context.Background(), "again", nil); err != nil {
		t.Fatalf("a resumed session with a changed tool set failed to run: %v", err)
	}
	mb := a.backend.(*mockBackend)
	if !strings.Contains(mb.calls[1][len(mb.calls[1])-1].Content, "unknown tool") {
		t.Errorf("the removed tool was not refused: %q", mb.calls[1][len(mb.calls[1])-1].Content)
	}
}
