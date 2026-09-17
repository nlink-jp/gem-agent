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
	"encoding/base64"
	"errors"
	"fmt"
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
		return kitty(data, b), nil
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
