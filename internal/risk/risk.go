// Package risk is the logical (LLM-free) tier of the auto-approve
// escalation ladder (ADR-0004). Classify is a pure function: same input,
// same verdict, no I/O, no model. Its Block tier is the deterministic
// floor that a model verdict cannot override.
package risk

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// scratchRoots are the scratch locations the sandbox profile lets a
// shell write to — read from the sandbox package, not repeated here
// (ADR-0070 §2): the redirect rule's job is to explain what Seatbelt
// will deny, so it has to read Seatbelt's list. Resolved once at
// init; Classify itself does no I/O.
var scratchRoots = sandbox.ScratchDirs()

// homeDir expands a leading `~` in a walk's starting point (ADR-0070
// §3). Empty when the home is unknown, in which case `~` is treated as
// outside every root — the conservative reading.
var homeDir, _ = os.UserHomeDir()

// Tier is the logical verdict for one tool call.
type Tier int

const (
	// Safe: auto-approve without consulting the model.
	Safe Tier = iota
	// Review: uncertain — hand to the model tier.
	Review
	// Block: always escalate to the human; the model cannot override.
	Block
)

func (t Tier) String() string {
	switch t {
	case Safe:
		return "safe"
	case Review:
		return "review"
	default:
		return "block"
	}
}

// Verdict is a tier plus the reason to show the operator.
type Verdict struct {
	Tier   Tier
	Reason string
}

// blockPattern is one dangerous-shell rule.
type blockPattern struct {
	re     *regexp.Regexp
	reason string
}

// blockPatterns catches destructive, irreversible, or scope-escaping
// shell commands. Matching is deliberately generous: a false Block costs
// one approval prompt, a false Safe costs the user's data.
var blockPatterns = []blockPattern{
	{regexp.MustCompile(`(^|[\s;&|(])rm\s+(-[a-zA-Z]*\s+)*-?[a-zA-Z]*[rR][a-zA-Z]*f|(^|[\s;&|(])rm\s+(-[a-zA-Z]*\s+)*-?[a-zA-Z]*f[a-zA-Z]*[rR]`), "recursive force delete"},
	{regexp.MustCompile(`(^|[\s;&|(])(sudo|doas|su)\s`), "privilege escalation"},
	{regexp.MustCompile(`(^|[\s;&|(])(dd|mkfs\S*|fdisk|diskutil|newfs\S*)\s`), "raw disk / filesystem operation"},
	{regexp.MustCompile(`(^|[\s;&|(])git\s+(push|reset\s+--hard|clean\s+-[a-zA-Z]*f|checkout\s+--\s|branch\s+-D|rebase|filter-branch)`), "history-rewriting or remote-publishing git operation"},
	{regexp.MustCompile(`(curl|wget|fetch)[^|;&]*\|\s*(sudo\s+)?(ba|z|k|da)?sh`), "piping a download into a shell"},
	{regexp.MustCompile(`>\s*/dev/(sd|disk|nvme|rdisk)`), "write to a raw device"},
	{regexp.MustCompile(`(^|[\s;&|(])chmod\s+(-[a-zA-Z]*\s+)*(777|a\+w|o\+w)`), "world-writable permissions"},
	{regexp.MustCompile(`(^|[\s;&|(])(shutdown|reboot|halt|killall|pkill)\s`), "system or process-wide control"},
	{regexp.MustCompile(`:\s*\(\s*\)\s*\{.*\}\s*;?\s*:`), "fork bomb"},
	{regexp.MustCompile(`(^|[\s;&|(])(launchctl|systemctl|crontab|defaults\s+write)\s`), "system configuration change"},
	{regexp.MustCompile(`(^|[\s;&|(=])(scp|rsync)\s[^|;&]*\s[^|;&]*:`), "copying data to a remote host"},
	{regexp.MustCompile(`(^|[\s;&|(])(nc|ncat|netcat|telnet)\s`), "raw network client"},
	{regexp.MustCompile(`\bgpg\b|\bopenssl\s+(enc|rsa|genrsa)|security\s+(find|dump|delete)-`), "cryptographic material handling"},
}

// credentialPaths are locations whose contents are secrets; touching
// them is never auto-approved, read or write.
var credentialPaths = []string{
	"~/.ssh", "/.ssh/", "~/.aws", "/.aws/", "~/.config/gcloud", "/.config/gcloud/",
	"~/.kube", "/.kube/", "~/.gnupg", "/.gnupg/", ".env", "id_rsa", "id_ed25519",
	"credentials.json", "service-account", ".netrc", ".npmrc", ".pypirc",
	"application_default_credentials",
}

