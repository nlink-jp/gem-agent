// Package statedir is the one implementation of gem-agent's per-project
// machine-state convention (ADR-0020/0022): a state root under
// ~/.local/state/gem-agent (overridable for test isolation), per-project
// subdirectories named by a lossy path escape, and a .project marker
// that keeps an escape collision from misattributing one project's
// state to another. Memory and session transcripts both build on it.
package statedir

import (
	"github.com/nlink-jp/gem-agent/internal/bounded"

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
// not loading it. A missing OR empty marker counts as a match (writers
// create it; a hand-made directory — or one left by a crashed pre-fix
// writer — has no better claim to check).
func MarkerMatches(dir, projectDir string) (bool, string) {
	data, err := readMarker(filepath.Join(dir, Marker))
	if err != nil {
		return true, ""
	}
	recorded := strings.TrimSpace(string(data))
	if recorded == "" || recorded == filepath.Clean(projectDir) {
		return true, ""
	}
	return false, fmt.Sprintf("state dir %s belongs to %s (path-escape collision)", dir, recorded)
}

// EnsureProjectDir creates dir (a per-project state directory) and its
// marker, refusing on a marker collision.
//
// The marker lands by temp-file + rename, never os.WriteFile: WriteFile
// is create-empty-then-write, and a parallel launch reading between
// those two steps saw an empty marker and refused the SAME project as a
// path-escape collision (measured: ~half of 16-way simultaneous starts
// tripped it). Rename is atomic — a reader sees no marker or the whole
// marker, both of which mean "proceed". Two genuinely different
// projects racing to claim a fresh dir at once can still both pass the
// pre-write check; the marker is a best-effort disambiguator for the
// lossy escape, not a lock, and the second rename simply wins.
func EnsureProjectDir(dir, projectDir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if ok, note := MarkerMatches(dir, projectDir); !ok {
		return fmt.Errorf("%s", note)
	}
	marker := filepath.Join(dir, Marker)
	if data, err := readMarker(marker); err == nil {
		if recorded := strings.TrimSpace(string(data)); recorded != "" {
			// Re-VERIFY, never assume: between the MarkerMatches read
			// above and this one, a colliding project's first launch
			// can have renamed ITS marker in — "non-empty therefore
			// ours" concluded ownership from a marker never compared
			// (review round 2).
			if recorded != filepath.Clean(projectDir) {
				return fmt.Errorf("state dir %s belongs to %s (path-escape collision)", dir, recorded)
			}
			return nil
		}
	}
	tmp, err := os.CreateTemp(dir, Marker+".tmp-")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(filepath.Clean(projectDir) + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), marker); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// markerCap bounds a marker read: a marker holds one path.
const markerCap = 64 * 1024

func readMarker(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, _, err := bounded.ReadAll(f, markerCap)
	return data, err
}
