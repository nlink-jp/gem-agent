package config

import (
	"os"
	"path/filepath"
	"testing"
)

// ADR-0101: trust, pins and allowlists recorded under one Unicode
// spelling of a project are found under the other, and a change lands
// in the table that already exists — one directory, one table.
func TestPolicyFindsTheProjectUnderAnotherSpelling(t *testing.T) {
	const nfc, nfd = "ドライブ", "ドライブ"
	base := t.TempDir()
	project := filepath.Join(base, nfd)
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	typed := filepath.Join(base, nfc)
	if _, err := os.Stat(typed); err != nil {
		t.Skipf("this volume does not treat the two spellings as one name: %v", err)
	}
	pf := &PolicyFile{Projects: map[string]ProjectPolicy{}}
	pf.SetTrust(project, TrustGranted)
	pf.SetPins(project, map[string]string{"AGENTS.md": "h"})
	pf.Set(project, "shell_exec", "always")

	if pf.TrustFor(typed) != TrustGranted || pf.PinsFor(typed)["AGENTS.md"] != "h" || pf.ForProject(typed)["shell_exec"] != "always" {
		t.Fatalf("the other spelling lost the project's records: trust %q, pins %v, tools %v",
			pf.TrustFor(typed), pf.PinsFor(typed), pf.ForProject(typed))
	}
	pf.SetCommand(typed, "go test", "always")
	if len(pf.Projects) != 1 {
		t.Errorf("a write under the other spelling started a second table: %d tables", len(pf.Projects))
	}
	if pf.CommandsFor(project)["go test"] != "always" {
		t.Error("the write did not land in the existing table")
	}
	// Clearing everything under the other spelling removes the one table.
	pf.SetCommand(typed, "go test", "")
	pf.Set(typed, "shell_exec", "")
	pf.SetTrust(typed, "")
	if len(pf.Projects) != 0 {
		t.Errorf("clearing under the other spelling left %d tables", len(pf.Projects))
	}
	if got := pf.ProjectKey(filepath.Join(base, "unrecorded")); got != filepath.Join(base, "unrecorded") {
		t.Errorf("an unrecorded project keyed as %q", got)
	}
}
