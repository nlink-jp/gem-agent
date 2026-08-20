package memory

import (
	"path/filepath"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// ADR-0022 §4: the env override isolates memory too — the whole state
// root moves, not just sessions.
func TestMemoryDefaultDirHonorsEnvOverride(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv(statedir.EnvRoot, scratch)
	dir, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(scratch, "memory") {
		t.Errorf("DefaultDir = %s, want under the override", dir)
	}
}
