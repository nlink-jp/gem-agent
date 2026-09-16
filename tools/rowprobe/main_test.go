package main

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/nlink-jp/gem-agent/tools/imgpayload"
)

// TestPayloadsMeasureZeroCells guards the fact the whole approach
// rests on: the width counter cannot see an image. If a future x/ansi
// ever gave these payloads a nonzero width, physicalRows would start
// counting them as text cells and the declaration scheme would need
// rethinking — so the day that changes, this test says so.
func TestPayloadsMeasureZeroCells(t *testing.T) {
	data := imgpayload.TestPNG(320, 180)
	for _, tc := range []struct {
		name string
		s    string
	}{
		{"iterm", imgpayload.Iterm(data, "width=40;height=6")},
		{"kitty", imgpayload.Kitty(data, "f=100,r=6,c=40")},
		// sixel was the third of "all three" that had no test until an
		// independent pass noticed the gap (2026-09-16).
		{"sixel", imgpayload.Sixel(12)},
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
