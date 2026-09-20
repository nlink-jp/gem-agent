package termimg

import (
	"bytes"
	"image"

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
// roughly 8x18 device pixels, so one row is worth about 2.25 columns of
// picture. This chooses how a box is SHAPED and never how it is counted —
// a wrong constant letterboxes the picture inside a box the terminal
// reserves whole (ADR-0089), it does not move the row count.
const cellAspect = 2.25

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

// BoxFor chooses the box to declare for a picture of pxW x pxH, given the
// terminal's width and a ceiling on rows. The columns are clamped below
// the terminal's width by Fit — a box at the full width invites the
// terminal to wrap the picture and add rows the declaration never claimed.
//
// The box is always at least 1x1: a zero box would be counted as some
// number and drawn as another, which is the failure the declaration
// exists to prevent.
func BoxFor(pxW, pxH, termCols, maxRows int) Box {
	if pxW <= 0 || pxH <= 0 {
		return Box{Rows: 1, Cols: 1}
	}
	if maxRows < 1 {
		maxRows = 1
	}
	cols := termCols - 1
	if cols > defaultCols {
		cols = defaultCols
	}
	if cols < 1 {
		cols = defaultCols
	}
	rows := int(float64(cols)*float64(pxH)/(float64(pxW)*cellAspect) + 0.5)
	if rows < 1 {
		rows = 1
	}
	if rows > maxRows {
		// Too tall for the ceiling: give back the columns the shorter box
		// no longer needs, so the picture keeps its shape instead of
		// being letterboxed inside a box it cannot fill.
		rows = maxRows
		cols = int(float64(rows)*float64(pxW)*cellAspect/float64(pxH) + 0.5)
		if cols < 1 {
			cols = 1
		}
	}
	return Fit(Box{Rows: rows, Cols: cols}, termCols)
}

// defaultCols is the width a picture is drawn at when the terminal has
// room: wide enough to read, narrow enough to leave the reply's text
// beside it in the operator's memory of the screen.
const defaultCols = 40
