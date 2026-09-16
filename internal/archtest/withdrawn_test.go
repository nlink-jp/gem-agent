package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWithdrawnClaimsStayWithdrawn is the mechanism three verification
// passes asked for and three rounds of human re-reading failed to supply.
// The recorded rule is that a withdrawal is counted in the surfaces a
// reader reads, and that the sweep gets a test in the same commit — "a
// withdrawn mechanism disappears from the code, not from the prose".
// Measured cost of not having it: round two swept the instruments and left
// the claim in the documents, round three swept the documents and left it
// in the CHANGELOG, and one round's remedy for a false claim was itself a
// false claim.
//
// A retired phrase may still appear where it is being retired — a record
// that says what it withdrew is doing its job. So an occurrence is allowed
// when the line, or one of the two before it, carries a withdrawal marker.
// Anything else is an assertion of something this project has decided is
// not true.
var withdrawn = []struct {
	phrase string
	why    string
}{
	{"past the declared box", "the cursor stops past the last cell written on that row; three rows of rowprobe's own table contradict the box reading"},
	{"emit adds it to the count", "the declared height REPLACES the one row physicalRows floors to; adding over-counts by one per image"},
	{"the pin drifts by", "measured on two terminals: the damage is a stranded frame per image, not a drifting pin"},
	{"nothing moved at all", "that iTerm2 run was in the not-full regime; re-taken in the arranged regime it strands three frames for three images"},
	{"bytes the intake already holds", "there is no such carrier: render returns a string and Tool.Run is string-only"},
	{"the intake already holds", "same"},
	{"A test pins the prompt's silence", "no such test exists; prompt_test.go pins the diagram silence only"},
	{"constructed at exactly one site", "tools/pinprobe constructs a second tea.NewProgram in the same module, so the claim is true of the product and false of the module"},
	{"nothing on the first three", "emit differs between the trees in its ADR-number prefixes"},
}

var withdrawalMarkers = []string{
	"withdrawn", "Withdrawn", "withdrew", "an earlier draft", "An earlier draft",
	"a first version", "A first version", "the first draft", "The first draft",
	"the rewrite", "The rewrite", "a draft", "A draft", "why it failed",
	"draft said", "refuted", "Refuted", "rather than", `Not "`,
	"撤回", "以前の稿", "初稿", "書き直し", "なぜ失敗したか", "反証", "ではない",
}

func TestWithdrawnClaimsStayWithdrawn(t *testing.T) {
	root := repoRoot(t)
	var scanned int
	for _, rel := range readSurfaces(t, root) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		scanned++
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			for _, w := range withdrawn {
				if !strings.Contains(line, w.phrase) {
					continue
				}
				// Whitespace-normalised, because a marker phrase that
				// wraps across two lines is still a marker — the first
				// version missed "An earlier\ndraft said" for that reason.
				context := strings.Join(strings.Fields(strings.Join(lines[max(0, i-2):i+1], " ")), " ")
				marked := false
				for _, m := range withdrawalMarkers {
					if strings.Contains(context, m) {
						marked = true
						break
					}
				}
				if !marked {
					t.Errorf("%s:%d asserts a retired claim %q with nothing nearby marking it as withdrawn.\n    %s\n    why it is retired: %s",
						rel, i+1, w.phrase, strings.TrimSpace(line), w.why)
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no surfaces scanned — a sweep test that reads nothing passes")
	}
	t.Logf("%d read surfaces scanned for %d retired claims", scanned, len(withdrawn))
}

// readSurfaces is every file a reader of this repository meets: the
// documents, the agent-facing files, the release notes, the build help and
// the instruments' own prose and printed strings.
func readSurfaces(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, f := range []string{"AGENTS.md", "CLAUDE.md", "CHANGELOG.md", "README.md", "README.ja.md", "Makefile"} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			out = append(out, f)
		}
	}
	for _, dir := range []string{"docs", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			switch filepath.Ext(p) {
			case ".md", ".go":
				rel, rerr := filepath.Rel(root, p)
				if rerr != nil {
					return rerr
				}
				out = append(out, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	return out
}
