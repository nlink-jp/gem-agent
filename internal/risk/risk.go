// Package risk is the logical (LLM-free) tier of the auto-approve
// escalation ladder (ADR-0004). Classify is a pure function: same input,
// same verdict, no I/O, no model. Its Block tier is the deterministic
// floor that a model verdict cannot override.
package risk

import (
	"fmt"
	"os"
	"path"
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

// scratchFiles are the device sinks the profile allows as literals
// (sandbox.ScratchFiles): a redirect to one is a write nowhere.
var scratchFiles = sandbox.ScratchFiles()

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
	// OperatorOnly marks a Review verdict the model tier may not answer
	// (ADR-0072 §4): the write lands in what later sessions trust —
	// instruction files, the runtime's own configuration — so the
	// party that proposed it cannot also be its judge (ADR-0020 §4,
	// applied beyond memory). The ladder hands such calls straight to
	// the operator.
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

// blockPatterns catches destructive, irreversible, or scope-escaping
// shell commands. Matching is deliberately generous: a false Block costs
// one approval prompt, a false Safe costs the user's data. They run on
// the command with every segment's first word canonicalised
// (normalizeHeads): a path prefix, a leading backslash and letter case
// all invoke the same program. `rm` is judged by rmRecursiveForce,
// which reads the flags however they are spelled.
var blockPatterns = []blockPattern{
	{regexp.MustCompile(`(^|[\s;&|(])(sudo|doas|su)\s`), "privilege escalation"},
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

// credentialPaths are locations whose contents are secrets; touching
// them is never auto-approved, read or write. Directory names match
// with or without a leading `~/` or `/`, so a relative `.ssh/config`
// is as much a credential as `~/.ssh/config`.
var credentialPaths = []string{
	".ssh/", ".aws/", ".kube/", ".gnupg/", ".config/gcloud/", ".config/gh/",
	".docker/config.json", ".git-credentials", ".bash_history", ".zsh_history",
	".env", "id_rsa", "id_ed25519",
	"credentials.json", "service-account", ".netrc", ".npmrc", ".pypirc",
	"application_default_credentials",
}

// readOnlyCommands are shell entry points that cannot mutate state in
// their plain form. Only a command built exclusively from these is
// Safe, and only when none is invoked in a form mutatingUse recognises
// as a write or a program launch (`find -delete`, `sed -i`, `env cmd`).
// `tee` and `xargs` are not here: one writes by definition, the other
// runs whatever it is given.
var readOnlyCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true, "file": true,
	"stat": true, "pwd": true, "echo": true, "which": true, "type": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "find": true, "fd": true,
	"sort": true, "uniq": true, "cut": true, "tr": true, "sed": true, "awk": true,
	"jq": true, "yq": true, "diff": true, "date": true, "env": true, "uname": true,
	"df": true, "du": true, "ps": true, "top": true, "id": true, "whoami": true,
	"true": true, "false": true, "basename": true, "dirname": true, "realpath": true,
	"column": true, "nl": true, "seq": true,
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

	case "shell_exec":
		command, _ := args["command"].(string)
		return classifyCommand(command, projectDir, workDir)

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

func classifyPath(p, projectDir, workDir string) (Verdict, bool) {
	if p == "" {
		return Verdict{Tier: Review, Reason: "missing path argument"}, true
	}
	if hasCredentialPath(p) {
		return Verdict{Tier: Block, Reason: "path looks like credential material"}, true
	}
	if filepath.IsAbs(p) {
		if !withinAnyRoot(filepath.Clean(p), projectDir, workDir) {
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
// The instruction files (AGENTS.md, CLAUDE.md, AGENT.md, GEMINI.md, a
// skill under .claude/) and the runtime's own configuration
// (.mcp.json, .gem-agent.toml, .claude/) are Review that only the
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
	if c == ".git" || strings.HasPrefix(c, ".git/") || strings.Contains(c, "/.git/") || strings.HasSuffix(c, "/.git") {
		return Verdict{Tier: Block, Reason: "writes inside the version-control internals — hooks and config there run outside the sandbox on the next git command"}, true
	}
	persistent := Verdict{Tier: Review, OperatorOnly: true,
		Reason: "changes the instructions or configuration later sessions trust — the operator decides, not the model tier"}
	if c == ".claude" || strings.HasPrefix(c, ".claude/") || strings.Contains(c, "/.claude/") {
		return persistent, true
	}
	switch path.Base(c) {
	case ".mcp.json", ".gem-agent.toml", "AGENTS.md", "AGENT.md", "CLAUDE.md", "GEMINI.md":
		return persistent, true
	}
	return Verdict{}, false
}

func classifyCommand(command, projectDir, workDir string) Verdict {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return Verdict{Tier: Review, Reason: "empty command"}
	}
	norm := normalizeHeads(trimmed)
	if rmRecursiveForce(norm) {
		return Verdict{Tier: Block, Reason: "recursive force delete"}
	}
	for _, p := range blockPatterns {
		if p.re.MatchString(norm) {
			return Verdict{Tier: Block, Reason: p.reason}
		}
	}
	if hasCredentialPath(trimmed) {
		return Verdict{Tier: Block, Reason: "references credential material"}
	}
	if v, ok := redirectTarget(trimmed, projectDir, workDir); ok {
		return v
	}
	// Command substitution hides the real target from any pattern rule
	// (the agent-skeleton finding) — never Safe, let the model look.
	// Process substitution is the same hole spelled with a paren:
	// `cat <(rm -r x)` runs the inner command under bash.
	if strings.Contains(trimmed, "$(") || strings.Contains(trimmed, "`") ||
		strings.Contains(trimmed, "${") || strings.Contains(trimmed, "eval ") ||
		strings.Contains(trimmed, "<(") || strings.Contains(trimmed, ">(") {
		return Verdict{Tier: Review, Reason: "dynamic command construction"}
	}
	// A read-only walk is still a walk: the sandbox denies writes only,
	// so a `find /` reaches every mount. Never Safe outside the roots;
	// the model tier weighs the cost (ADR-0070 §3).
	if walksOutsideRoots(trimmed, projectDir, workDir) {
		return Verdict{Tier: Review, Reason: outsideRootsReason("walks the filesystem", workDir)}
	}
	if readOnlyCommand(trimmed) {
		return Verdict{Tier: Safe, Reason: "read-only shell command"}
	}
	// A command that can write and names a persistent file (ADR-0072
	// §1.4) gets that file's verdict whatever the command is — `cp`,
	// `tee`, `install`, `sed -i`, `python3 -c` — not only a redirect
	// (review after v0.68.2). Read-only commands were judged above:
	// `cat .git/config` stays a read.
	if v, ok := persistentTokens(trimmed, projectDir); ok {
		return v
	}
	return Verdict{Tier: Review, Reason: "shell command with unclear effects"}
}

// persistentTokens scans every word of a writing command for a path
// into the version-control internals or an instruction/configuration
// file, relative to the project or absolute inside it.
func persistentTokens(command, projectDir string) (Verdict, bool) {
	for _, seg := range segmentSplit.Split(command, -1) {
		for _, tok := range strings.Fields(seg) {
			tok = strings.Trim(tok, `"'`)
			if tok == "" || strings.HasPrefix(tok, "-") {
				continue
			}
			if i := strings.Index(tok, "="); i > 0 && !strings.ContainsAny(tok[:i], "/.") {
				tok = tok[i+1:] // VAR=path, --flag=path
			}
			if v, ok := persistentTarget(projectRelative(tok, projectDir)); ok {
				return v, true
			}
		}
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

// writableRoots are the places a shell command may write: the project,
// the session work directory, and the sandbox's scratch roots.
func writableRoots(projectDir, workDir string) []string {
	return append([]string{projectDir, workDir}, scratchRoots...)
}

// redirectRe finds the target of an output redirection, absolute or
// relative; a descriptor duplication (`2>&1`) has no path and does not
// match.
var redirectRe = regexp.MustCompile(`>>?\s*("?)([^\s"';|&<>()]+)`)

// redirectTarget judges where a command's output redirections land: an
// absolute path outside the writable roots is Block (the sandbox would
// deny it, but escalating explains why), and a target that is one of
// the persistent files persistentTarget names gets that verdict —
// `echo x > AGENTS.md` is the same write as write_file's.
func redirectTarget(command, projectDir, workDir string) (Verdict, bool) {
	for _, m := range redirectRe.FindAllStringSubmatch(command, -1) {
		target := m[2]
		if strings.HasPrefix(target, "&") {
			continue // >&2
		}
		if filepath.IsAbs(target) {
			raw := filepath.Clean(target)
			if isScratchFile(raw) {
				continue // a device sink the profile allows as a literal
			}
			if !withinAnyRoot(aliasResolve(raw), writableRoots(projectDir, workDir)...) {
				return Verdict{Tier: Block, Reason: outsideRootsReason("redirects output", workDir)}, true
			}
			if v, ok := persistentTarget(projectRelative(raw, projectDir)); ok {
				return v, true
			}
			continue
		}
		if v, ok := persistentTarget(target); ok {
			return v, true
		}
	}
	return Verdict{}, false
}

// isScratchFile reports a device sink the profile allows as a literal.
func isScratchFile(p string) bool {
	for _, f := range scratchFiles {
		if p == f {
			return true
		}
	}
	return false
}

// pathAliases are macOS's top-level symlinks. Seatbelt sees the real
// path, so a lexical /tmp/x must be judged as /private/tmp/x — the
// rule tier does no I/O to find that out (review round 4: `> /tmp/x`
// was a false Block with an untrue reason, the ADR-0070 §2 drift).
var pathAliases = [][2]string{{"/tmp", "/private/tmp"}, {"/var", "/private/var"}, {"/etc", "/private/etc"}}

func aliasResolve(p string) string {
	for _, a := range pathAliases {
		if p == a[0] || strings.HasPrefix(p, a[0]+"/") {
			return a[1] + strings.TrimPrefix(p, a[0])
		}
	}
	return p
}

// walkers are read-only commands whose work is a tree walk from a
// starting point given as an argument; recursiveWhenFlagged walk only
// with a recursive flag (`grep -r`, `ls -R`).
var walkers = map[string]bool{"find": true, "fd": true, "du": true, "rg": true}
var recursiveWhenFlagged = map[string]bool{"grep": true, "egrep": true, "fgrep": true, "ls": true}

// walksOutsideRoots reports whether any segment of the command walks a
// tree from `/`, `~`, `~user`, a `$VAR`, a `..`-relative path, or an
// absolute path outside the writable roots (ADR-0070 §3). Other
// relative starting points stay inside the project — the command's
// working directory.
func walksOutsideRoots(command, projectDir, workDir string) bool {
	roots := writableRoots(projectDir, workDir)
	for _, seg := range segmentSplit.Split(command, -1) {
		fields := strings.Fields(strings.TrimSpace(seg))
		if len(fields) == 0 {
			continue
		}
		head := canonicalName(fields[0])
		walks := walkers[head] || (recursiveWhenFlagged[head] && hasRecursiveFlag(fields[1:]))
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
			case strings.HasPrefix(f, "~"):
				return true // ~user: another account's tree
			case strings.HasPrefix(f, "$"):
				return true // $HOME, $PWD/..: unknown until the shell runs
			case !filepath.IsAbs(f):
				if c := filepath.Clean(f); c == ".." || strings.HasPrefix(c, "../") {
					return true
				}
				continue
			}
			if !withinAnyRoot(aliasResolve(filepath.Clean(f)), roots...) {
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

// devNullRedirect matches redirections that write nowhere: to a
// device sink (sandbox.ScratchFiles, /dev/fd/N), or one descriptor
// onto another (`2>&1`). They do not make a read-only command a
// writing one (ADR-0070 §2).
var devNullRedirect = regexp.MustCompile(`(\d*>>?|&>)\s*/dev/(null|zero|stdout|stderr|stdin|fd/\d+)\b|\d>&\d`)

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

// readOnlyCommand reports whether every segment of a pipeline/sequence
// starts with a known read-only command, in a form that neither writes
// nor launches a program, and carries no redirection.
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
		name := canonicalName(head)
		if !readOnlyCommands[name] || mutatingUse(name, fields[1:]) {
			return false
		}
	}
	return true
}

// awkSystemRe finds awk's system() call, with any whitespace before
// the paren.
var awkSystemRe = regexp.MustCompile(`\bsystem\s*\(`)

// sedWriteCmd finds a `w`/`W` command in a sed script — `/re/w file`,
// `1,5w file`, the `s///w file` flag — which writes a file whatever
// the options say. Matched against the script and file arguments
// joined, since the shell's quoting split them into fields.
var sedWriteCmd = regexp.MustCompile(`(^|[;{}\n])\s*[^a-zA-Z]*[wW]\s|/[wW]\s`)

// mutatingUse reports whether a read-only command is invoked in a form
// that writes or runs a program: the flag turns a search into a rewrite
// (`sed -i`, `sort -o`, `yq -i`), a walk into a launcher (`find -exec`,
// `fd -x`, `rg --pre`), or a printer into a runner (`env cmd`,
// `awk 'system(…)'`). Review, not Block: the model weighs it.
func mutatingUse(name string, args []string) bool {
	shortHas := func(a string, letters string) bool {
		return strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, letters)
	}
	switch name {
	case "find":
		for _, a := range args {
			switch a {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
				return true
			}
		}
	case "fd":
		for _, a := range args {
			if a == "--exec" || a == "--exec-batch" || shortHas(a, "xX") {
				return true
			}
		}
	case "rg":
		for _, a := range args {
			if a == "--pre" || strings.HasPrefix(a, "--pre=") {
				return true
			}
		}
	case "sed":
		var script []string
		for _, a := range args {
			if strings.HasPrefix(a, "--in-place") || shortHas(a, "i") {
				return true
			}
			if !strings.HasPrefix(a, "-") {
				script = append(script, strings.Trim(a, `"'`))
			}
		}
		if sedWriteCmd.MatchString(strings.Join(script, " ")) {
			return true
		}
	case "awk":
		for _, a := range args {
			if a == "-f" || strings.HasPrefix(a, "-i") {
				return true
			}
		}
		// `system (…)` is valid awk: the call is matched across the
		// joined script, not per whitespace token (review after v0.68.0).
		if awkSystemRe.MatchString(strings.Join(args, " ")) {
			return true
		}
	case "sort":
		for _, a := range args {
			if strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "--output") {
				return true
			}
		}
	case "yq":
		for _, a := range args {
			if a == "-i" || a == "--inplace" || shortHas(a, "i") {
				return true
			}
		}
	case "env":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
				return true // env runs the named program
			}
		}
	}
	return false
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
