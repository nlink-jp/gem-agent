// Package termimg draws an image into the terminal's own cells inside a box
// the caller DECLARES (ADR-0089).
//
// The declaration is the whole point. The TUI counts the physical rows of
// every line it prints and the bottom pinning rests on that count, but an
// image payload measures zero cells wide to every surface the TUI has —
// ansi.StringWidth returns 0 for all of these escapes — so physicalRows
// credits an image line with the one row it floors to while the terminal
// advances N. Measured on two terminals with a plain control at the same
// fill: once the screen is full, a terminal that draws what the counter
// cannot see strands one frame per image in the scrollback.
//
// So the counter is never asked to measure. The caller chooses a Box, the
// payload declares it, and Box.Rows is the number emit adds — in place of
// the floor of 1, never in addition to it. Both dimensions are declared:
// an image wider than the terminal would be wrapped by the terminal itself,
// adding rows the declared height never claimed, so Fit clamps the columns
// before the payload is built.
//
// Only protocols that can declare a row count are here. Sixel cannot, and
// is not supported.
package termimg

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
)

// Protocol is an inline-image protocol the terminal understands.
type Protocol int

const (
	// None means no protocol was detected; nothing is ever drawn.
	None Protocol = iota
	// ITerm2 is the OSC 1337 File= inline image protocol.
	ITerm2
	// Kitty is the APC _G graphics protocol.
	Kitty
)

func (p Protocol) String() string {
	switch p {
	case ITerm2:
		return "iterm2"
	case Kitty:
		return "kitty"
	default:
		return "none"
	}
}

// Box is the cage the emitter declares: the cells the terminal reserves,
// whatever the picture does inside them. Rows is what the row counter is
// told.
type Box struct{ Rows, Cols int }

// ErrNoProtocol is returned when a payload is asked for without a protocol.
var ErrNoProtocol = errors.New("termimg: no inline-image protocol")

// ErrEmptyBox is returned for a box that reserves nothing. A zero-row image
// would be counted as zero rows and drawn as some other number, which is
// the failure this package exists to prevent.
var ErrEmptyBox = errors.New("termimg: box must reserve at least one row and one column")

// Fit clamps a box to what the terminal can hold. The width goes to
// termWidth-1, the same margin wrapForScrollback keeps so that no printed
// line reaches the terminal's last column; a box at the full width invites
// the terminal to wrap the picture and add rows nobody declared. A
// non-positive termWidth means the width is unknown and the box is left
// alone — guessing would be worse than the caller's own number.
func Fit(b Box, termWidth int) Box {
	if termWidth > 1 && b.Cols > termWidth-1 {
		b.Cols = termWidth - 1
	}
	return b
}

// kittyChunk is the payload limit of one kitty graphics escape.
const kittyChunk = 4096

// Payload builds the escape that draws data inside the declared box. The
// box is written into the payload, so the screen and the row accounting
// read the same number from the same place.
func Payload(p Protocol, data []byte, b Box) (string, error) {
	if b.Rows < 1 || b.Cols < 1 {
		return "", ErrEmptyBox
	}
	if len(data) == 0 {
		return "", errors.New("termimg: no image data")
	}
	switch p {
	case ITerm2:
		return iterm(data, b), nil
	case Kitty:
		pngData, err := kittyPNG(data, b)
		if err != nil {
			return "", err
		}
		return kitty(pngData, b), nil
	default:
		return "", ErrNoProtocol
	}
}

// iterm builds an OSC 1337 File= payload. width and height are in cells;
// preserveAspectRatio keeps the picture undistorted inside the box, which
// the terminal reserves whole either way.
func iterm(data []byte, b Box) string {
	return fmt.Sprintf(
		"\x1b]1337;File=inline=1;size=%d;width=%d;height=%d;preserveAspectRatio=1:%s\a",
		len(data), b.Cols, b.Rows, base64.StdEncoding.EncodeToString(data))
}

