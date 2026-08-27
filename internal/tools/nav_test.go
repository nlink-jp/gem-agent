package tools

import (
	"fmt"
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
		"main.go":             "package main\n\nfunc main() { start() }\n",
		"internal/agent/a.go": "package agent\n\nconst maxRetries = 3\n",
		"internal/agent/b.go": "package agent\n\nfunc start() { _ = maxRetries }\n",
		"docs/readme.md":      "# docs\nmaxRetries is documented here\n",
		".git/config":         "[core]\n",
		".git/objects/junk":   strings.Repeat("x", 100),
		"assets/logo.png":     string(tinyPNG),
		// Ignored content (ADR-0052): a dependency store and a
		// .gitignore'd file, both holding the needle.
		"node_modules/pkg/index.js": "const maxRetries = 1\n",
		".gitignore":                "*.secret\n",
		"notes.secret":              "maxRetries hidden\n",
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
	for _, want := range []string{"internal/", "  agent/", "    a.go", "docs/", "main.go", "link.txt@",
		"node_modules/ [ignored]", "include_ignored=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("tree missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, ".git/") {
		t.Errorf("VCS internals in the tree:\n%s", out)
	}
	// Ignored content is marked, not descended or listed.
	if strings.Contains(out, "pkg") || strings.Contains(out, "notes.secret") {
		t.Errorf("ignored content enumerated:\n%s", out)
	}
}

func TestListTreeIncludeIgnored(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "list_tree", map[string]any{"include_ignored": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"node_modules/", "  pkg/", "    index.js", "notes.secret"} {
		if !strings.Contains(out, want) {
			t.Errorf("include_ignored tree missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[ignored") {
		t.Errorf("include_ignored still reports ignoring:\n%s", out)
	}
}

func TestListTreeDirsOnly(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "list_tree", map[string]any{"dirs_only": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"docs/ (1 files)", "agent/ (2 files)", "node_modules/ [ignored]"} {
		if !strings.Contains(out, want) {
			t.Errorf("dirs_only missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "main.go") || strings.Contains(out, "a.go") {
		t.Errorf("dirs_only listed files:\n%s", out)
	}
}

func TestListTreePerDirElision(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	if err := os.MkdirAll(filepath.Join(dir, "big"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < treePerDirCap+10; i++ {
		if err := os.WriteFile(filepath.Join(dir, "big", filenameN(i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A sibling that sorts after the big directory must still appear.
	if err := os.MkdirAll(filepath.Join(dir, "zlast"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "list_tree", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[+10 more entries]") {
		t.Errorf("per-directory elision was silent:\n%s", out)
	}
	if !strings.Contains(out, "zlast/") {
		t.Errorf("a big directory starved its sibling:\n%s", out)
	}
}

func TestListTreeCapsAreReportedNotSilent(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	// Spread entries across directories so the per-directory cap does
	// not preempt the global one: 20 dirs × 45 files = 920 entries.
	for d := 0; d < 20; d++ {
		sub := filepath.Join(dir, fmt.Sprintf("d%02d", d))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 45; i++ {
			if err := os.WriteFile(filepath.Join(sub, filenameN(i)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
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

func TestListFilesIgnoredMarker(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "list_files", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"node_modules/ [ignored]", "notes.secret [ignored]", "main.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("list_files missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "main.go [ignored]") {
		t.Errorf("list_files marked a live file:\n%s", out)
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
		"matches in 3 files", "[ignored:", "include_ignored=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("search missing %q:\n%s", want, out)
		}
	}
	// Binary skipped by sniff; the out-of-project symlink never followed;
	// ignored content skipped and reported (ADR-0052).
	if strings.Contains(out, "blob.bin") || strings.Contains(out, "SECRET") || strings.Contains(out, "link.txt") {
		t.Errorf("search read what it must skip:\n%s", out)
	}
	if strings.Contains(out, "index.js") || strings.Contains(out, "notes.secret") {
		t.Errorf("search enumerated ignored content:\n%s", out)
	}
}

func TestSearchFilesIncludeIgnored(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "search_files", map[string]any{"pattern": "maxRetries", "include_ignored": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"node_modules/pkg/index.js:1", "notes.secret:1", "matches in 5 files"} {
		if !strings.Contains(out, want) {
			t.Errorf("include_ignored search missing %q:\n%s", want, out)
		}
	}
}

func TestSearchFilesExplicitPathIntoIgnoredArea(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "search_files", map[string]any{"pattern": "maxRetries", "path": "node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "node_modules/pkg/index.js:1") {
		t.Errorf("an explicit path into an ignored area must search it:\n%s", out)
	}
	if !strings.Contains(out, "inside an ignored area") {
		t.Errorf("the disabled layers must be noted:\n%s", out)
	}
}

func TestSearchFilesPerFileCapCountsTheRest(t *testing.T) {
	r := navProject(t)
	many := strings.Repeat("needle\n", 12)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "many.txt"), []byte(many), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "search_files", map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "many.txt:"); got != searchPerFileCap {
		t.Errorf("expected %d shown lines, got %d:\n%s", searchPerFileCap, got, out)
	}
	for _, want := range []string{"… +7 more in this file", "12 matches in 1 files"} {
		if !strings.Contains(out, want) {
			t.Errorf("per-file cap report missing %q:\n%s", want, out)
		}
	}
}

func TestSearchFilesIncludeFilter(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "search_files", map[string]any{"pattern": "maxRetries", "include": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "readme.md") || !strings.Contains(out, "a.go") {
		t.Errorf("include filter failed:\n%s", out)
	}
	if !strings.Contains(out, "filtered by include") {
		t.Errorf("include filtering was silent:\n%s", out)
	}
	if _, err := run(t, r, "search_files", map[string]any{"pattern": "x", "include": "!bad"}); err == nil {
		t.Error("invalid include should error")
	}
}

func TestSearchFilesMode(t *testing.T) {
	r := navProject(t)
	out, err := run(t, r, "search_files", map[string]any{"pattern": "maxRetries", "mode": "files"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "internal/agent/a.go (1 matches)") {
		t.Errorf("files mode missing per-file count:\n%s", out)
	}
	if strings.Contains(out, ":3:") {
		t.Errorf("files mode leaked content lines:\n%s", out)
	}
	if _, err := run(t, r, "search_files", map[string]any{"pattern": "x", "mode": "bogus"}); err == nil {
		t.Error("unknown mode should error")
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

	// The shown-line cap is reported. Spread matches across files so
	// the per-file cap does not absorb them first: 50 files × 5 shown
	// lines = 250 > the 200-line cap.
	for i := 0; i < 50; i++ {
		many := strings.Repeat("needle\n", searchPerFileCap)
		if err := os.WriteFile(filepath.Join(r.ProjectDir(), fmt.Sprintf("m%02d.txt", i)), []byte(many), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err = run(t, r, "search_files", map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stopped at the 200-line cap") {
		t.Errorf("the line cap was silent:\n%s", out)
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

func TestNavWalksSkipGitPointerFile(t *testing.T) {
	// In a submodule checkout .git is a file, not a directory — VCS
	// plumbing is skipped by name either way.
	r := newRegistry(t)
	dir := r.ProjectDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: ../../.git/modules/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("gitdir here too\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "list_tree", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, ".git") {
		t.Errorf("tree lists the submodule .git pointer file:\n%s", out)
	}
	out, err = run(t, r, "search_files", map[string]any{"pattern": "gitdir"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, ".git:") || !strings.Contains(out, "real.txt:1") {
		t.Errorf("search read the submodule .git pointer file:\n%s", out)
	}
}
