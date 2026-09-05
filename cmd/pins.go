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
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/statedir"
	"github.com/nlink-jp/gem-agent/internal/trustpin"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// projectGrant is what the trust decision (ADR-0023) and the content
// pins (ADR-0074) together allow this session to load from the project:
// trusted says the directory is, excluded names the agent-facing files
// whose content changed since the operator trusted them and were not
// re-trusted — loaded by nobody until they are. Every loader of project
// content takes the grant (internal/archtest pins that).
type projectGrant struct {
	trusted  bool
	excluded map[string]bool
}

// instruction reports whether the project's own instruction file name
// may be loaded.
func (g projectGrant) instruction(name string) bool { return g.trusted && !g.excluded[name] }

// mcp reports whether the project's .mcp.json may be read.
func (g projectGrant) mcp() bool { return g.trusted && !g.excluded[".mcp.json"] }

// config reports whether the project's .gem-agent.toml is trusted
// content: when it is not, the file may still tighten the approval
// policy (as an untrusted project's may, ADR-0023 §4) but never loosen.
func (g projectGrant) config() bool { return g.trusted && !g.excluded[".gem-agent.toml"] }

// skill reports whether the project skill directory entry may be loaded.
// The key is the directory name, as the pin is — not the frontmatter
// name a skill declares for itself.
func (g projectGrant) skill(entry string) bool {
	return g.trusted && !g.excluded[trustpin.SkillsDir+"/"+entry]
}

// checkPins compares the project's agent-facing files with the pins the
// operator trusted (ADR-0074 §1) and returns the names to exclude this
// session.
//
// No pins recorded yet (the first start after the upgrade, a grant that
// predates them): an interactive start pins the current content and
// names what it pinned — the operator is there to read the list; a
// non-interactive run loads as before and says that nothing is pinned
// yet, because recording trust nobody confirmed is what ADR-0023 §5
// refuses to do. An empty set counts as recorded (pinned_at), so a
// project with no agent-facing files is not re-pinned forever.
//
// With pins: a changed or added file asks in an interactive start; a
// non-interactive one (or a mid-session re-check) leaves it out and says
// so. A removed file has nothing to load; its pin stays, so content that
// comes back asks rather than passing as new. Pins are written through
// the flocked policy-file mutation, and only when something was accepted.
func checkPins(cfg *config.Config, policyFile *config.PolicyFile, policyPath, projectDir string, trusted, interactive bool, in io.Reader, out io.Writer, msgs *uitext.Messages) (excluded map[string]bool, notes []string) {
	if !trusted || !cfg.Approval.PinTrustedFiles {
		return nil, nil
	}
	current, capNotes := trustpin.Compute(projectDir)
	notes = append(notes, capNotes...)
	if !policyFile.HasPins(projectDir) {
		if !interactive {
			return nil, append(notes, msgs.PinNonePending)
		}
		if err := savePins(policyPath, projectDir, current, policyFile); err != nil {
			return nil, append(notes, fmt.Sprintf("could not record the pins: %v", err))
		}
		return nil, append(notes, fmt.Sprintf(msgs.PinRecordedFmt, len(current), pinNames(current)))
	}
	recorded := policyFile.PinsFor(projectDir)
	changes := trustpin.Diff(recorded, current)
	if len(changes) == 0 {
		return nil, notes
	}
	next := trustpin.Pins{}
	for k, v := range recorded {
		next[k] = v
	}
	accepted := false
	excluded = map[string]bool{}
	for _, c := range changes {
		if c.Kind == "removed" {
			notes = append(notes, fmt.Sprintf(msgs.PinRemovedFmt, c.Name))
			continue
		}
		ok := false
		if interactive {
			fmt.Fprintf(out, msgs.PinChangedFmt, describeChange(projectDir, c))
			fmt.Fprint(out, "\n"+msgs.PinQuestion)
			answer := strings.TrimSpace(readLineUnbuffered(in))
			ok = strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
		}
		if ok {
			next[c.Name] = current[c.Name]
			accepted = true
			notes = append(notes, fmt.Sprintf(msgs.PinAcceptedFmt, c.Name))
		} else {
			excluded[c.Name] = true
			notes = append(notes, fmt.Sprintf(msgs.PinNotLoadedFmt, describeChange(projectDir, c)))
		}
	}
	if accepted {
		if err := savePins(policyPath, projectDir, next, policyFile); err != nil {
			notes = append(notes, fmt.Sprintf("could not record the pins: %v", err))
		}
	}
	if len(excluded) == 0 {
		excluded = nil
	}
	return excluded, notes
}

