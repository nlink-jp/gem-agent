package cmd

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/memory"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

func memTestRegistry(t *testing.T, projectDir string) *tools.Registry {
	t.Helper()
	registry, err := tools.New(projectDir, func(ctx context.Context, command string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/echo")
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestMemoryToolsRoundtrip(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()
	registry := memTestRegistry(t, project)
	if err := registerMemoryTools(registry, base, project); err != nil {
		t.Fatal(err)
	}

	save, ok := registry.Get("save_memory")
	if !ok {
		t.Fatal("save_memory not registered")
	}
	if !save.Mutating {
		t.Error("save_memory must be Mutating — the write is the approval boundary (ADR-0020)")
	}
	out, err := save.Run(context.Background(), map[string]any{
		"scope": "project", "name": "staging-host", "content": "The staging host is quokka-7.",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.Contains(out, "saved") || !strings.Contains(out, "next session") {
		t.Errorf("save output %q must state the name and when it takes effect", out)
	}

	// Update path reports "updated", not "saved".
	out, err = save.Run(context.Background(), map[string]any{
		"scope": "project", "name": "staging-host", "content": "The staging host is quokka-8.",
	})
	if err != nil || !strings.Contains(out, "updated") {
		t.Errorf("second save: out=%q err=%v, want an update report", out, err)
	}

	mems, _ := memory.Load(base, project, memory.DefaultLimits())
	if len(mems) != 1 || !strings.Contains(mems[0].Content, "quokka-8") {
		t.Fatalf("Load after tool save = %+v", mems)
	}

	del, ok := registry.Get("delete_memory")
	if !ok {
		t.Fatal("delete_memory not registered")
	}
	if !del.Mutating {
		t.Error("delete_memory must be Mutating")
	}
	if _, err := del.Run(context.Background(), map[string]any{"scope": "project", "name": "staging-host"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if mems, _ := memory.Load(base, project, memory.DefaultLimits()); len(mems) != 0 {
		t.Errorf("memory survived delete_memory: %+v", mems)
	}
}

func TestMemoryToolErrors(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()
	registry := memTestRegistry(t, project)
	if err := registerMemoryTools(registry, base, project); err != nil {
		t.Fatal(err)
	}
	save, _ := registry.Get("save_memory")
	del, _ := registry.Get("delete_memory")

	// Client-side validation: the error must teach, not just refuse.
	if _, err := save.Run(context.Background(), map[string]any{
		"scope": "session", "name": "x", "content": "y",
	}); err == nil || !strings.Contains(err.Error(), `"global"`) {
		t.Errorf("unknown scope error %v must name the valid scopes", err)
	}
	if _, err := save.Run(context.Background(), map[string]any{
		"scope": "global", "name": "../escape", "content": "y",
	}); err == nil || !strings.Contains(err.Error(), "slug") {
		t.Errorf("invalid name error %v must describe the expected format", err)
	}
	if _, err := del.Run(context.Background(), map[string]any{
		"scope": "global", "name": "nonexistent",
	}); err == nil || !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("missing-memory delete error %v must name the miss", err)
	}
}

func TestMemoryListing(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()

	empty := memoryListing(base, project)
	if !strings.Contains(empty, "no memories saved") || !strings.Contains(empty, base) {
		t.Errorf("empty listing %q must say so and show where memories would live", empty)
	}

	if _, _, err := memory.Save(base, project, memory.ScopeGlobal, "operator-lang",
		"The operator writes in Japanese.\nSecond line.", memory.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	got := memoryListing(base, project)
	for _, want := range []string{"[global]", "operator-lang", "The operator writes in Japanese.", "next session"} {
		if !strings.Contains(got, want) {
			t.Errorf("listing missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Second line") {
		t.Errorf("listing must preview only the first line:\n%s", got)
	}

	if got := memoryListing("", project); !strings.Contains(got, "disabled") {
		t.Errorf("disabled listing = %q", got)
	}
}

func TestMemoryPromptWiring(t *testing.T) {
	// The system prompt gains the memory section with the recorded fact;
	// the framing must mark it as background, not instructions.
	sec := memory.PromptSection([]memory.Memory{
		{Scope: memory.ScopeGlobal, Name: "operator-lang", Content: "The operator writes in Japanese."},
	})
	full := buildSystemPrompt("/tmp/p", "") + sec
	for _, want := range []string{"### memory global/operator-lang", "not instructions"} {
		if !strings.Contains(full, want) {
			t.Errorf("assembled prompt missing %q", want)
		}
	}
}
