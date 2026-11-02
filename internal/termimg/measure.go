package termimg

import (
	"bytes"
	"image"
	"math"

	// The decoders this runtime will draw. Registering them here rather
	// than wherever an image arrives keeps one answer to "is this an
	// image?" — image.DecodeConfig is only as capable as what is
	// registered, so the import list IS the supported-format list.
	_ "image/jpeg"
	_ "image/png"
)

// Formats names what the import list above can decode, in the words a tool
// description uses. It is here, beside the imports, because it is the same
// fact: show_image once promised "PNG, JPEG, WebP, GIF, HEIC" — view_image's
// list, which is about what the MODEL can read — while this package decodes
// two of them, so three kinds of file were accepted, read, and then always
// refused at the draw. TestFormatsAreWhatDecodes holds the two together.
const Formats = "PNG or JPEG"

// MaxBytes is the ceiling on a decoded image (ADR-0090 §4). Measured on
// the counter the emit path uses: physicalRows costs about 3.6 ms per MiB
// and the payload string is held in three places at once — the emit path,
// Bubble Tea's queued-message buffer and the terminal's scrollback — so
// the JSON-RPC frame cap's ~7.5 MiB would cost four times this.
//
// It is a ceiling, not a target: the picture is scaled into a box of at
// most a screenful of cells, so a larger original is pixels the terminal
// discards.
const MaxBytes = 2 << 20 // 2 MiB

// cellAspect is how much wider than tall a terminal cell is NOT: a cell is
// roughly 8x18 pixels, so one row is worth about 2.25 columns of picture.
// It is the shape assumed when the terminal reports no pixels; a box is
// shaped by the cell that was read wherever there is one (ADR-0103 §2).
const cellAspect = 2.25

// Cell is one terminal cell's size in the pixels the terminal reports
// with TIOCGWINSZ. On the operator's Retina screens iTerm2 reported 8x18
// and kitty 7x13 — points, not device pixels (mermaid-render RFP, stage 0).
type Cell struct{ W, H float64 }

// Aspect is the cell's height over its width.
func (c Cell) Aspect() float64 { return c.H / c.W }

// usable is false for a cell no terminal has: nothing reported, or a shape
// outside the 1-4 DiagramBox also refuses.
func (c Cell) usable() bool {
	if !(c.W > 0 && c.H > 0) {
		return false
	}
	a := c.Aspect()
	return a >= 1 && a <= 4
}

// fallbackCell is the cell assumed when the terminal reports none:
// iTerm2's measured 8x18, the cellAspect a box was always shaped by.
var fallbackCell = Cell{W: 8, H: 18}

// Measure reports a picture's pixel size and whether this runtime will
// draw it at all. A block whose MIME claims an image but whose bytes do
// not decode is not one: DecodeConfig is the validator, and it reads only
// the header rather than the picture.
//
// Bytes over MaxBytes are refused here, before any payload is built.
func Measure(data []byte) (w, h int, ok bool) {
	if len(data) == 0 || len(data) > MaxBytes {
		return 0, 0, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// BoxFor chooses the box to declare for a picture of pxW x pxH (ADR-0103):
// as large as the terminal allows — its width less one column, maxRows
// tall — and never larger than the picture itself, one picture pixel to
// one pixel the terminal reports for cell. The picture keeps its shape;
// an unusable cell is taken to be fallbackCell. The columns are clamped
// below the terminal's width by Fit — a box at the full width invites the
// terminal to wrap the picture and add rows the declaration never claimed.
//
// The box is always at least 1x1: a zero box would be counted as some
// number and drawn as another, which is the failure the declaration
// exists to prevent.
func BoxFor(pxW, pxH, termCols, maxRows int, cell Cell) Box {
	if pxW <= 0 || pxH <= 0 {
		return Box{Rows: 1, Cols: 1}
	}
	maxRows = max(1, maxRows)
	if !cell.usable() {
		cell = fallbackCell
	}
	cols := float64(pxW) / cell.W
	rows := float64(pxH) / cell.H
	limit := float64(unknownWidthCols)
	if termCols > 1 {
		limit = float64(termCols - 1)
	}
	scale := math.Min(1, math.Min(limit/cols, float64(maxRows)/rows))
	b := Box{
		Rows: min(maxRows, max(1, int(math.Round(rows*scale)))),
		Cols: max(1, int(math.Round(cols*scale))),
	}
	return Fit(b, termCols)
}

// unknownWidthCols is the widest a picture is drawn when the terminal's
// width is unknown: a guess would be worse than a modest number.
const unknownWidthCols = 40
