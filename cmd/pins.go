package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/bounded"
	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/statedir"
	"github.com/nlink-jp/gem-agent/internal/trustpin"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// projectGrant is what the trust decision (ADR-0023) and the content
// pins (ADR-0074) together allow this session to load from the project:
// trusted says the directory is, excluded names the agent-facing files
// whose content changed since the operator trusted them and were not
// re-trusted — loaded by nobody until they are.
type projectGrant struct {
	trusted  bool
	excluded map[string]bool
}

// instruction reports whether the project's own instruction file name
// may be loaded.
func (g projectGrant) instruction(name string) bool { return g.trusted && !g.excluded[name] }

// mcp reports whether the project's .mcp.json may be read.
func (g projectGrant) mcp() bool { return g.trusted && !g.excluded[".mcp.json"] }

// skill reports whether the project skill dir (by name) may be loaded.
func (g projectGrant) skill(name string) bool {
	return g.trusted && !g.excluded[trustpin.SkillsDir+"/"+name]
}

// checkPins compares the project's agent-facing files with the pins the
// operator trusted (ADR-0074 §1) and returns the names to exclude this
// session. With no pins recorded yet for a trusted project (the first
// start after the upgrade), the current content is pinned as it is —
// trust-on-first-use — and said so. A changed file asks in an
// interactive start; a non-interactive one (or a mid-session re-check)
// leaves it out and says so. Pins are written through the flocked
// policy-file mutation.
func checkPins(cfg *config.Config, policyFile *config.PolicyFile, policyPath, projectDir string, trusted, interactive bool, in io.Reader, out io.Writer, msgs *uitext.Messages) (excluded map[string]bool, notes []string) {
	if !trusted || !cfg.Approval.PinTrustedFiles {
		return nil, nil
	}
	current, capNotes := trustpin.Compute(projectDir)
	notes = append(notes, capNotes...)
	recorded := policyFile.PinsFor(projectDir)
	if len(recorded) == 0 {
		if len(current) > 0 {
			if err := savePins(policyPath, projectDir, current, policyFile); err != nil {
				notes = append(notes, fmt.Sprintf("could not record the pins: %v", err))
			} else {
				notes = append(notes, fmt.Sprintf(msgs.PinRecordedFmt, len(current)))
			}
		}
		return nil, notes
	}
	changes := trustpin.Diff(recorded, current)
	if len(changes) == 0 {
		return nil, notes
	}
	next := trustpin.Pins{}
	for k, v := range recorded {
		next[k] = v
	}
	excluded = map[string]bool{}
	for _, c := range changes {
		if c.Kind == "removed" {
			delete(next, c.Name) // nothing to load; the pin goes
			continue
		}
		accepted := false
		if interactive {
			fmt.Fprintf(out, msgs.PinChangedFmt, trustpin.Describe(projectDir, c))
			fmt.Fprint(out, "\n"+msgs.PinQuestion)
			answer := strings.TrimSpace(readLineUnbuffered(in))
			accepted = strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
		}
		if accepted {
			next[c.Name] = current[c.Name]
			notes = append(notes, fmt.Sprintf(msgs.PinAcceptedFmt, c.Name))
		} else {
			excluded[c.Name] = true
			notes = append(notes, fmt.Sprintf(msgs.PinNotLoadedFmt, c.Name))
		}
	}
	if err := savePins(policyPath, projectDir, next, policyFile); err != nil {
		notes = append(notes, fmt.Sprintf("could not record the pins: %v", err))
	}
	if len(excluded) == 0 {
		excluded = nil
	}
	return excluded, notes
}

// repin records the project's current agent-facing content as trusted:
// after a write the operator approved as OperatorOnly (they saw it), or
// on `gem-agent trust --accept`.
func repin(policyPath, projectDir string, policyFile *config.PolicyFile) error {
	current, _ := trustpin.Compute(projectDir)
	return savePins(policyPath, projectDir, current, policyFile)
}

func savePins(policyPath, projectDir string, pins trustpin.Pins, policyFile *config.PolicyFile) error {
	fresh, err := config.MutatePolicyFile(policyPath, func(pf *config.PolicyFile) {
		pf.SetPins(projectDir, pins)
	})
	if err != nil {
		return err
	}
	if policyFile != nil {
		*policyFile = *fresh
	}
	return nil
}

// persistentStateFile is where the previous session's snapshot of the
// project's persistent files lives: the per-project state directory,
// beside the work directories (ADR-0074 §3/§4).
func persistentStateFile(projectDir string) (string, error) {
	root, err := statedir.Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, statedir.EscapeProject(projectDir))
	if err := statedir.EnsureProjectDir(dir, projectDir); err != nil {
		return "", err
	}
	return filepath.Join(dir, "persistent.json"), nil
}

// loadPersistentSnapshot reads the previous session's snapshot; a
// missing or unreadable file is an empty snapshot (nothing to compare).
func loadPersistentSnapshot(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	data, more, err := bounded.ReadAll(f, 8<<20)
	if err != nil || more {
		return nil
	}
	var snap map[string]string
	if json.Unmarshal(data, &snap) != nil {
		return nil
	}
	return snap
}

// savePersistentSnapshot writes the snapshot by temp-file + rename.
func savePersistentSnapshot(path string, snap map[string]string) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// describeSnapshotDiff renders added/changed/removed names as one
// comma-separated list, bounded to keep a banner line a line.
func describeSnapshotDiff(added, changed, removed []string) string {
	var parts []string
	for _, a := range added {
		parts = append(parts, a+" (added)")
	}
	for _, c := range changed {
		parts = append(parts, c+" (changed)")
	}
	for _, r := range removed {
		parts = append(parts, r+" (removed)")
	}
	sort.Strings(parts)
	if len(parts) > 12 {
		parts = append(parts[:12], fmt.Sprintf("… %d more", len(parts)-12))
	}
	return strings.Join(parts, ", ")
}
