package risk

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// The verdict side of ADR-0099 §1/§6: a write root is written like the
// project, its persistent names judged relative to itself; a read root
// is a Block like a write outside every root, with its own reason; the
// innermost root decides; a path in no root names the added directories
// in its reason.
func TestAddedRootVerdicts(t *testing.T) {
	added := []sandbox.AddedRoot{
		{Dir: "/Users/op/notes", Write: true},
		{Dir: "/Users/op/ref", Write: false},
		{Dir: "/Users/op/ref/drafts", Write: true},
	}
	write := func(p string) Verdict {
		return Classify("write_file", true, map[string]any{"path": p, "content": "x"}, proj, "", added...)
	}
	for _, tc := range []struct {
		path   string
		tier   Tier
		reason string
		opOnly bool
	}{
		{"/Users/op/notes/today.md", Safe, "inside an added directory", false},
		{"/Users/op/notes/sub/AGENTS.md", Review, "instructions or configuration", true},
		{"/Users/op/notes/.git/hooks/pre-commit", Block, "version-control internals", false},
		{"/Users/op/ref/paper.md", Block, "added read-only", false},
		{"/Users/op/ref/drafts/d.md", Safe, "inside an added directory", false},
		{"/Users/op/elsewhere.md", Block, "outside the project and added directories", false},
		{"/Users/op/notes/.env", Block, "credential", false},
	} {
		v := write(tc.path)
		if v.Tier != tc.tier || !strings.Contains(v.Reason, tc.reason) || v.OperatorOnly != tc.opOnly {
			t.Errorf("%s: got %v %q op=%v, want %v ~%q op=%v", tc.path, v.Tier, v.Reason, v.OperatorOnly, tc.tier, tc.reason, tc.opOnly)
		}
	}
	// A read in any added root is a read-only tool, as in the project.
	if v := Classify("read_file", false, map[string]any{"path": "/Users/op/ref/paper.md"}, proj, "", added...); v.Tier != Safe {
		t.Errorf("read in a read root = %v", v)
	}
	// With no added roots the wording is unchanged.
	if v := Classify("write_file", true, map[string]any{"path": "/Users/op/x.md"}, proj, ""); v.Reason != "absolute path outside the project directory" {
		t.Errorf("reason without added roots = %q", v.Reason)
	}
	if v := Classify("write_file", true, map[string]any{"path": "/Users/op/x.md"}, proj, "/w", added...); !strings.Contains(v.Reason, "outside the project, session work and added directories") {
		t.Errorf("reason with a work dir and added roots = %q", v.Reason)
	}
	// The write lane's reason names the write roots only when there is one.
	sh := func(a ...sandbox.AddedRoot) string {
		return Classify("shell_exec", true, map[string]any{"command": "make", "access": "write"}, proj, "", a...).Reason
	}
	if !strings.Contains(sh(added...), "added write directories") {
		t.Errorf("write-lane reason with a write root = %q", sh(added...))
	}
	if strings.Contains(sh(sandbox.AddedRoot{Dir: "/Users/op/ref"}), "added") {
		t.Errorf("write-lane reason with only a read root names added directories: %q", sh(sandbox.AddedRoot{Dir: "/Users/op/ref"}))
	}
}
