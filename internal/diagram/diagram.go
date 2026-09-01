// Package diagram draws mermaid fenced blocks as Unicode box art for
// the terminal (ADR-0042, ADR-0063). It is a view-layer concern and
// nothing else: the model is never told about it, the transcript keeps
// the source the model wrote, and the screen shows the drawing when it
// is faithful and the source when it is not.
//
// TWO RULES, AND NOTHING ELSE (ADR-0042 §5 as amended by ADR-0063):
//
//  1. TRANSLATE — a deterministic mapping of mermaid constructs the
//     renderer's grammar rejects into ones it accepts, preserving the
//     graph: node shapes to boxes, `A -- text --> B` to `-->|text|`,
//     `&` inside a label to the full-width ＆, presentation-only
//     statements dropped. No guessing: each entry is a syntax fact.
//     This table is FROZEN. Measured before freezing (v0.38.0):
//     removing it costs 2–3 correct diagrams of 18 and lets one wrong
//     graph through, so it earns its place — but when a new construct
//     breaks, the fix is a renderer-grammar fact for this table or it
//     is nowhere (rule 2 already shows the source).
//  2. VERIFY — every label the source wrote must appear in the art,
//     and a flowchart's edge count must equal the arrowheads drawn.
//
// There is no FIT rule (ADR-0063 deleted it): art wider than the
// terminal wraps there, art taller than a screen scrolls. For that to
// lose nothing the art must BYPASS the Markdown renderer — glamour
// word-wraps code-block lines at spaces, which shears wide box art
// into interleaved fragments (measured; a space-free probe line
// wrongly suggested pass-through, and the independent review caught
// it). Split therefore returns art as its own segments for the TUI to
// emit verbatim, the lane shell output uses; the terminal's own wrap
// splits overflowing rows in order. That is ugliness, and ugliness is
// the reader's call, not a gate (the standard that reverted the ER
// complexity cap in v0.37.4). The guards that remain are about being
// WRONG (labels, edge counts, phantom nodes), never about being ugly.
package diagram

import (
	"fmt"
	"regexp"
	"strings"

	mermaid "github.com/AlexanderGrooff/mermaid-ascii/cmd"
	mdiagram "github.com/AlexanderGrooff/mermaid-ascii/pkg/diagram"
	"github.com/charmbracelet/x/ansi"
)

// kind is the renderable family of a mermaid block.
type kind int

const (
	kindUnsupported kind = iota
	kindFlow
	kindSequence
	kindER
)

