package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The defect this class of test exists to catch: Save wrote [tools] and
// [projects.*] and silently dropped [mcp], so the panel reported "saved"
// and the file held nothing. Every test that asserted on the in-memory
// struct passed. Round-trip through the disk, or it is not saved.
func TestMCPExclusionsSurviveTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), PolicyFileName)
	pf := &PolicyFile{Tools: map[string]string{}, Projects: map[string]ProjectPolicy{}}
	pf.SetMCPExclusions("obsidian", []string{"obsidian/patch_vault_file", "obsidian/search_and_replace"})
	if err := pf.Save(path); err != nil {
		t.Fatal(err)
	}

	back, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(back.MCP.Exclude, ","); got != "obsidian/patch_vault_file,obsidian/search_and_replace" {
		t.Errorf("exclusions after a round trip = %q", got)
	}
	if len(back.MCP.Decided) != 1 || back.MCP.Decided[0] != "obsidian" {
		t.Errorf("decided after a round trip = %v", back.MCP.Decided)
	}
}

// "This server has nothing excluded" is an opinion, and it has to reach
// the file: without it a server excluded in config.toml can never be
// turned back on from the panel.
func TestAnEmptySetIsStillAnOpinion(t *testing.T) {
	path := filepath.Join(t.TempDir(), PolicyFileName)
	pf := &PolicyFile{Tools: map[string]string{}, Projects: map[string]ProjectPolicy{}}
	pf.SetMCPExclusions("chrome-pilot", nil)
	if err := pf.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "chrome-pilot") {
		t.Fatalf("the file does not record the decision:\n%s", raw)
	}

	back, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.MCP.Exclude) != 0 {
		t.Errorf("exclude = %v, want empty", back.MCP.Exclude)
	}
	if len(back.MCP.Decided) != 1 || back.MCP.Decided[0] != "chrome-pilot" {
		t.Errorf("decided = %v, want the server", back.MCP.Decided)
	}
}

// One server's write leaves the others alone, and repeats do not pile up.
func TestSetMCPExclusionsIsPerServer(t *testing.T) {
	pf := &PolicyFile{}
	pf.SetMCPExclusions("obsidian", []string{"obsidian/a"})
	pf.SetMCPExclusions("github", []string{"github"})
	pf.SetMCPExclusions("obsidian", []string{"obsidian/b"})

	if got := strings.Join(pf.MCP.Exclude, ","); got != "github,obsidian/b" {
		t.Errorf("exclude = %q, want the second obsidian write to replace the first", got)
	}
	if got := strings.Join(pf.MCP.Decided, ","); got != "github,obsidian" {
		t.Errorf("decided = %q, want each server once", got)
	}
}

// A server name is not a prefix match: github and github-enterprise are
// two servers.
func TestSetMCPExclusionsDoesNotMatchByPrefix(t *testing.T) {
	pf := &PolicyFile{}
	pf.SetMCPExclusions("github-enterprise", []string{"github-enterprise/x"})
	pf.SetMCPExclusions("github", []string{"github/y"})
	if len(pf.MCP.Exclude) != 2 {
		t.Errorf("exclude = %v, want both servers kept", pf.MCP.Exclude)
	}
}
