package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ScratchDirs is the one list both the profile and the rule tier read
// (ADR-0070 §2): every entry is a real, absolute, existing directory.
// /dev/fd is among them (descriptor duplication); the device sinks are
// ScratchFiles, allowed as literals — never /dev as a whole.
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
			t.Errorf("/dev as a whole is writable: %v", dirs)
		}
		if d == "/dev/fd" {
			seenDev = true
		}
	}
	if !seenDev {
		t.Errorf("/dev/fd missing from %v", dirs)
	}
	files := ScratchFiles()
	if len(files) == 0 || files[0] != "/dev/null" {
		t.Errorf("ScratchFiles = %v, want /dev/null first", files)
	}
}

// The profile allows the device sinks as literals, never /dev as a
// subpath (review after v0.68.2).
func TestProfileAllowsDeviceSinksAsLiterals(t *testing.T) {
	p, err := Profile([]string{"/private/tmp/proj"}, ScratchFiles())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, `(literal "/dev/null")`) {
		t.Errorf("no /dev/null literal in:\n%s", p)
	}
	if strings.Contains(p, `(subpath "/dev")`) {
		t.Errorf("/dev allowed as a whole in:\n%s", p)
	}
}
