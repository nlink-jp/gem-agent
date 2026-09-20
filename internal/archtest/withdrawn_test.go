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
	{"drawn if and only if the intake", "the intake draws nothing: an MCP image block is addressed to the model, and no server that emits one declares an audience (ADR-0091)"},
	{"intake が保存し記述したとき", "same"},
	{"a server can now put a picture on the operator", "withdrawn with the intake source; what a server returns reaches the model, and the model decides what the operator sees"},
	{"images from MCP tools", "the intake draws nothing (ADR-0091/lagent ADR-0022): what is drawn is show_image or /show"},
	{"MCP ツールの画像をインライン描画", "same"},
	{"The sender is the MCP intake", "same"},
	{"an image an MCP tool returned, for a block", "same"},
	{"nothing draws yet", "the source was settled and wired: the MCP intake draws, for a block it both saved and described"},
	{"No implementation here yet", "implemented here after gem-agent, in the same work that wrote this"},
	{"こちらにはまだ実装が無い", "same"},
	{"nil in every entrance", "the sink is NOT nil outside a TUI — MCP connects before the runtime knows whether it has a UI, so it is inert there instead"},
	{"sink は対話的 TUI でない入口すべてで nil", "same"},
	{"reaches the operator is still open", "how an image reaches the operator was settled; ADR-0090 named the source"},
	{"stops rendering the reply as one piece", "newGlamourRenderer was never changed; an image arrives out-of-band, not inside a reply"},
	{"Not wired to any source yet", "the intake is the source and it is wired"},
	{"three verification passes", "four, the fourth being the real terminal that found the erase"},
	{"検証パス 3 本", "same"},
	{"Closed by mechanism", "narrowed by mechanism; the records say narrowed, not closed, and an INDEX line said otherwise"},
	{"the implementation commit carries an architecture test", "it did not; the escape-site test was written after an independent pass found the promise standing alone"},
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
	for _, f := range []string{"AGENTS.md", "CLAUDE.md", "CHANGELOG.md", "README.md", "README.ja.md", "Makefile", "config.example.toml"} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			out = append(out, f)
		}
	}
	// cmd and internal are read surfaces too: the settings panel's help text
	// and a type's doc comment both went on describing the intake as the
	// source after ADR-0091 withdrew it, and the sweep that followed scanned
	// neither. This file holds every retired phrase by design and is skipped.
	for _, dir := range []string{"docs", "tools", "cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if filepath.Base(p) == "withdrawn_test.go" {
				return nil
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
