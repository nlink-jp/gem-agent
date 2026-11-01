package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// ADR-0101: the project is the directory as the kernel spells it, so
// the same directory entered in another Unicode form — $PWD carrying
// that form — is the same project string.
func TestCurrentProjectTakesTheSpellingOnDisk(t *testing.T) {
	const nfc, nfd = "ドライブ", "ドライブ"
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, nfd), 0o755); err != nil {
		t.Fatal(err)
	}
	typed := filepath.Join(base, nfc)
	if _, err := os.Stat(typed); err != nil {
		t.Skipf("this volume does not treat the two spellings as one name: %v", err)
	}
	t.Chdir(typed)
	t.Setenv("PWD", typed)
	got, err := currentProject()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != nfd {
		t.Errorf("project = %q, want the on-disk spelling %q", filepath.Base(got), nfd)
	}
}
