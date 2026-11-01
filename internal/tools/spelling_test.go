package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ADR-0101: an absolute path typed in one Unicode form is inside a
// project the kernel spells in the other.
func TestAnAbsolutePathInTheOtherSpellingIsInsideTheProject(t *testing.T) {
	const nfc, nfd = "ドライブ", "ドライブ"
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(base, nfd)
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "f.txt"), []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := New(proj, directExec, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "read_file", map[string]any{"path": filepath.Join(base, nfc, "f.txt")})
	if err != nil || !strings.Contains(out, "inside") {
		t.Errorf("read through the other spelling: %q, %v", out, err)
	}
}
