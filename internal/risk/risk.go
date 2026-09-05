// Package risk is the rule tier of the approval ladder (ADR-0004): a
// pure, model-free classifier that reads a tool call's named arguments
// and returns Safe, Review or Block with a reason.
//
// Since ADR-0073 the tier decides nothing about what a shell command
// will do. That question belongs to the kernel: shell_exec runs in a
// Seatbelt lane the model declares (read, write, operator) and the
// lane, not the command text, bounds its effects. What remains here
// for shell commands is the Block floor — patterns for the operations
// an operator wants to see whatever lane they run in (privilege
// escalation, recursive force deletes, history rewrites, downloads
// piped into a shell, credential paths). A floor only raises a
// verdict; a spelling the patterns miss costs a prompt the cage would
// have caught anyway, never a hole. For the file tools, whose
// arguments are structured paths, the tier is exact: where the path
// lies and what the file is decide the verdict.
package risk

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

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
	// OperatorOnly marks a Review verdict the model tier may not answer
	// (ADR-0072 §4): the write lands in what later sessions trust —
	// instruction files, the runtime's own configuration — so the
	// party that proposed it cannot also be its judge (ADR-0020 §4,
	// applied beyond memory). The ladder hands such calls straight to
	// the operator. A shell command in the operator lane (ADR-0073) is
	// the same verdict: the lane can write those files.
	OperatorOnly bool
}

// blockPattern is one dangerous-shell rule.
type blockPattern struct {
	re     *regexp.Regexp
	reason string
}

// gitGlobalOpts skips the options git accepts before its subcommand
// (`git -C dir push`, `git -c k=v push`, `git --no-pager push`): the
// subcommand decides the verdict, wherever it sits.
const gitGlobalOpts = `(?:(?:-C|-c|--git-dir|--work-tree|--namespace)(?:=\S+|\s+\S+)\s+|--?[A-Za-z][\w-]*\s+)*`

