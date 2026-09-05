package sandbox

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Lane is the capability a shell command runs with (ADR-0073). The
// kernel enforces the lane; nothing about the command's text does.
// A model that declares the wrong lane gains nothing: read can only
// tighten the cage, write and operator can only add scrutiny.
type Lane int

const (
	// LaneRead: no writes outside the scratch locations, no network,
	// no preference/sysctl writes, no signals beyond the command's own
	// children, no IPC-capable programs, no credential reads. A command
	// here is non-mutating by construction — the standing of read_file.
	LaneRead Lane = iota
	// LaneWrite: the project and the work directory are writable, minus
	// the files later sessions trust (PersistentFiles); credential
	// reads stay denied. Approved by the ladder or the operator.
	LaneWrite
	// LaneOperator: the write lane with the persistent files writable
	// and credential reads allowed. Only the operator may approve it.
	LaneOperator
)

// String is the lane's name as the tool argument spells it.
func (l Lane) String() string {
	switch l {
	case LaneRead:
		return "read"
	case LaneWrite:
		return "write"
	default:
		return "operator"
	}
}

// ParseLane reads the tool argument; "" is the read lane (a missing
// declaration is not punished — the command runs in the tightest
// cage), and an unknown word is an error the model can act on.
func ParseLane(s string) (Lane, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "read":
		return LaneRead, nil
	case "write":
		return LaneWrite, nil
	case "operator":
		return LaneOperator, nil
	}
	return LaneRead, fmt.Errorf("access must be \"read\", \"write\" or \"operator\", not %q", s)
}

// Spec is what a lane profile is built from.
type Spec struct {
	// ProjectDir and WorkDir are the writable roots, resolved to the
	// real paths Seatbelt matches (ResolveWriteDir). WorkDir may be "".
	ProjectDir string
	WorkDir    string
	// Home is the operator's home directory, for the credential list.
	Home string
	// DenyExec names programs the read lane may not launch, by name or
	// absolute path (DefaultDenyExec plus the operator's additions).
	DenyExec []string
}

// DefaultDenyExec are the IPC-capable programs the read lane refuses to
// launch: each reaches a side effect through a Mach service rather
// than a file write or a socket, so no file or network rule sees it —
// and on this macOS not even an appleevent-send deny stops osascript,
// while a process-exec deny on the binary does (ADR-0073 probe).
// The kernel matches the real binary at exec, so `/usr/bin/osascript`,
// `\osascript` and `OSASCRIPT` are one program here.
var DefaultDenyExec = []string{
	"osascript", "open", "launchctl", "defaults", "security", "pbcopy",
	"shortcuts", "automator", "scutil", "networksetup", "systemsetup",
}

// PersistentFiles returns the SBPL filters for the files later
// sessions trust, anchored under projectDir at any depth: the
// version-control hooks, config and info directory, the instruction
// files (AGENTS.md, CLAUDE.md, AGENT.md, GEMINI.md), the runtime's own
// configuration (.mcp.json, .gem-agent.toml) and the .claude directory.
// The write lane denies writes to them; the operator lane allows them.
// The same set decides the file tools' OperatorOnly verdict (ADR-0072
// §1.4), so the two cannot disagree.
func PersistentFiles(projectDir string) []string {
	p := regexp.QuoteMeta(filepath.Clean(projectDir))
	// `(.*/)?` admits any depth: a nested repository's .git/hooks and a
	// subdirectory's AGENTS.md persist just the same.
	anchor := "^" + p + "/(.*/)?"
	return []string{
		fmt.Sprintf(`(regex #"%s\.git/(hooks|info)(/|$)")`, anchor),
		fmt.Sprintf(`(regex #"%s\.git/config(\.lock)?$")`, anchor),
		fmt.Sprintf(`(regex #"%s\.claude(/|$)")`, anchor),
		fmt.Sprintf(`(regex #"%s(AGENTS|AGENT|CLAUDE|GEMINI)\.md$")`, anchor),
		fmt.Sprintf(`(regex #"%s(\.mcp\.json|\.gem-agent\.toml)$")`, anchor),
	}
}

