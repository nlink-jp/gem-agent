package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// Review round 4: /clear rotates the work directory (ADR-0071 §2), and
// every consumer must follow — the file tools' second root, the
// sandbox profile (the shell could not write to the new directory), and
// the system prompt. Real sandbox-exec, like root_workdir_test.go.
func TestRotateWorkDirMovesEveryConsumer(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	project := t.TempDir()
	// NOT t.TempDir(): on macOS that is under TMPDIR, which the profile
	// already allows (root_workdir_test.go). The denial half of the
	// assertion needs a directory outside every root.
	base := outsideEveryRoot(t)
	oldWork := filepath.Join(base, "old")
	newWork := filepath.Join(base, "new")
	for _, d := range []string{oldWork, newWork} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	execFn, err := buildExecFn(true, project, oldWork)
	if err != nil {
		t.Fatal(err)
	}
	shellExec := &liveExec{fn: execFn}
	registry, err := tools.New(project, shellExec.run, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.UseWorkDir(oldWork); err != nil {
		t.Fatal(err)
	}
	systemRebuilt := false
	notes := rotateWorkDir(registry, shellExec, true, project, newWork, func() { systemRebuilt = true })
	if len(notes) != 0 {
		t.Fatalf("rotation raised notes: %v", notes)
	}
	if !systemRebuilt {
		t.Error("the system prompt was not rebuilt")
	}
	resolvedNew, _ := filepath.EvalSymlinks(newWork)
	if registry.WorkDir() != resolvedNew {
		t.Errorf("file tools root = %q, want %q", registry.WorkDir(), resolvedNew)
	}
	// The shell writes to the new directory and is denied the old one.
	run := func(command string) error {
		return shellExec.run(context.Background(), command).Run()
	}
	if err := run("echo x > " + newWork + "/out.txt"); err != nil {
		t.Errorf("sandbox denied a write to the new work directory: %v", err)
	}
	if err := run("echo x > " + oldWork + "/out.txt"); err == nil {
		t.Error("sandbox still allows the old work directory")
	}
	// No work directory at all: the second root is removed, not kept.
	if notes := rotateWorkDir(registry, shellExec, true, project, "", nil); len(notes) != 0 {
		t.Fatalf("rotation to none raised notes: %v", notes)
	}
	if registry.WorkDir() != "" {
		t.Errorf("file tools kept a root after rotation to none: %q", registry.WorkDir())
	}
}

type countingLog struct{ n int }

func (c *countingLog) Log(string, any) error { c.n++; return nil }

// Review round 4: the side-call tools registered at startup log
// through liveLog, which reads the variable /clear reassigns — the
// value captured at startup wrote to the closed transcript.
func TestLiveLogFollowsTheSwap(t *testing.T) {
	first, second := &countingLog{}, &countingLog{}
	var current agent.SessionLog = first
	lg := liveLog{get: func() agent.SessionLog { return current }}
	_ = lg.Log("usage", nil)
	current = second
	_ = lg.Log("usage", nil)
	if first.n != 1 || second.n != 1 {
		t.Fatalf("records: first=%d second=%d, want one each", first.n, second.n)
	}
	current = nil
	if err := lg.Log("usage", nil); err == nil {
		t.Fatal("a disabled log must fail the write, not drop it silently")
	}
}

// Review round 4: the live strategy hands the registry the swapped
// function, so a profile built for the new work directory is what the
// next shell call runs under.
func TestLiveExecSwaps(t *testing.T) {
	calls := []string{}
	mk := func(tag string) tools.ExecFunc {
		return func(ctx context.Context, command string) *exec.Cmd {
			calls = append(calls, tag)
			return exec.CommandContext(ctx, "/usr/bin/true")
		}
	}
	e := &liveExec{fn: mk("old")}
	_ = e.run(context.Background(), "x")
	e.set(mk("new"))
	_ = e.run(context.Background(), "x")
	if strings.Join(calls, ",") != "old,new" {
		t.Fatalf("calls = %v", calls)
	}
}

// The plain confirmation helper refuses without a terminal even when a
// "y" is waiting (ADR-0059).
func TestConfirmYesNeedsATerminal(t *testing.T) {
	if confirmYes(strings.NewReader("y\n"), false) {
		t.Fatal("a piped y counted as consent")
	}
	if !confirmYes(strings.NewReader("y\n"), true) {
		t.Fatal("a typed y did not count")
	}
	if confirmYes(strings.NewReader(""), true) {
		t.Fatal("EOF counted as consent")
	}
}

var _ = errors.New

// /clear prints what onClear returns — the hook notes and the MCP
// reconnection report — inside the slash output (ADR-0071 addendum).
func TestClearOutputCarriesTheRestartReport(t *testing.T) {
	out, isErr, quit := slashOutput("/clear", nil, nil, nil, nil, slashReloads{}, nil, nil, nil, "", uitext.For(uitext.EN),
		func() string { return "[⚠ note]\nmcp reloaded: 1 server(s), 2 tool(s)\n" })
	if isErr || quit {
		t.Fatalf("isErr=%v quit=%v", isErr, quit)
	}
	for _, want := range []string{"[⚠ note]", "mcp reloaded: 1 server(s)", uitext.For(uitext.EN).HistoryCleared} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
