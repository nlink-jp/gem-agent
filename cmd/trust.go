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
)

// broadRoot names why projectDir is too broad to confine anything
// (ADR-0023 §1), or "" when it is an ordinary project directory. Broad
// means: the filesystem root, the home directory, or an ancestor of
// home — "confined to the project" would quietly mean the operator's
// entire tree.
func broadRoot(projectDir, home string) string {
	if projectDir == string(filepath.Separator) {
		return "the filesystem root"
	}
	if home == "" {
		return ""
	}
	home = filepath.Clean(home)
	if projectDir == home {
		return "your home directory"
	}
	if strings.HasPrefix(home, projectDir+string(filepath.Separator)) {
		return "an ancestor of your home directory"
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
func (o projectOffering) describe() []string {
	var lines []string
	if len(o.Instructions) > 0 {
		lines = append(lines, strings.Join(o.Instructions, ", ")+" (injected as instructions)")
	}
	if o.HasMCP {
		lines = append(lines, fmt.Sprintf(".mcp.json (%d server(s) — each starts a child process)", o.MCPServers))
	}
	if o.Skills > 0 {
		lines = append(lines, fmt.Sprintf(".claude/skills/ (%d entr(y/ies) — loaded as your instructions)", o.Skills))
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
	if entries, err := os.ReadDir(filepath.Join(projectDir, ".claude", "skills")); err == nil {
		o.Skills = len(entries)
	}
	return o
}

// resolveProjectTrust decides whether projectDir's own agent-facing
// files may load (ADR-0023). It may prompt (interactive, undecided,
// offering non-empty) and may persist the answer. The returned note,
// when non-empty, belongs in the banner.
func resolveProjectTrust(cfg *config.Config, policyFile *config.PolicyFile, policyPath, projectDir string, interactive bool, in io.Reader, out io.Writer) (trusted bool, note string) {
	switch {
	case cfg.TrustsProject(projectDir):
		// Hand-declared in [approval].trusted_projects — the stronger
		// statement (it even loosens approvals); asking again would be
		// noise (ADR-0023 §4).
		return true, ""
	case policyFile.TrustFor(projectDir) == config.TrustGranted:
		return true, ""
	case policyFile.TrustFor(projectDir) == config.TrustDeclined:
		return false, "project trust: declined — the project's instruction files, .mcp.json, and skills are not loaded (edit " + policyPath + " to re-ask)"
	}

	offering := probeProject(projectDir)
	if offering.empty() {
		return true, "" // nothing to trust; ask nothing, record nothing
	}
	if !interactive {
		// Undecided and nobody to ask: run bare, decide nothing
		// (ADR-0023 §5) — refusing would break read-only -p pipelines
		// over fresh clones, the legitimate inspection workflow.
		return false, "project trust: undecided (non-interactive) — the project's instruction files, .mcp.json, and skills are not loaded; run interactively once to decide"
	}

	fmt.Fprintf(out, "\nnew project: %s\nthis project provides:\n", projectDir)
	for _, line := range offering.describe() {
		fmt.Fprintf(out, "  %s\n", line)
	}
	fmt.Fprint(out, "trust this project? These files will be treated as YOUR instructions and its MCP servers will run. [y/N]: ")
	answer := readLineUnbuffered(in)
	if strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes") {
		policyFile.SetTrust(projectDir, config.TrustGranted)
		if err := policyFile.Save(policyPath); err != nil {
			fmt.Fprintf(out, "warning: could not record the decision: %v\n", err)
		}
		return true, ""
	}
	policyFile.SetTrust(projectDir, config.TrustDeclined)
	if err := policyFile.Save(policyPath); err != nil {
		fmt.Fprintf(out, "warning: could not record the decision: %v\n", err)
	}
	return false, "project trust: declined — the project's instruction files, .mcp.json, and skills are not loaded (edit " + policyPath + " to re-ask)"
}

// confirmBroadRoot asks the ADR-0023 §1 question. Non-interactive runs
// are refused outright: a prompt nobody answers is a hang.
func confirmBroadRoot(reason, projectDir string, interactive bool, in io.Reader, out io.Writer) error {
	if !interactive {
		return fmt.Errorf("refusing to start in %s (%s): file tools and shell writes would span this entire tree; run interactively to confirm, or start in a project directory", projectDir, reason)
	}
	fmt.Fprintf(out, "\n⚠ %s is %s.\nFile tools and sandboxed shell writes would span this ENTIRE tree.\nstart anyway? [y/N]: ", projectDir, reason)
	answer := strings.TrimSpace(readLineUnbuffered(in))
	if strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
		return nil
	}
	return fmt.Errorf("not starting in %s — cd into a project directory first", projectDir)
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