// blockPatterns is the Block floor for shell commands: destructive,
// irreversible, or scope-escaping operations the operator sees in
// every lane. Matching is deliberately generous: a false Block costs
// one approval prompt. They run on the command with every segment's
// first word canonicalised (normalizeHeads): a path prefix, a leading
// backslash and letter case all invoke the same program. `rm` is
// judged by rmRecursiveForce, which reads the flags however spelled.
var blockPatterns = []blockPattern{
	{regexp.MustCompile(`(^|[\s;&|(])(sudo|doas|su)\s`), "privilege escalation"},
	// AppleScript's administrator prompt is the same escalation by
	// another door (ADR-0072 §4.8).
	{regexp.MustCompile(`(?i)with\s+administrator\s+privileges`), "privilege escalation (administrator privileges)"},
	{regexp.MustCompile(`(^|[\s;&|(])(dd|mkfs\S*|fdisk|diskutil|newfs\S*)\s`), "raw disk / filesystem operation"},
	{regexp.MustCompile(`(^|[\s;&|(])git\s+` + gitGlobalOpts +
		`(push|reset\s+--hard|clean\s+(?:-[\w-]+\s+)*(?:-[a-zA-Z]*f[a-zA-Z]*|--force)|checkout\s+(?:[^\s;&|]+\s+)*--(?:\s|$)|restore(?:\s|$)|branch\s+-D|rebase|filter-branch|stash\s+(?:drop|clear))`),
		"history-rewriting or remote-publishing git operation"},
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

// Classify returns the logical verdict for one tool call. projectDir is
// the confinement root and workDir the session work directory — the
// second root of ADR-0058, empty when the session has none. Both are
// ordinary places for a session to write. args come straight from the
// model. mutating is the tool's own word on whether this call changes
// state — for shell_exec that depends on the lane (tools.Tool.MutatesFor).
func Classify(toolName string, mutating bool, args map[string]any, projectDir, workDir string) Verdict {
	if toolName == "shell_exec" {
		// Judged before the read-only shortcut: the Block floor applies
		// to a read-lane command too (a `sudo` the cage would refuse is
		// still something the operator wants to see).
		return classifyShell(args, mutating)
	}
	if !mutating {
		return Verdict{Tier: Safe, Reason: "read-only tool"}
	}

	switch toolName {
	case "write_file", "edit_file":
		p, _ := args["path"].(string)
		if v, blocked := classifyPath(p, projectDir, workDir); blocked {
			return v
		}
		// Content is not inspected here: the file tools cannot execute
		// it, and judging intent from content is the model's job. The
		// reason names the root that matched — recording a work-dir
		// write as "inside the project" would be a false audit line.
		if workDir != "" && filepath.IsAbs(p) && withinDir(workDir, filepath.Clean(p)) {
			return Verdict{Tier: Safe, Reason: "edits a file inside the session work directory"}
		}
		// What the file is decides the rest (ADR-0072 §4): version
		// control internals and the files later sessions take
		// instructions or configuration from are not ordinary edits.
		if v, ok := persistentTarget(projectRelative(p, projectDir)); ok {
			return v
		}
		return Verdict{Tier: Safe, Reason: "edits a file inside the project"}

	case "save_memory", "delete_memory":
		// Never Safe: a persisted memory reappears in every later
		// session's prompt, so memory is a persistence vector for
		// injected instructions (ADR-0020 §4).
		return Verdict{Tier: Review, Reason: "changes what the agent remembers across sessions"}
	}

	if strings.HasPrefix(toolName, "mcp__") {
		// External server: effects are unknown to this classifier.
		return Verdict{Tier: Review, Reason: "external MCP tool — effects unknown to the rule tier"}
	}
	return Verdict{Tier: Review, Reason: "unrecognised tool"}
}

// classifyShell judges a shell_exec call (ADR-0073): the Block floor
// first, in every lane; then the lane decides. mutating is false only
// when the registry has a read lane and the call asked for it — a
// read-lane request with no sandbox to back it is a write-lane call.
func classifyShell(args map[string]any, mutating bool) Verdict {
	command, _ := args["command"].(string)
	access, _ := args["access"].(string)
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return Verdict{Tier: Review, Reason: "empty command"}
	}
	if v, ok := blockFloor(trimmed); ok {
		return v
	}
	lane, err := sandbox.ParseLane(access)
	if err != nil {
		return Verdict{Tier: Review, Reason: err.Error()}
	}
	switch {
	case lane == sandbox.LaneOperator:
		return Verdict{Tier: Review, OperatorOnly: true,
			Reason: "operator access: the command may write the instruction and configuration files later sessions trust, and read credentials — the operator decides, not the model tier"}
	case lane == sandbox.LaneRead && !mutating:
		return Verdict{Tier: Safe, Reason: "read lane: the sandbox denies writes outside scratch, the network, preference writes, signals to other processes and IPC-capable programs"}
	case lane == sandbox.LaneRead:
		// Declared read, but this session has no unasked read lane: the
		// sandbox is off, its read lane failed verification, or the
		// operator asked for read-lane prompts. The command still runs
		// under the read profile where there is one; the ladder or the
		// operator decides (review V7: the reason once said "off").
		return Verdict{Tier: Review, Reason: "read lane declared, but read-lane commands ask in this session — the model tier weighs it"}
	}
	return Verdict{Tier: Review, Reason: "write lane: writes the project and the work directory, reaches the network — the model tier weighs it"}
}

// blockFloor is the text part of the shell verdict that survives
// ADR-0073: it can only raise a call to Block.
func blockFloor(trimmed string) (Verdict, bool) {
	norm := normalizeHeads(trimmed)
	if rmRecursiveForce(norm) {
		return Verdict{Tier: Block, Reason: "recursive force delete"}, true
	}
	if gitBranchForceDelete(norm) {
		return Verdict{Tier: Block, Reason: "history-rewriting or remote-publishing git operation"}, true
	}
	for _, p := range blockPatterns {
		if p.re.MatchString(norm) {
			return Verdict{Tier: Block, Reason: p.reason}, true
		}
	}
	if hasCredentialPath(trimmed) {
		// The read and write lanes deny the read at the kernel; the
		// floor puts the attempt in front of the operator as well.
		return Verdict{Tier: Block, Reason: "references credential material"}, true
	}
	return Verdict{}, false
}

func classifyPath(p, projectDir, workDir string) (Verdict, bool) {
	if p == "" {
		return Verdict{Tier: Review, Reason: "missing path argument"}, true
	}
	if hasCredentialPath(p) {
		return Verdict{Tier: Block, Reason: "path looks like credential material"}, true
	}
	if filepath.IsAbs(p) {
		// Either spelling of a macOS alias (/tmp, /private/tmp) is the
		// same place to the kernel and to the registry's resolved root.
		c := filepath.Clean(p)
		if !withinAnyRoot(c, projectDir, workDir) && !withinAnyRoot(aliasResolve(c), projectDir, workDir) {
			return Verdict{Tier: Block, Reason: outsideRootsReason("absolute path", workDir)}, true
		}
		return Verdict{}, false
	}
	if strings.Contains(p, "..") {
		return Verdict{Tier: Block, Reason: "path escapes the project directory"}, true
	}
	return Verdict{}, false
}

