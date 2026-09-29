package diagram

import (
	"image"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/termimg"

	"github.com/nlink-jp/mermaid-render/raster"
)

const flowFence = "```mermaid\nflowchart TD\n    A([開始]) --> B{判定}\n    B -- はい --> C[完了]\n```"

// With a Picture, a fence becomes a picture segment holding its source; a
// refusal is the source with the note, never box art (ADR-0092 B4); an
// unsupported type is the source, silently; a panic is a refusal.
func TestSplitWithPicture(t *testing.T) {
	var got []string
	pic := func(src string) (image.Image, string, bool) {
		got = append(got, src)
		switch {
		case strings.HasPrefix(src, "gantt"):
			return nil, "", false
		case strings.Contains(src, "refuse"):
			return nil, "syntax error: no", true
		case strings.Contains(src, "panic"):
			panic("boom")
		}
		return image.NewRGBA(image.Rect(0, 0, 300, 200)), "", true
	}
	md := "before\n\n" + flowFence + "\n\nafter"
	segs := Split(md, pic)
	if len(segs) != 3 || segs[1].Img == nil || segs[1].Img.Bounds().Dx() != 300 || segs[1].Source != flowFence {
		t.Fatalf("segments = %+v", segs)
	}
	if segs[0].Text != "before\n" || segs[2].Text != "\nafter" {
		t.Errorf("text around the picture = %q, %q", segs[0].Text, segs[2].Text)
	}
	// The source as written reaches the engine: ([…]) and {…} shapes and
	// the `-- text -->` form, which the art table would have rewritten.
	if len(got) != 1 || got[0] != "flowchart TD\n    A([開始]) --> B{判定}\n    B -- はい --> C[完了]" {
		t.Errorf("the engine got %q", got)
	}
	for src, want := range map[string]string{
		"flowchart TD\n    refuse": "*diagram shown as source: syntax error: no*",
		"flowchart TD\n    panic":  "*diagram shown as source: the diagram renderer failed: boom*",
		"gantt\n    title x":       "",
	} {
		fence := "```mermaid\n" + src + "\n```"
		var segs []Segment
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%q: the renderer's panic escaped Split: %v", src, r)
				}
			}()
			segs = Split(fence, pic)
		}()
		if len(segs) != 1 || segs[0].Img != nil || segs[0].Art || !strings.HasPrefix(segs[0].Text, fence) {
			t.Errorf("%q: %+v", src, segs)
			continue
		}
		if note := strings.TrimSpace(strings.TrimPrefix(segs[0].Text, fence)); note != want {
			t.Errorf("%q: note %q, want %q", src, note, want)
		}
	}
}

// NewPicture draws with the engine — a pie chart and a state diagram too;
// an unsupported type
// is not attempted;
// a character no font has is refused.
func TestNewPicture(t *testing.T) {
	font, err := raster.DefaultFont()
	if err != nil {
		t.Skipf("no system font: %v", err)
	}
	img, why, attempted := NewPicture(font)("flowchart TD\n    A([開始]) --> B{判定}")
	if !attempted || why != "" || img == nil || img.Bounds().Dx() <= 0 {
		t.Fatalf("drawn: %v, why %q, attempted %v", img != nil, why, attempted)
	}
	img, why, attempted = NewPicture(font)("pie title 内訳\n    \"犬\" : 3\n    \"猫\" : 1")
	if !attempted || why != "" || img == nil {
		t.Errorf("a pie chart (mermaid-render v0.2.1): drawn %v, why %q, attempted %v", img != nil, why, attempted)
	}
	img, why, attempted = NewPicture(font)("stateDiagram-v2\n    [*] --> 待機\n    待機 --> 完了 : 受信\n    完了 --> [*]")
	if !attempted || why != "" || img == nil {
		t.Errorf("a state diagram (mermaid-render v0.3.0): drawn %v, why %q, attempted %v", img != nil, why, attempted)
	}
	if _, _, attempted := NewPicture(font)("gantt\n    title x"); attempted {
		t.Error("an unsupported type was attempted")
	}
	if _, why, _ := NewPicture(font)("flowchart TD\n    A --> 😀"); why == "" {
		t.Error("a character no font has was drawn")
	}
}

// The engine's scale and the box's pixels per em are one fact: a Scale-2
// render is 28 px per em (termimg.DiagramPxPerEm).
func TestPictureScaleMatchesTheBox(t *testing.T) {
	if pxPerEmScale*14 != termimg.DiagramPxPerEm {
		t.Errorf("Scale %d on a 14 px base is %d px per em; the box assumes %d", pxPerEmScale, pxPerEmScale*14, termimg.DiagramPxPerEm)
	}
	if NewPicture(nil) != nil {
		t.Error("a nil font made a Picture that would read fonts in the view layer")
	}
}

// A Picture that returns nothing and no reason still names one.
func TestEmptyPictureStillHasANote(t *testing.T) {
	pic := func(string) (image.Image, string, bool) { return nil, "", true }
	segs := Split("```mermaid\nflowchart TD\n  A\n```", pic)
	if len(segs) != 1 || !strings.Contains(segs[0].Text, "diagram shown as source: the renderer returned no picture") {
		t.Errorf("segments: %+v", segs)
	}
}
