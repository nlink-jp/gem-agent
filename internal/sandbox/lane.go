package sandbox

import (
	"fmt"
	"net"
	"os"
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
	// LaneRead: no writes outside its private scratch, no network, no
	// IPC, no preference writes, no signals beyond the command's own
	// children, no credential or private-library reads. What it may
	// change is its scratch directory and the device sinks — the
	// standing of read_file.
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
	// Defence in depth behind the capability denials, not their basis.
	DenyExec []string
	// ReadScratch is the session-private directory the read lane may
	// write; the runner points TMPDIR at it. Empty means the read lane
	// may write nothing but the device sinks.
	ReadScratch string
	// PersistentParents are the directories that contain a persistent
	// file (and their ancestors below the project root): the write lane
	// denies operations on those directory entries themselves — rename,
	// unlink — so a nested AGENTS.md cannot be replaced by swapping its
	// parent (ADR-0074 §2). Writes inside them stay allowed.
	PersistentParents []string
}

// DefaultDenyExec are the IPC-capable programs the read lane refuses to
// launch — defence in depth behind readLaneDenies, which already deny
// the Mach, Apple Event and launch-services capabilities these programs
// use. The list is not what makes the lane safe (design review of
// ADR-0073: a program list is a regex by another name); it makes a
// refusal legible (`Operation not permitted` at exec, not a crash
// inside the program). The kernel matches the real binary at exec, so
// `/usr/bin/osascript`, `\osascript` and `OSASCRIPT` are one program.
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
		// The .git entry itself: renaming it away and back with planted
		// hooks, or replacing it with a `gitdir:` pointer file, moves
		// the hooks the operator's next commit runs (review F-02).
		fmt.Sprintf(`(regex #"%s\.git$")`, anchor),
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
	if filepath.Base(c) == ".git" {
		return true
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
var credentialDirs = []string{
	".ssh", ".aws", ".kube", ".gnupg", ".config/gcloud", ".config/gh",
	// Agent and cloud token stores (review F-07, V5).
	".gemini", ".codex", ".claude", ".azure", ".terraform.d", "Library/Keychains",
}

// homeOnlyDirs are credentialDirs names that also occur inside projects
// with another meaning (`.claude/` holds a project's skills): the path
// rule matches them only under a home directory, as the profile does.
var homeOnlyDirs = map[string]bool{".claude": true, ".gemini": true, ".codex": true}

var homePrefixRe = regexp.MustCompile(`^(~|/users/[^/]+|/home/[^/]+|/var/root)/`)
var credentialFiles = []string{
	".docker/config.json", ".git-credentials", ".bash_history", ".zsh_history",
	".netrc", ".npmrc", ".pypirc", ".vault-token", ".claude.json",
}

// credentialNames are file names that are secrets wherever they sit.
var credentialNames = `(\.env(\.[^/]*)?|id_rsa|id_ed25519|id_ecdsa|id_dsa|\.?credentials\.json|[^/]*service-account[^/]*\.json|application_default_credentials\.json)`

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
		if homeOnlyDirs[d] {
			if m := homePrefixRe.FindString(lower); m != "" {
				rest := lower[len(m):]
				if rest == strings.ToLower(d) || strings.HasPrefix(rest, strings.ToLower(d)+"/") {
					return true
				}
			}
			continue
		}
		d = strings.ToLower(d)
		if strings.Contains(lower, d+"/") || strings.HasSuffix(lower, d) {
			return true
		}
	}
	for _, f := range credentialFiles {
		if strings.HasSuffix(lower, strings.ToLower(f)) {
			return true
		}
	}
	base := filepath.Base(lower)
	switch {
	case base == ".env" || (strings.HasPrefix(base, ".env.") && !envTemplate(base)):
		return true
	case base == "id_rsa", base == "id_ed25519", base == "id_ecdsa", base == "id_dsa":
		return true
	case base == "credentials.json", base == ".credentials.json", base == "application_default_credentials.json":
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

// readLaneDenies are the capability families the read lane denies
// wholesale (ADR-0073 §1, revised after design review): the basis of
// "non-mutating" is the kernel's list of side-effect operations, not a
// list of programs. Each entry is an SBPL operation; together with
// file-write* (below) and network* they cover files, sockets, Mach and
// POSIX IPC, Apple Events, devices, launch services, preferences, NVRAM
// and job control. Probed on macOS 26: git, go, python, node, swift,
// perl, ruby, make, tar, xcodebuild -version and codesign still run
// under all of them; `ps` does not run under any Seatbelt profile at
// all (pre-existing since ADR-0001), and `sysctl-write` was left out
// because uname and node's allocator use it.
var readLaneDenies = []string{
	"network*", "mach-lookup", "mach-register", "appleevent-send", "ipc-posix*",
	"iokit-open", "system-socket", "nvram*", "job-creation",
	"distributed-notification-post", "user-preference-write", "lsopen",
}

// readLaneReadDenies are the trees a read-lane command may not read
// beyond the credential list (design review V2/V5): external and
// network mounts — a walk from `/` reaching every share was the cost
// ADR-0070 §3 named — and the user's Library, which holds mail,
// messages, cookies and token stores; the toolchain directories under
// it are re-allowed (readLaneReadAllows).
var readLaneReadDenies = []string{"/Volumes", "/Network"}
var readLaneLibraryAllows = []string{"Library/Caches", "Library/Developer", "Library/Python", "Library/Go"}

// Enforcement is what the runtime could establish about the sandbox
// (ADR-0073 §5): Confined means sandbox-exec applies the lane profiles
// to every shell command; ReadLane means the read lane's denials were
// verified on this machine at startup (VerifyReadLane), so a read-lane
// call may run unasked. Confined && !ReadLane gates every call as a
// write-lane call; !Confined is the unconfined mode — every call is
// the operator's alone.
type Enforcement struct {
	Confined bool
	ReadLane bool
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
	var writeDirs []string
	if lane == LaneRead {
		// What a read-lane command may change: its session-private
		// scratch directory (TMPDIR is pointed there) and the device
		// sinks — never the shared /private/tmp or the user's TMPDIR,
		// which other processes read (design review: "non-mutating"
		// must say what change is permitted).
		if spec.ReadScratch != "" {
			writeDirs = append(writeDirs, spec.ReadScratch)
		}
		// Descriptors the command already holds (`> /dev/fd/1`,
		// `>(…)`): no new reach (review F-06).
		writeDirs = append(writeDirs, "/dev/fd")
	} else {
		writeDirs = append(writeDirs, spec.ProjectDir)
		if spec.WorkDir != "" {
			writeDirs = append(writeDirs, spec.WorkDir)
		}
		for _, d := range ScratchDirs() {
			// A scratch root that contains the project (a checkout under
			// /private/tmp) would let the write lane rename the project
			// itself away and back with new instruction files (review
			// F-03/W19): the project's parent is never writable.
			if within(d, spec.ProjectDir) {
				continue
			}
			writeDirs = append(writeDirs, d)
		}
	}
	var b strings.Builder
	base, err := profileBody(writeDirs, ScratchFiles())
	if err != nil {
		return "", err
	}
	b.WriteString(base)
	// Every lane: the operator's terminal is not the command's. A child
	// that keeps the controlling tty can inject keystrokes with
	// TIOCSTI on a read-only /dev/tty (review F-01 — write was denied,
	// ioctl and read were not); the runner also puts the command in
	// its own session, so /dev/tty does not resolve at all.
	b.WriteString("(deny file-ioctl\n    (literal \"/dev/tty\")\n    (regex #\"^/dev/ttys[0-9]+$\")\n    (literal \"/dev/ptmx\")\n)\n")
	b.WriteString("(deny file-read*\n    (literal \"/dev/tty\")\n    (regex #\"^/dev/ttys[0-9]+$\")\n)\n")
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
		for _, p := range spec.PersistentParents {
			if within(spec.ProjectDir, p) && filepath.Clean(p) != filepath.Clean(spec.ProjectDir) {
				fmt.Fprintf(&b, "    (literal %s)\n", sbplString(filepath.Clean(p)))
			}
		}
		b.WriteString(")\n")
		return b.String(), nil
	}
	// The read lane's side-effect denials, by capability.
	for _, op := range readLaneDenies {
		fmt.Fprintf(&b, "(deny %s)\n", op)
	}
	// The shells' here-document directory is a shared allow, so the
	// project and the work directory are denied by name AFTER it — a
	// project or a state directory placed under /private/tmp or
	// /private/var/tmp sits inside a shared root (live E2E; final
	// review R3) — and the private scratch, which lives inside the work
	// directory, is re-allowed last. Seatbelt is last-match-wins.
	b.WriteString("(allow file-write* (subpath \"/private/var/tmp\"))\n")
	b.WriteString("(deny file-write*\n")
	fmt.Fprintf(&b, "    (subpath %s)\n", sbplString(filepath.Clean(spec.ProjectDir)))
	if spec.WorkDir != "" {
		fmt.Fprintf(&b, "    (subpath %s)\n", sbplString(filepath.Clean(spec.WorkDir)))
	}
	b.WriteString(")\n")
	if spec.ReadScratch != "" {
		fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", sbplString(filepath.Clean(spec.ReadScratch)))
	}
	// Reach: external/network mounts and the user's Library are not
	// the project (design review V2). The system shells (bash 3.2, zsh,
	// sh) create their here-document files under /private/var/tmp
	// whatever TMPDIR says (review F-05; probed: no name pattern
	// narrower than the directory lets them through), so that one
	// shared, sticky directory stays writable — the documented
	// exception to "nothing shared".
	b.WriteString("(deny file-read*\n")
	for _, d := range readLaneReadDenies {
		fmt.Fprintf(&b, "    (subpath %s)\n", sbplString(d))
	}
	if spec.Home != "" {
		fmt.Fprintf(&b, "    (subpath %s)\n", sbplString(filepath.Join(filepath.Clean(spec.Home), "Library")))
	}
	b.WriteString(")\n")
	if spec.Home != "" {
		b.WriteString("(allow file-read*\n")
		for _, d := range readLaneLibraryAllows {
			fmt.Fprintf(&b, "    (subpath %s)\n", sbplString(filepath.Join(filepath.Clean(spec.Home), d)))
		}
		b.WriteString(")\n")
	}
	b.WriteString("(deny signal)\n(allow signal (target self) (target children))\n")
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
	// The tail only: a refusal ends the output; a "permission denied"
	// quoted in the middle of a log the command printed is not one
	// (review F-10).
	if len(output) > 400 {
		output = output[len(output)-400:]
	}
	lower := strings.ToLower(output)
	for _, s := range []string{
		// Go and Python spell the errno in lower case ("open x: operation
		// not permitted"), bash and the BSD tools capitalise it.
		"operation not permitted", "permission denied", "read-only file system",
		"could not resolve host", "could not write domain", "network is unreachable",
		"nodename nor servname provided",
	} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// within reports whether p is dir or under it.
func within(dir, p string) bool {
	dir, p = filepath.Clean(dir), filepath.Clean(p)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// VerifyReadLane runs the read-lane profile against probes that must
// fail — a project write, a write into the shared temporary directory,
// a socket connect to a listener this process opens, a signal to this
// process, opening the terminal, launching a denied program — and two
// that must succeed (a scratch write, running a program), under real
// sandbox-exec (ADR-0073 §5). Each must-fail probe is first run
// without the sandbox and must succeed there, so a probe that fails
// for an unrelated reason cannot count as a denial (review F-04/V1:
// `kill -0 1` and a connect to a closed port fail for any user). A
// read-lane call runs unasked only where this passed at startup. The
// returned error names the first expectation that failed.
func VerifyReadLane(profile string, spec Spec) error {
	if err := Available(); err != nil {
		return fmt.Errorf("sandbox-exec cannot apply a profile here: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("cannot open a loopback listener for the network probe: %w", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	// The probe targets are files this process creates exclusively
	// (O_EXCL, random names) and removes afterwards: a fixed name could
	// collide with the project's own file or follow a planted symlink
	// (final review R1 — the control run overwrote a link's target).
	projectFile, err := os.CreateTemp(spec.ProjectDir, ".gem-agent-lane-probe-*")
	if err != nil {
		return fmt.Errorf("cannot create a probe file in the project: %w", err)
	}
	projectProbe := projectFile.Name()
	_ = projectFile.Close()
	sharedFile, err := os.CreateTemp("/private/tmp", ".gem-agent-lane-probe-*")
	if err != nil {
		_ = os.Remove(projectProbe)
		return fmt.Errorf("cannot create a probe file in /private/tmp: %w", err)
	}
	sharedProbe := sharedFile.Name()
	_ = sharedFile.Close()
	cleanup := func() {
		_ = os.Remove(projectProbe)
		_ = os.Remove(sharedProbe)
	}
	defer cleanup()
	run := func(sandboxed bool, command string) error {
		var cmd *exec.Cmd
		if sandboxed {
			argv := Wrap(profile, "/bin/bash", command)
			cmd = exec.Command(argv[0], argv[1:]...)
		} else {
			cmd = exec.Command("/bin/bash", "-c", command)
		}
		if spec.ReadScratch != "" {
			cmd.Env = append(os.Environ(), "TMPDIR="+spec.ReadScratch)
		}
		return cmd.Run()
	}
	mustFail := []struct{ what, command string }{
		{"a write into the project", "echo x > " + shellQuote(projectProbe)},
		{"a write into the shared temporary directory", "echo x > " + shellQuote(sharedProbe)},
		{"a socket connect", fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d", port)},
		{"a signal to its parent process", "kill -0 $PPID"},
	}
	if len(spec.DenyExec) > 0 {
		mustFail = append(mustFail, struct{ what, command string }{"launching a denied program", "/usr/bin/osascript -e 'return 1'"})
	}
	for _, m := range mustFail {
		// The control: the same probe succeeds outside the sandbox, so a
		// failure inside is the sandbox's doing. The write probes write
		// into the files created above, never anywhere else.
		if err := run(false, m.command); err != nil {
			return fmt.Errorf("the control run of %s failed outside the sandbox (%v) — the probe cannot tell a denial from an unrelated failure", m.what, err)
		}
		if err := run(true, m.command); err == nil {
			return fmt.Errorf("the read lane allowed %s", m.what)
		}
	}
	// The terminal: no control — the runner detaches the command from
	// the operator's terminal, so the open must fail whatever the
	// environment (review F-01).
	if err := run(true, "exec 3</dev/tty"); err == nil {
		return fmt.Errorf("the read lane allowed opening the terminal")
	}
	if spec.ReadScratch != "" {
		if err := run(true, `echo x > "$TMPDIR/.gem-agent-lane-probe" && rm "$TMPDIR/.gem-agent-lane-probe"`); err != nil {
			return fmt.Errorf("the read lane cannot write its own scratch directory: %w", err)
		}
	}
	if err := run(true, "/usr/bin/true"); err != nil {
		return fmt.Errorf("the read lane cannot run a program: %w", err)
	}
	return nil
}

// shellQuote single-quotes s for bash.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// secretEnvRe names environment variables a read-lane command does not
// inherit: the operator's exported tokens, keys and passwords. The
// read lane runs unasked and its output reaches the model, so what a
// bare `env` prints is bounded here (review F-07). Everything else —
// PATH, HOME, LANG, the toolchain variables — passes through.
var secretEnvRe = regexp.MustCompile(`(?i)(token|secret|passw|api[_-]?key|credential|private[_-]?key|access[_-]?key|auth)`)

// ScrubEnv returns env without the variables secretEnvRe names.
func ScrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if strings.HasPrefix(name, "GEMAGENT_") || !secretEnvRe.MatchString(name) {
			out = append(out, kv)
		}
	}
	return out
}
