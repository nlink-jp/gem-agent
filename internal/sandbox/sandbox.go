// Package sandbox generates macOS Seatbelt (SBPL) profiles and wraps
// commands with sandbox-exec. This is the defense-in-depth layer of
// ADR-0001: file writes are confined to explicitly allowed directories;
// the decision boundary (MITL approval) lives in internal/approve.
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Executable is the sandbox-exec binary path. macOS ships it in /usr/bin;
// Apple marks it deprecated but it remains the de facto standard for
// CLI agents (see ADR-0001 for the recorded platform risk).
const Executable = "/usr/bin/sandbox-exec"

// Profile builds an SBPL profile that allows everything except file
// writes, then re-allows writes under the given directories only.
// Seatbelt rule evaluation is last-match-wins, so the allow-subpath rules
// override the blanket write deny.
//
// writeDirs must be absolute; each is resolved through symlinks by the
// caller where the path exists (Seatbelt matches on real paths — /tmp
// must arrive as /private/tmp or the allow rule never fires).
//
// writeFiles are single paths allowed as literals — the device sinks
// (/dev/null and friends). They were a `(subpath "/dev")` until the
// review after v0.68.2: that allowed every character device, the
// operator's terminal included.
func Profile(writeDirs []string, writeFiles []string) (string, error) {
	if len(writeDirs) == 0 {
		return "", fmt.Errorf("sandbox profile needs at least one writable directory")
	}
	return profileBody(writeDirs, writeFiles)
}

// profileBody is Profile without the non-empty check: the read lane of
// ADR-0073 may legitimately allow no directory at all.
func profileBody(writeDirs []string, writeFiles []string) (string, error) {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	b.WriteString("(deny file-write*)\n")
	b.WriteString("(allow file-write*\n")
	for _, dir := range writeDirs {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("sandbox write dir must be absolute: %q", dir)
		}
		fmt.Fprintf(&b, "    (subpath %s)\n", sbplString(filepath.Clean(dir)))
	}
	for _, f := range writeFiles {
		if !filepath.IsAbs(f) {
			return "", fmt.Errorf("sandbox write file must be absolute: %q", f)
		}
		fmt.Fprintf(&b, "    (literal %s)\n", sbplString(filepath.Clean(f)))
	}
	b.WriteString(")\n")
	return b.String(), nil
}

// Wrap returns the argv that runs command under sandbox-exec with the
// given profile. The command itself is passed as a single shell command
// string to keep quoting semantics identical to the unsandboxed path.
func Wrap(profile string, shell string, command string) []string {
	return []string{Executable, "-p", profile, shell, "-c", command}
}

// ScratchDirs returns the scratch directories shell tools legitimately
// write to — TMPDIR, /private/tmp, and /dev/fd (descriptor
// duplication) — resolved to the real paths Seatbelt matches against
// (/tmp arrives as /private/tmp). It is the one list (ADR-0070 §2):
// the profile allows exactly these, and the rule tier reads the same
// slice, so its "outside the writable roots" can never disagree with
// what Seatbelt denies. A location that does not exist on this
// machine is left out.
func ScratchDirs() []string {
	var dirs []string
	for _, d := range []string{os.TempDir(), "/private/tmp", "/dev/fd"} {
		if resolved, err := ResolveWriteDir(d); err == nil {
			dirs = append(dirs, resolved)
		}
	}
	return dirs
}

// ScratchFiles returns the device sinks a shell command may write to
// as single files: /dev/null and its kin. The profile allows them as
// literals and the rule tier reads the same list, so `2>/dev/null` is
// a write the sandbox allows while `> /dev/tty` — the operator's
// terminal — is not (review after v0.68.2: `/dev` as a whole subpath
// allowed every character device).
func ScratchFiles() []string {
	return []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/stdout", "/dev/stderr", "/dev/stdin"}
}

// Available reports whether sandbox-exec can apply a profile here. A
// process already inside a Seatbelt sandbox cannot nest one
// (sandbox_apply: Operation not permitted, exit 71); tests skip on it.
func Available() error {
	if _, err := os.Stat(Executable); err != nil {
		return err
	}
	return exec.Command(Executable, "-p", "(version 1)(allow default)", "/usr/bin/true").Run()
}

// ResolveWriteDir resolves a directory to the real path Seatbelt matches
// against (absolute + symlinks evaluated). The directory must exist.
func ResolveWriteDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks for %s: %w", abs, err)
	}
	return real, nil
}

// sbplString quotes a string as an SBPL literal. SBPL string syntax is
// double-quoted with backslash escapes; paths containing quotes or
// backslashes must not be able to break out of the literal and inject
// profile rules.
func sbplString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