var (
	fenceOpen = regexp.MustCompile("^(`{3,}|~{3,})\\s*([A-Za-z0-9_+-]*)")
	// headerRe classifies the first meaningful line.
	headerRe = regexp.MustCompile(`^\s*(graph|flowchart|sequenceDiagram|erDiagram)\b\s*([A-Za-z]{2})?`)
	// shapeRe matches node definitions whose shape the renderer does not
	// parse; they become boxes (ADR-0042 §3). Group 1 is the id, exactly
	// one of the later groups the label. Order matters: `((` before `(`.
	shapeRe = regexp.MustCompile(`\b([A-Za-z0-9_]+)(\{\{([^{}]*)\}\}|\{([^{}]*)\}|\(\(([^()]*)\)\)|\(\[([^\]]*)\]\)|\[\[([^\]]*)\]\]|\[\(([^()]*)\)\]|\[/([^/\]]*)/\]|>([^\]]*)\]|\(([^()]*)\))`)
	// ampLabelRe finds '&' inside a box label; the renderer reads a bare
	// '&' as the fan-in operator even inside a label (measured), so it
	// becomes the full-width ＆ — same meaning to a reader, no operator.
	ampLabelRe = regexp.MustCompile(`\[([^\]]*)&([^\]]*)\]`)
	// edgeTextRes rewrite the `A -- text --> B` family of edge labels to
	// the `-->|text|` form the renderer parses; left alone, the renderer
	// read "A -- text" as a node (measured: a decision node lost its
	// branches and phantom nodes appeared, v0.37.2).
	edgeTextRes = []struct {
		re  *regexp.Regexp
		out string
	}{
		{regexp.MustCompile(`--\s+(.+?)\s+-->`), "-->|$1|"},
		{regexp.MustCompile(`-\.\s+(.+?)\s+\.->`), "-.->|$1|"},
		{regexp.MustCompile(`==\s+(.+?)\s+==>`), "==>|$1|"},
	}
	// arrowTokRe tokenizes a flowchart statement into its arrow-bearing
	// edges (open links --- are not counted: they draw no head).
	arrowTokRe  = regexp.MustCompile(`-\.+->|-->|==>`)
	edgeLabelRm = regexp.MustCompile(`\|[^|]*\|`)
	// directionRe matches a `direction XX` statement — a subgraph layout
	// hint the renderer draws as a literal node (measured v0.37.3: it
	// also fused adjacent subgraph titles). Dropped before rendering.
	directionRe = regexp.MustCompile(`(?m)^\s*direction\s+[A-Za-z]{2}\s*$`)
	// presentational statements carry no graph semantics and the renderer
	// may reject them.
	presentationRe = regexp.MustCompile(`^\s*(classDef|class|style|linkStyle|click)\b`)
	classSuffixRe  = regexp.MustCompile(`:::[A-Za-z0-9_-]+`)
	// decorationRe strips the renderer's line art (box drawing, block
	// elements, geometric arrowheads) and whitespace. The fidelity
	// guard compares through it because the renderer PADS labels with
	// its own glyphs: a horizontal edge label is drawn as
	// "──IP─/─CIDR──" and a label crossing a subgraph border as
	// "Domain│/ FQDN". Stripping only whitespace read those as lost
	// labels and refused correct diagrams (v0.37.5). Stripping
	// decoration can only make the guard more permissive about
	// PRESENCE; the edge-count guard still proves the structure.
	decorationRe = regexp.MustCompile(`[\x{2500}-\x{25FF}\s]+`)
	// label extraction for the fidelity guard
	boxLabelRe  = regexp.MustCompile(`\[("?)([^\]"]+)"?\]`)
	edgeLabelRe = regexp.MustCompile(`\|([^|]+)\|`)
	seqPartRe   = regexp.MustCompile(`^\s*(?:participant|actor)\s+(\S+)(?:\s+as\s+(.+))?\s*$`)
	seqMsgRe    = regexp.MustCompile(`^\s*\S+\s*(?:->>|-->>|->|-->|-x|--x|-\)|--\))\s*\S+\s*:\s*(.+?)\s*$`)
	erRelRe     = regexp.MustCompile(`^\s*(\S+)\s+[|}o{\-\.]+\s+(\S+)\s*:\s*(.+?)\s*$`)
)

// Segment is one run of a reply: markdown for the Markdown renderer,
// or finished box art the TUI must emit verbatim (see the package
// comment for why art may never pass through glamour).
type Segment struct {
	Text string
	Art  bool
}

