package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestADRFileCitationsResolve closes the class three verification passes
// kept reopening: a record asserts something about code and cites
// `file.go:NNN` for it, and the number is off — which is worse than no
// citation, because it looks checked. Round three's repair of an off-by-one
// citation landed two lines off, onto an unrelated symbol; that is the third
// instance of the same class, and the rule for three of a kind is to stop
// fixing instances and close the class with a mechanism.
//
// The rule, deliberately bounded: for every `[name.ext:NNN](path)` link in
// docs/{en,ja}/adr, the path must resolve, the file must have that many
// lines, and at least one backticked identifier from the citation's own
// paragraph must appear within CiteWindow lines of the cited one. A
// citation whose paragraph names `emit` must land near something that says
// `emit`. Exemptions live in one list below, each with its reason, so a
// stale exemption is visible rather than silent.
const citeWindow = 0

// No exemptions. The first version carried one, for a citation whose
// paragraph writes the pad as arithmetic rather than as an identifier — and
// a verification pass measured that the entry was a no-op (the paragraph
// backticks `height − printed − view − 1`, whose tokens the cited line
// contains) and that a stale entry could never announce itself, which is
// what the recorded rule requires of an allowlist. An empty list is a
// better control than a dead one: when a citation genuinely needs an
// exemption, add it with its reason AND a check that fails when it stops
// being needed.
var citeRe = regexp.MustCompile(`\[([A-Za-z0-9_.-]+\.(?:go|mod|md)):(\d+)\]\(([^)]+)\)`)
var tickRe = regexp.MustCompile("`([^`]+)`")

func TestADRFileCitationsResolve(t *testing.T) {
	root := repoRoot(t)
	var checked int
	for _, lang := range []string{"en", "ja"} {
		dir := filepath.Join(root, "docs", lang, "adr")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			checked += checkOneADR(t, dir, e.Name())
		}
	}
	if checked == 0 {
		t.Fatal("no citations checked — the pattern or the layout changed, and a test that checks nothing passes")
	}
	t.Logf("%d file citations resolved across docs/en/adr and docs/ja/adr", checked)
}

func checkOneADR(t *testing.T, dir, name string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var n int
	for _, para := range strings.Split(string(b), "\n\n") {
		ticks := map[string]bool{}
		for _, m := range tickRe.FindAllStringSubmatch(para, -1) {
			for _, tok := range strings.FieldsFunc(m[1], func(r rune) bool {
				return r != '_' && r != '.' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9')
			}) {
				if len(tok) >= 3 {
					ticks[tok] = true
				}
			}
		}
		for _, c := range citeRe.FindAllStringSubmatch(para, -1) {
			n++
			base, lineStr, rel := c[1], c[2], c[3]
			target := filepath.Clean(filepath.Join(dir, rel))
			src, err := os.ReadFile(target)
			if err != nil {
				t.Errorf("%s: cites %s:%s but %s does not resolve: %v", name, base, lineStr, rel, err)
				continue
			}
			if filepath.Base(target) != base {
				t.Errorf("%s: link text says %s but the path is %s", name, base, filepath.Base(target))
				continue
			}
			lines := strings.Split(string(src), "\n")
			ln, _ := strconv.Atoi(lineStr)
			if ln < 1 || ln > len(lines) {
				t.Errorf("%s: cites %s:%d but the file has %d lines", name, base, ln, len(lines))
				continue
			}
			lo, hi := max(1, ln-citeWindow), min(len(lines), ln+citeWindow)
			window := strings.Join(lines[lo-1:hi], "\n")
			found := false
			for tok := range ticks {
				if strings.Contains(window, tok) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: cites %s:%d, but no identifier its paragraph backticks appears within %d lines.\n    line %d is: %s\n    paragraph names: %s",
					name, base, ln, citeWindow, ln, strings.TrimSpace(lines[ln-1]), strings.Join(sortedKeys(ticks), " "))
			}
		}
	}
	return n
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
