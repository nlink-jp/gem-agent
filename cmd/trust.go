package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/instructions"
	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// broadRoot names why projectDir is too broad to confine anything
// (ADR-0023 §1), or "" when it is an ordinary project directory. Broad
// means: the filesystem root, the home directory, or an ancestor of
// home — "confined to the project" would quietly mean the operator's
// entire tree. The return value is a stable key ("root", "home",
// "home-ancestor") that uitext localizes for display (ADR-0029).
func broadRoot(projectDir, home string) string {
	if projectDir == string(filepath.Separator) {
		return "root"
	}
	if home == "" {
		return ""
	}
	home = filepath.Clean(home)
	if projectDir == home {
		return "home"
	}
	if strings.HasPrefix(home, projectDir+string(filepath.Separator)) {
		return "home-ancestor"
	}
	return ""
}

// projectOffering is what a project directory provides that the trust
// gate covers (ADR-0023 §2).
type projectOffering struct {
	Instructions []string // instruction file names present in projectDir
	MCPServers   int      // server entries in the project's .mcp.json
	HasMCP       bool
	Skills       int // entries under .claude/skills
}

func (o projectOffering) empty() bool {
	return len(o.Instructions) == 0 && !o.HasMCP && o.Skills == 0
}

// describe renders the offering for the trust prompt, naming what each
// item implies — a server entry is a child process, not a config line.
func (o projectOffering) describe(msgs *uitext.Messages) []string {
	var lines []string
	if len(o.Instructions) > 0 {
		lines = append(lines, fmt.Sprintf(msgs.TrustItemInstructionsFmt, strings.Join(o.Instructions, ", ")))
	}
	if o.HasMCP {
		lines = append(lines, fmt.Sprintf(msgs.TrustItemMCPFmt, o.MCPServers))
	}
	if o.Skills > 0 {
		lines = append(lines, fmt.Sprintf(msgs.TrustItemSkillsFmt, o.Skills))
	}
	return lines
}

// probeProject inspects what projectDir provides, without loading any
// of it.
func probeProject(projectDir string) projectOffering {
	var o projectOffering
	for _, name := range instructions.Names {
		if st, err := os.Stat(filepath.Join(projectDir, name)); err == nil && !st.IsDir() {
			o.Instructions = append(o.Instructions, name)
		}
	}
	if servers, _, err := mcp.LoadConfig(filepath.Join(projectDir, ".mcp.json")); err == nil && len(servers) > 0 {
		o.HasMCP = true
		o.MCPServers = len(servers)
	} else if err == nil {
		// An empty but present .mcp.json is not worth a prompt.
	} else if _, statErr := os.Stat(filepath.Join(projectDir, ".mcp.json")); statErr == nil {
		// Unparseable but present: gate it — trusting shows the parse
		// error, declining hides the file entirely.
		o.HasMCP = true
	}
	if entries, more, err := readDirBounded(filepath.Join(projectDir, ".claude", "skills"), trustProbeCap); err == nil {
		if more {
			// A scan cut short still counts as an offering: the prompt
			// must not read a huge directory as "nothing to trust".
			o.Skills++
		}
		// Count entries that look like skills (a dir with SKILL.md),
		// not raw directory entries — .DS_Store and stray files
		// inflated the number shown in a security prompt (review
		// round 2; over-reporting was the safe direction, but a wrong
		// count in a trust prompt invites doubt about the rest of it).
		for _, e := range entries {
			// os.Stat, as skills.Discover does: a symlinked skill
			// directory reports IsDir()=false on the DirEntry, and a
			// project whose skills are all links counted as offering
			// none — and was trusted without a prompt (ADR-0072 §4.5).
			dir := filepath.Join(projectDir, ".claude", "skills", e.Name())
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				continue
			}
			if st, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && !st.IsDir() {
				o.Skills++
			}
		}
	}
	return o
}