// readOnlyCommands are shell entry points that cannot mutate state.
// Only a command built exclusively from these is Safe.
var readOnlyCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true, "file": true,
	"stat": true, "pwd": true, "echo": true, "which": true, "type": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "find": true, "fd": true,
	"sort": true, "uniq": true, "cut": true, "tr": true, "sed": true, "awk": true,
	"jq": true, "yq": true, "diff": true, "date": true, "env": true, "uname": true,
	"df": true, "du": true, "ps": true, "top": true, "id": true, "whoami": true,
	"true": true, "false": true, "basename": true, "dirname": true, "realpath": true,
	"column": true, "tee": true, "xargs": true, "nl": true, "seq": true,
}

// Classify returns the logical verdict for one tool call. projectDir is
// the confinement root and workDir the session work directory — the
// second root of ADR-0058, empty when the session has none. Both are
// ordinary places for a session to write; treating the work directory
// as foreign made every spill, staging step, and mkdir there cost a
// model review or a human prompt (found in the v0.56.0 field test).
// args come straight from the model.
func Classify(toolName string, mutating bool, args map[string]any, projectDir, workDir string) Verdict {
	if !mutating {
		return Verdict{Safe, "read-only tool"}
	}

	switch toolName {
	case "write_file", "edit_file":
		path, _ := args["path"].(string)
		if v, blocked := classifyPath(path, projectDir, workDir); blocked {
			return v
		}
		// Content is not inspected here: the file tools cannot execute
		// it, and judging intent from content is the model's job. The
		// reason names the root that matched — recording a work-dir
		// write as "inside the project" would be a false audit line.
		if workDir != "" && filepath.IsAbs(path) && withinDir(workDir, filepath.Clean(path)) {
			return Verdict{Safe, "edits a file inside the session work directory"}
		}
		return Verdict{Safe, "edits a file inside the project"}

	case "shell_exec":
		command, _ := args["command"].(string)
		return classifyCommand(command, projectDir, workDir)

	case "save_memory", "delete_memory":
		// Never Safe: a persisted memory reappears in every later
		// session's prompt, so memory is a persistence vector for
		// injected instructions (ADR-0020 §4).
		return Verdict{Review, "changes what the agent remembers across sessions"}
	}

	if strings.HasPrefix(toolName, "mcp__") {
		// External server: effects are unknown to this classifier.
		return Verdict{Review, "external MCP tool — effects unknown to the rule tier"}
	}
	return Verdict{Review, "unrecognised tool"}
}

func classifyPath(path, projectDir, workDir string) (Verdict, bool) {
	if path == "" {
		return Verdict{Review, "missing path argument"}, true
	}
	if hasCredentialPath(path) {
		return Verdict{Block, "path looks like credential material"}, true
	}
	if filepath.IsAbs(path) {
		if !withinAnyRoot(filepath.Clean(path), projectDir, workDir) {
			return Verdict{Block, outsideRootsReason("absolute path", workDir)}, true
		}
		return Verdict{}, false
	}
	if strings.Contains(path, "..") {
		return Verdict{Block, "path escapes the project directory"}, true
	}
	return Verdict{}, false
}

func classifyCommand(command, projectDir, workDir string) Verdict {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return Verdict{Review, "empty command"}
	}
	for _, p := range blockPatterns {
		if p.re.MatchString(trimmed) {
			return Verdict{Block, p.reason}
		}
	}
	if hasCredentialPath(trimmed) {
		return Verdict{Block, "references credential material"}
	}
	if v, ok := blockedRedirect(trimmed, projectDir, workDir); ok {
		return v
	}
	// Command substitution hides the real target from any pattern rule
	// (the agent-skeleton finding) — never Safe, let the model look.
	if strings.Contains(trimmed, "$(") || strings.Contains(trimmed, "`") ||
		strings.Contains(trimmed, "${") || strings.Contains(trimmed, "eval ") {
		return Verdict{Review, "dynamic command construction"}
	}
	// A read-only walk is still a walk: the sandbox denies writes only,
	// so a `find /` reaches every mount. Never Safe outside the roots;
	// the model tier weighs the cost (ADR-0070 §3).
	if walksOutsideRoots(trimmed, projectDir, workDir) {
		return Verdict{Review, outsideRootsReason("walks the filesystem", workDir)}
	}
	if readOnlyCommand(trimmed) {
		return Verdict{Safe, "read-only shell command"}
	}
	return Verdict{Review, "shell command with unclear effects"}
}

