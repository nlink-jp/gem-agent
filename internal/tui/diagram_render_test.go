package tui

import (
	"image"
	"math/rand"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nlink-jp/gem-agent/internal/diagram"
	"github.com/nlink-jp/gem-agent/internal/termimg"
)

// replyModel is a plain-styled model at width 80 with the real renderer.
func replyModel(opts Options) *Model {
	opts.Theme = "notty"
	m := New(opts)
	return &m
}

// replyText is what renderReply sends to scrollback, joined as emitted.
func replyText(segs []Segment) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.Text
	}
	return strings.Join(parts, "\n")
}

// The reply renderer draws mermaid fences in place (ADR-0063): the reply
// shows box art where the model wrote a fence, and the fence path is the
// ONLY diagram path — there is no tool.
func TestReplyDrawsMermaidFence(t *testing.T) {
	m := replyModel(Options{})
	out := replyText(m.renderReply("intro\n\n```mermaid\nflowchart TD\n  A[alpha] --> B[beta]\n```\n\noutro\n"))
	if strings.Contains(out, "flowchart TD") {
		t.Fatalf("mermaid source shown instead of art:\n%s", out)
	}
	for _, want := range []string{"intro", "outro", "alpha", "beta", "┌"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in rendered reply:\n%s", want, out)
		}
	}
}

// Wide art bypasses glamour and reaches the output verbatim — glamour
// word-wraps code-block lines at spaces, which would shear a drawing
// wider than the wrap width into interleaved fragments (the ADR-0063
// independent-review case). The terminal's own wrap is the accepted
// overflow behavior, and it needs the intact lines.
func TestReplyKeepsWideArtIntact(t *testing.T) {
	md := "```mermaid\ngraph LR\n  A[Parse config] --> B[Resolve project] --> C[Connect MCP] --> D[Discover skills] --> E[Build prompt] --> F[Start TUI]\n```\n"
	var art string
	for _, seg := range diagram.Split(md, nil) {
		if seg.Art {
			art = seg.Text
		}
	}
	if art == "" {
		t.Fatal("no art segment for the wide chain")
	}
	if out := replyText(replyModel(Options{}).renderReply(md)); !strings.Contains(out, art) {
		t.Fatalf("wide art did not pass through verbatim at width 80:\n%s", out)
	}
}

// A supported diagram that cannot be drawn keeps its source and shows
// the reader-facing note; the model is never told (the reader closes
// the loop, ADR-0063 §4).
func TestReplyKeepsSourceWithNote(t *testing.T) {
	out := replyText(replyModel(Options{}).renderReply("```mermaid\nsequenceDiagram\n  participant U as 操作者\n  U->>A: 質問\n```\n"))
	if !strings.Contains(out, "sequenceDiagram") || !strings.Contains(out, "操作者") {
		t.Fatalf("source lost:\n%s", out)
	}
	if !strings.Contains(out, "diagram shown as source") {
		t.Errorf("note missing:\n%s", out)
	}
}

// emPicture is a blank picture w x h terminal lines at the diagram scale.
func emPicture(w, h int) image.Image {
	return image.NewRGBA(image.Rect(0, 0, w*termimg.DiagramPxPerEm*12/10, h*termimg.DiagramPxPerEm*12/10))
}

// fakePicture draws every fence as a 20 x 10 line picture, one that says
// "tall" as 20 x 100, and refuses one that says so.
func fakePicture(src string) (image.Image, string, bool) {
	switch {
	case strings.Contains(src, "refuse"):
		return nil, "syntax error: refused", true
	case strings.Contains(src, "tall"):
		return emPicture(20, 100), "", true
	}
	return emPicture(20, 10), "", true
}

// Where the session draws images, a fence becomes a picture in a
// declared box (ADR-0092): one em of diagram text per terminal line, the
// width from the cell's aspect, blank lines around it as between
// paragraphs, and its rows told to the counter.
func TestReplyDrawsPicture(t *testing.T) {
	m := replyModel(Options{Images: termimg.ITerm2, Picture: fakePicture,
		CellAspect: func() (float64, bool) { return 1.86, true }})
	m.aspect = 1.86 // as the first size report reads it
	segs := m.renderReply("before\n\n```mermaid\nflowchart TD\n  A --> B\n```\n\nafter")
	if len(segs) != 5 || segs[1].Text != "" || segs[3].Text != "" {
		t.Fatalf("segments: %+v", segs)
	}
	pic := segs[2]
	if pic.Rows != 10 || !strings.HasPrefix(pic.Text, "\x1b]1337;File=") || !strings.Contains(pic.Text, "width=37;height=10;") {
		t.Errorf("picture segment: rows %d, %q", pic.Rows, pic.Text[:min(80, len(pic.Text))])
	}
	if !strings.Contains(segs[0].Text, "before") || !strings.Contains(segs[4].Text, "after") {
		t.Errorf("text around the picture: %q, %q", segs[0].Text, segs[4].Text)
	}
}

