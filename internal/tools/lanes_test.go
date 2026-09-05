package tools

import (
	"context"
	"os/exec"
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
	if strings.Contains(out, `access: "write"`) {
		t.Errorf("a write-lane failure must not be blamed on the read lane: %q", out)
	}
	out, _ = tool.Run(context.Background(), map[string]any{"command": "echo plain failure >&2; exit 2"})
	if strings.Contains(out, "read lane denied") {
		t.Errorf("an ordinary failure must not be blamed on the lane: %q", out)
	}
}
