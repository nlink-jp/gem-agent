// Package diagram draws mermaid fenced blocks as Unicode box art for the
// terminal (ADR-0042). It draws the types the renderer is measured to
// draw faithfully — flowchart/graph, sequenceDiagram with ASCII labels,
// erDiagram — and leaves everything else as source. The advertised list
// (PromptSection) and the renderer's capability are one list here, so
// the model is never promised what the terminal cannot do.
package diagram

import (
	"fmt"
	"regexp"
	"strings"

	mermaid "github.com/AlexanderGrooff/mermaid-ascii/cmd"
	mdiagram "github.com/AlexanderGrooff/mermaid-ascii/pkg/diagram"
	"github.com/charmbracelet/x/ansi"
)

const (
	// widthMargin keeps the art inside glamour's code-block indent and
	// the TUI's width-1 clip: nothing the rewrite emits may ever need
	// to wrap in scrollback (v0.34.1 invariant).
	widthMargin = 6
	// maxArtLines bounds the art's height: a taller diagram is shown as
	// source — a screenful of box art stops being a summary.
	maxArtLines = 80
)

// An ER complexity cap (relationships/degree) was tried in v0.37.3 and
// reverted in v0.37.4 (operator direction): a dense diagram that FITS
// the screen is shown — readability is the operator's call, and "too
// complex, simplify" is a message to the model, not a threshold. The
// guards that remain are about being WRONG (labels, edge counts,
// phantom nodes), never about being ugly.

// kind is the renderable family of a mermaid block.
type kind int

const (
	kindUnsupported kind = iota
	kindFlow
	kindSequence
	kindER
)

// supported is the single source of truth for what the terminal draws,
// in model-facing wording (PromptSection) — pinned by test to the
// classifier below.
var supported = []string{
	"flowchart/graph (any direction, subgraphs; node shapes are drawn as boxes)",
	"sequenceDiagram with ASCII labels only",
	"erDiagram",
}

// Supported lists the renderable mermaid types, model-facing wording.
func Supported() []string { return append([]string(nil), supported...) }

// PromptSection is the system-prompt paragraph that tells the model what
// the terminal renders (ADR-0042 §1). Only the TUI composes it in —
// the plain REPL and one-shot mode show source and must not advertise
// a capability they lack.
func PromptSection() string {
	return "\n\nDiagrams in chat: this terminal renders these mermaid types inline — " +
		strings.Join(supported, "; ") +
		". Prefer them when a diagram helps. Any other mermaid type, or a sequence " +
		"diagram with non-ASCII labels, is shown in the chat as raw source — when you use " +
		"one anyway, add a one-line caption saying what it shows. Files are unaffected: " +
		"write any diagram type into files freely."
}

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
	subgraphRe  = regexp.MustCompile(`^\s*subgraph\s+([A-Za-z0-9_]+)`)
	nodeIDRe    = regexp.MustCompile(`^\s*([A-Za-z0-9_]+)`)
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

// Rewrite replaces every eligible ```mermaid block in markdown with a
// plain code block holding the box art, at the given terminal width.
// Blocks that cannot be drawn faithfully are left untouched.
func Rewrite(markdown string, width int) string {
	if !strings.Contains(markdown, "mermaid") {
		return markdown
	}
	lines := strings.Split(markdown, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		m := fenceOpen.FindStringSubmatch(lines[i])
		if m == nil || !strings.EqualFold(m[2], "mermaid") {
			out = append(out, lines[i])
			continue
		}
		fence := m[1]
		// Find the closing fence (same character, at least as long).
		end := -1
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], fence) && strings.TrimSpace(lines[j]) == strings.Repeat(fence[:1], len(strings.TrimSpace(lines[j]))) {
				end = j
				break
			}
		}
		if end < 0 {
			out = append(out, lines[i])
			continue
		}
		src := strings.Join(lines[i+1:end], "\n")
		if art, ok := Render(src, width); ok {
			out = append(out, "```text")
			out = append(out, strings.Split(art, "\n")...)
			out = append(out, "```")
		} else {
			out = append(out, lines[i:end+1]...)
		}
		i = end
	}
	return strings.Join(out, "\n")
}

