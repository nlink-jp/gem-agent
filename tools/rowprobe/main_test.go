package main

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestPayloadsMeasureZeroCells guards the fact the whole approach
// rests on: the width counter cannot see an image. If a future x/ansi
// ever gave these payloads a nonzero width, physicalRows would start
// counting them as text cells and the declaration scheme would need
// rethinking — so the day that changes, this test says so.
func TestPayloadsMeasureZeroCells(t *testing.T) {
	data := testPNG(320, 180)
	for _, tc := range []struct {
		name string
		s    string
	}{
		{"iterm", itermPayload(data, "width=40;height=6")},
		{"kitty", kittyPayload(data, "f=100,r=6,c=40")},
	} {
		if w := ansi.StringWidth(tc.s); w != 0 {
			t.Errorf("%s: width = %d, want 0", tc.name, w)
		}
		if got := ansi.Strip(tc.s); got != "" {
			t.Errorf("%s: strip = %q, want empty", tc.name, got)
		}
		// The wrap the TUI applies to every scrollback line must leave
		// the payload byte-identical; a sheared base64 run is not an
		// image.
		if got := ansi.Hardwrap(tc.s, 79, true); got != tc.s {
			t.Errorf("%s: Hardwrap altered the payload (%d B -> %d B)", tc.name, len(tc.s), len(got))
		}
	}
}

func TestItermPayload(t *testing.T) {
	data := []byte("not really a png")
	got := itermPayload(data, "height=6")
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
	if strings.Contains(itermPayload(data, ""), ";:") {
		t.Fatal("empty args left a stray separator")
	}
}

func TestKittyPayloadChunks(t *testing.T) {
	// Large enough to need several chunks.
	data := testPNG(1024, 1024)
	got := kittyPayload(data, "f=100,r=6,c=40")
	if !strings.Contains(got, "q=2") {
		t.Fatal("responses not suppressed: a reply would corrupt the cursor report")
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
		if len(payload) > kittyChunk {
			t.Errorf("escape %d: payload %d B exceeds the %d B limit", i, len(payload), kittyChunk)
		}
		b64.WriteString(payload)
	}
	dec, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil || !bytes.Equal(dec, data) {
		t.Fatalf("chunks did not reassemble: %v", err)
	}
}

func TestKittySingleChunkCarriesControls(t *testing.T) {
	got := kittyPayload([]byte("tiny"), "f=100,r=1")
	if !strings.HasPrefix(got, "\x1b_Ga=T,q=2,f=100,r=1;") || !strings.HasSuffix(got, "\x1b\\") {
		t.Fatalf("single-chunk form wrong: %q", got)
	}
	if strings.Contains(got, "m=") {
		t.Error("single chunk should carry no m= continuation")
	}
}

func TestTestPNGDecodes(t *testing.T) {
	for _, d := range [][2]int{{320, 180}, {180, 320}} {
		cfg, err := png.DecodeConfig(bytes.NewReader(testPNG(d[0], d[1])))
		if err != nil {
			t.Fatalf("%dx%d: %v", d[0], d[1], err)
		}
		if cfg.Width != d[0] || cfg.Height != d[1] {
			t.Errorf("got %dx%d, want %dx%d", cfg.Width, cfg.Height, d[0], d[1])
		}
	}
}

// TestConsumedRowsAgainstMeasuredITerm2 carries the run this probe
// exists for: iTerm2 3.7.2, 180x80 cells, 16 of 16 cursor reports
// answered. Every case is the raw (cursor delta, end column) the
// terminal reported, paired with the height the payload declared. The
// correction is only defensible because it reproduces all of them —
// without it the same data reads as "7 of 7 declarations broken", which
// is how it was first (mis)read.
func TestConsumedRowsAgainstMeasuredITerm2(t *testing.T) {
	for _, tc := range []struct {
		name     string
		delta    int
		endCol   int
		declared int // 0: none declared, so `want` is the observed native size
		want     int
	}{
		{"no size declared", 9, 41, 0, 10},
		{"height=6 only", 5, 25, 6, 6},
		{"40x6 box, aspect kept (wide img)", 5, 41, 6, 6},
		{"40x6 box, aspect kept (tall img)", 5, 41, 6, 6},
		{"40x6 box, stretched", 5, 41, 6, 6},
		{"height=1", 0, 5, 1, 1},
		{"40x12 box, aspect kept", 11, 41, 12, 12},
		{"text + image + text on one line", 5, 38, 6, 6},
		// A terminal that draws nothing leaves the cursor at column 1;
		// the correction must not invent a row for it, or an unsupported
		// terminal would report a phantom occupancy.
		{"nothing drawn", 0, 1, 6, 0},
	} {
		got := consumedRows(tc.delta, tc.endCol)
		if got != tc.want {
			t.Errorf("%s: consumedRows(%d, %d) = %d, want %d", tc.name, tc.delta, tc.endCol, got, tc.want)
		}
		if tc.declared != 0 && tc.want != 0 && got != tc.declared {
			t.Errorf("%s: occupies %d rows but declared %d — the declaration was not honoured after all", tc.name, got, tc.declared)
		}
	}
}