// Split partitions markdown around its renderable ```mermaid blocks
// and draws them. A block of a supported kind that cannot be drawn
// faithfully keeps its fence and gains a one-line note saying why —
// for the reader, who closes the loop; the model never sees the
// screen (ADR-0063 §4). Unsupported diagram types pass through
// untouched and note-free: a gantt in the chat is not an error. A
// ```mermaid line that is CONTENT of an enclosing fence (an example
// inside a ````markdown block, a quoted fence in a ```text block) is
// data, not a diagram — a closing fence carries no info string, so a
// labeled opener inside an open fence can never be its close.
func Split(markdown string) []Segment {
	if !strings.Contains(strings.ToLower(markdown), "mermaid") {
		return []Segment{{Text: markdown}}
	}
	lines := strings.Split(markdown, "\n")
	var segs []Segment
	var md []string
	flush := func() {
		if len(md) > 0 {
			segs = append(segs, Segment{Text: strings.Join(md, "\n")})
			md = nil
		}
	}
	enclosing := "" // opener of the non-mermaid fence we are inside
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if enclosing != "" {
			md = append(md, line)
			if closesFence(line, enclosing) {
				enclosing = ""
			}
			continue
		}
		m := fenceOpen.FindStringSubmatch(line)
		if m == nil {
			md = append(md, line)
			continue
		}
		if !strings.EqualFold(m[2], "mermaid") {
			enclosing = m[1]
			md = append(md, line)
			continue
		}
		fence := m[1]
		end := -1
		for j := i + 1; j < len(lines); j++ {
			if closesFence(lines[j], fence) {
				end = j
				break
			}
		}
		if end < 0 {
			md = append(md, line)
			continue
		}
		src := strings.Join(lines[i+1:end], "\n")
		art, why, attempted := render(src)
		switch {
		case attempted && why == "":
			flush()
			segs = append(segs, Segment{Text: art, Art: true})
		case attempted:
			md = append(md, lines[i:end+1]...)
			// Blank lines on both sides: the note is its own
			// paragraph, never a prefix of what follows.
			md = append(md, "", "*diagram shown as source: "+noteSafe(why)+"*", "")
		default:
			md = append(md, lines[i:end+1]...)
		}
		i = end
	}
	flush()
	return segs
}

// closesFence reports whether line closes a fence opened by opener:
// the same character, at least as long, and nothing else on the line.
func closesFence(line, opener string) bool {
	if !strings.HasPrefix(line, opener) {
		return false
	}
	t := strings.TrimSpace(line)
	return t == strings.Repeat(opener[:1], len(t))
}

// noteSafe keeps a reason from breaking the note's emphasis markup —
// a renderer error can contain any character.
var noteSafe = strings.NewReplacer("*", "＊", "_", "＿", "`", "'").Replace

// render draws one mermaid source. attempted is false for a diagram
// type the terminal renderer does not know. For an attempted draw,
// why is empty on success and names what went wrong otherwise —
// wrongness only, never size: there is no width or height gate
// (ADR-0063 §3).
func render(src string) (art, why string, attempted bool) {
	body, _ := mdiagram.StripFrontmatter(src)
	k := classify(body)
	if k == kindUnsupported {
		return "", "", false
	}
	if k == kindSequence && hasWide(body) {
		return "", "the terminal renderer misaligns non-ASCII sequence labels", true
	}
	body = directionRe.ReplaceAllString(body, "")
	prepared := prepare(k, body)
	out, err := mermaid.RenderDiagram(prepared, mdiagram.DefaultConfig())
	if err != nil {
		return "", "not valid mermaid for the terminal renderer: " +
			strings.ReplaceAll(err.Error(), "\n", " "), true
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	art = strings.Join(lines, "\n")
	if !faithful(k, prepared, art) {
		return "", "the renderer lost a label from the source", true
	}
	if k == kindFlow && flowEdgeCount(prepared) != arrowheads(art) {
		// Structural guard (v0.37.2): every source edge must have drawn
		// exactly one arrowhead. Label presence alone let a mis-parsed
		// edge syntax through as a plausible-looking wrong graph.
		return "", fmt.Sprintf("the renderer drew %d arrowheads for %d edges",
			arrowheads(art), flowEdgeCount(prepared)), true
	}
	return art, "", true
}

// flowEdgeCount counts the arrow-bearing edges a flowchart source
// declares: for each statement, consecutive arrow-separated segments
// contribute |left| × |right| edges, where a segment's size is its
// number of '&'-separated endpoints.
func flowEdgeCount(src string) int {
	total := 0
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "%%") || headerRe.MatchString(t) ||
			strings.HasPrefix(t, "subgraph") || t == "end" || strings.HasPrefix(t, "direction") {
			continue
		}
		t = edgeLabelRm.ReplaceAllString(t, "")
		segs := arrowTokRe.Split(t, -1)
		if len(segs) < 2 {
			continue
		}
		count := func(seg string) int {
			n := 0
			for _, item := range strings.Split(seg, "&") {
				if strings.TrimSpace(item) != "" {
					n++
				}
			}
			return n
		}
		for i := 0; i+1 < len(segs); i++ {
			total += count(segs[i]) * count(segs[i+1])
		}
	}
	return total
}

