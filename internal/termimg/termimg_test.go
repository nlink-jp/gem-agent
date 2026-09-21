package termimg

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestPayloadMeasuresZeroCells is the fact the whole design rests on: the
// row counter cannot see an image, so it must be told. If a future x/ansi
// gave these payloads a width, physicalRows would start counting them as
// text cells and the declaration scheme would need rethinking — so the day
// that changes, this says so.
func TestPayloadMeasuresZeroCells(t *testing.T) {
	data := fakePNG(2048)
	for _, p := range []Protocol{ITerm2, Kitty} {
		got, err := Payload(p, data, Box{Rows: 6, Cols: 40})
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if w := ansi.StringWidth(got); w != 0 {
			t.Errorf("%s: width = %d, want 0", p, w)
		}
		if s := ansi.Strip(got); s != "" {
			t.Errorf("%s: strip = %q, want empty", p, s)
		}
		// Every scrollback line is hard-wrapped before it is printed; a
		// sheared base64 run is not an image.
		for _, width := range []int{20, 79, 179} {
			if w := ansi.Hardwrap(got, width, true); w != got {
				t.Errorf("%s: Hardwrap(%d) altered the payload", p, width)
			}
		}
	}
}

// TestPayloadDeclaresTheBox: the number the row counter will be told has to
// be in the bytes the terminal reads, or the screen and the accounting are
// two numbers from two places.
func TestPayloadDeclaresTheBox(t *testing.T) {
	data := fakePNG(64)
	box := Box{Rows: 12, Cols: 40}

	it, err := Payload(ITerm2, data, box)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"height=12", "width=40", "preserveAspectRatio=1", "inline=1"} {
		if !strings.Contains(it, want) {
			t.Errorf("iterm2 payload lacks %q", want)
		}
	}
	if !strings.HasSuffix(it, "\a") {
		t.Error("iterm2 payload does not end with BEL")
	}

	k, err := Payload(Kitty, data, box)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"r=12", "c=40", "f=100", "a=T", "q=2"} {
		if !strings.Contains(k, want) {
			t.Errorf("kitty payload lacks %q", want)
		}
	}
}

// TestKittyChunking: the protocol caps one escape's payload, and a reply
// from the terminal would land in Bubble Tea's input, so q=2 rides on the
// first escape and the continuation escapes carry m=.
func TestKittyChunking(t *testing.T) {
	data := fakePNG(12 * 1024)
	got, err := Payload(Kitty, data, Box{Rows: 6, Cols: 40})
	if err != nil {
		t.Fatal(err)
	}
	escs := strings.Split(strings.TrimSuffix(got, "\x1b\\"), "\x1b\\")
	if len(escs) < 2 {
		t.Fatalf("got %d escapes, want a chunked sequence", len(escs))
	}
	var b64 strings.Builder
	for i, esc := range escs {
		ctl, payload, ok := strings.Cut(strings.TrimPrefix(esc, "\x1b_G"), ";")
		if !ok {
			t.Fatalf("escape %d has no payload separator", i)
		}
		want := "m=1"
		if i == len(escs)-1 {
			want = "m=0"
		}
		if !strings.Contains(ctl, want) {
			t.Errorf("escape %d: controls %q lack %s", i, ctl, want)
		}
		if len(payload) > kittyChunk {
			t.Errorf("escape %d: %d B exceeds the %d B limit", i, len(payload), kittyChunk)
		}
		b64.WriteString(payload)
	}
	dec, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil || len(dec) != len(data) {
		t.Fatalf("chunks did not reassemble: %v", err)
	}
}

// TestFitClampsBelowTheTerminal: an image at the full width invites the
// terminal to wrap the picture and add rows the declared height never
// claimed, which is decision 2's whole reason. The margin matches
// wrapForScrollback's width-1.
func TestFitClampsBelowTheTerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		box      Box
		width    int
		wantCols int
		wantRows int
	}{
		{"wider than the terminal", Box{Rows: 6, Cols: 200}, 80, 79, 6},
		{"exactly the terminal width", Box{Rows: 6, Cols: 80}, 80, 79, 6},
		{"one under already", Box{Rows: 6, Cols: 79}, 80, 79, 6},
		{"comfortably inside", Box{Rows: 6, Cols: 40}, 80, 40, 6},
		{"unknown width is left alone", Box{Rows: 6, Cols: 200}, 0, 200, 6},
	} {
		got := Fit(tc.box, tc.width)
		if got.Cols != tc.wantCols || got.Rows != tc.wantRows {
			t.Errorf("%s: Fit(%+v, %d) = %+v, want {%d %d}", tc.name, tc.box, tc.width, got, tc.wantRows, tc.wantCols)
		}
	}
}

