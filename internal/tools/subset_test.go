package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0037: Subset builds a positive allowlist — exact membership in
// the given order, and an unknown name is a loud error, never a
// silently smaller registry.
func TestSubset(t *testing.T) {
	reg, err := New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := reg.Subset("read_file", "list_files")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range sub.List() {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "read_file,list_files" {
		t.Errorf("subset = %v", names)
	}
	if _, ok := sub.Get("shell_exec"); ok {
		t.Error("subset leaked a tool it was not given")
	}

	if _, err := reg.Subset("read_file", "no_such_tool"); err == nil ||
		!strings.Contains(err.Error(), "no_such_tool") {
		t.Errorf("unknown name: err = %v, want a loud error naming it", err)
	}
}

// ADR-0039: RemoveByPrefix clears exactly the matching tools (the
// mcp__* adapters on reload) and leaves order and everything else
// intact.
func TestRemoveByPrefix(t *testing.T) {
	reg, err := New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"mcp__a__x", "mcp__b__y"} {
		if err := reg.Register(&Tool{Name: n, Parameters: map[string]any{"type": "object"},
			Run: func(context.Context, map[string]any) (string, error) { return "", nil }}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(reg.List())
	if got := reg.RemoveByPrefix("mcp__"); got != 2 {
		t.Errorf("removed %d, want 2", got)
	}
	if _, ok := reg.Get("mcp__a__x"); ok {
		t.Error("mcp tool survived removal")
	}
	if got := len(reg.List()); got != before-2 {
		t.Errorf("registry has %d tools, want %d", got, before-2)
	}
	if _, ok := reg.Get("read_file"); !ok {
		t.Error("built-in vanished with the prefix removal")
	}
	// Idempotent and re-registerable (the reload cycle).
	if got := reg.RemoveByPrefix("mcp__"); got != 0 {
		t.Errorf("second removal removed %d", got)
	}
	if err := reg.Register(&Tool{Name: "mcp__a__x", Parameters: map[string]any{"type": "object"},
		Run: func(context.Context, map[string]any) (string, error) { return "", nil }}); err != nil {
		t.Errorf("re-register after removal: %v", err)
	}
}

// The subset shares the parent's project confinement: its tools keep
// resolving and refusing paths exactly as the parent's do.
func TestSubsetKeepsConfinement(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := New(dir, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := reg.Subset("read_file")
	if err != nil {
		t.Fatal(err)
	}
	read, _ := sub.Get("read_file")
	if out, err := read.Run(context.Background(), map[string]any{"path": "a.txt"}); err != nil || out != "hello\n" {
		t.Errorf("in-project read: %q, %v", out, err)
	}
	if _, err := read.Run(context.Background(), map[string]any{"path": "../escape"}); err == nil {
		t.Error("subset read escaped the project directory")
	}
}
