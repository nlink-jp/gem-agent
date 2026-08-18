package sandbox

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProfileContainsResolvedDirs(t *testing.T) {
	p, err := Profile([]string{"/private/tmp/proj", "/private/tmp/scratch"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"(deny file-write*)",
		`(subpath "/private/tmp/proj")`,
		`(subpath "/private/tmp/scratch")`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("profile missing %q:\n%s", want, p)
		}
	}
}

func TestProfileRejectsRelativeDir(t *testing.T) {
	if _, err := Profile([]string{"relative/path"}); err == nil {
		t.Fatal("relative dir should be rejected")
	}
}

func TestProfileRejectsEmpty(t *testing.T) {
	if _, err := Profile(nil); err == nil {
		t.Fatal("empty write list should be rejected")
	}
}

func TestSBPLStringEscaping(t *testing.T) {
	got := sbplString(`/path/with"quote\back`)
	want := `"/path/with\"quote\\back"`
	if got != want {
		t.Errorf("sbplString = %s, want %s", got, want)
	}
}

// TestSandboxExecEnforcement runs real sandbox-exec (darwin only): a write
// inside the allowed project dir must succeed, a write outside must fail.
// This is the load-bearing test — the profile text being well-formed means
// nothing unless Seatbelt actually enforces it.
func TestSandboxExecEnforcement(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	inside, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	profile, err := Profile([]string{inside})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run := func(command string) error {
		argv := Wrap(profile, "/bin/bash", command)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		return cmd.Run()
	}

	if err := run("echo ok > " + filepath.Join(inside, "allowed.txt")); err != nil {
		t.Errorf("write inside allowed dir should succeed: %v", err)
	}
	if err := run("echo bad > " + filepath.Join(outside, "denied.txt")); err == nil {
		t.Error("write outside allowed dir should be denied by sandbox-exec")
	}
	// Reads outside the write allowlist must still work (allow default).
	if err := run("ls / > " + filepath.Join(inside, "ls.txt")); err != nil {
		t.Errorf("read outside + write inside should succeed: %v", err)
	}
}
