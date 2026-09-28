package diagram

import (
	"image"
	"strings"
	"testing"

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

// NewPicture draws with the engine; an unsupported type is not attempted;
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
	if _, _, attempted := NewPicture(font)("stateDiagram-v2\n    [*] --> A"); attempted {
		t.Error("an unsupported type was attempted")
	}
	if _, why, _ := NewPicture(font)("flowchart TD\n    A --> 😀"); why == "" {
		t.Error("a character no font has was drawn")
	}
}