// arrowheads counts the arrowhead glyphs the renderer draws.
func arrowheads(art string) int {
	n := 0
	for _, r := range art {
		if strings.ContainsRune("►◄▲▼", r) {
			n++
		}
	}
	return n
}

func classify(body string) kind {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "%%") {
			continue
		}
		m := headerRe.FindStringSubmatch(t)
		if m == nil {
			return kindUnsupported
		}
		switch m[1] {
		case "graph", "flowchart":
			return kindFlow
		case "sequenceDiagram":
			return kindSequence
		case "erDiagram":
			return kindER
		}
		return kindUnsupported
	}
	return kindUnsupported
}

// hasWide reports any rune the terminal draws in two cells — the case
// the sequence renderer cannot align (measured).
func hasWide(s string) bool {
	for _, r := range s {
		if r > 0x7F && ansi.StringWidth(string(r)) > 1 {
			return true
		}
	}
	return false
}

// prepare normalizes what the renderer does not parse: node shapes to
// boxes, presentational lines and class suffixes dropped.
func prepare(k kind, body string) string {
	if k != kindFlow {
		return body
	}
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		if presentationRe.MatchString(line) {
			continue
		}
		line = classSuffixRe.ReplaceAllString(line, "")
		line = shapeRe.ReplaceAllStringFunc(line, func(m string) string {
			g := shapeRe.FindStringSubmatch(m)
			for _, t := range g[3:] {
				if t != "" {
					return g[1] + "[" + t + "]"
				}
			}
			return m
		})
		for ampLabelRe.MatchString(line) {
			line = ampLabelRe.ReplaceAllString(line, "[$1＆$2]")
		}
		for _, e := range edgeTextRes {
			line = e.re.ReplaceAllString(line, e.out)
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// faithful checks that every label written in the source appears in
// the art — the renderer must never draw less than was written.
func faithful(k kind, src, art string) bool {
	var labels []string
	for _, line := range strings.Split(src, "\n") {
		switch k {
		case kindFlow:
			for _, m := range boxLabelRe.FindAllStringSubmatch(line, -1) {
				labels = append(labels, m[2])
			}
			for _, m := range edgeLabelRe.FindAllStringSubmatch(line, -1) {
				labels = append(labels, m[1])
			}
		case kindSequence:
			if m := seqPartRe.FindStringSubmatch(line); m != nil {
				if m[2] != "" {
					labels = append(labels, m[2])
				} else {
					labels = append(labels, m[1])
				}
			} else if m := seqMsgRe.FindStringSubmatch(line); m != nil {
				labels = append(labels, m[1])
			}
		case kindER:
			if m := erRelRe.FindStringSubmatch(line); m != nil {
				labels = append(labels, m[1], m[2], m[3])
			}
		}
	}
	flat := decorationRe.ReplaceAllString(art, "")
	for _, l := range labels {
		l = strings.TrimSpace(strings.Trim(l, `"`))
		if l == "" {
			continue
		}
		if !strings.Contains(flat, decorationRe.ReplaceAllString(l, "")) {
			return false
		}
	}
	return true
}

// String renders a kind for diagnostics.
func (k kind) String() string {
	switch k {
	case kindFlow:
		return "flowchart"
	case kindSequence:
		return "sequence"
	case kindER:
		return "er"
	}
	return fmt.Sprintf("unsupported(%d)", int(k))
}