// writableRoots are the places a shell command may write: the project,
// the session work directory, and the sandbox's scratch roots.
func writableRoots(projectDir, workDir string) []string {
	return append([]string{projectDir, workDir}, scratchRoots...)
}

// blockedRedirect flags output redirection to an absolute path outside
// the writable roots (the sandbox would deny it, but escalating
// explains why).
func blockedRedirect(command, projectDir, workDir string) (Verdict, bool) {
	re := regexp.MustCompile(`>>?\s*("?)(/[^\s"';|&]+)`)
	for _, m := range re.FindAllStringSubmatch(command, -1) {
		if !withinAnyRoot(filepath.Clean(m[2]), writableRoots(projectDir, workDir)...) {
			return Verdict{Block, outsideRootsReason("redirects output", workDir)}, true
		}
	}
	return Verdict{}, false
}

// walkers are read-only commands whose work is a tree walk from a
// starting point given as an argument; grepFamily walks only with a
// recursive flag.
var walkers = map[string]bool{"find": true, "fd": true, "du": true, "rg": true}
var grepFamily = map[string]bool{"grep": true, "egrep": true, "fgrep": true}

var segmentSplit = regexp.MustCompile(`\||;|&&|\|\||&`)

// walksOutsideRoots reports whether any segment of the command walks a
// tree from `/`, `~`, or an absolute path outside the writable roots
// (ADR-0070 §3). Relative starting points stay inside the project —
// the command's working directory.
func walksOutsideRoots(command, projectDir, workDir string) bool {
	roots := writableRoots(projectDir, workDir)
	for _, seg := range segmentSplit.Split(command, -1) {
		fields := strings.Fields(strings.TrimSpace(seg))
		if len(fields) == 0 {
			continue
		}
		head := filepath.Base(fields[0])
		walks := walkers[head] || (grepFamily[head] && hasRecursiveFlag(fields[1:]))
		if !walks {
			continue
		}
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") {
				continue
			}
			f = strings.Trim(f, `"'`)
			switch {
			case f == "~" || strings.HasPrefix(f, "~/"):
				if homeDir == "" {
					return true
				}
				f = filepath.Join(homeDir, strings.TrimPrefix(f, "~"))
			case !filepath.IsAbs(f):
				continue
			}
			if !withinAnyRoot(filepath.Clean(f), roots...) {
				return true
			}
		}
	}
	return false
}

func hasRecursiveFlag(args []string) bool {
	for _, a := range args {
		if a == "--recursive" || a == "--dereference-recursive" {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "rR") {
			return true
		}
	}
	return false
}

// devNullRedirect matches redirections that write nowhere: to
// /dev/null, or one descriptor onto another (`2>&1`). They do not make
// a read-only command a writing one (ADR-0070 §2).
var devNullRedirect = regexp.MustCompile(`(\d*>>?|&>)\s*/dev/null|\d>&\d`)

// withinAnyRoot reports whether an absolute, cleaned path sits under
// any non-empty root.
func withinAnyRoot(path string, roots ...string) bool {
	for _, r := range roots {
		if r != "" && withinDir(r, path) {
			return true
		}
	}
	return false
}

// outsideRootsReason names what the operator actually configured: with
// no work directory the old wording stays, so the message never claims
// a root that does not exist.
func outsideRootsReason(what, workDir string) string {
	if workDir == "" {
		return what + " outside the project directory"
	}
	return what + " outside the project and session work directories"
}

// readOnlyCommand reports whether every segment of a pipeline/sequence
// starts with a known read-only command and carries no redirection.
func readOnlyCommand(command string) bool {
	command = devNullRedirect.ReplaceAllString(command, "")
	if strings.ContainsAny(command, ">") {
		return false // any other redirection writes somewhere
	}
	segments := segmentSplit.Split(command, -1)
	for _, seg := range segments {
		fields := strings.Fields(strings.TrimSpace(seg))
		if len(fields) == 0 {
			return false
		}
		head := fields[0]
		if strings.Contains(head, "=") {
			return false // env assignment prefix; unclear
		}
		if !readOnlyCommands[filepath.Base(head)] {
			return false
		}
	}
	return true
}

func hasCredentialPath(s string) bool {
	lower := strings.ToLower(s)
	for _, p := range credentialPaths {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func withinDir(base, p string) bool {
	base = filepath.Clean(base)
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

// Describe renders a verdict for display.
func (v Verdict) Describe() string {
	return fmt.Sprintf("%s: %s", v.Tier, v.Reason)
}
