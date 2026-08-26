package config

import (
	"path/filepath"
	"testing"
)

// ADR-0045 §4: learned command rules live in the machine-owned file
// under the project they were learned in, and survive a rewrite.
func TestCommandRulesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), PolicyFileName)
	pf, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pf.SetCommand("/work/proj", "go test", "never")
	pf.SetCommand("/work/proj", "git push", "always")
	if err := pf.Save(path); err != nil {
		t.Fatal(err)
	}

	back, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatalf("the file gem-agent wrote does not load: %v", err)
	}
	got := back.CommandsFor("/work/proj")
	if got["go test"] != "never" || got["git push"] != "always" {
		t.Errorf("commands = %v", got)
	}
	if len(back.CommandsFor("/work/other")) != 0 {
		t.Error("a command rule leaked into another project")
	}
	// Project scope only: there is no global command table to read.
	if len(back.CommandsFor("")) != 0 {
		t.Error("CommandsFor(\"\") returned entries — command rules are project-scoped")
	}
}

func TestSetCommandEmptyRemoves(t *testing.T) {
	pf := &PolicyFile{Projects: map[string]ProjectPolicy{}}
	pf.SetCommand("/p", "go test", "never")
	pf.SetCommand("/p", "go test", "")
	if _, ok := pf.Projects["/p"]; ok {
		t.Error("the project entry outlived its last command rule")
	}
}

// The two tables and the trust decision share one project entry, so
// clearing one must not take the others with it.
func TestProjectEntryKeepsItsOtherFields(t *testing.T) {
	pf := &PolicyFile{Projects: map[string]ProjectPolicy{}}
	pf.SetTrust("/p", TrustGranted)
	pf.Set("/p", "shell_exec", "always")
	pf.SetCommand("/p", "go test", "never")

	pf.Set("/p", "shell_exec", "")
	if got := pf.CommandsFor("/p"); got["go test"] != "never" {
		t.Errorf("clearing a tool policy dropped the command rules: %v", got)
	}
	if pf.TrustFor("/p") != TrustGranted {
		t.Error("clearing a tool policy dropped the trust decision")
	}

	pf.SetCommand("/p", "go test", "")
	if pf.TrustFor("/p") != TrustGranted {
		t.Error("clearing the last command rule dropped the trust decision")
	}
}

// A global command rule is unrepresentable, not merely undocumented:
// `make build` being settled here says nothing about the next clone.
func TestSetCommandIgnoresGlobalScope(t *testing.T) {
	pf := &PolicyFile{Projects: map[string]ProjectPolicy{}}
	pf.SetCommand("", "go test", "never")
	if len(pf.Projects) != 0 {
		t.Errorf("a global command rule was recorded: %v", pf.Projects)
	}
}
