package termimg

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestMeasureIsTheValidator: a block whose MIME claims an image proves
// nothing — the bytes have to decode. The MIME type is the one part of an
// MCP block a server chooses freely, so it selects a container, never a
// fact.
func TestMeasureIsTheValidator(t *testing.T) {
	w, h, ok := Measure(pngOf(t, 320, 180))
	if !ok || w != 320 || h != 180 {
		t.Errorf("png: got (%d, %d, %v), want (320, 180, true)", w, h, ok)
	}

	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, image.NewGray(image.Rect(0, 0, 64, 48)), nil); err != nil {
		t.Fatal(err)
	}
	if w, h, ok := Measure(jbuf.Bytes()); !ok || w != 64 || h != 48 {
		t.Errorf("jpeg: got (%d, %d, %v), want (64, 48, true)", w, h, ok)
	}

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"not an image at all", []byte("this is a text file pretending")},
		{"a truncated png header", pngOf(t, 8, 8)[:12]},
	} {
		if _, _, ok := Measure(tc.data); ok {
			t.Errorf("%s: measured as an image", tc.name)
		}
	}
}

// TestMeasureRefusesOverTheCeiling: the bound is checked before any
// payload is built, and it is on the DECODED bytes because that is what
// the measured cost tracks.
func TestMeasureRefusesOverTheCeiling(t *testing.T) {
	if _, _, ok := Measure(make([]byte, MaxBytes+1)); ok {
		t.Error("a block over MaxBytes must not be drawn")
	}
	// Just under the ceiling still has to be a real image — the size
	// check is not a substitute for decoding.
	if _, _, ok := Measure(make([]byte, MaxBytes-1)); ok {
		t.Error("garbage under the ceiling must not be drawn")
	}
}

// TestBoxForFillsTheScreenNotMoreThanThePicture: the box is as large as
// the terminal allows — its width less one, maxRows tall — and never
// larger than the picture at one pixel per reported pixel (ADR-0103).
func TestBoxForFillsTheScreenNotMoreThanThePicture(t *testing.T) {
	iterm, kitty := Cell{W: 8, H: 18}, Cell{W: 7, H: 13}
	for _, tc := range []struct {
		name               string
		pxW, pxH           int
		termCols, maxRows  int
		cell               Cell
		wantRows, wantCols int
	}{
		// 400 x 100 cells natively; the width binds: 180/400 = 0.45.
		{"wide photo fills the width", 3200, 1800, 181, 70, iterm, 45, 180},
		// 225 x 177.8 cells; the height binds: 40/177.8 = 0.225.
		{"tall photo fills the height", 1800, 3200, 181, 40, iterm, 40, 51},
		// 8 x 3.6 cells natively: drawn at that, not at the screen.
		{"an icon stays its own size", 64, 64, 181, 70, iterm, 4, 8},
		// kitty's cell shapes the box: 200 x 100 cells, halved to fit.
		{"kitty's cell", 1400, 1300, 101, 100, kitty, 50, 100},
		// No pixels reported, or a cell no terminal has: 8x18.
		{"no cell reported", 3200, 1800, 181, 70, Cell{}, 45, 180},
		{"absurd cell", 3200, 1800, 181, 70, Cell{W: 2, H: 40}, 45, 180},
		// An unknown width keeps the old 40 columns.
		{"unknown width", 3200, 1800, 0, 70, iterm, 10, 40},
		// A narrow terminal clamps the width below its own.
		{"narrow terminal", 3200, 1800, 20, 70, iterm, 5, 19},
	} {
		got := BoxFor(tc.pxW, tc.pxH, tc.termCols, tc.maxRows, tc.cell)
		if got.Rows != tc.wantRows || got.Cols != tc.wantCols {
			t.Errorf("%s: BoxFor(%d, %d, %d, %d, %+v) = %+v, want {%d %d}",
				tc.name, tc.pxW, tc.pxH, tc.termCols, tc.maxRows, tc.cell, got, tc.wantRows, tc.wantCols)
		}
	}
}