// pngSignature opens every PNG file.
var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// The pixels a cell is assumed to hold when a picture is scaled down for
// kitty. Generous on purpose — a cell on a Retina screen is about 16x36
// device pixels — so the terminal still scales the picture down into the
// box and never up.
const (
	kittyPxPerCol = 20
	kittyPxPerRow = 40
)

// kittyPNG returns the picture as PNG, the only thing kitty's f=100 means:
// the protocol has no JPEG format, a JPEG sent as f=100 is rejected, and q=2
// hides the rejection. The operator saw it on kitty (2026-09-22): a JPEG drew
// nothing. A PNG passes through unchanged. Anything else is decoded and
// re-encoded, scaled down first to what the box can show — a photo
// re-encoded as PNG at full size can be many times the MaxBytes its JPEG
// fitted in, and every pixel past the box is one the terminal discards.
func kittyPNG(data []byte, b Box) ([]byte, error) {
	if bytes.HasPrefix(data, pngSignature) {
		return data, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("termimg: kitty takes PNG only, and this does not decode: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, shrink(src, b.Cols*kittyPxPerCol, b.Rows*kittyPxPerRow)); err != nil {
		return nil, fmt.Errorf("termimg: re-encoding as PNG: %w", err)
	}
	if buf.Len() > MaxBytes {
		return nil, fmt.Errorf("termimg: %d B as PNG is past MaxBytes", buf.Len())
	}
	return buf.Bytes(), nil
}

// shrink scales src down to fit maxW x maxH, keeping its shape, averaging a
// 2x2 grid of samples for each output pixel. A picture that already fits is
// returned as it is. The standard library has no scaler, and a
// sample per output pixel reads a fraction of a large photo's pixels, which
// matters because this runs on the TUI's update path.
func shrink(src image.Image, maxW, maxH int) image.Image {
	r := src.Bounds()
	w, h := r.Dx(), r.Dy()
	if maxW < 1 || maxH < 1 || (w <= maxW && h <= maxH) {
		return src
	}
	scale := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	dw := max(1, int(float64(w)*scale))
	dh := max(1, int(float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	offsets := [2]float64{0.25, 0.75}
	for y := range dh {
		for x := range dw {
			var rs, gs, bs, as uint32
			for _, fy := range offsets {
				for _, fx := range offsets {
					sx := r.Min.X + int((float64(x)+fx)*float64(w)/float64(dw))
					sy := r.Min.Y + int((float64(y)+fy)*float64(h)/float64(dh))
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					rs, gs, bs, as = rs+cr, gs+cg, bs+cb, as+ca
				}
			}
			dst.Set(x, y, color.RGBA64{R: uint16(rs / 4), G: uint16(gs / 4), B: uint16(bs / 4), A: uint16(as / 4)})
		}
	}
	return dst
}

// kitty builds an APC _G payload, chunked at 4096 base64 bytes as the
// protocol requires. q=2 suppresses the terminal's OK/error replies: they
// would arrive on stdin, which Bubble Tea owns, and land in the input box
// as phantom keystrokes.
func kitty(data []byte, b Box) string {
	b64 := base64.StdEncoding.EncodeToString(data)
	ctl := fmt.Sprintf("a=T,q=2,f=100,r=%d,c=%d", b.Rows, b.Cols)
	if len(b64) <= kittyChunk {
		return "\x1b_G" + ctl + ";" + b64 + "\x1b\\"
	}
	var out strings.Builder
	first := true
	for len(b64) > 0 {
		n := min(kittyChunk, len(b64))
		chunk := b64[:n]
		b64 = b64[n:]
		more := "0"
		if len(b64) > 0 {
			more = "1"
		}
		if first {
			out.WriteString("\x1b_G" + ctl + ",m=" + more + ";" + chunk + "\x1b\\")
			first = false
			continue
		}
		out.WriteString("\x1b_Gm=" + more + ";" + chunk + "\x1b\\")
	}
	return out.String()
}
