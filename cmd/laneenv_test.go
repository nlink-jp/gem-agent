package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// ADR-0084 §1: every lane's shell runs with the scratch-cache table
// pointed into the session scratch — the read lane at the table's
// directory, the approved lanes at a separate one — and the read lane
// additionally points its temporary directory at the scratch.
//
// ADR-0087: no lane filters the operator's environment. What every
// lane does lose is the runtime's own configuration variables.
func TestLaneEnvPointsToolchainCacheAtScratch(t *testing.T) {
	parent := []string{"HOME=/Users/x", "MY_API_TOKEN=secret", "PATH=/usr/bin", "GEMAGENT_MODEL=m"}
	caches := map[string]string{"GOCACHE": "go-build", "PIP_CACHE_DIR": "pip"}
	for _, lane := range []sandbox.Lane{sandbox.LaneRead, sandbox.LaneWrite, sandbox.LaneOperator} {
		env := laneEnv(lane, "/work/scratch", caches, parent)
		// The unasked lane and the approved lanes never share a cache
		// directory: a shared content-addressed cache is a route from
		// a read-lane command to an approved build's output.
		suffix := "-approved"
		if lane == sandbox.LaneRead {
			suffix = ""
		}
		for _, want := range []string{"GOCACHE=/work/scratch/go-build" + suffix, "PIP_CACHE_DIR=/work/scratch/pip" + suffix} {
			if !slices.Contains(env, want) {
				t.Errorf("%v: %s not in the environment: %v", lane, want, env)
			}
		}
		hasTmp := slices.Contains(env, "TMPDIR=/work/scratch")
		if (lane == sandbox.LaneRead) != hasTmp {
			t.Errorf("%v: TMPDIR=%v", lane, hasTmp)
		}
		// The operator's variables pass in every lane, whatever they
		// are called; the runtime's own reach no lane at all.
		if !slices.Contains(env, "MY_API_TOKEN=secret") {
			t.Errorf("%v: the operator's environment was filtered: %v", lane, env)
		}
		if slices.Contains(env, "GEMAGENT_MODEL=m") {
			t.Errorf("%v: a runtime configuration variable reached the child: %v", lane, env)
		}
	}
	// No scratch (read lane disabled): nothing is redirected, and the
	// write lane is the parent minus the runtime's own.
	if env := laneEnv(sandbox.LaneWrite, "", caches, parent); !slices.Equal(env, sandbox.ChildEnv(parent)) {
		t.Errorf("write lane without scratch changed the environment: %v", env)
	}
	// The read lane without a scratch gets no cache pointed at a
	// directory that does not exist, and still keeps the operator's.
	if env := laneEnv(sandbox.LaneRead, "", caches, parent); !slices.Contains(env, "MY_API_TOKEN=secret") || slices.ContainsFunc(env, func(s string) bool { return strings.HasPrefix(s, "GOCACHE=") || strings.HasPrefix(s, "TMPDIR=") }) {
		t.Errorf("read lane without scratch: %v", env)
	}
	// The table renders in name order, so the environment is stable
	// across runs; an empty table renders nothing.
	got := toolchainCacheEnv("/s", sandbox.LaneRead, caches)
	if !slices.Equal(got, []string{"GOCACHE=/s/go-build", "PIP_CACHE_DIR=/s/pip"}) {
		t.Errorf("toolchainCacheEnv = %v", got)
	}
	if got := toolchainCacheEnv("/s", sandbox.LaneWrite, nil); len(got) != 0 {
		t.Errorf("empty table rendered %v", got)
	}
	// The shipped table is Go's build cache, and a removed row is gone.
	def := config.SandboxConfig{ScratchCaches: map[string]string{"GOCACHE": "go-build"}}
	if got := toolchainCacheEnv("/s", sandbox.LaneRead, def.Caches()); !slices.Equal(got, []string{"GOCACHE=/s/go-build"}) {
		t.Errorf("default table = %v", got)
	}
	removed := config.SandboxConfig{ScratchCaches: map[string]string{"GOCACHE": "", "UV_CACHE_DIR": "uv"}}
	if got := toolchainCacheEnv("/s", sandbox.LaneRead, removed.Caches()); !slices.Equal(got, []string{"UV_CACHE_DIR=/s/uv"}) {
		t.Errorf("table with a removed row = %v", got)
	}
	if scratchCachesLabel(caches) != "GOCACHE→go-build, PIP_CACHE_DIR→pip" || scratchCachesLabel(nil) != "(none)" {
		t.Errorf("label = %q / %q", scratchCachesLabel(caches), scratchCachesLabel(nil))
	}
}
