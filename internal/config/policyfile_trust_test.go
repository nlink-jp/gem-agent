package config

import (
	"path/filepath"
	"testing"
)

// ADR-0023: the trust decision and tool policies coexist in one project
// entry, survive a save/load round trip, and clearing trust with no
// tools removes the entry (re-ask on next start).
func TestPolicyFileTrustRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	pf := &PolicyFile{Tools: map[string]string{}, Projects: map[string]ProjectPolicy{}}
	pf.SetTrust("/proj/a", TrustGranted)
	pf.Set("/proj/a", "shell_exec", "always")
	pf.SetTrust("/proj/b", TrustDeclined)
	if err := pf.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrustFor("/proj/a") != TrustGranted || got.TrustFor("/proj/b") != TrustDeclined {
		t.Errorf("trust lost in round trip: a=%q b=%q", got.TrustFor("/proj/a"), got.TrustFor("/proj/b"))
	}
	if got.ForProject("/proj/a")["shell_exec"] != "always" {
		t.Error("tool policy lost when trust coexists")
	}

	// Removing the tool policy must not drop the trust decision.
	got.Set("/proj/a", "shell_exec", "")
	if got.TrustFor("/proj/a") != TrustGranted {
		t.Error("clearing a tool policy erased the trust decision")
	}
	// Clearing trust with no tools removes the entry entirely.
	got.SetTrust("/proj/b", "")
	if _, exists := got.Projects["/proj/b"]; exists {
		t.Error("cleared entry lingers")
	}
}
