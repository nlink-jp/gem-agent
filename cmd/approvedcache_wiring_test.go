package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/workdir"
)

// ADR-0109, through the real lane builder: the approved lanes' cache is
// the session's own directory beside the work directory, the read lane
// is denied it by the kernel, and the approved lanes can write it.
func TestApprovedCacheWiredThroughBuildLanes(t *testing.T) {
	if err := sandbox.Available(); err != nil {
		t.Skip(err)
	}
	// The state root is where the default one is, under home: outside
	// every root the lanes write, so the assertion on another session
	// below does not depend on a temporary root being dropped.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	state, err := os.MkdirTemp(home, ".gem-agent-test-state-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("GEMAGENT_STATE_DIR", state)
	project, _ := sandbox.ResolveWriteDir(t.TempDir())
	work, err := workdir.Ensure(project, "sid")
	if err != nil {
		t.Fatal(err)
	}
	work, _ = sandbox.ResolveWriteDir(work)
	execFn, enf, notes, err := buildExecFn(true, project, work, nil, map[string]string{"GOCACHE": "go-build"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !enf.ReadLane {
		t.Fatalf("the read lane is off: %v", notes)
	}
	approved, ok := workdir.ApprovedCachePath(work)
	if !ok {
		t.Fatalf("no approved path for %q", work)
	}
	run := func(lane sandbox.Lane, command string) (string, error) {
		out, err := execFn(context.Background(), command, lane).Output()
		return strings.TrimSpace(string(out)), err
	}
	want := filepath.Join(approved, "go-build")
	for _, lane := range []sandbox.Lane{sandbox.LaneWrite, sandbox.LaneOperator} {
		got, err := run(lane, `printf %s "$GOCACHE"`)
		if err != nil || got != want {
			t.Errorf("%v: GOCACHE = %q, %v; want %q", lane, got, err, want)
		}
	}
	if got, err := run(sandbox.LaneRead, `printf %s "$GOCACHE"`); err != nil || got != filepath.Join(work, "scratch", "go-build") {
		t.Errorf("read lane GOCACHE = %q, %v", got, err)
	}
	if rel, err := filepath.Rel(work, approved); err != nil || filepath.IsLocal(rel) {
		t.Errorf("the approved cache %q is under the work directory, which the file tools write", approved)
	}

	planted := filepath.Join(approved, "planted")
	_, _ = run(sandbox.LaneRead, "echo x > "+shellQuoted(planted))
	if _, err := os.Stat(planted); err == nil {
		t.Error("the read lane wrote the approved cache")
	}
	if _, err := run(sandbox.LaneWrite, "echo x > "+shellQuoted(filepath.Join(approved, "built"))); err != nil {
		t.Errorf("the write lane cannot write its cache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(approved, "built")); err != nil {
		t.Errorf("the write lane's file is missing: %v", err)
	}

	// Another session's lanes are not given this session's cache.
	otherWork, err := workdir.Ensure(project, "other")
	if err != nil {
		t.Fatal(err)
	}
	otherWork, _ = sandbox.ResolveWriteDir(otherWork)
	otherExec, _, _, err := buildExecFn(true, project, otherWork, nil, map[string]string{"GOCACHE": "go-build"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(approved, "foreign")
	_ = otherExec(context.Background(), "echo x > "+shellQuoted(foreign), sandbox.LaneWrite).Run()
	if _, err := os.Stat(foreign); err == nil {
		t.Error("another session's write lane wrote this session's approved cache")
	}
}

func shellQuoted(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
