package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestVersionFlag pins the --version contract: a Homebrew formula's
// `brew test` runs `gem-agent --version`, so the flag must always answer
// with the injected version string (CONVENTIONS.md §Scaffold checklist).
func TestVersionFlag(t *testing.T) {
	rootCmd.Version = "v9.9.9-test"
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--version"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("--version failed: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "v9.9.9-test") {
		t.Fatalf("--version output %q does not contain the version string", got)
	}
}
