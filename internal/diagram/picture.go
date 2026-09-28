package diagram

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"

	mr "github.com/nlink-jp/mermaid-render"
	"github.com/nlink-jp/mermaid-render/raster"
)

// NewPicture draws with mermaid-render (ADR-0092) in font, which the cmd
// layer loaded once — the view layer opens no file (ADR-0089 §5). A PNG
// over maxBytes is refused here, on the encoded bytes, before any segment
// is made (§5): a payload the screen would drop silently must never
// replace the source.
func NewPicture(font *raster.Font, maxBytes int) Picture {
	return func(src string) ([]byte, int, int, string, bool) {
		d, err := mr.Parse(src)
		if err != nil {
			var e *mr.Error
			if errors.As(err, &e) && e.Kind == mr.UnsupportedType {
				return nil, 0, 0, "", false // a gantt in the chat is not an error
			}
			return nil, 0, 0, err.Error(), true
		}
		img, err := raster.Render(d, raster.Options{Font: font})
		if err != nil {
			return nil, 0, 0, err.Error(), true
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, 0, 0, "encoding the picture: " + err.Error(), true
		}
		if buf.Len() > maxBytes {
			return nil, 0, 0, fmt.Sprintf("the picture is %d KiB, over the %d KiB an inline image may be", buf.Len()>>10, maxBytes>>10), true
		}
		b := img.Bounds()
		return buf.Bytes(), b.Dx(), b.Dy(), "", true
	}
}
