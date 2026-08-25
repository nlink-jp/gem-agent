package cmd

import (
	"runtime"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/uitext"
)

func TestVersionLine(t *testing.T) {
	line := versionLine("v9.9.9-test")
	if !strings.HasPrefix(line, "gem-agent v9.9.9-test on ") {
		t.Errorf("version missing: %q", line)
	}
	if !strings.Contains(line, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("platform missing: %q", line)
	}
	if !strings.HasSuffix(line, "\n") {
		t.Errorf("no trailing newline: %q", line)
	}
}

// TestSlashVersion pins /version to the injected version string — the
// slash path must show the build the binary was stamped with, not a
// default.
func TestSlashVersion(t *testing.T) {
	out, isErr, quit := slashOutput("/version", nil, nil, nil, nil, slashReloads{}, nil, nil, "v9.9.9-test", uitext.For(uitext.EN))
	if isErr || quit {
		t.Fatalf("isErr=%v quit=%v", isErr, quit)
	}
	if !strings.Contains(out, "v9.9.9-test") {
		t.Errorf("output lacks version: %q", out)
	}
}
