package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func navProject(t *testing.T) *Registry {
	t.Helper()
	r := newRegistry(t)
	dir := r.ProjectDir()
	for path, content := range map[string]string{
		"main.go":              "package main\n\nfunc main() { start() }\n",
		"internal/agent/a.go":  "package agent\n\nconst maxRetries = 3\n",
		"internal/agent/b.go":  "package agent\n\nfunc start() { _ = maxRetries }\n",
		"docs/readme.md":       "# docs\nmaxRetries is documented here\n",
		".git/config":          "[core]\n",
		".git/objects/junk":    strings.Repeat("x", 100),
		"assets/logo.png":      string(tinyPNG),
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A binary blob and a symlink escaping the project.
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, 'm', 'a', 'x', 'R'}, 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("maxRetries SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestListTreeShowsStructureSkipsVCS(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "list_tree", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"internal/", "  agent/", "    a.go", "docs/", "main.go", "link.txt@"} {
		if !strings.Contains(out, want) {
			t.Errorf("tree missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, ".git") {
		t.Errorf("VCS internals in the tree:\n%s", out)
	}
}

func TestListTreeCapsAreReportedNotSilent(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	for i := 0; i < treeEntryCap+50; i++ {
		if err := os.WriteFile(filepath.Join(dir, filenameN(i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, r, "list_tree", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stopped at") {
		t.Error("the entry cap was silent")
	}

	// Depth cap: reported with the path to descend.
	r2 := newRegistry(t)
	deep := r2.ProjectDir()
	for i := 0; i < 4; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, r2, "list_tree", map[string]any{"depth": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "depth limit") {
		t.Errorf("the depth cap was silent:\n%s", out)
	}
}

func TestListTreeConfined(t *testing.T) {
	r := navProject(t)
	if _, err := run(t, r, "list_tree", map[string]any{"path": "../"}); err == nil {
		t.Fatal("list_tree escaped the project")
	}
}

func TestSearchFilesFindsAcrossTheTree(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "search_files", map[string]any{"pattern": "maxRetries"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"internal/agent/a.go:3", "internal/agent/b.go:3", "docs/readme.md:2",
		"matches in 3 files",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("search missing %q:\n%s", want, out)
		}
	}
	// Binary skipped by sniff; the out-of-project symlink never followed.
	if strings.Contains(out, "blob.bin") || strings.Contains(out, "SECRET") || strings.Contains(out, "link.txt") {
		t.Errorf("search read what it must skip:\n%s", out)
	}
}

func TestSearchFilesRegexAndLiteral(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "search_files", map[string]any{"pattern": `func \w+\(`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "main.go:3") {
		t.Errorf("regex search failed:\n%s", out)
	}
	// literal=true: the same pattern is now an exact string — no matches.
	out, err = run(t, r, "search_files", map[string]any{"pattern": `func \w+\(`, "literal": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no matches") {
		t.Errorf("literal mode still regexed:\n%s", out)
	}
	// An invalid regexp is an error that names the escape hatch.
	if _, err := run(t, r, "search_files", map[string]any{"pattern": "(["}); err == nil || !strings.Contains(err.Error(), "literal") {
		t.Errorf("invalid pattern error should point at literal=true: %v", err)
	}
}

func TestSearchFilesScopedAndCapped(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "search_files", map[string]any{"pattern": "maxRetries", "path": "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "a.go") || !strings.Contains(out, "readme.md") {
		t.Errorf("path scoping failed:\n%s", out)
	}

	// The match cap is reported.
	many := strings.Repeat("needle\n", searchMatchCap+50)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "many.txt"), []byte(many), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, r, "search_files", map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stopped at the 200-match cap") {
		t.Errorf("the match cap was silent:\n%s", out)
	}

	if _, err := run(t, r, "search_files", map[string]any{"pattern": "x", "path": "/etc"}); err == nil {
		t.Fatal("search_files escaped the project")
	}
}

func filenameN(i int) string {
	return "f" + strings.Repeat("0", 4-len(itoa(i))) + itoa(i) + ".txt"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for i > 0 {
		d = append([]byte{byte('0' + i%10)}, d...)
		i /= 10
	}
	return string(d)
}
