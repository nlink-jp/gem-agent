// Package statedir is the one implementation of gem-agent's per-project
// machine-state convention (ADR-0020/0022): a state root under
// ~/.local/state/gem-agent (overridable for test isolation), per-project
// subdirectories named by a lossy path escape, and a .project marker
// that keeps an escape collision from misattributing one project's
// state to another. Memory and session transcripts both build on it.
package statedir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvRoot overrides the state root — the parent of sessions/ and
// memory/. Its purpose is isolation: an E2E or drill pointed at a
// scratch tree cannot see, and therefore cannot delete, the operator's
// real state (ADR-0022 §4).
const EnvRoot = "GEMAGENT_STATE_DIR"

// Root returns the state root: $GEMAGENT_STATE_DIR, or the org-standard
// ~/.local/state/gem-agent.
func Root() (string, error) {
	if dir := os.Getenv(EnvRoot); dir != "" {
		return filepath.Clean(dir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "gem-agent"), nil
}

// EscapeProject flattens an absolute project path into one directory
// name. Lossy — the marker disambiguates collisions.
func EscapeProject(projectDir string) string {
	return strings.ReplaceAll(filepath.Clean(projectDir), string(filepath.Separator), "-")
}

// Marker is the file recording which project a per-project state
// directory belongs to.
const Marker = ".project"

// MarkerMatches verifies dir's marker against projectDir: the path
// escaping is lossy, so two projects can share a directory name, and
// misattributing one project's state to another would be worse than
// not loading it. A missing marker counts as a match (writers create
// it; a hand-made directory has no better claim to check).
func MarkerMatches(dir, projectDir string) (bool, string) {
	data, err := os.ReadFile(filepath.Join(dir, Marker))
	if err != nil {
		return true, ""
	}
	recorded := strings.TrimSpace(string(data))
	if recorded == filepath.Clean(projectDir) {
		return true, ""
	}
	return false, fmt.Sprintf("state dir %s belongs to %s (path-escape collision)", dir, recorded)
}

// EnsureProjectDir creates dir (a per-project state directory) and its
// marker, refusing on a marker collision.
func EnsureProjectDir(dir, projectDir string) error {
	if ok, note := MarkerMatches(dir, projectDir); !ok {
		return fmt.Errorf("%s", note)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	marker := filepath.Join(dir, Marker)
	if _, err := os.Stat(marker); err != nil {
		if err := os.WriteFile(marker, []byte(filepath.Clean(projectDir)+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}
