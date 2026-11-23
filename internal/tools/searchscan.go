package tools

import (
	"bytes"
	"regexp"
	"regexp/syntax"
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

// scanLines matches re against each line of data — the lines
// strings.Split(string(data), "\n") makes — and returns how many match
// and the first keep of them, each copied out of data. With blocks, each
// run of scanBlock lines is matched as a whole first and skipped when it
// does not match; the result is the same (blockCheckable says when that
// holds). It works on data's bytes: no copy of the file, no slice of
// every line, so a scan holds one file (verification of the fixes,
// ADR-0125). stop is consulted before every block after the first; when
// it reports true the scan ends with interrupted set and the count is
// not the file's.
func scanLines(data []byte, re *regexp.Regexp, blocks bool, keep int, stop func() bool) (count int, hits []lineHit, interrupted bool) {
	start, lineNo := 0, 0 // the block's first byte; lines before it
	for {
		if lineNo > 0 && stop() {
			return count, hits, true
		}
		// The block: up to scanBlock lines from start. end is just past
		// its last line's text; next is where the line after begins.
		end, next, k, last := start, start, 0, false
		for k < scanBlock {
			k++
			i := bytes.IndexByte(data[next:], '\n')
			if i < 0 {
				end, last = len(data), true
				break
			}
			end = next + i
			next = end + 1
		}
		if !blocks || re.Match(data[start:end]) {
			p := start
			for j := 0; j < k; j++ {
				e := end
				if j < k-1 {
					e = p + bytes.IndexByte(data[p:], '\n')
				}
				line := data[p:e]
				if re.Match(line) {
					count++
					if len(hits) < keep {
						hits = append(hits, lineHit{n: lineNo + j + 1, text: string(line)})
					}
				}
				p = e + 1
			}
		}
		lineNo += k
		if last {
			return count, hits, false
		}
		start = next
	}
}
