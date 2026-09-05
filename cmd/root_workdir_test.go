package cmd

import (
	"context"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// A shell command told to put its output in $GEMAGENT_WORK_DIR has to
// be able to: the work directory is a writable root of the sandbox
// profile, not just a path the model is told about (ADR-0058).
func TestSandboxAllowsWritesToTheWorkDirectory(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here (nested sandbox?): %v", err)
	}
	project := t.TempDir()
	work := t.TempDir()
	// NOT t.TempDir(): on macOS that is under TMPDIR, which the profile
	// already allows. A test for "outside" has to be outside.
	outside := outsideEveryRoot(t)

	execFn, err := buildExecFn(true, project, work, nil)
	if err != nil {
		t.Skipf("sandbox unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(command string) error { return execFn(ctx, command, sandbox.LaneWrite).Run() }

	if err := run("echo ok > " + filepath.Join(work, "out.txt")); err != nil {
		t.Errorf("a write to the work directory was denied: %v", err)
	}
	if err := run("echo ok > " + filepath.Join(project, "out.txt")); err != nil {
		t.Errorf("a write to the project was denied: %v", err)
	}
	// Adding a root must not widen anything else.
	if err := run("echo bad > " + filepath.Join(outside, "out.txt")); err == nil {
		t.Error("a write outside both roots was allowed")
	}
}

// With no work directory the profile is exactly what it was before.
func TestSandboxWithoutAWorkDirectoryIsUnchanged(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here (nested sandbox?): %v", err)
	}
	project := t.TempDir()
	outside := outsideEveryRoot(t)
	execFn, err := buildExecFn(true, project, "", nil)
	if err != nil {
		t.Skipf("sandbox unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := execFn(ctx, "echo bad > "+filepath.Join(outside, "out.txt"), sandbox.LaneWrite).Run(); err == nil {
		t.Error("a write outside the project was allowed")
	}
	if _, err := os.Stat(filepath.Join(outside, "out.txt")); err == nil {
		t.Error("the file was created despite the sandbox")
	}
}

// outsideEveryRoot returns a path in none of the sandbox's writable
// roots — not the project, not the work dir, and not TMPDIR, which the
// profile allows wholesale. The home directory is the one place always
// present and always denied. Nothing should ever be created there; the
// cleanup exists so a regression does not leave litter behind.
func outsideEveryRoot(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory to test denial against: %v", err)
	}
	dir := filepath.Join(home, ".gem-agent-sandbox-denial-test")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Skipf("cannot prepare a denial target: %v", err)
	}
	return dir
}

// The rule tier now reads the sandbox's scratch list (ADR-0070 §2); the
// profile must actually allow what that list promises — /dev/null in
// particular, the redirect session 20260904-225330 was Blocked for.
func TestSandboxAllowsDevNull(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here (nested sandbox?): %v", err)
	}
	execFn, err := buildExecFn(true, t.TempDir(), "", nil)
	if err != nil {
		t.Skipf("sandbox unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := execFn(ctx, "echo ok > /dev/null && ls / 2>/dev/null >/dev/null", sandbox.LaneWrite).Run(); err != nil {
		t.Errorf("a write to /dev/null was denied: %v", err)
	}
}
