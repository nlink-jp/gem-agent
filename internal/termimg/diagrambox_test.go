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
