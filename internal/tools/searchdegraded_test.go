package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The walk refuses credential material even where the file-read cage
// could not be installed — the degraded path of ADR-0086 §5, as
// amended. A registry built here has no file child, which IS that
// path: before the amendment this test read .env and printed the
// matching line to the model, with no gate anywhere, because the walk
// takes no path argument and so the rule layer never judged it.
func TestSearchFilesRefusesCredentialsWithoutTheCage(t *testing.T) {
	r := newRegistry(t)
	if r.KernelReads() {
		t.Fatal("a bare registry must not claim the kernel adjudicates its reads")
	}
	dir := r.ProjectDir()
	for path, content := range map[string]string{
		"app.go":       "package main\n\nconst hint = \"needle\"\n",
		".env":         "API_TOKEN=needle-in-a-credential-file\n",
		".env.example": "API_TOKEN=needle-placeholder\n",
		"sub/.env":     "OTHER=needle-nested\n",
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, r, "search_files", map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"needle-in-a-credential-file", "needle-nested"} {
		if strings.Contains(out, leaked) {
			t.Errorf("the walk read credential material:\n%s", out)
		}
	}
	// Refused, not hidden: the same footer the caged walk produces.
	for _, want := range []string{"app.go", "[not read:", ".env"} {
		if !strings.Contains(out, want) {
			t.Errorf("result missing %q:\n%s", want, out)
		}
	}
	// The template stays an ordinary file: the re-allow the profile
	// carries is the one the list carries.
	if !strings.Contains(out, "needle-placeholder") {
		t.Errorf(".env.example must stay searchable:\n%s", out)
	}
}