// resolveProjectTrust decides whether projectDir's own agent-facing
// files may load (ADR-0023). It may prompt (interactive, undecided,
// offering non-empty) and may persist the answer. The returned note,
// when non-empty, belongs in the banner.
func resolveProjectTrust(cfg *config.Config, policyFile *config.PolicyFile, policyPath, projectDir string, interactive bool, in io.Reader, out io.Writer, msgs *uitext.Messages) (trusted bool, note string) {
	switch {
	case cfg.TrustsProject(projectDir):
		// Hand-declared in [approval].trusted_projects — the stronger
		// statement (it even loosens approvals); asking again would be
		// noise (ADR-0023 §4).
		return true, ""
	case policyFile.TrustFor(projectDir) == config.TrustGranted:
		return true, ""
	case policyFile.TrustFor(projectDir) == config.TrustDeclined:
		return false, fmt.Sprintf(msgs.TrustDeclinedFmt, policyPath)
	}

	offering := probeProject(projectDir)
	if offering.empty() {
		return true, "" // nothing to trust; ask nothing, record nothing
	}
	if !interactive {
		// Undecided and nobody to ask: run bare, decide nothing
		// (ADR-0023 §5) — refusing would break read-only -p pipelines
		// over fresh clones, the legitimate inspection workflow.
		return false, msgs.TrustUndecided
	}

	fmt.Fprintf(out, msgs.TrustHeaderFmt, projectDir)
	for _, line := range offering.describe(msgs) {
		fmt.Fprintf(out, "  %s\n", line)
	}
	fmt.Fprint(out, msgs.TrustQuestion)
	answer := readLineUnbuffered(in)
	granted := strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
	trust := config.TrustDeclined
	if granted {
		trust = config.TrustGranted
	}
	// Persist through the flocked read-modify-write, not this process's
	// startup snapshot: a concurrent instance's whole-file Save from a
	// stale snapshot could resurrect a trust the operator just revoked
	// (review round 2).
	if fresh, err := config.MutatePolicyFile(policyPath, func(pf *config.PolicyFile) {
		pf.SetTrust(projectDir, trust)
	}); err != nil {
		fmt.Fprintf(out, "warning: could not record the decision: %v\n", err)
		policyFile.SetTrust(projectDir, trust) // keep the session consistent anyway
	} else {
		*policyFile = *fresh
	}
	if granted {
		return true, ""
	}
	return false, fmt.Sprintf(msgs.TrustDeclinedFmt, policyPath)
}

// confirmBroadRoot asks the ADR-0023 §1 question. Non-interactive runs
// are refused outright: a prompt nobody answers is a hang. reason is a
// broadRoot key.
func confirmBroadRoot(reason, projectDir string, interactive bool, in io.Reader, out io.Writer, msgs *uitext.Messages) error {
	localized := msgs.BroadReason(reason)
	if !interactive {
		return fmt.Errorf(msgs.BroadRootRefusedFmt, projectDir, localized)
	}
	fmt.Fprintf(out, msgs.BroadRootPromptFmt, projectDir, localized)
	answer := strings.TrimSpace(readLineUnbuffered(in))
	if strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
		return nil
	}
	return fmt.Errorf(msgs.BroadRootAbortFmt, projectDir)
}

// readLineUnbuffered reads one line byte-by-byte, deliberately without
// bufio: these prompts run before the REPL/TUI takes over stdin, and a
// buffered reader could swallow typed-ahead input into a buffer that is
// then discarded.
func readLineUnbuffered(in io.Reader) string {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}

// trustProbeCap bounds the trust probe's directory scans: an untrusted
// project's directory size is not the probe's to load whole (review
// after v0.68.2, R12).
const trustProbeCap = 1000

func readDirBounded(dir string, n int) ([]os.DirEntry, bool, error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = d.Close() }()
	entries, err := d.ReadDir(n + 1)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	if len(entries) > n {
		return entries[:n], true, nil
	}
	return entries, false, nil
}
