package diagram

import (
	"errors"
	"image"

	mr "github.com/nlink-jp/mermaid-render"
	"github.com/nlink-jp/mermaid-render/raster"
)

// NewPicture draws with mermaid-render (ADR-0092) in font, which the cmd
// layer loaded once — the view layer opens no file (ADR-0089 §5). The
// image is not encoded here: the TUI cuts a tall one into bands a screen
// can hold, encodes each, and refuses the whole over termimg.MaxBytes.
// The scale is spelled out: termimg.DiagramBox's 28 px per em is this
// Scale 2, not whatever the library's default becomes. A nil font is not
// a default: the engine would read Hiragino from disk on every render,
// inside the view layer.
func NewPicture(font *raster.Font) Picture {
	if font == nil {
		return nil
	}
	return func(src string) (image.Image, string, bool) {
		d, err := mr.Parse(src)
		if err != nil {
			var e *mr.Error
			if errors.As(err, &e) && e.Kind == mr.UnsupportedType {
				return nil, "", false // a gantt in the chat is not an error
			}
			return nil, err.Error(), true
		}
		img, err := raster.Render(d, raster.Options{Font: font, Scale: pxPerEmScale})
		if err != nil {
			return nil, err.Error(), true
		}
		return img, "", true
	}
}

// pxPerEmScale is mermaid-render's Scale for 28 px per em on its 14 px
// base — termimg.DiagramPxPerEm; TestPictureScaleMatchesTheBox holds them
// together.
const pxPerEmScale = 2
