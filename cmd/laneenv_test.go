package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// ADR-0084 §1: every lane's shell runs with the Go build cache under
// the session scratch; the read lane additionally scrubs the operator's
// secrets and points its temporary directory at the scratch.
func TestLaneEnvPointsToolchainCacheAtScratch(t *testing.T) {
	parent := []string{"HOME=/Users/x", "MY_API_TOKEN=secret", "PATH=/usr/bin"}
	for _, lane := range []sandbox.Lane{sandbox.LaneRead, sandbox.LaneWrite, sandbox.LaneOperator} {
		env := laneEnv(lane, "/work/scratch", parent)
		// The unasked lane and the approved lanes never share a cache
		// directory: a shared content-addressed cache is a route from
		// a read-lane command to an approved build's output.
		want := "GOCACHE=/work/scratch/go-build-approved"
		if lane == sandbox.LaneRead {
			want = "GOCACHE=/work/scratch/go-build"
		}
		if !slices.Contains(env, want) {
			t.Errorf("%v: %s not in the environment: %v", lane, want, env)
		}
		hasTmp := slices.Contains(env, "TMPDIR=/work/scratch")
		hasSecret := slices.Contains(env, "MY_API_TOKEN=secret")
		if lane == sandbox.LaneRead && (!hasTmp || hasSecret) {
			t.Errorf("read lane: TMPDIR=%v secret=%v", hasTmp, hasSecret)
		}
		if lane != sandbox.LaneRead && (hasTmp || !hasSecret) {
			t.Errorf("%v: must inherit the parent environment untouched: %v", lane, env)
		}
	}
	// No scratch (read lane disabled): nothing is redirected, and the
	// write lane is exactly the parent.
	if env := laneEnv(sandbox.LaneWrite, "", parent); !slices.Equal(env, parent) {
		t.Errorf("write lane without scratch changed the environment: %v", env)
	}
	// The read lane without a scratch still scrubs secrets and gets no
	// cache pointed at a directory that does not exist.
	if env := laneEnv(sandbox.LaneRead, "", parent); slices.Contains(env, "MY_API_TOKEN=secret") || slices.ContainsFunc(env, func(s string) bool { return strings.HasPrefix(s, "GOCACHE=") || strings.HasPrefix(s, "TMPDIR=") }) {
		t.Errorf("read lane without scratch: %v", env)
	}
	if got := toolchainCacheEnv("/s", sandbox.LaneWrite); len(got) != 1 || !strings.HasPrefix(got[0], "GOCACHE=") {
		t.Errorf("toolchainCacheEnv = %v: one list, Go only", got)
	}
}