// PersistentFile reports whether rel — a project-relative, slash-
// separated path — names one of the persistent files (the file-tool
// side of PersistentFiles; one rule, two enforcers).
func PersistentFile(rel string) bool {
	c := strings.Trim(filepath.ToSlash(filepath.Clean(rel)), "/")
	for _, seg := range strings.Split(c, "/") {
		if seg == ".claude" {
			return true
		}
	}
	if i := strings.Index(c+"/", ".git/"); i >= 0 && (i == 0 || c[i-1] == '/') {
		rest := c[i+len(".git/"):]
		if rest == "config" || rest == "config.lock" || rest == "hooks" || rest == "info" ||
			strings.HasPrefix(rest, "hooks/") || strings.HasPrefix(rest, "info/") {
			return true
		}
	}
	switch filepath.Base(c) {
	case "AGENTS.md", "AGENT.md", "CLAUDE.md", "GEMINI.md", ".mcp.json", ".gem-agent.toml":
		return true
	}
	return false
}

// credentialDirs are the home-relative directories whose contents are
// secrets; credentialFiles the home-relative files.
var credentialDirs = []string{".ssh", ".aws", ".kube", ".gnupg", ".config/gcloud", ".config/gh"}
var credentialFiles = []string{".docker/config.json", ".git-credentials", ".bash_history", ".zsh_history", ".netrc", ".npmrc", ".pypirc"}

// credentialNames are file names that are secrets wherever they sit.
var credentialNames = `(\.env(\.[^/]*)?|id_rsa|id_ed25519|id_ecdsa|id_dsa|credentials\.json|[^/]*service-account[^/]*\.json|application_default_credentials\.json)`

// CredentialFilters returns the SBPL file-read filters for credential
// material under home and by name anywhere, and the re-allow for the
// committed .env templates (.env.example and friends are not secrets;
// Seatbelt is last-match-wins, so the allow follows the deny).
func CredentialFilters(home string) (deny []string, allow []string) {
	if home != "" {
		h := filepath.Clean(home)
		for _, d := range credentialDirs {
			deny = append(deny, fmt.Sprintf(`(subpath %s)`, sbplString(filepath.Join(h, d))))
		}
		for _, f := range credentialFiles {
			deny = append(deny, fmt.Sprintf(`(literal %s)`, sbplString(filepath.Join(h, f))))
		}
	}
	deny = append(deny, fmt.Sprintf(`(regex #"/%s$")`, credentialNames))
	allow = append(allow, `(regex #"/\.env\.(example|sample|template|dist)$")`)
	return deny, allow
}

// CredentialPath reports whether p (any spelling: relative, absolute,
// ~-prefixed) names credential material by the same rule the profile
// enforces — the file tools' side of CredentialFilters.
func CredentialPath(p string) bool {
	lower := strings.ToLower(filepath.ToSlash(p))
	for _, d := range credentialDirs {
		if strings.Contains(lower, d+"/") || strings.HasSuffix(lower, d) {
			return true
		}
	}
	for _, f := range credentialFiles {
		if strings.HasSuffix(lower, f) {
			return true
		}
	}
	base := filepath.Base(lower)
	switch {
	case base == ".env" || (strings.HasPrefix(base, ".env.") && !envTemplate(base)):
		return true
	case base == "id_rsa", base == "id_ed25519", base == "id_ecdsa", base == "id_dsa":
		return true
	case base == "credentials.json", base == "application_default_credentials.json":
		return true
	case strings.Contains(base, "service-account") && strings.HasSuffix(base, ".json"):
		return true
	}
	return false
}

func envTemplate(base string) bool {
	for _, s := range []string{".env.example", ".env.sample", ".env.template", ".env.dist"} {
		if base == s {
			return true
		}
	}
	return false
}

// readLaneServices are the Mach services whose lookup the read lane
// denies: the pasteboard and the keychain daemons — side effects that
// travel over IPC, not files.
var readLaneServices = []string{
	"com.apple.pasteboard.1", "com.apple.SecurityServer", "com.apple.securityd.xpc",
}

// resolveDenyExec turns program names into the real binary paths the
// kernel matches. A name not on PATH is kept as a bare literal under
// the usual bin directories, so a program installed later is still
// covered; an absolute path is used as given.
func resolveDenyExec(names []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if filepath.IsAbs(n) {
			add(n)
			continue
		}
		if p, err := exec.LookPath(n); err == nil {
			add(p)
		}
		for _, dir := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/opt/homebrew/bin", "/usr/local/bin"} {
			add(filepath.Join(dir, n))
		}
	}
	sort.Strings(out)
	return out
}

