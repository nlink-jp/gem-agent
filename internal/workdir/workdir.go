// Package workdir owns the per-session work directory: the one place a
// session puts files that are not part of the project — MCP results too
// large to hold in context, binary content a server returned, scratch a
// shell command produced.
//
// It exists because the alternative places are both wrong. The project
// directory is the operator's source tree: writing intermediates there
// makes a git working copy dirty with things nobody asked for. /tmp is
// one flat namespace shared with every other process on the machine,
// with no session isolation and no way for a resumed session to find
// what its earlier self produced.
//
// The directory is keyed by session id and lives under the same state
// root as transcripts and memory (ADR-0020/0022), so GEMAGENT_STATE_DIR
// isolates it for tests and drills exactly as it isolates those, and a
// resume lands back in the same directory.
//
// Nothing here deletes on its own initiative. The files are the point —
// a report the model produced, a screenshot it was told to look at —
// and an agent that tidies its own output away between runs is worse
// than one that accumulates. Retention is the operator's call, and
// Remove is its instrument: invoked only by the explicit workdirs
// command (ADR-0059), behind a confirmation this package never gives
// itself.
package workdir

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// EnvVar is the environment variable naming the session work directory.
// It is exported into the process environment at startup, which is what
// puts it in front of everything the session spawns: shell_exec's child
// (and the `!` direct shell), every MCP server (internal/mcp inherits
// os.Environ), and every hook. `${GEMAGENT_WORK_DIR}` in an mcp.json
// args entry expands to it, so a server can be pointed at the session's
// own directory without gem-agent knowing anything about that server.
const EnvVar = "GEMAGENT_WORK_DIR"

// ProjectEnvVar names the project directory for children (ADR-0071
// §3): the third of the three facts a Claude Code child also sees
// (its CLAUDE_PROJECT_DIR), beside the session id and the work
// directory.
const ProjectEnvVar = "GEMAGENT_PROJECT_DIR"

// Ensure creates and returns the work directory for one session of one
// project. sessionID must be a single path segment — it names a
// directory, and a session id carrying a separator would place the
// session's files outside the tree that is supposed to hold them.
func Ensure(projectDir, sessionID string) (string, error) {
	dir, err := Path(projectDir, sessionID)
	if err != nil {
		return "", err
	}
	// The project directory carries the marker (a lossy path escape can
	// collide), so claim it before creating anything beneath it.
	if err := statedir.EnsureProjectDir(filepath.Dir(filepath.Dir(dir)), projectDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create work directory: %w", err)
	}
	return dir, nil
}

// Path returns where a session's work directory belongs, without
// creating it.
func Path(projectDir, sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("work directory needs a session id")
	}
	if sessionID != filepath.Base(sessionID) || sessionID == "." || sessionID == ".." {
		return "", fmt.Errorf("session id %q is not a single path segment", sessionID)
	}
	root, err := statedir.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, statedir.EscapeProject(projectDir), "work", sessionID), nil
}

// Info describes one session's work directory for the listing and the
// startup report.
type Info struct {
	ID       string
	Path     string
	Files    int
	Bytes    int64
	LastUsed time.Time
}

// List returns a project's session work directories, newest first,
// excluding excludeSessionID when non-empty (the session doing the
// asking). Read-only: sizes are summed, nothing is touched.
func List(projectDir, excludeSessionID string) ([]Info, error) {
	root, err := statedir.Root()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(root, statedir.EscapeProject(projectDir), "work")
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, e := range entries {
		if !e.IsDir() || e.Name() == excludeSessionID {
			continue
		}
		in := Info{ID: e.Name(), Path: filepath.Join(base, e.Name())}
		if fi, err := e.Info(); err == nil {
			in.LastUsed = fi.ModTime()
		}
		_ = filepath.WalkDir(in.Path, func(_ string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable entry is skipped, not fatal
			}
			if info, err := d.Info(); err == nil {
				in.Files++
				in.Bytes += info.Size()
				if info.ModTime().After(in.LastUsed) {
					in.LastUsed = info.ModTime()
				}
			}
			return nil
		})
		infos = append(infos, in)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].LastUsed.After(infos[j].LastUsed) })
	return infos, nil
}

// Sweep aggregates List for the startup report: how many earlier work
// directories exist and how many bytes they hold. It is deliberately a
// report and not a deletion. Removing a tree of files the operator may
// not have looked at yet is not a decision an agent gets to make on its
// own; the explicit remedy is the workdirs command (ADR-0059).
func Sweep(projectDir, currentSessionID string) (dirs int, bytes int64, err error) {
	if _, err := Path(projectDir, currentSessionID); err != nil {
		return 0, 0, err
	}
	infos, err := List(projectDir, currentSessionID)
	if err != nil {
		return 0, 0, err
	}
	for _, in := range infos {
		dirs++
		bytes += in.Bytes
	}
	return dirs, bytes, nil
}

// Remove deletes one session's work directory (ADR-0059). The path is
// computed from a validated single-segment id, never taken from input,
// so there is nothing to traverse; the caller owns confirmation and the
// not-while-live check. The parent work/ directory is folded up when
// this leaves it empty — a non-recursive rmdir that cannot remove
// anything that still holds files.
func Remove(projectDir, sessionID string) error {
	dir, err := Path(projectDir, sessionID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	RemoveIfEmpty(filepath.Dir(dir))
	return nil
}

// RemoveIfEmpty deletes the directory when the session put nothing in
// it, so a run that never needed one leaves no trace. It is a single
// non-recursive rmdir of a path this package computed: it cannot remove
// a directory that still holds anything, which is what makes it safe to
// do without asking.
func RemoveIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return
	}
	_ = os.Remove(dir)
}
