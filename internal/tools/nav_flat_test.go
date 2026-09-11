package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0084 §3: list_tree dirs_only on a directory that has files and no
// subdirectory lists the files instead of "(empty directory)" — the
// false report cost every orientation a list_files round.
func TestListTreeDirsOnlyListsAFlatDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"main.go", "go.mod"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := New(dir, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	tree, _ := reg.Get("list_tree")
	out, err := tree.Run(context.Background(), map[string]any{"dirs_only": true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "empty directory") {
		t.Errorf("a flat directory reads as empty:\n%s", out)
	}
	for _, want := range []string{"no subdirectories; 2 files", "main.go", "go.mod"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, out)
		}
	}
	// A directory with a subdirectory keeps today's shape: the
	// subdirectory with its count, the top-level files hidden.
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err = tree.Run(context.Background(), map[string]any{"dirs_only": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sub/") || strings.Contains(out, "main.go") {
		t.Errorf("with a subdirectory, dirs_only must show it and hide the files:\n%s", out)
	}
	// A symlink among the files keeps its marker (shown, never
	// followed), and the listing is capped like any directory's.
	flat := t.TempDir()
	for i := 0; i < treePerDirCap+3; i++ {
		if err := os.WriteFile(filepath.Join(flat, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("f000.txt", filepath.Join(flat, "a-link")); err != nil {
		t.Fatal(err)
	}
	reg3, _ := New(flat, nil, 0)
	tree3, _ := reg3.Get("list_tree")
	out, err = tree3.Run(context.Background(), map[string]any{"dirs_only": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, fmt.Sprintf("no subdirectories; %d files", treePerDirCap+4)) || !strings.Contains(out, "[+4 more files]") {
		t.Errorf("capped flat listing:\n%s", out)
	}
	if strings.Contains(out, "f052.txt") || !strings.Contains(out, "a-link@\n") {
		t.Errorf("cap or symlink marker wrong:\n%s", out)
	}
	// Truly empty stays empty.
	empty := t.TempDir()
	reg2, _ := New(empty, nil, 0)
	tree2, _ := reg2.Get("list_tree")
	if out, _ := tree2.Run(context.Background(), map[string]any{"dirs_only": true}); !strings.Contains(out, "empty directory") {
		t.Errorf("empty directory = %q", out)
	}
}
