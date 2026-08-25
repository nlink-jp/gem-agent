package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/skills"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// ADR-0039 §1: "/mcp reload" and "/skills reload" dispatch to the
// reload closures; any other trailing word is a typo and says so.
func TestSlashReloadSubcommands(t *testing.T) {
	en := uitext.For(uitext.EN)
	reloads := slashReloads{
		mcp:    func() string { return "MCP-RELOADED" },
		skills: func() string { return "SKILLS-RELOADED" },
	}
	out, isErr, _ := slashOutput("/mcp reload", nil, nil, nil, nil, reloads, nil, nil, "", en)
	if isErr || out != "MCP-RELOADED" {
		t.Errorf("/mcp reload: %q isErr=%v", out, isErr)
	}
	out, isErr, _ = slashOutput("/skills reload", nil, nil, nil, nil, reloads, nil, nil, "", en)
	if isErr || out != "SKILLS-RELOADED" {
		t.Errorf("/skills reload: %q isErr=%v", out, isErr)
	}
	if _, isErr, _ = slashOutput("/mcp restart", nil, nil, nil, nil, reloads, nil, nil, "", en); !isErr {
		t.Error("unknown subcommand accepted")
	}
	// Reload unavailable (nil closure) reads as unknown, not a panic.
	if _, isErr, _ = slashOutput("/skills reload", nil, nil, nil, nil, slashReloads{}, nil, nil, "", en); !isErr {
		t.Error("nil reload closure did not refuse")
	}
}

// ADR-0039 §3: load_skill reads the live list through its getter, and
// is registered even when the session starts with zero skills — a
// reload can populate an empty session.
func TestSkillToolSeesReloadedList(t *testing.T) {
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var list []skills.Skill
	if err := registerSkillTool(reg, func() []skills.Skill { return list }); err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get(skills.ToolName)
	if !ok {
		t.Fatal("load_skill not registered for a zero-skill session")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"name": "greeter"}); err == nil {
		t.Error("empty list: unknown skill should error")
	}

	// A skill appears on disk; a reload swaps the list the getter serves.
	global := t.TempDir()
	dir := filepath.Join(global, "greeter")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: greeter\ndescription: says hi\n---\nSay hello politely.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, notes := skills.Discover(global, t.TempDir(), skills.DefaultLimits())
	if len(found) != 1 {
		t.Fatalf("discover: %d skills (notes: %v)", len(found), notes)
	}
	list = found

	out, err := tool.Run(context.Background(), map[string]any{"name": "greeter"})
	if err != nil || !strings.Contains(out, "Say hello politely.") {
		t.Errorf("after reload: %q, %v", out, err)
	}
}