// Render draws one mermaid source as box art that fits width, reporting
// false when the block must stay source: unsupported type, wide labels
// in a sequence diagram, renderer error, too wide/tall, or a label the
// renderer lost (ADR-0042 §3).
func Render(src string, width int) (string, bool) {
	body, _ := mdiagram.StripFrontmatter(src)
	k, dir := classify(body)
	if k == kindUnsupported {
		return "", false
	}
	if k == kindSequence && hasWide(body) {
		return "", false
	}
	body = directionRe.ReplaceAllString(body, "")
	prepared := prepare(k, body)
	budget := width - widthMargin
	if budget < 20 {
		return "", false
	}
	if k == kindFlow && edgesToSubgraphs(prepared) {
		// An edge whose endpoint is a subgraph id: the renderer draws a
		// phantom node named after the id — a wrong graph that the label
		// guard cannot see. Source is the honest display.
		return "", false
	}
	art, ok := renderFit(prepared, dir, budget)
	if !ok {
		return "", false
	}
	if !faithful(k, prepared, art) {
		return "", false
	}
	if k == kindFlow && flowEdgeCount(prepared) != arrowheads(art) {
		// Structural guard (v0.37.2): every source edge must have drawn
		// exactly one arrowhead. Label presence alone let a mis-parsed
		// edge syntax through as a plausible-looking wrong graph.
		return "", false
	}
	return art, true
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

// edgesToSubgraphs reports an edge endpoint that is a subgraph id.
func edgesToSubgraphs(src string) bool {
	ids := map[string]bool{}
	for _, line := range strings.Split(src, "\n") {
		if m := subgraphRe.FindStringSubmatch(line); m != nil {
			ids[m[1]] = true
		}
	}
	if len(ids) == 0 {
		return false
	}
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "subgraph") || !arrowTokRe.MatchString(t) {
			continue
		}
		t = edgeLabelRm.ReplaceAllString(t, "")
		for _, seg := range arrowTokRe.Split(t, -1) {
			for _, item := range strings.Split(seg, "&") {
				if m := nodeIDRe.FindStringSubmatch(item); m != nil && ids[m[1]] {
					return true
				}
			}
		}
	}
	return false
}

func classify(body string) (kind, string) {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "%%") {
			continue
		}
		m := headerRe.FindStringSubmatch(t)
		if m == nil {
			return kindUnsupported, ""
		}
		switch m[1] {
		case "graph", "flowchart":
			dir := "TD"
			if d := strings.ToUpper(m[2]); d == "LR" || d == "RL" {
				dir = "LR"
			}
			return kindFlow, dir
		case "sequenceDiagram":
			return kindSequence, ""
		case "erDiagram":
			return kindER, ""
		}
		return kindUnsupported, ""
	}
	return kindUnsupported, ""
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

// renderFit renders with the default layout, then with tight padding,
// and refuses anything that would not fit the width or the height cap.
func renderFit(src, dir string, budget int) (string, bool) {
	try := func(cfg *mdiagram.Config) (string, bool) {
		out, err := mermaid.RenderDiagram(src, cfg)
		if err != nil {
			return "", false
		}
		out = strings.TrimRight(out, "\n")
		lines := strings.Split(out, "\n")
		if len(lines) > maxArtLines {
			return "", false
		}
		for i, l := range lines {
			lines[i] = strings.TrimRight(l, " ")
			if ansi.StringWidth(lines[i]) > budget {
				return "", false
			}
		}
		return strings.Join(lines, "\n"), true
	}
	if art, ok := try(mdiagram.DefaultConfig()); ok {
		return art, true
	}
	if dir == "" {
		dir = "TD"
	}
	compact, err := mdiagram.NewCLIConfig(false, false, false, 0, 1, 0, dir)
	if err != nil {
		return "", false
	}
	return try(compact)
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
