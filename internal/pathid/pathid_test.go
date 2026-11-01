package pathid

import (
	"os"
	"path/filepath"
	"testing"
)

// The two spellings of "ドライブ": precomposed (NFC, what an input
// method types) and decomposed (NFD, what some providers create).
const (
	nfc = "ドライブ"
	nfd = "ドライブ"
)

func TestSameMatchesOneDirectoryUnderBothSpellings(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, nfd), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, nfc)); err != nil {
		t.Skipf("this volume does not treat the two spellings as one name: %v", err)
	}
	if !Same(filepath.Join(base, nfc), filepath.Join(base, nfd)) {
		t.Error("the NFC and NFD spellings of one directory do not match")
	}
}

func TestSameKeepsDifferentDirectoriesApart(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a-b", "c"), filepath.Join(base, "a", "b-c")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if Same(a, b) {
		t.Error("two directories whose paths escape alike matched")
	}
	gone := filepath.Join(base, "gone")
	if !Same(gone, gone) || Same(gone, a) || !Same("", "") || Same("", a) {
		t.Error("a missing path matches only its own string; empty matches only empty")
	}
}
