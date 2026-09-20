package termimg

import (
	"bytes"
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

// TestBoxForKeepsTheShapeAndTheCage: the box is chosen from the picture
// and clamped by the terminal. What matters for the accounting is only
// that it is never zero and never reaches the terminal's width; the shape
// is a comfort, and a wrong cell-aspect constant letterboxes rather than
// miscounts.
func TestBoxForKeepsTheShapeAndTheCage(t *testing.T) {
	for _, tc := range []struct {
		name               string
		pxW, pxH           int
		termCols, maxRows  int
		wantRows, wantCols int
	}{
		// 16:9 at 40 columns: 40 * 9 / (16 * 2.25) = 10 rows.
		{"landscape", 320, 180, 180, 20, 10, 40},
		// Square: 40 / 2.25 = 17.8 -> 18 rows, inside the ceiling.
		{"square", 200, 200, 180, 20, 18, 40},
		// Portrait hits the ceiling, so the columns come back down.
		{"portrait clipped by maxRows", 180, 320, 180, 12, 12, 15},
		// A narrow terminal clamps the width below its own, and the
		// picture gets shorter with it rather than being stretched:
		// 19 * 180 / (320 * 2.25) = 4.75 -> 5.
		{"narrow terminal", 320, 180, 20, 20, 5, 19},
	} {
		got := BoxFor(tc.pxW, tc.pxH, tc.termCols, tc.maxRows)
		if got.Rows != tc.wantRows || got.Cols != tc.wantCols {
			t.Errorf("%s: BoxFor(%d, %d, %d, %d) = %+v, want {%d %d}",
				tc.name, tc.pxW, tc.pxH, tc.termCols, tc.maxRows, got, tc.wantRows, tc.wantCols)
		}
		if got.Rows < 1 || got.Cols < 1 {
			t.Errorf("%s: a box that reserves nothing would be counted as one number and drawn as another", tc.name)
		}
		if tc.termCols > 1 && got.Cols >= tc.termCols {
			t.Errorf("%s: box reaches the terminal width; the terminal would wrap the picture", tc.name)
		}
		if got.Rows > tc.maxRows {
			t.Errorf("%s: %d rows exceeds the ceiling of %d", tc.name, got.Rows, tc.maxRows)
		}
	}
}

// TestBoxForRefusesNothing: every input yields a drawable box, because the
// caller has already decided to draw by the time it asks.
func TestBoxForRefusesNothing(t *testing.T) {
	for _, tc := range [][4]int{{0, 0, 80, 20}, {-1, 10, 80, 20}, {320, 180, 0, 20}, {320, 180, 80, 0}} {
		got := BoxFor(tc[0], tc[1], tc[2], tc[3])
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