// TestPayloadRefusesWhatCannotBeCounted: a box that reserves nothing would
// be counted as some number and drawn as another, which is the failure this
// package exists to prevent. So is a payload with no protocol.
func TestPayloadRefusesWhatCannotBeCounted(t *testing.T) {
	data := fakePNG(64)
	for _, tc := range []struct {
		name string
		p    Protocol
		box  Box
		want error
	}{
		{"zero rows", ITerm2, Box{Rows: 0, Cols: 40}, ErrEmptyBox},
		{"zero cols", ITerm2, Box{Rows: 6, Cols: 0}, ErrEmptyBox},
		{"negative rows", Kitty, Box{Rows: -1, Cols: 40}, ErrEmptyBox},
		{"no protocol", None, Box{Rows: 6, Cols: 40}, ErrNoProtocol},
	} {
		if _, err := Payload(tc.p, data, tc.box); err != tc.want {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := Payload(ITerm2, nil, Box{Rows: 6, Cols: 40}); err == nil {
		t.Error("an empty image must not produce a payload")
	}
}

// fakePNG is the PNG signature followed by bytes, not a picture: the framing
// tests are about how a payload is cut and declared, and the kitty path
// passes anything that opens like a PNG through untouched. Using a real
// encoder here would test the encoder.
func fakePNG(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i%251 + 1)
	}
	copy(b, pngSignature)
	return b
}

// kittyImage reassembles the picture a kitty payload carries, and its
// control keys from the first escape.
func kittyImage(t *testing.T, payload string) (ctl string, data []byte) {
	t.Helper()
	var b64 strings.Builder
	for i, esc := range strings.Split(strings.TrimSuffix(payload, "\x1b\\"), "\x1b\\") {
		c, chunk, ok := strings.Cut(strings.TrimPrefix(esc, "\x1b_G"), ";")
		if !ok {
			t.Fatalf("escape %d has no payload separator", i)
		}
		if i == 0 {
			ctl = c
		}
		b64.WriteString(chunk)
	}
	data, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		t.Fatalf("payload does not reassemble: %v", err)
	}
	return ctl, data
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestKittyGetsPNGEvenFromAJPEG is the regression for a JPEG that drew
// nothing on kitty (operator, 2026-09-22): f=100 means PNG and only PNG, the
// terminal rejected the JPEG bytes, and q=2 hid the rejection. The payload
// now always carries a PNG, and a picture larger than the box can show is
// scaled down into it rather than re-encoded at full size.
func TestKittyGetsPNGEvenFromAJPEG(t *testing.T) {
	box := Box{Rows: 3, Cols: 10}
	got, err := Payload(Kitty, testJPEG(t, 2000, 1000), box)
	if err != nil {
		t.Fatal(err)
	}
	ctl, data := kittyImage(t, got)
	if !strings.Contains(ctl, "f=100") {
		t.Errorf("controls %q do not say PNG", ctl)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the kitty payload is not a PNG: %v", err)
	}
	if cfg.Width > box.Cols*kittyPxPerCol || cfg.Height > box.Rows*kittyPxPerRow {
		t.Errorf("PNG is %dx%d, larger than the %dx%d px the box can show",
			cfg.Width, cfg.Height, box.Cols*kittyPxPerCol, box.Rows*kittyPxPerRow)
	}
	if ratio := float64(cfg.Width) / float64(cfg.Height); ratio < 1.95 || ratio > 2.05 {
		t.Errorf("PNG is %dx%d: the 2:1 picture lost its shape", cfg.Width, cfg.Height)
	}

	// A picture already within the box keeps its size.
	got, err = Payload(Kitty, testJPEG(t, 64, 32), box)
	if err != nil {
		t.Fatal(err)
	}
	_, data = kittyImage(t, got)
	if cfg, err := png.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width != 64 || cfg.Height != 32 {
		t.Errorf("small JPEG: %v, %dx%d, want a 64x32 PNG", err, cfg.Width, cfg.Height)
	}
}

// TestKittyRefusesWhatItCannotMakeAPNGOf: bytes that are neither PNG nor a
// decodable picture get no payload, so drawImage draws nothing rather than
// sending a picture the terminal would silently drop.
func TestKittyRefusesWhatItCannotMakeAPNGOf(t *testing.T) {
	if _, err := Payload(Kitty, []byte("not a picture at all"), Box{Rows: 3, Cols: 10}); err == nil {
		t.Error("undecodable bytes produced a kitty payload")
	}
}

// TestITerm2GetsTheBytesAsTheyAre: iTerm2's File= names no format and draws
// JPEG itself, so nothing is converted on that path.
func TestITerm2GetsTheBytesAsTheyAre(t *testing.T) {
	jpg := testJPEG(t, 64, 32)
	got, err := Payload(ITerm2, jpg, Box{Rows: 3, Cols: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, base64.StdEncoding.EncodeToString(jpg)) {
		t.Error("the iTerm2 payload does not carry the JPEG unchanged")
	}
}