// LaneProfile builds the SBPL profile for one lane (ADR-0073). Every
// lane starts from the write allowlist of ADR-0001 (the project, the
// work directory, the scratch locations, the device sinks); the read
// lane keeps only the scratch part of it and adds the side-effect
// denials; the write lane denies the persistent files; the operator
// lane is the ADR-0001 profile unchanged. Seatbelt evaluates rules
// last-match-wins, so each section's denials follow the allows they
// narrow.
func LaneProfile(lane Lane, spec Spec) (string, error) {
	if spec.ProjectDir == "" {
		return "", fmt.Errorf("sandbox profile needs a project directory")
	}
	writeDirs := []string{}
	if lane != LaneRead {
		writeDirs = append(writeDirs, spec.ProjectDir)
		if spec.WorkDir != "" {
			writeDirs = append(writeDirs, spec.WorkDir)
		}
	}
	writeDirs = append(writeDirs, ScratchDirs()...)
	if len(writeDirs) == 0 {
		// A machine with no scratch location at all: the read lane
		// still needs one allow rule to be well-formed. /dev/null is
		// the literal; the list stays empty.
		writeDirs = nil
	}
	var b strings.Builder
	base, err := profileBody(writeDirs, ScratchFiles())
	if err != nil {
		return "", err
	}
	b.WriteString(base)
	if lane == LaneOperator {
		return b.String(), nil
	}
	// Credential material: unreadable in the read and write lanes.
	deny, allow := CredentialFilters(spec.Home)
	b.WriteString("(deny file-read*\n")
	for _, f := range deny {
		fmt.Fprintf(&b, "    %s\n", f)
	}
	b.WriteString(")\n(allow file-read*\n")
	for _, f := range allow {
		fmt.Fprintf(&b, "    %s\n", f)
	}
	b.WriteString(")\n")
	if lane == LaneWrite {
		b.WriteString("(deny file-write*\n")
		for _, f := range PersistentFiles(spec.ProjectDir) {
			fmt.Fprintf(&b, "    %s\n", f)
		}
		b.WriteString(")\n")
		return b.String(), nil
	}
	// The read lane's side-effect denials. The project and the work
	// directory are denied by name after the scratch allow: a project
	// checked out under /private/tmp sits inside a scratch root, and
	// the read lane must not write it for that reason (live E2E of
	// ADR-0073 — the first probe project was under TMPDIR).
	b.WriteString("(deny file-write*\n")
	fmt.Fprintf(&b, "    (subpath %s)\n", sbplString(filepath.Clean(spec.ProjectDir)))
	if spec.WorkDir != "" {
		fmt.Fprintf(&b, "    (subpath %s)\n", sbplString(filepath.Clean(spec.WorkDir)))
	}
	b.WriteString(")\n")
	b.WriteString("(deny network*)\n")
	b.WriteString("(deny user-preference-write)\n")
	b.WriteString("(deny sysctl-write)\n")
	b.WriteString("(deny signal)\n(allow signal (target self) (target children))\n")
	b.WriteString("(deny lsopen)\n")
	b.WriteString("(deny mach-lookup\n")
	for _, s := range readLaneServices {
		fmt.Fprintf(&b, "    (global-name %s)\n", sbplString(s))
	}
	b.WriteString(")\n")
	if progs := resolveDenyExec(spec.DenyExec); len(progs) > 0 {
		b.WriteString("(deny process-exec\n")
		for _, p := range progs {
			fmt.Fprintf(&b, "    (literal %s)\n", sbplString(p))
		}
		b.WriteString(")\n")
	}
	return b.String(), nil
}

// DeniedHint reports whether a command's output looks like the kernel
// refused it — the read lane's signature — so the tool can tell the
// model which lane to ask for instead of leaving it to guess.
func DeniedHint(output string) bool {
	for _, s := range []string{
		"Operation not permitted", "Permission denied", "Read-only file system",
		"Could not resolve host", "Could not write domain", "Network is unreachable",
		"nodename nor servname provided",
	} {
		if strings.Contains(output, s) {
			return true
		}
	}
	return false
}
