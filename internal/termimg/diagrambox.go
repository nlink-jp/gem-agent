package termimg

import "math"

// DiagramPxPerEm is the pixels per em a diagram picture is rendered at:
// mermaid-render's Scale 2 on its 14 px base (ADR-0092 §4).
const DiagramPxPerEm = 28

// cellEm is a cell's height as a multiple of the terminal font's em. It is
// a constant, tuned on the operator's terminals (mermaid-render RFP, stage
// 0); the cell's aspect is read, not assumed.
const cellEm = 1.2

// DiagramBox chooses the box for a diagram of pxW x pxH so that one em of
// its text is one line of the terminal's (ADR-0092 §4): rows are the
// height in terminal lines, columns the width in lines times the cell's
// aspect (height over width). A picture wider than the terminal less one
// column shrinks, keeping its shape — smaller text is ugly, not wrong. A
// tall one is never shrunk: its top scrolls into the scrollback, as a long
// reply's does (the operator's decision). A missing or absurd aspect falls
// back to the one BoxFor assumes.
func DiagramBox(pxW, pxH, termCols int, aspect float64) Box {
	if pxW <= 0 || pxH <= 0 {
		return Box{Rows: 1, Cols: 1}
	}
	if math.IsNaN(aspect) || aspect < 1 || aspect > 4 {
		aspect = cellAspect
	}
	lines := func(px int) float64 { return float64(px) / DiagramPxPerEm / cellEm }
	rows := lines(pxH)
	cols := lines(pxW) * aspect
	if limit := float64(termCols - 1); termCols > 1 && cols > limit {
		rows *= limit / cols
		cols = limit
	}
	return Box{Rows: max(1, int(math.Round(rows))), Cols: max(1, int(math.Round(cols)))}
}

// aspectOf is a cell's height over its width from a window size in cells
// and pixels; false when the terminal reports no pixels (many do not).
func aspectOf(rows, cols, ypx, xpx uint16) (float64, bool) {
	if rows == 0 || cols == 0 || ypx == 0 || xpx == 0 {
		return 0, false
	}
	return (float64(ypx) / float64(rows)) / (float64(xpx) / float64(cols)), true
}

// Band is one horizontal strip of a picture: pixel rows [Y0, Y1), drawn
// in Rows terminal rows at the picture's full width.
type Band struct{ Y0, Y1, Rows int }

// Bands cuts a picture pxH pixels tall, declared in box, into strips of at
// most maxRows rows, top to bottom. A picture taller than the screen is
// not something every terminal scrolls: kitty clipped one and the frame
// was drawn over it (measured, ADR-0092 §4), while each band — a picture
// no taller than the screen — scrolls like any other. The strips share
// the box's columns and one scale, so they abut into the whole picture.
func Bands(pxH int, box Box, maxRows int) []Band {
	if pxH <= 0 || box.Rows < 1 {
		return nil
	}
	maxRows = max(1, maxRows)
	y := func(row int) int { return int(math.Round(float64(row) * float64(pxH) / float64(box.Rows))) }
	var out []Band
	for r := 0; r < box.Rows; r += maxRows {
		n := min(maxRows, box.Rows-r)
		if y0, y1 := y(r), y(r+n); y1 > y0 {
			out = append(out, Band{Y0: y0, Y1: y1, Rows: n})
		}
	}
	return out
}