// projectRelative returns p relative to the project when p is an
// absolute path inside it, p itself when relative, and "" when p lies
// elsewhere (the work directory, or outside — both judged before).
func projectRelative(p, projectDir string) string {
	if !filepath.IsAbs(p) {
		return p
	}
	if projectDir == "" {
		return ""
	}
	// Either spelling of a macOS alias may be the project's (the
	// registry resolves its root; the model may type the alias).
	for _, c := range []string{filepath.Clean(p), aliasResolve(filepath.Clean(p))} {
		if withinDir(projectDir, c) {
			if rel, err := filepath.Rel(projectDir, c); err == nil {
				return rel
			}
		}
	}
	return ""
}

// persistentTarget judges a project-relative write target by what the
// file is (ADR-0072 §4). Version-control internals are Block: a hook
// or a config value under .git/ runs outside the sandbox on the
// operator's next git command, and no file tool has business there.
// The instruction files, the runtime's own configuration and the
// .claude directory — sandbox.PersistentFile, the one list the write
// lane's profile also denies (ADR-0073 §3) — are Review that only the
// operator may answer: the edit persists into what every later session
// trusts, so the evaluator-is-the-proposer objection of ADR-0020 §4
// applies to it exactly as to memory.
func persistentTarget(rel string) (Verdict, bool) {
	if rel == "" {
		return Verdict{}, false
	}
	c := filepath.ToSlash(filepath.Clean(rel))
	if strings.HasPrefix(c, "../") || c == ".." {
		return Verdict{}, false // outside the project: judged elsewhere
	}
	// Folded: the volume does not distinguish .GIT from .git.
	if l := strings.ToLower(c); l == ".git" || strings.HasPrefix(l, ".git/") || strings.Contains(l, "/.git/") || strings.HasSuffix(l, "/.git") {
		return Verdict{Tier: Block, Reason: "writes inside the version-control internals — hooks and config there run outside the sandbox on the next git command"}, true
	}
	if sandbox.PersistentFile(c) {
		return Verdict{Tier: Review, OperatorOnly: true,
			Reason: "changes the instructions or configuration later sessions trust — the operator decides, not the model tier"}, true
	}
	return Verdict{}, false
}

// segmentSplit separates the simple commands of a shell text: pipes,
// lists, background, and newlines — bash runs each line as a separate
// command, so a newline is a separator like `;` (review round 4: a
// command hidden after a newline inherited the first line's verdict).
var segmentSplit = regexp.MustCompile(`\|\||&&|\||;|&|\r\n|\n|\r`)

