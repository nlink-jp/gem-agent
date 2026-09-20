package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestIndexStatusMatchesTheADR: where an INDEX entry states an ADR's status,
// it states the status the ADR's own header carries.
//
// Nothing else reads the two together. An entry is written when the ADR is
// proposed, the header is edited when it is accepted, and the INDEX goes on
// saying "Proposed, design only" about code that has shipped — found for two
// ADRs at once, one of them two releases after its implementation.
func TestIndexStatusMatchesTheADR(t *testing.T) {
	root := repoRoot(t)
	entry := regexp.MustCompile(`\]\((adr/[0-9]{4}-[^)]+\.md)\)`)
	stated := regexp.MustCompile(`[(（]\*\*([A-Za-z]+)\*\*`)
	header := regexp.MustCompile(`(?m)^\|\s*Status\s*\|\s*\*\*([A-Za-z]+)\*\*`)

	checked := 0
	for _, index := range []string{"docs/en/INDEX.md", "docs/ja/INDEX.ja.md"} {
		raw, err := os.ReadFile(filepath.Join(root, index))
		if err != nil {
			t.Fatalf("%s: %v", index, err)
		}
		// One entry is one list item, which may wrap over several lines.
		for _, item := range strings.Split("\n"+string(raw), "\n- ")[1:] {
			item = strings.Join(strings.Fields(item), " ")
			link := entry.FindStringSubmatch(item)
			if link == nil || !strings.HasPrefix(item, "[`ADR-") {
				continue
			}
			// The status an entry states is the first one after its own link;
			// later bold words may describe other records it relates to.
			rest := item[strings.Index(item, link[0])+len(link[0]):]
			said := stated.FindStringSubmatch(rest)
			if said == nil {
				continue // this entry states no status
			}
			adr, err := os.ReadFile(filepath.Join(root, filepath.Dir(index), link[1]))
			if err != nil {
				t.Errorf("%s links %s: %v", index, link[1], err)
				continue
			}
			is := header.FindSubmatch(adr)
			if is == nil {
				continue // an older header form with no status row
			}
			checked++
			if string(is[1]) != said[1] {
				t.Errorf("%s says %s is %s; its header says %s", index, link[1], said[1], is[1])
			}
		}
	}
	if checked == 0 {
		t.Fatal("no INDEX entry with a stated status was compared: the patterns no longer match the documents")
	}
	t.Logf("%d stated statuses compared with their ADR headers", checked)
}
