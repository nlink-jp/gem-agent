package tools

import (
	"regexp"
	"regexp/syntax"
	"strings"
)

// scanBlock is how many lines are checked at once before they are
// matched one by one (ADR-0125 §3), and how often a scan consults its
// stop function (ADR-0065 §1).
const scanBlock = 1024

// lineHit is one matching line: its 1-based number and its text.
type lineHit struct {
	n    int
	text string
}

// blockCheckable reports whether a block of lines may be matched as a
// whole before its lines are (ADR-0125 §3). Only a pattern with a
// literal prefix gains: Go finds the prefix by searching for it, while
// a pattern without one runs the general matcher over the whole block,
// which costs more than the same lines one by one. A pattern that
// anchors at the beginning or end of the text (^ $ \A \z outside (?m))
// matches at the edges of every line but only at the edges of a block,
// so a block could miss what a line finds; it is matched line by line.
// pattern is what was compiled into re, parsed with the flags
// regexp.Compile uses.
func blockCheckable(pattern string, re *regexp.Regexp) bool {
	if prefix, _ := re.LiteralPrefix(); prefix == "" {
		return false
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return false
	}
	return !anchorsText(parsed)
}

func anchorsText(re *syntax.Regexp) bool {
	if re.Op == syntax.OpBeginText || re.Op == syntax.OpEndText {
		return true
	}
	for _, sub := range re.Sub {
		if anchorsText(sub) {
			return true
		}
	}
	return false
}

// scanLines matches re against each line of text — the lines
// strings.Split(text, "\n") makes — and returns how many match and the
// first keep of them. With blocks, each run of scanBlock lines is
// matched as a whole first and skipped when it does not match; the
// result is the same (blockCheckable says when that holds). stop is
// consulted before every block after the first; when it reports true
// the scan ends with interrupted set and the count is not the file's.
func scanLines(text string, re *regexp.Regexp, blocks bool, keep int, stop func() bool) (count int, hits []lineHit, interrupted bool) {
	lines := strings.Split(text, "\n")
	off := 0 // byte offset of lines[b] in text
	for b := 0; b < len(lines); b += scanBlock {
		if b > 0 && stop() {
			return count, hits, true
		}
		e := min(b+scanBlock, len(lines))
		end := off // byte offset just past lines[e-1]
		for _, l := range lines[b:e] {
			end += len(l) + 1
		}
		end-- // the newline after the last line of the block is not in it
		if blocks && !re.MatchString(text[off:end]) {
			off = end + 1
			continue
		}
		for i := b; i < e; i++ {
			if !re.MatchString(lines[i]) {
				continue
			}
			count++
			if len(hits) < keep {
				hits = append(hits, lineHit{n: i + 1, text: lines[i]})
			}
		}
		off = end + 1
	}
	return count, hits, false
}
