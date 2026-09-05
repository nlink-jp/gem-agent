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

// ADR-0074: pins ride the project entry, survive a save/load, and go
// when trust is withdrawn.
// An empty pin set is still "pinned": the marker round-trips, so a
// project with no agent-facing files is not trust-on-first-used again
// every start (review F2).
func TestEmptyPinSetIsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	pf := &PolicyFile{Tools: map[string]string{}, Projects: map[string]ProjectPolicy{}}
	pf.SetTrust("/p", TrustGranted)
	if pf.HasPins("/p") {
		t.Fatal("pins before any were set")
	}
	pf.SetPins("/p", nil)
	if !pf.HasPins("/p") {
		t.Fatal("empty set not marked as pinned")
	}
	if err := pf.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := LoadPolicyFile(path)
	if err != nil || !back.HasPins("/p") || len(back.PinsFor("/p")) != 0 {
		t.Fatalf("round trip: err=%v has=%v pins=%v", err, back.HasPins("/p"), back.PinsFor("/p"))
	}
	back.SetTrust("/p", "")
	if back.HasPins("/p") {
		t.Error("withdrawn trust kept the pin marker")
	}
}

func TestPinsRoundTripAndFollowTrust(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	pf := &PolicyFile{Tools: map[string]string{}, Projects: map[string]ProjectPolicy{}}
	pf.SetTrust("/p", TrustGranted)
	pf.SetPins("/p", map[string]string{"AGENTS.md": "sha256:aa", ".claude/skills/x": "sha256:bb"})
	if err := pf.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.PinsFor("/p"); got["AGENTS.md"] != "sha256:aa" || got[".claude/skills/x"] != "sha256:bb" {
		t.Errorf("pins after reload: %v", got)
	}
	if back.TrustFor("/p") != TrustGranted {
		t.Errorf("trust after reload: %q", back.TrustFor("/p"))
	}
	back.SetTrust("/p", "")
	if back.PinsFor("/p") != nil {
		t.Error("pins survived trust withdrawal")
	}
}
