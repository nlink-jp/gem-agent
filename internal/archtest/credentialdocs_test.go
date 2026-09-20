package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestDocsNameEveryCredentialReadTool: the documents that tell an operator
// which tools ask before reading a credential-named file name all of them.
//
// The list lives in code (risk.credentialReadTools) and is repeated in prose,
// and a tool added to the first was not added to the second: show_image joined
// the map when it shipped and no document mentioned it. An operator reading
// the reference would have concluded the model could put `.env` on the screen
// unasked. The test finds the passages by what they say rather than by line,
// and fails if it finds none.
func TestDocsNameEveryCredentialReadTool(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "internal", "risk", "risk.go"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)var credentialReadTools = map\[string\]bool\{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatal("risk.credentialReadTools not found: the test no longer knows where the list is")
	}
	var tools []string
	for _, m := range regexp.MustCompile(`"([a-z_]+)":\s*true`).FindAllSubmatch(block[1], -1) {
		tools = append(tools, string(m[1]))
	}
	sort.Strings(tools)
	if len(tools) < 3 {
		t.Fatalf("parsed only %v from credentialReadTools", tools)
	}

	// A passage is about this rule when it names the credential-read ADR,
	// says "credential", and lists read_file in backticks.
	adr := regexp.MustCompile(`ADR-00(85|15)\b`)
	passages := 0
	for _, dir := range []string{"docs/en/reference", "docs/ja/reference"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, e := range entries {
			raw, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, para := range strings.Split(string(raw), "\n\n") {
				if !adr.MatchString(para) || !strings.Contains(para, "`read_file`") {
					continue
				}
				if !strings.Contains(para, "redential") && !strings.Contains(para, "資格情報") {
					continue
				}
				passages++
				for _, tool := range tools {
					if !strings.Contains(para, "`"+tool+"`") {
						first := strings.SplitN(strings.TrimSpace(para), "\n", 2)[0]
						t.Errorf("%s/%s: the passage beginning %q lists the tools that ask before a credential read, without `%s`",
							dir, e.Name(), first, tool)
					}
				}
			}
		}
	}
	if passages == 0 {
		t.Fatal("no passage about the credential-read rule was found: the test would pass without reading anything")
	}
	t.Logf("%d passages checked against %v", passages, tools)
}
