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
func NewPicture(font *raster.Font) Picture {
	return func(src string) (image.Image, string, bool) {
		d, err := mr.Parse(src)
		if err != nil {
			var e *mr.Error
			if errors.As(err, &e) && e.Kind == mr.UnsupportedType {
				return nil, "", false // a gantt in the chat is not an error
			}
			return nil, err.Error(), true
		}
		img, err := raster.Render(d, raster.Options{Font: font})
		if err != nil {
			return nil, err.Error(), true
		}
		return img, "", true
	}
}
