// Package sandbox generates macOS Seatbelt (SBPL) profiles and wraps
// commands with sandbox-exec. This is the defense-in-depth layer of
// ADR-0001: file writes are confined to explicitly allowed directories;
// the decision boundary (MITL approval) lives in internal/approve.
package sandbox

import (
	"fmt"
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
func Profile(writeDirs []string) (string, error) {
	if len(writeDirs) == 0 {
		return "", fmt.Errorf("sandbox profile needs at least one writable directory")
	}
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
	b.WriteString(")\n")
	return b.String(), nil
}

// Wrap returns the argv that runs command under sandbox-exec with the
// given profile. The command itself is passed as a single shell command
// string to keep quoting semantics identical to the unsandboxed path.
func Wrap(profile string, shell string, command string) []string {
	return []string{Executable, "-p", profile, shell, "-c", command}
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
