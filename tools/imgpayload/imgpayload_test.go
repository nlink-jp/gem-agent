package imgpayload

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"
)

func TestIterm(t *testing.T) {
	data := []byte("not really a png")
	got := Iterm(data, "height=6")
	const want = "\x1b]1337;File=inline=1;size=16;height=6:"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("prefix = %q, want %q", got[:min(len(got), len(want))], want)
	}
	if !strings.HasSuffix(got, "\a") {
		t.Fatal("payload does not end with BEL")
	}
	body := strings.TrimSuffix(strings.TrimPrefix(got, want), "\a")
	dec, err := base64.StdEncoding.DecodeString(body)
	if err != nil || !bytes.Equal(dec, data) {
		t.Fatalf("body did not round-trip: %v", err)
	}
	if strings.Contains(Iterm(data, ""), ";:") {
		t.Fatal("empty args left a stray separator")
	}
}

func TestKittyChunks(t *testing.T) {
	data := TestPNG(1024, 1024) // large enough to need several chunks
	got := Kitty(data, "f=100,r=6,c=40")
	if !strings.Contains(got, "q=2") {
		t.Fatal("responses not suppressed: a reply would corrupt a cursor report")
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
		wantMore := "m=1"
		if i == len(escs)-1 {
			wantMore = "m=0"
		}
		if !strings.Contains(ctl, wantMore) {
			t.Errorf("escape %d: controls %q lack %s", i, ctl, wantMore)
		}
		if len(payload) > KittyChunk {
			t.Errorf("escape %d: payload %d B exceeds the %d B limit", i, len(payload), KittyChunk)
		}
		b64.WriteString(payload)
	}
	dec, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil || !bytes.Equal(dec, data) {
		t.Fatalf("chunks did not reassemble: %v", err)
	}
}

func TestKittySingleChunkCarriesControls(t *testing.T) {
	got := Kitty([]byte("tiny"), "f=100,r=1")
	if !strings.HasPrefix(got, "\x1b_Ga=T,q=2,f=100,r=1;") || !strings.HasSuffix(got, "\x1b\\") {
		t.Fatalf("single-chunk form wrong: %q", got)
	}
	if strings.Contains(got, "m=") {
		t.Error("single chunk should carry no m= continuation")
	}
}

// TestSixelBandsScaleTheImage pins the one property pinprobe's
// dose-response run depends on: more bands is a taller picture. The run
// measured 36px/72px/144px occupying 3/4/6 rows while the counter said
// 1 every time, and that reading is only meaningful if the bands really
// do change the height the terminal is told about.
func TestSixelBandsScaleTheImage(t *testing.T) {
	for _, bands := range []int{6, 12, 24} {
		got := Sixel(bands)
		if !strings.HasPrefix(got, "\x1bPq") || !strings.HasSuffix(got, "\x1b\\") {
			t.Fatalf("%d bands: not a DCS q sequence", bands)
		}
		if want := ";" + itoa(bands*6) + "#0"; !strings.Contains(got, want) {
			t.Errorf("%d bands: raster attributes do not declare %d pixels tall", bands, bands*6)
		}
		if n := strings.Count(got, "-"); n != bands {
			t.Errorf("%d bands: %d band separators, want %d", bands, n, bands)
		}
	}
	if len(Sixel(24)) <= len(Sixel(6)) {
		t.Error("more bands did not make a longer payload")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestTestPNGDecodes(t *testing.T) {
	for _, d := range [][2]int{{320, 180}, {180, 320}} {
		cfg, err := png.DecodeConfig(bytes.NewReader(TestPNG(d[0], d[1])))
		if err != nil {
			t.Fatalf("%dx%d: %v", d[0], d[1], err)
		}
		if cfg.Width != d[0] || cfg.Height != d[1] {
			t.Errorf("got %dx%d, want %dx%d", cfg.Width, cfg.Height, d[0], d[1])
		}
	}
}
