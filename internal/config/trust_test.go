package config

import (
	"os"
	"path/filepath"
	"testing"
)

// ADR-0021: trust entries match as resolved paths — /tmp/x must trust a
// projectDir that arrived as /private/tmp/x, and ~ expands.
func TestTrustsProjectResolvesPaths(t *testing.T) {
	dir := t.TempDir() // typically /var/... which resolves to /private/var/...
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	cfg.Approval.TrustedProjects = []string{dir}
	if !cfg.TrustsProject(resolved) {
		t.Errorf("entry %q did not trust resolved project dir %q", dir, resolved)
	}
	cfg.Approval.TrustedProjects = []string{dir + "/"}
	if !cfg.TrustsProject(resolved) {
		t.Error("a trailing slash defeated the trust entry")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	cfg.Approval.TrustedProjects = []string{"~/some-project"}
	want := filepath.Join(home, "some-project")
	if !cfg.TrustsProject(want) {
		t.Error("~ entry did not expand")
	}
	if cfg.TrustsProject(filepath.Join(home, "other")) {
		t.Error("unrelated path trusted")
	}
}