// describeChange renders one change for the operator: the name, the
// kind and the current size ("AGENTS.md changed (1234 bytes)").
func describeChange(projectDir string, c trustpin.Change) string {
	s := c.Name + " " + c.Kind
	if size := trustpin.Size(projectDir, c.Name); size != "" {
		s += " " + size
	}
	return s
}

// pinNames lists the pinned names, sorted, for a note.
func pinNames(pins trustpin.Pins) string {
	names := make([]string, 0, len(pins))
	for n := range pins {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// pinNameForWrite maps an operator-approved write_file/edit_file call
// to the pin its target belongs to, or "" when the write touched no
// pinned content (or was not a file write at all).
func pinNameForWrite(projectDir string, tc llm.ToolCall) string {
	if tc.Name != "write_file" && tc.Name != "edit_file" {
		return ""
	}
	p, _ := tc.Args["path"].(string)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(projectDir, p)
	}
	rel, err := filepath.Rel(projectDir, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return trustpin.PinName(rel)
}

// repinName re-records the pin of one name after a write the operator
// approved as OperatorOnly: they saw that write, and only that one
// (ADR-0074 §1). Nothing else in the set moves — not a file that
// changed on its own since it was trusted, and not a name this session
// excluded: the operator has not seen the content it replaced. No
// pins recorded yet means nothing to update.
func repinName(policyPath, projectDir, name string, policyFile *config.PolicyFile, excluded map[string]bool) error {
	if name == "" || excluded[name] || !policyFile.HasPins(projectDir) {
		return nil
	}
	current, _ := trustpin.Compute(projectDir)
	next := trustpin.Pins{}
	for k, v := range policyFile.PinsFor(projectDir) {
		next[k] = v
	}
	if d, ok := current[name]; ok {
		next[name] = d
	} else {
		delete(next, name)
	}
	return savePins(policyPath, projectDir, next, policyFile)
}

// pinChangesNote reports, after an operator-lane command or a `!`
// command, which pinned files now differ from their pins. The command
// was approved; its effect on the trusted files was not shown, so the
// pins are not refreshed — the difference is named and the next start
// asks (review of ADR-0074, F7).
func pinChangesNote(cfg *config.Config, policyFile *config.PolicyFile, projectDir string, trusted bool, msgs *uitext.Messages) string {
	if !trusted || !cfg.Approval.PinTrustedFiles || !policyFile.HasPins(projectDir) {
		return ""
	}
	current, _ := trustpin.Compute(projectDir)
	changes := trustpin.Diff(policyFile.PinsFor(projectDir), current)
	if len(changes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, describeChange(projectDir, c))
	}
	return fmt.Sprintf(msgs.PinPendingFmt, strings.Join(parts, ", "))
}

// repin records the project's current agent-facing content as trusted:
// `gem-agent trust --accept`, the operator's explicit statement that
// what is there now is what they trust.
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

// loadProjectConfig reads the project's .gem-agent.toml and reports
// whether it may loosen the approval policy: only when the operator's
// own config names the project (ADR-0023 §4) AND the file is trusted
// content under the grant (ADR-0074) — a changed file may still
// tighten, as an untrusted project's may, never loosen.
func loadProjectConfig(cfg *config.Config, projectDir string, grant projectGrant) (projectCfg *config.ProjectConfig, mayLoosen bool, err error) {
	projectCfg, err = config.LoadProject(projectDir)
	if err != nil {
		return nil, false, err
	}
	return projectCfg, cfg.TrustsProject(projectDir) && grant.config(), nil
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