// A session without an image protocol keeps the art lane whatever
// Picture holds; a refusal shows the source with the note, never art.
func TestReplyPictureOnlyWhereImagesDraw(t *testing.T) {
	src := "```mermaid\nflowchart TD\n  A[alpha] --> B[beta]\n```"
	if out := replyText(replyModel(Options{Picture: fakePicture}).renderReply(src)); !strings.Contains(out, "┌") {
		t.Errorf("no protocol: want art, got\n%s", out)
	}
	refused := "```mermaid\nflowchart TD\n  A[refuse] --> B\n```"
	out := replyText(replyModel(Options{Images: termimg.Kitty, Picture: fakePicture}).renderReply(refused))
	if strings.Contains(out, "┌") || !strings.Contains(out, "A[refuse]") || !strings.Contains(out, "diagram shown as source: syntax error: refused") {
		t.Errorf("refusal: want the source and the note, got\n%s", out)
	}
}

// A picture that cannot become payloads after the fence was replaced —
// here, over the byte limit once encoded — shows the source with the
// note: the picture never vanishes with its source.
func TestReplyPictureFailureShowsSource(t *testing.T) {
	noisy := func(string) (image.Image, string, bool) {
		img := image.NewRGBA(image.Rect(0, 0, 1200, 1200))
		r := rand.New(rand.NewSource(1))
		r.Read(img.Pix)
		return img, "", true
	}
	m := replyModel(Options{Images: termimg.ITerm2, Picture: noisy})
	out := replyText(m.renderReply("```mermaid\nflowchart TD\n  A --> B\n```"))
	if !strings.Contains(out, "A --> B") || !strings.Contains(out, "diagram shown as source: the picture is over") {
		t.Errorf("want the source and the note, got\n%.300s", out)
	}
}

// A picture taller than half the screen is drawn as bands of at most half
// the screen, back to back, their rows adding up to the whole: kitty clips
// a picture taller than the screen (measured, ADR-0092 §4).
func TestTallPictureIsDrawnInBands(t *testing.T) {
	m := replyModel(Options{Images: termimg.Kitty, Picture: fakePicture})
	m.height = 40
	segs := m.renderReply("```mermaid\nflowchart TD\n  tall --> B\n```")
	rows := 0
	for _, s := range segs {
		if s.Rows == 0 || s.Rows > 20 || !strings.HasPrefix(s.Text, "\x1b_G") {
			t.Fatalf("segment %+.60v: want kitty bands of at most 20 rows, back to back", s)
		}
		rows += s.Rows
	}
	if len(segs) != 5 || rows != 100 {
		t.Errorf("%d bands, %d rows; want 5 and 100", len(segs), rows)
	}
}

// A streamed reply with a diagram flushes as ONE write, the picture's
// declared rows told to the counter and the lines that follow it after
// it (ADR-0092 §3): the flush a tool call triggers mid-reply included.
func TestFlushedPictureIsCounted(t *testing.T) {
	c := &capture{}
	m := New(Options{Theme: "notty", Images: termimg.ITerm2, Picture: fakePicture, Printer: c.printer,
		RenderFactory: func(int) func(string) string { return func(s string) string { return s } }})
	m.live.WriteString("text\n\n```mermaid\nflowchart TD\n  A --> B\n```")
	before := m.hold.printed
	m.emitAfterLive(m.takeLive(), "tail")
	if len(c.printed) != 1 {
		t.Fatalf("%d writes, want one", len(c.printed))
	}
	out := c.printed[0]
	pic, tail := strings.Index(out, "\x1b]1337;File="), strings.Index(out, "tail")
	if pic < 0 || tail < pic {
		t.Errorf("want the picture, then the tail: %q", out)
	}
	// "text", a blank line, the picture's 10 rows, "tail".
	if got := m.hold.printed - before; got != 13 {
		t.Errorf("counted %d rows, want 13", got)
	}
}

// The cell's aspect is read at every size report (ADR-0092 §4), and a
// terminal that reports no pixels leaves the last reading.
func TestSizeReportReadsCellAspect(t *testing.T) {
	aspect, ok := 1.86, true
	m := New(Options{Theme: "notty", Printer: (&capture{}).printer,
		CellAspect: func() (float64, bool) { return aspect, ok }})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	if m.aspect != 1.86 {
		t.Fatalf("aspect after the first size report = %v", m.aspect)
	}
	aspect, ok = 0, false
	next, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 40})
	if got := next.(Model).aspect; got != 1.86 {
		t.Errorf("a report without pixels changed the aspect to %v", got)
	}
}

// iTerm2 gets a tall picture whole: it scrolls one correctly, and bands
// there showed seams and a missing corner (measured, ADR-0092 §4).
func TestTallPictureIsWholeOnITerm2(t *testing.T) {
	m := replyModel(Options{Images: termimg.ITerm2, Picture: fakePicture})
	m.height = 40
	segs := m.renderReply("```mermaid\nflowchart TD\n  tall --> B\n```")
	if len(segs) != 1 || segs[0].Rows != 100 {
		t.Errorf("%d segments (first %d rows), want one of 100", len(segs), segs[0].Rows)
	}
}
