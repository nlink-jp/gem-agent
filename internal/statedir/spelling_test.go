package statedir

import (
	"os"
	"path/filepath"
	"testing"
)

// ADR-0101: a marker recorded under one Unicode spelling of the project
// claims the same directory reached under the other; a different
// directory is still a collision.
func TestMarkerMatchesTheSameDirectoryUnderAnotherSpelling(t *testing.T) {
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
	state := filepath.Join(t.TempDir(), "state")
	if err := EnsureProjectDir(state, project); err != nil {
		t.Fatal(err)
	}
	if err := EnsureProjectDir(state, typed); err != nil {
		t.Errorf("the same project under its other spelling was refused: %v", err)
	}
	if ok, note := MarkerMatches(state, typed); !ok {
		t.Errorf("marker refused the other spelling: %s", note)
	}
	other := filepath.Join(base, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, _ := MarkerMatches(state, other); ok {
		t.Error("a different directory matched the marker")
	}
}
