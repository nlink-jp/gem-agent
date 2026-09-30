// Package diagram draws mermaid fenced blocks in a reply for the
// terminal: as pictures where the TUI draws images (ADR-0092), as box
// art where it does not (ADR-0042, ADR-0063, ADR-0095). It is a
// view-layer concern and nothing else: the model is never told about
// it, the transcript keeps the source the model wrote, and the screen
// shows the drawing when the engine drew it and the source, with a
// one-line note, when it refused.
//
// Both come from mermaid-render, from the same parse: the art is the
// picture's layout on a grid of cells, checked on the grid every time
// (ADR-0095). There is no translation table and no guard here any more:
// the engine reads mermaid as mermaid does and refuses what it cannot
// draw right.
//
// There is no FIT rule (ADR-0063 deleted it): art wider than the
// terminal wraps there, art taller than a screen scrolls. For that to
// lose nothing the art must BYPASS the Markdown renderer — glamour
// word-wraps code-block lines at spaces, which shears wide box art
// into interleaved fragments (measured). Split therefore returns art as
// its own segments for the TUI to emit verbatim; the terminal's own
// wrap splits overflowing rows in order. That is ugliness, and ugliness
// is the reader's call, not a gate.
package diagram

import (
	"errors"
	"fmt"
	"image"
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
	mr "github.com/nlink-jp/mermaid-render"
	"github.com/nlink-jp/mermaid-render/raster"
)

var fenceOpen = regexp.MustCompile("^(`{3,}|~{3,})\\s*([A-Za-z0-9_+-]*)")

// Segment is one run of a reply: markdown for the Markdown renderer,
// finished box art the TUI must emit verbatim (see the package comment
// for why art may never pass through glamour), or a picture.
type Segment struct {
	Text string
	Art  bool
	// Img is set for a fence drawn as a picture (ADR-0092). The TUI cuts
	// it into bands a screen can hold and encodes those. Source is the
	// fence as the model wrote it, so a failure after this point still
	// shows the source (WithNote) — the picture never disappears together
	// with it.
	Img    image.Image
	Source string
}

// Picture draws one mermaid source as an image (ADR-0092). attempted is
// false for a diagram type it does not draw; otherwise why is empty on
// success and names the refusal.
type Picture func(src string) (img image.Image, why string, attempted bool)

// WithNote is a fence shown as source with the one-line note that says
// why it is not drawn (ADR-0063 §4): blank lines on both sides, so the
// note is its own paragraph, never a prefix of what follows.
func WithNote(source, why string) string {
	return source + "\n\n*diagram shown as source: " + noteSafe(why) + "*\n"
}

// Split partitions markdown around its renderable ```mermaid blocks
// and draws them. A block of a supported kind the engine refuses keeps
// its fence and gains a one-line note saying why —
// for the reader, who closes the loop; the model never sees the
// screen (ADR-0063 §4). Unsupported diagram types pass through
// untouched and note-free: a gantt in the chat is not an error. A
// ```mermaid line that is CONTENT of an enclosing fence (an example
// inside a ````markdown block, a quoted fence in a ```text block) is
// data, not a diagram — a closing fence carries no info string, so a
// labeled opener inside an open fence can never be its close.
//
// With a Picture (the session draws images, ADR-0092 §1) every fence is
// offered to it instead of the box-art renderer, as written. A refusal
// shows the source with the note; nothing falls back to art (ADR-0092
// B4).
func Split(markdown string, pic Picture) []Segment {
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
		block := strings.Join(lines[i:end+1], "\n")
		if pic != nil {
			img, why, attempted := drawPicture(pic, src)
			switch {
			case attempted && why == "" && img != nil:
				flush()
				segs = append(segs, Segment{Img: img, Source: block})
			case attempted:
				if why == "" {
					why = "the renderer returned no picture"
				}
				md = append(md, WithNote(block, why))
			default:
				md = append(md, block)
			}
			i = end
			continue
		}
		art, why, attempted := render(src)
		switch {
		case attempted && why == "":
			flush()
			segs = append(segs, Segment{Text: art, Art: true})
		case attempted:
			md = append(md, WithNote(block, why))
		default:
			md = append(md, block)
		}
		i = end
	}
	flush()
	return segs
}

// drawPicture calls pic, turning a panic into a refusal: the engine runs
// on the UI's update path, and a defect in it must cost a picture, not the
// session (ADR-0092 §5).
func drawPicture(pic Picture, src string) (img image.Image, why string, attempted bool) {
	defer func() {
		if r := recover(); r != nil {
			img, why, attempted = nil, fmt.Sprintf("the diagram renderer failed: %v", r), true
		}
	}()
	return pic(src)
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

// render draws one mermaid source as box art (ADR-0095). attempted is
// false for a diagram type the engine does not draw as text (a gantt in
// the chat is not an error). For an attempted draw, why is empty on
// success and names the refusal otherwise — wrongness only, never size:
// there is no width or height gate (ADR-0063 §3). A panic in the engine
// is a refusal: it runs on the UI's update path, and a defect in it must
// cost a drawing, not the session.
func render(src string) (art, why string, attempted bool) {
	defer func() {
		if r := recover(); r != nil {
			art, why, attempted = "", fmt.Sprintf("the diagram renderer failed: %v", r), true
		}
	}()
	d, err := mr.Parse(src)
	if err == nil {
		art, err = raster.RenderText(d, raster.TextOptions{Width: cellWidth})
	}
	if err != nil {
		var e *mr.Error
		if errors.As(err, &e) && e.Kind == mr.UnsupportedType {
			return "", "", false
		}
		return "", strings.ReplaceAll(err.Error(), "\n", " "), true
	}
	return art, "", true
}

// cellWidth is a character's width as the TUI measures it, so the art's
// columns and the rest of the screen agree.
func cellWidth(r rune) int { return ansi.StringWidth(string(r)) }
