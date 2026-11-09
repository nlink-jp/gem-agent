package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// The probe never writes through or removes what it did not create.
func TestVerifyFileReadLaneDoesNotFollowAPlantedLink(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip(err)
	}
	for _, name := range []string{".env", "probe-ordinary.txt"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, name)
			if err := os.Symlink(victim, link); err != nil {
				t.Fatal(err)
			}
			err := VerifyFileReadLane(dir, filepath.Dir(dir), func(string, string) error { return nil })
			if err == nil {
				t.Fatal("the probe accepted a directory holding a planted link")
			}
			if got, _ := os.ReadFile(victim); string(got) != "keep" {
				t.Errorf("the victim was overwritten: %q", got)
			}
			if _, err := os.Lstat(link); err != nil {
				t.Errorf("the planted link was removed: %v", err)
			}
		})
	}
}