// TestBoxForHoldsItsBounds over a grid of pictures, terminals and cells:
// never empty, never at the terminal's width, never past maxRows, never
// more than half a cell past the picture's own size, and its shape within
// a cell's rounding of the picture's.
func TestBoxForHoldsItsBounds(t *testing.T) {
	sizes := []int{1, 7, 13, 64, 100, 333, 1024, 1800, 3200, 9000}
	for _, cell := range []Cell{{W: 8, H: 18}, {W: 7, H: 13}, {W: 16, H: 36}} {
		for _, pxW := range sizes {
			for _, pxH := range sizes {
				for _, term := range [][2]int{{80, 23}, {181, 70}, {20, 3}, {300, 1}} {
					cols, maxRows := term[0], term[1]
					got := BoxFor(pxW, pxH, cols, maxRows, cell)
					where := fmt.Sprintf("%dx%d px, %dx%d term, cell %+v: %+v", pxW, pxH, cols, maxRows, cell, got)
					if got.Rows < 1 || got.Cols < 1 {
						t.Fatalf("%s: a box that reserves nothing", where)
					}
					if got.Cols >= cols || got.Rows > maxRows {
						t.Fatalf("%s: past the screen", where)
					}
					natW, natH := float64(pxW)/cell.W, float64(pxH)/cell.H
					if float64(got.Cols) > max(1, natW+0.5) || float64(got.Rows) > max(1, natH+0.5) {
						t.Fatalf("%s: larger than the picture (%.1f x %.1f cells)", where, natW, natH)
					}
					// Shape: where both sides are many cells, the box's
					// width over height matches the picture's to within
					// a cell of rounding on either side.
					if got.Cols >= 10 && got.Rows >= 10 {
						want := natW / natH
						lo := (float64(got.Cols) - 1) / (float64(got.Rows) + 1)
						hi := (float64(got.Cols) + 1) / (float64(got.Rows) - 1)
						if want < lo || want > hi {
							t.Fatalf("%s: shape %.3f, picture's %.3f", where, float64(got.Cols)/float64(got.Rows), want)
						}
					}
				}
			}
		}
	}
}

// TestBoxForRefusesNothing: every input yields a drawable box, because the
// caller has already decided to draw by the time it asks.
func TestBoxForRefusesNothing(t *testing.T) {
	for _, tc := range [][4]int{{0, 0, 80, 20}, {-1, 10, 80, 20}, {320, 180, 0, 20}, {320, 180, 80, 0}} {
		got := BoxFor(tc[0], tc[1], tc[2], tc[3], Cell{})
		if got.Rows < 1 || got.Cols < 1 {
			t.Errorf("BoxFor%v = %+v, want at least 1x1", tc, got)
		}
	}
}

// TestFormatsAreWhatDecodes: the format list a tool description repeats is
// the decoder list, no more and no less.
func TestFormatsAreWhatDecodes(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	// The GIF is spelled out in bytes: importing image/gif to encode one
	// would register its decoder in this test binary, and the test would then
	// measure itself instead of the package.
	oneByOneGIF := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff" +
		",\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02L\x01\x00;")
	encoders := map[string]func(*bytes.Buffer) error{
		"PNG":  func(b *bytes.Buffer) error { return png.Encode(b, img) },
		"JPEG": func(b *bytes.Buffer) error { return jpeg.Encode(b, img, nil) },
		"GIF":  func(b *bytes.Buffer) error { _, err := b.Write(oneByOneGIF); return err },
	}
	for name, encode := range encoders {
		var b bytes.Buffer
		if err := encode(&b); err != nil {
			t.Fatal(err)
		}
		_, _, drawable := Measure(b.Bytes())
		if promised := strings.Contains(Formats, name); promised != drawable {
			t.Errorf("%s: Formats promises it = %v, Measure decodes it = %v", name, promised, drawable)
		}
	}
	for _, never := range []string{"WebP", "HEIC", "HEIF", "BMP", "TIFF"} {
		if strings.Contains(Formats, never) {
			t.Errorf("Formats promises %s, and nothing here decodes it", never)
		}
	}
}
