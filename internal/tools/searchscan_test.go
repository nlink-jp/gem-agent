package tools

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

// ADR-0125 §3: only a pattern with a literal prefix checks blocks, and
// never one that anchors at the beginning or end of the text.
func TestBlockCheckable(t *testing.T) {
	for _, c := range []struct {
		pattern string
		want    bool
	}{
		{regexp.QuoteMeta("needle"), true},
		{regexp.QuoteMeta("a.b(c)"), true},
		{`func \w+Registry`, true},
		{`func (\w+\s+){10}zzzz`, true},
		{`(?m)^needle`, false}, // no literal prefix the matcher can search for
		{`^func`, false},       // has the prefix "func", anchors the text
		{`needle$`, false},
		{`\Aheader`, false},
		{`trailer\z`, false},
		{`foo(^bar)?`, false},
		{`foo(?-m:$)`, false},
		{`(?i)needle`, false},
		{`\w+Registry`, false},
		{`x*`, false},
	} {
		re := regexp.MustCompile(c.pattern)
		if got := blockCheckable(c.pattern, re); got != c.want {
			t.Errorf("blockCheckable(%q) = %v, want %v", c.pattern, got, c.want)
		}
	}
}

// referenceScan is the loop v0.108.2 ran: every line matched one by one.
func referenceScan(text string, re *regexp.Regexp, keep int) (int, []lineHit) {
	count := 0
	var hits []lineHit
	for i, l := range strings.Split(text, "\n") {
		if re.MatchString(l) {
			count++
			if len(hits) < keep {
				hits = append(hits, lineHit{n: i + 1, text: l})
			}
		}
	}
	return count, hits
}

// Checking blocks first changes no result, for every pattern
// blockCheckable admits, over text with CR, invalid UTF-8, empty lines,
// a missing or present trailing newline, and matches on both sides of a
// block edge.
func TestScanLinesBlocksMatchLineByLine(t *testing.T) {
	patterns := []string{
		regexp.QuoteMeta("needle"), `need\w*`, `ne+dle\r`, `needle\b`, `needle\B`,
		`needle[^x]*end`, `needle(?s:.)*end`, `needle.?`, `needle(?m:$)`, `ne(?m:^)?edle`,
	}
	pieces := []string{"needle", "end", "x", "\r", "\n", "\n", "\n", "\xff", "é", " ", "needlend", "word", ""}
	rng := rand.New(rand.NewSource(1))
	multiBlock := 0
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		if !blockCheckable(p, re) {
			t.Fatalf("%q was meant to take the block path", p)
		}
		for iter := 0; iter < 300; iter++ {
			var b strings.Builder
			n := rng.Intn(4000)
			if iter%4 == 0 {
				// Thousands of lines: several blocks, so anchors, \b and
				// matches meet block edges, not only the one text below
				// (pre-release review).
				n = 8000 + rng.Intn(12000)
			}
			for i := 0; i < n; i++ {
				b.WriteString(pieces[rng.Intn(len(pieces))])
			}
			text := b.String()
			if strings.Count(text, "\n") >= scanBlock {
				multiBlock++
			}
			wantCount, wantHits := referenceScan(text, re, 5)
			gotCount, gotHits, cut := scanLines(text, re, true, 5, func() bool { return false })
			if cut || gotCount != wantCount || !equalHits(gotHits, wantHits) {
				t.Fatalf("pattern %q, text of %d bytes: got %d %v, want %d %v", p, len(text), gotCount, gotHits, wantCount, wantHits)
			}
		}
	}
	if multiBlock < 500 {
		t.Fatalf("only %d texts spanned more than one block", multiBlock)
	}
	// Matches on lines 1023–1026 straddle the first block edge.
	var b strings.Builder
	for i := 1; i <= 3000; i++ {
		if i >= 1023 && i <= 1026 {
			b.WriteString("needle\n")
		} else {
			b.WriteString("filler\n")
		}
	}
	re := regexp.MustCompile("needle")
	count, hits, _ := scanLines(b.String(), re, true, 5, func() bool { return false })
	if count != 4 || len(hits) != 4 || hits[0].n != 1023 || hits[3].n != 1026 {
		t.Errorf("block edge: count %d hits %v", count, hits)
	}
}

func equalHits(a, b []lineHit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The scan consults stop before every block after the first, and a cut
// scan says so (ADR-0065 §1).
func TestScanLinesStopsBetweenBlocks(t *testing.T) {
	text := strings.Repeat("needle\n", 3*scanBlock)
	re := regexp.MustCompile("needle")
	for _, blocks := range []bool{false, true} {
		asked := 0
		_, _, cut := scanLines(text, re, blocks, 5, func() bool { asked++; return asked == 2 })
		if !cut || asked != 2 {
			t.Errorf("blocks=%v: cut=%v after %d asks, want a cut at the second ask", blocks, cut, asked)
		}
		asked = 0
		count, _, cut := scanLines(text, re, blocks, 5, func() bool { asked++; return false })
		if cut || count != 3*scanBlock || asked != 3 {
			t.Errorf("blocks=%v: count %d cut %v asks %d", blocks, count, cut, asked)
		}
	}
}
