package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// ADR-0073: shell_exec declares a lane; the registry's word on whether
// the call mutates depends on the lane and on whether a kernel-enforced
// read lane exists.
func TestShellLaneDecidesMutation(t *testing.T) {
	r, err := New(t.TempDir(), func(ctx context.Context, c string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", c)
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := r.Get(ShellExecName)
	read := map[string]any{"command": "ls", "access": "read"}
	bare := map[string]any{"command": "ls"}
	write := map[string]any{"command": "ls", "access": "write"}
	op := map[string]any{"command": "ls", "access": "operator"}
	// No lane runner: nothing enforces a read lane, so every call mutates.
	for _, args := range []map[string]any{read, bare, write, op} {
		if !tool.MutatesFor(args) {
			t.Errorf("without a read lane, %v must mutate", args)
		}
	}
	var lanes []sandbox.Lane
	r.SetLaneExec(func(ctx context.Context, c string, lane sandbox.Lane) *exec.Cmd {
		lanes = append(lanes, lane)
		return exec.CommandContext(ctx, "/bin/bash", "-c", c)
	}, sandbox.Enforcement{Confined: true, ReadLane: true})
	if tool.MutatesFor(read) || tool.MutatesFor(bare) {
		t.Error("a read-lane call with a kernel-enforced read lane must not mutate")
	}
	if !tool.MutatesFor(write) || !tool.MutatesFor(op) {
		t.Error("write and operator lanes mutate")
	}
	// The runner receives the declared lane; missing means read.
	for _, args := range []map[string]any{read, bare, write, op} {
		if _, err := tool.Run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
	}
	want := []sandbox.Lane{sandbox.LaneRead, sandbox.LaneRead, sandbox.LaneWrite, sandbox.LaneOperator}
	for i, l := range want {
		if lanes[i] != l {
			t.Errorf("call %d ran in %s, want %s", i, lanes[i], l)
		}
	}
	if _, err := tool.Run(context.Background(), map[string]any{"command": "ls", "access": "root"}); err == nil {
		t.Error("an unknown lane must be an error the model can act on")
	}
	// A Subset shares the parent's lane.
	sub, err := r.Subset(ShellExecName)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := sub.Get(ShellExecName); st.MutatesFor(read) {
		t.Error("the subset lost the parent's read lane")
	}
}

// A read-lane command the kernel refused is told which lane to ask for.
func TestReadLaneDenialIsExplained(t *testing.T) {
	r, err := New(t.TempDir(), nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r.SetLaneExec(func(ctx context.Context, c string, lane sandbox.Lane) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", c)
	}, sandbox.Enforcement{Confined: true, ReadLane: true})
	tool, _ := r.Get(ShellExecName)
	out, err := tool.Run(context.Background(), map[string]any{"command": "echo 'x: Operation not permitted' >&2; exit 1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[exit status 1]") || !strings.Contains(out, `access: "write"`) {
		t.Errorf("denial not explained: %q", out)
	}
	out, _ = tool.Run(context.Background(), map[string]any{"command": "echo 'x: Operation not permitted' >&2; exit 1", "access": "write"})
	if strings.Contains(out, `access: "write"`) || !strings.Contains(out, `access: "operator"`) {
		t.Errorf("a write-lane denial must point at the operator lane, not the read lane: %q", out)
	}
	// Go spells the errno in lower case; the hint must still fire.
	out, _ = tool.Run(context.Background(), map[string]any{"command": "echo 'open x: operation not permitted' >&2; exit 1"})
	if !strings.Contains(out, `access: "write"`) {
		t.Errorf("lower-case errno not recognised: %q", out)
	}
	out, _ = tool.Run(context.Background(), map[string]any{"command": "echo 'x: Operation not permitted' >&2; exit 1", "access": "operator"})
	if strings.Contains(out, "lane denied") {
		t.Errorf("an operator-lane failure has no wider lane to suggest: %q", out)
	}
	out, _ = tool.Run(context.Background(), map[string]any{"command": "echo plain failure >&2; exit 2"})
	if strings.Contains(out, "read lane denied") {
		t.Errorf("an ordinary failure must not be blamed on the lane: %q", out)
	}
}

// Final review R2: a write through a link named like an ordinary file
// must not change the persistent file the link points at — the write
// replaces the name with a fresh regular file.
func TestWriteNeverLandsInALinkedInode(t *testing.T) {
	r, err := New(t.TempDir(), nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	proj := r.ProjectDir()
	agents := filepath.Join(proj, "AGENTS.md")
	if err := os.WriteFile(agents, []byte("rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(proj, "sym.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(agents, filepath.Join(proj, "hard.md")); err != nil {
		t.Fatal(err)
	}
	wf, _ := r.Get("write_file")
	for _, name := range []string{"sym.md", "hard.md"} {
		if _, err := wf.Run(context.Background(), map[string]any{"path": name, "content": "pwned\n", "allow_shrink": true}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, _ := os.ReadFile(agents); string(got) != "rules\n" {
			t.Fatalf("writing %s changed AGENTS.md: %q", name, got)
		}
		if st, err := os.Lstat(filepath.Join(proj, name)); err != nil || !st.Mode().IsRegular() {
			t.Errorf("%s is not a fresh regular file after the write: %v %v", name, st, err)
		}
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(proj, "sym2.md")); err != nil {
		t.Fatal(err)
	}
	ef, _ := r.Get("edit_file")
	if _, err := ef.Run(context.Background(), map[string]any{"path": "sym2.md", "old_string": "rules", "new_string": "gone"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(agents); string(got) != "rules\n" {
		t.Errorf("edit_file through a link changed AGENTS.md: %q", got)
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(proj, "sym3.md")); err != nil {
		t.Fatal(err)
	}
	if real, err := r.RealPath("sym3.md"); err != nil || filepath.Base(real) != "AGENTS.md" {
		t.Errorf("RealPath(sym3.md) = %q %v, want AGENTS.md", real, err)
	}
}
