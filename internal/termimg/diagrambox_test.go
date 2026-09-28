package termimg

import (
	"math"
	"testing"
)

func TestDiagramBox(t *testing.T) {
	em := func(n float64) int { return int(n * DiagramPxPerEm * cellEm) }
	for _, c := range []struct {
		name       string
		w, h, cols int
		aspect     float64
		want       Box
	}{
		// One em of diagram text is one terminal line: 10 lines tall, 20
		// wide at iTerm2's 2.25 is 45 columns; at kitty's 1.86, 37.
		{"iTerm2", em(20), em(10), 200, 2.25, Box{Rows: 10, Cols: 45}},
		{"kitty", em(20), em(10), 200, 1.86, Box{Rows: 10, Cols: 37}},
		// Wider than the terminal less one: shrunk to 79, keeping shape.
		{"wide", em(80), em(10), 80, 2.25, Box{Rows: 4, Cols: 79}},
		// Taller than any screen: never shrunk.
		{"tall", em(10), em(300), 200, 2.25, Box{Rows: 300, Cols: 23}},
		// No pixels reported, or nonsense: the assumed 2.25.
		{"no aspect", em(20), em(10), 200, 0, Box{Rows: 10, Cols: 45}},
		{"NaN aspect", em(20), em(10), 200, math.NaN(), Box{Rows: 10, Cols: 45}},
		{"empty", 0, 10, 80, 2.25, Box{Rows: 1, Cols: 1}},
		{"a sliver", 1, 1, 80, 2.25, Box{Rows: 1, Cols: 1}},
	} {
		if got := DiagramBox(c.w, c.h, c.cols, c.aspect); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestAspectOf(t *testing.T) {
	// 50 rows x 200 cols over 1800 x 1600 px: cells 36 tall, 8 wide.
	if a, ok := aspectOf(50, 200, 1800, 1600); !ok || a != 4.5 {
		t.Errorf("aspectOf = %v, %v", a, ok)
	}
	if _, ok := aspectOf(50, 200, 0, 0); ok {
		t.Error("a terminal that reports no pixels gave an aspect")
	}
}

// Bands cover the picture exactly, top to bottom, none taller than the
// limit, and their rows add up to the box's.
func TestBands(t *testing.T) {
	for _, c := range []struct{ pxH, rows, max int }{
		{3360, 100, 20}, {3360, 100, 30}, {1000, 7, 7}, {1000, 7, 50}, {999, 13, 4}, {50, 1, 1},
	} {
		bs := Bands(c.pxH, Box{Rows: c.rows, Cols: 40}, c.max)
		y, rows := 0, 0
		for _, b := range bs {
			if b.Y0 != y || b.Y1 <= b.Y0 || b.Rows < 1 || b.Rows > c.max {
				t.Fatalf("%+v: band %+v after y %d", c, b, y)
			}
			y, rows = b.Y1, rows+b.Rows
		}
		if y != c.pxH || rows != c.rows {
			t.Errorf("%+v: bands end at y %d with %d rows, want %d and %d", c, y, rows, c.pxH, c.rows)
		}
	}
	if len(Bands(0, Box{Rows: 1, Cols: 1}, 5)) != 0 {
		t.Error("an empty picture has bands")
	}
}
