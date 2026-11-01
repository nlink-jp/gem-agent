package config

import (
	"os"
	"path/filepath"
	"testing"
)

// ADR-0101: a trusted_projects entry typed in one Unicode form trusts
// the project the kernel spells in the other (pre-release review: the
// kernel spelling made a hand-typed NFC entry stop matching).
func TestTrustedProjectsMatchAcrossSpellings(t *testing.T) {
	const nfc, nfd = "ドライブ", "ドライブ"
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, nfd), 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(base, nfd))
	if err != nil {
		t.Fatal(err)
	}
	c := &Config{}
	c.Approval.TrustedProjects = []string{filepath.Join(filepath.Dir(real), nfc)}
	if !c.TrustsProject(real) {
		t.Error("an NFC entry does not trust the NFD project")
	}
	c.Approval.TrustedProjects = []string{filepath.Join(filepath.Dir(real), "other")}
	if c.TrustsProject(real) {
		t.Error("an unrelated entry trusted the project")
	}
}
