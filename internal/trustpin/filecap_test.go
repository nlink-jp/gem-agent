package trustpin

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The file cap is met by a file about to be added, not by whatever the
// walk reaches next: a skill of exactly SkillFiles files with a folder,
// or a file that cannot be digested, sorting after the last of them is
// covered whole. v0.103.1 marked it cut, and the note said the pin
// covered only the first files.
func TestTheFileCapCountsFilesOnly(t *testing.T) {
	fill := func(t *testing.T, n int) string {
		t.Helper()
		dir := t.TempDir()
		for i := 0; i < n; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d", i)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	t.Run("exactly the cap", func(t *testing.T) {
		if _, cut := dirDigest(fill(t, SkillFiles), SkillFiles); cut {
			t.Error("a skill of exactly the cap was cut")
		}
	})
	t.Run("the cap and a folder after it", func(t *testing.T) {
		dir := fill(t, SkillFiles)
		if err := os.Mkdir(filepath.Join(dir, "zz"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, cut := dirDigest(dir, SkillFiles); cut {
			t.Error("a folder after the last file cut the skill")
		}
	})
	t.Run("the cap and an unreadable file after it", func(t *testing.T) {
		dir := fill(t, SkillFiles)
		p := filepath.Join(dir, "zz")
		if err := os.WriteFile(p, []byte("x"), 0o000); err != nil {
			t.Fatal(err)
		}
		if _, cut := dirDigest(dir, SkillFiles); cut {
			t.Error("a file that cannot be digested was counted")
		}
	})
	t.Run("one past the cap", func(t *testing.T) {
		if _, cut := dirDigest(fill(t, SkillFiles+1), SkillFiles); !cut {
			t.Error("a skill past the cap was not cut")
		}
	})
}
