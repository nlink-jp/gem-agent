package mention

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An absolute reference under an added directory attaches like one
// under the project (ADR-0099 §6); one under no root is refused, and
// the reason names the added directories.
func TestExpandReachesAddedDirectories(t *testing.T) {
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	added, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	note := filepath.Join(added, "note.md")
	if err := os.WriteFile(note, []byte("ADDED-SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(outside, "stray.md")
	if err := os.WriteFile(stray, []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	atts, problems := Expand(context.Background(), "@"+note+" と @"+stray, proj, "", DefaultLimits(), added)
	if len(atts) != 1 || !strings.Contains(atts[0].Content, "ADDED-SENTINEL") {
		t.Errorf("attachments = %+v", atts)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "outside the project, work and added directories") {
		t.Errorf("problems = %+v", problems)
	}
	// Without the added directory the same reference is refused.
	if atts, _ := Expand(context.Background(), "@"+note, proj, "", DefaultLimits()); len(atts) != 0 {
		t.Error("a reference under an unadded directory attached")
	}
}
