package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
)

// PolicyFileName is the machine-owned approval-policy file (ADR-0009).
// It is separate from config.toml on purpose: the TOML encoder does not
// preserve comments, and config.toml is hand-written and commented. A
// settings panel that rewrote it would delete its own documentation the
// first time somebody changed a value.
const PolicyFileName = "policy.toml"

// policyFileHeader is written on every save. Anyone who opens this file
// should learn in one line that editing it by hand is not the intended
// path — and where the intended path is.
const policyFileHeader = `# gem-agent approval policy — WRITTEN BY gem-agent.
#
# This file is rewritten whenever you change a policy from /settings or
# answer "never ask again" in an approval dialog. Comments you add here
# will not survive; hand-written policy belongs in config.toml, which
# gem-agent never touches.
#
# Entries here take precedence over [approval.tools] in config.toml, so a
# change made in the UI is never silently overridden by the file you
# wrote by hand. /settings shows which file each entry came from.
`

// PolicyFile is the machine-owned policy: a global table plus per-project
// tables, which is how the panel scopes a policy to one project without
// writing a file into that project's repository (ADR-0009 §4).
type PolicyFile struct {
	Tools    map[string]string        `toml:"tools"`
	Projects map[string]ProjectPolicy `toml:"projects"`
}

// Trust values for ProjectPolicy.Trust (ADR-0023).
const (
	TrustGranted  = "granted"
	TrustDeclined = "declined"
)

// ProjectPolicy is one entry of the [projects] table.
type ProjectPolicy struct {
	// Trust records the first-run answer to "trust this project's
	// agent-facing files?" (ADR-0023): TrustGranted, TrustDeclined, or
	// "" while unasked. Deleting the key re-asks on the next start.
	Trust string            `toml:"trust,omitempty"`
	Tools map[string]string `toml:"tools"`
	// Pins are the SHA-256 digests of the project's agent-facing files
	// as the operator last trusted them (ADR-0074): a changed file asks
	// again before it is loaded.
	Pins map[string]string `toml:"pins"`
	// Commands holds the per-command rules the withdrawn /learn wrote
	// (ADR-0045 §4), keyed by policy.CommandKey. Parsed so existing
	// files keep loading; NOT fed into the live policy since ADR-0049.
	// There is deliberately no global counterpart.
	Commands map[string]string `toml:"commands"`
}

// PolicyPath returns the machine-owned policy file's path, beside the
// operator's own config.
func PolicyPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), PolicyFileName)
}

// LoadPolicyFile reads the machine-owned policy. A missing file is not an
// error: it simply has not been written yet.
func LoadPolicyFile(path string) (*PolicyFile, error) {
	pf := &PolicyFile{Tools: map[string]string{}, Projects: map[string]ProjectPolicy{}}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return pf, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	md, err := toml.DecodeFile(path, pf)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("unknown key(s) in %s: %s", path, strings.Join(keys, ", "))
	}
	if pf.Tools == nil {
		pf.Tools = map[string]string{}
	}
	if pf.Projects == nil {
		pf.Projects = map[string]ProjectPolicy{}
	}
	return pf, nil
}

// ForProject returns the entries this file contributes in projectDir:
// the global table, then the project's own table layered on top.
func (pf *PolicyFile) ForProject(projectDir string) map[string]string {
	out := make(map[string]string, len(pf.Tools))
	for k, v := range pf.Tools {
		out[k] = v
	}
	for k, v := range pf.Projects[projectDir].Tools {
		out[k] = v
	}
	return out
}

// Set records a policy for a tool. An empty decision removes the entry,
// which is how the panel expresses "back to default". projectDir empty
// means the global scope.
func (pf *PolicyFile) Set(projectDir, tool, decision string) {
	if projectDir == "" {
		setOrDelete(pf.Tools, tool, decision)
		return
	}
	entry := pf.Projects[projectDir]
	if entry.Tools == nil {
		entry.Tools = map[string]string{}
	}
	setOrDelete(entry.Tools, tool, decision)
	// The entry survives with no tools when it still carries a trust
	// decision (ADR-0023) or learned command rules (ADR-0045).
	if len(entry.Tools) == 0 && len(entry.Commands) == 0 && entry.Trust == "" {
		delete(pf.Projects, projectDir)
		return
	}
	pf.Projects[projectDir] = entry
}

