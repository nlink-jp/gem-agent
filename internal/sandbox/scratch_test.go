package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// ScratchDirs is the one list both the profile and the rule tier read
// (ADR-0070 §2): every entry is a real, absolute, existing directory,
// and /dev is among them so `2>/dev/null` is a write the profile allows.
func TestScratchDirsAreResolvedExistingDirectories(t *testing.T) {
	dirs := ScratchDirs()
	if len(dirs) == 0 {
		t.Fatal("no scratch dirs")
	}
	seenDev := false
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			t.Errorf("%q is not absolute", d)
		}
		real, err := filepath.EvalSymlinks(d)
		if err != nil || real != d {
			t.Errorf("%q is not symlink-resolved (real %q, err %v)", d, real, err)
		}
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Errorf("%q is not an existing directory", d)
		}
		if d == "/dev" {
			seenDev = true
		}
	}
	if !seenDev {
		t.Errorf("/dev missing from %v", dirs)
	}
}