// normalizeHeads returns command with each segment's first word
// canonicalised for the block rules: `/bin/rm`, `\rm` and `RM` (the
// default filesystem is case-insensitive) all run rm. Only the first
// word of a segment is touched — after any VAR=value prefixes and
// opening parens — so an argument that merely names a program keeps
// its spelling. Separators are kept in place, so the rules' anchors
// still see them.
func normalizeHeads(command string) string {
	var b strings.Builder
	last := 0
	for _, loc := range segmentSplit.FindAllStringIndex(command, -1) {
		b.WriteString(normalizeHead(command[last:loc[0]]))
		b.WriteString(command[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(normalizeHead(command[last:]))
	return b.String()
}

// wrappers run the command that follows them: the word after one is
// a head too, and is canonicalised like the first (review after
// v0.68.2: `env /usr/bin/sudo id` kept the path and slipped the
// privilege-escalation rule).
var wrappers = map[string]bool{
	"env": true, "time": true, "nohup": true, "nice": true, "ionice": true,
	"command": true, "exec": true, "builtin": true, "xargs": true, "timeout": true,
	"caffeinate": true, "stdbuf": true, "chroot": true, "sudo": true, "doas": true,
}

func normalizeHead(seg string) string {
	i := 0
	for {
		for i < len(seg) && (seg[i] == ' ' || seg[i] == '\t' || seg[i] == '(') {
			i++
		}
		j := i
		for j < len(seg) && seg[j] != ' ' && seg[j] != '\t' {
			j++
		}
		if i == j {
			return seg
		}
		tok := seg[i:j]
		if (strings.Contains(tok, "=") && !strings.HasPrefix(tok, "=")) || strings.HasPrefix(tok, "-") {
			i = j // VAR=value prefix or a wrapper's flag: the command follows
			continue
		}
		name := canonicalName(tok)
		seg = seg[:i] + name + seg[j:]
		if !wrappers[name] {
			return seg
		}
		// A wrapper's segment: every path-spelled word after it is a
		// program it may run (`nice -n 5 /usr/bin/git push`), so each
		// is canonicalised — the rules match names, and a wrapper's own
		// flags cannot be told from its arguments generically.
		rest := seg[i+len(name):]
		fields := strings.Fields(rest)
		for _, f := range fields {
			if strings.Contains(f, "/") || strings.HasPrefix(f, `\`) {
				rest = strings.Replace(rest, f, canonicalName(f), 1)
			}
		}
		return seg[:i+len(name)] + rest
	}
}

// canonicalName is the program a token invokes, however it is spelled.
func canonicalName(tok string) string {
	tok = strings.TrimPrefix(tok, `\`)
	if strings.Contains(tok, "/") {
		tok = filepath.Base(tok)
	}
	return strings.ToLower(tok)
}

// rmRecursiveForce reports an rm anywhere in the command — a segment
// head, or behind a wrapper such as xargs or env — whose flags carry
// both recursive and force, in any spelling: `-rf`, `-fr`, `-r -f`,
// `--recursive --force`, `-Rf`.
func rmRecursiveForce(command string) bool {
	for _, seg := range segmentSplit.Split(command, -1) {
		fields := strings.Fields(seg)
		for i, f := range fields {
			if canonicalName(f) != "rm" {
				continue
			}
			recursive, force := false, false
			for _, a := range fields[i+1:] {
				switch {
				case a == "--":
					goto done
				case a == "--recursive":
					recursive = true
				case a == "--force":
					force = true
				case strings.HasPrefix(a, "--"):
					// another long option
				case strings.HasPrefix(a, "-"):
					if strings.ContainsAny(a, "rR") {
						recursive = true
					}
					if strings.Contains(a, "f") {
						force = true
					}
				}
			}
		done:
			if recursive && force {
				return true
			}
		}
	}
	return false
}

// gitBranchForceDelete reports `git branch` deleting with force in any
// spelling — `-D`, `-d -f`, `-f -d`, `-df`, `--delete --force` (ADR-0072
// §4.8: the pattern knew `-D` alone).
func gitBranchForceDelete(command string) bool {
	for _, seg := range segmentSplit.Split(command, -1) {
		fields := strings.Fields(seg)
		for i, f := range fields {
			if canonicalName(f) != "git" {
				continue
			}
			rest := fields[i+1:]
			// Skip git's global options to the subcommand.
			for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
				if (rest[0] == "-C" || rest[0] == "-c") && len(rest) > 1 {
					rest = rest[2:]
					continue
				}
				rest = rest[1:]
			}
			if len(rest) == 0 || rest[0] != "branch" {
				continue
			}
			del, force := false, false
			for _, a := range rest[1:] {
				switch {
				case a == "--":
					goto done
				case a == "--delete":
					del = true
				case a == "--force":
					force = true
				case strings.HasPrefix(a, "--"):
				case strings.HasPrefix(a, "-"):
					if strings.Contains(a, "D") {
						del, force = true, true
					}
					if strings.Contains(a, "d") {
						del = true
					}
					if strings.Contains(a, "f") {
						force = true
					}
				}
			}
		done:
			if del && force {
				return true
			}
		}
	}
	return false
}

// pathAliases are macOS's top-level symlinks. Seatbelt sees the real
// path, so a lexical /tmp/x must be judged as /private/tmp/x — the
// rule tier does no I/O to find that out.
var pathAliases = [][2]string{{"/tmp", "/private/tmp"}, {"/var", "/private/var"}, {"/etc", "/private/etc"}}

func aliasResolve(p string) string {
	for _, a := range pathAliases {
		if p == a[0] || strings.HasPrefix(p, a[0]+"/") {
			return a[1] + strings.TrimPrefix(p, a[0])
		}
	}
	return p
}

// withinAnyRoot reports whether an absolute, cleaned path sits under
// any non-empty root.
func withinAnyRoot(p string, roots ...string) bool {
	for _, r := range roots {
		if r != "" && withinDir(r, p) {
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

// hasCredentialPath reports credential material named anywhere in s —
// a file-tool path or a shell command — by the one rule the sandbox
// profile enforces (sandbox.CredentialPath). For a command every word
// is tried, quotes removed, so `cat "~/.ssh/id_rsa"` is seen.
func hasCredentialPath(s string) bool {
	if sandbox.CredentialPath(s) {
		return true
	}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '"' || r == '\'' || r == '=' || r == ';' || r == '|' || r == '&' || r == '(' || r == ')'
	}) {
		if sandbox.CredentialPath(w) {
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
