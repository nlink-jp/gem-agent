package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPolicyFileRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), PolicyFileName)
	pf, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pf.Set("", "mcp__tor__*", "never")
	pf.Set("/work/proj", "shell_exec", "never")
	if err := pf.Save(path); err != nil {
		t.Fatal(err)
	}

	back, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatalf("the file gem-agent wrote does not load: %v", err)
	}
	if back.Tools["mcp__tor__*"] != "never" {
		t.Errorf("global tools = %v", back.Tools)
	}
	if back.Projects["/work/proj"].Tools["shell_exec"] != "never" {
		t.Errorf("project tools = %v", back.Projects)
	}

	// The header has to survive a rewrite: it is the only warning that
	// hand-edits here are lost.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# gem-agent approval policy — WRITTEN BY gem-agent.") {
		t.Errorf("saved file does not announce itself:\n%s", data)
	}
}

func TestPolicyFileForProjectLayersProjectOverGlobal(t *testing.T) {
	pf := &PolicyFile{
		Tools:    map[string]string{"a": "never", "b": "always"},
		Projects: map[string]ProjectPolicy{"/p": {Tools: map[string]string{"b": "never"}}},
	}
	got := pf.ForProject("/p")
	if got["a"] != "never" || got["b"] != "never" {
		t.Errorf("ForProject = %v", got)
	}
	// Another project sees only the global table.
	if other := pf.ForProject("/q"); other["b"] != "always" {
		t.Errorf("ForProject(/q) = %v", other)
	}
}

// "back to default" is expressed by removing the entry, not by writing a
// third value — otherwise the file accumulates rows that say nothing.
func TestPolicyFileSetEmptyRemoves(t *testing.T) {
	pf := &PolicyFile{
		Tools:    map[string]string{"a": "never"},
		Projects: map[string]ProjectPolicy{"/p": {Tools: map[string]string{"b": "never"}}},
	}
	pf.Set("", "a", "")
	pf.Set("/p", "b", "")
	if len(pf.Tools) != 0 {
		t.Errorf("global tools = %v", pf.Tools)
	}
	if _, ok := pf.Projects["/p"]; ok {
		t.Errorf("an empty project table was left behind: %v", pf.Projects)
	}
}

func TestPolicyFileQuotesPatternsAndPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), PolicyFileName)
	pf := &PolicyFile{Tools: map[string]string{}, Projects: map[string]ProjectPolicy{}}
	// A wildcard pattern and a path with spaces are both ordinary here.
	pf.Set("", "mcp__urlscan-lookup__*", "never")
	pf.Set("/Users/me/my work/repo", "write_file", "always")
	if err := pf.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatalf("quoting is wrong: %v", err)
	}
	if back.Tools["mcp__urlscan-lookup__*"] != "never" {
		t.Errorf("wildcard key lost: %v", back.Tools)
	}
	if back.Projects["/Users/me/my work/repo"].Tools["write_file"] != "always" {
		t.Errorf("path with a space lost: %v", back.Projects)
	}
}

func TestLoadPolicyFileMissingIsEmptyNotAnError(t *testing.T) {
	pf, err := LoadPolicyFile(filepath.Join(t.TempDir(), PolicyFileName))
	if err != nil || len(pf.Tools) != 0 {
		t.Errorf("first run: %+v %v", pf, err)
	}
}

func TestLoadPolicyFileRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, PolicyFileName)
	if err := os.WriteFile(path, []byte("[sandbox]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicyFile(path); err == nil {
		t.Fatal("the machine-owned policy file accepted a sandbox setting")
	}
}

func TestPolicyPathSitsBesideTheConfig(t *testing.T) {
	if got := PolicyPath("/home/me/.config/gem-agent/config.toml"); got != "/home/me/.config/gem-agent/policy.toml" {
		t.Errorf("PolicyPath = %q", got)
	}
}

// MutatePolicyFile is a flocked read-modify-write: a second instance's
// stale in-memory snapshot must not clobber decisions persisted since
// it loaded — the measured hazard was a whole-file Save resurrecting a
// project trust the operator had just declined (review round 2).
func TestMutatePolicyFileDoesNotClobberConcurrentDecisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")

	// Instance A declines project X (persisted through Mutate).
	if _, err := MutatePolicyFile(path, func(pf *PolicyFile) {
		pf.SetTrust("/x", TrustDeclined)
	}); err != nil {
		t.Fatal(err)
	}
	// Instance B — which loaded BEFORE A's decline — edits a tool
	// policy. Through Mutate it operates on a fresh load, so A's
	// decline survives.
	if _, err := MutatePolicyFile(path, func(pf *PolicyFile) {
		pf.Set("", "write_file", "always")
	}); err != nil {
		t.Fatal(err)
	}
	pf, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if pf.TrustFor("/x") != TrustDeclined {
		t.Errorf("trust for /x = %q — the concurrent edit clobbered the decline", pf.TrustFor("/x"))
	}
	if pf.Tools["write_file"] != "always" {
		t.Errorf("tool policy lost: %v", pf.Tools)
	}
}

// Concurrent mutators must serialize, not lose updates.
func TestMutatePolicyFileParallel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := MutatePolicyFile(path, func(pf *PolicyFile) {
				pf.Set("", fmt.Sprintf("tool_%d", i), "always")
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	pf, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Tools) != 8 {
		t.Errorf("tools = %d, want 8 (lost updates)", len(pf.Tools))
	}
}
