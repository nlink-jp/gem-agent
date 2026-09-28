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