// CommandsFor returns the per-command rules recorded for projectDir
// (ADR-0045 §4). Project scope only, so an empty projectDir has none.
func (pf *PolicyFile) CommandsFor(projectDir string) map[string]string {
	if projectDir == "" {
		return nil
	}
	src := pf.Projects[projectDir].Commands
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// SetCommand records a per-command rule for projectDir. An empty
// decision removes the entry, which is how /settings expresses "back to
// default".
func (pf *PolicyFile) SetCommand(projectDir, key, decision string) {
	if projectDir == "" {
		return
	}
	entry := pf.Projects[projectDir]
	if entry.Commands == nil {
		entry.Commands = map[string]string{}
	}
	setOrDelete(entry.Commands, key, decision)
	// The entry survives with nothing in it only while it carries a
	// trust decision (ADR-0023).
	if len(entry.Commands) == 0 && len(entry.Tools) == 0 && entry.Trust == "" {
		delete(pf.Projects, projectDir)
		return
	}
	pf.Projects[projectDir] = entry
}

// TrustFor returns the recorded first-run trust decision for projectDir
// ("" while unasked).
func (pf *PolicyFile) TrustFor(projectDir string) string {
	return pf.Projects[projectDir].Trust
}

// SetTrust records the first-run trust decision (ADR-0023). An empty
// value removes it, which re-asks on the next start.
func (pf *PolicyFile) SetTrust(projectDir, trust string) {
	entry := pf.Projects[projectDir]
	entry.Trust = trust
	if entry.Trust == "" {
		// Trust withdrawn: the pins go with it — they were the content
		// that trust covered.
		entry.Pins = nil
	}
	if entry.Trust == "" && len(entry.Tools) == 0 && len(entry.Commands) == 0 {
		delete(pf.Projects, projectDir)
		return
	}
	pf.Projects[projectDir] = entry
}

// PinsFor returns the recorded pins for projectDir (nil when none).
func (pf *PolicyFile) PinsFor(projectDir string) map[string]string {
	return pf.Projects[projectDir].Pins
}

// SetPins replaces the recorded pins for projectDir.
func (pf *PolicyFile) SetPins(projectDir string, pins map[string]string) {
	entry := pf.Projects[projectDir]
	if len(pins) == 0 {
		entry.Pins = nil
	} else {
		entry.Pins = map[string]string{}
		for k, v := range pins {
			entry.Pins[k] = v
		}
	}
	pf.Projects[projectDir] = entry
}

func setOrDelete(m map[string]string, key, value string) {
	if value == "" {
		delete(m, key)
		return
	}
	m[key] = value
}

// Save writes the file atomically, creating the directory if needed.
// Atomically because this file is rewritten from a UI keypress: a
// half-written policy file would fail to parse at the next startup, and
// the failure would arrive long after the keypress that caused it.
// MutatePolicyFile applies fn to a FRESH load of the policy file and
// saves the result, under an exclusive flock on a sibling .lock file.
// Mutating a startup-time in-memory snapshot and rewriting the whole
// file let a second instance's stale snapshot clobber concurrent
// decisions — including resurrecting a project trust the operator had
// just declined in the other instance (review round 2). The returned
// PolicyFile is the post-mutation state, so the caller can refresh its
// in-memory view. Blocking lock: mutations are rare and milliseconds.
func MutatePolicyFile(path string, fn func(*PolicyFile)) (*PolicyFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	// Close releases the flock
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock %s: %v", path+".lock", err)
	}
	pf, err := LoadPolicyFile(path)
	if err != nil {
		return nil, err
	}
	fn(pf)
	if err := pf.Save(path); err != nil {
		return nil, err
	}
	return pf, nil
}

func (pf *PolicyFile) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(policyFileHeader)
	if len(pf.Tools) > 0 {
		b.WriteString("\n[tools]\n")
		writeTools(&b, pf.Tools)
	}
	for _, dir := range sortedProjects(pf.Projects) {
		entry := pf.Projects[dir]
		if entry.Trust != "" {
			fmt.Fprintf(&b, "\n[projects.%s]\n", quoteKey(dir))
			fmt.Fprintf(&b, "trust = %s\n", quoteKey(entry.Trust))
		}
		if len(entry.Tools) > 0 {
			fmt.Fprintf(&b, "\n[projects.%s.tools]\n", quoteKey(dir))
			writeTools(&b, entry.Tools)
		}
		if len(entry.Pins) > 0 {
			fmt.Fprintf(&b, "\n[projects.%s.pins]\n", quoteKey(dir))
			writeTools(&b, entry.Pins)
		}
		if len(entry.Commands) > 0 {
			fmt.Fprintf(&b, "\n[projects.%s.commands]\n", quoteKey(dir))
			writeTools(&b, entry.Commands)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func writeTools(b *strings.Builder, tools map[string]string) {
	keys := make([]string, 0, len(tools))
	for k := range tools {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s = %s\n", quoteKey(k), quoteKey(tools[k]))
	}
}

func sortedProjects(m map[string]ProjectPolicy) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// quoteKey renders a TOML basic string. Tool patterns contain "*" and
// project paths contain "/", so everything here is quoted.
func quoteKey(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}
