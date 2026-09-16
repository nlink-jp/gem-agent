// Package imgpayload builds the inline-image escapes the probes draw
// with, and the test pictures they draw. It exists so that rowprobe
// (what does one image cost?) and pinprobe (does the accounting hold
// when the screen scrolls?) cannot drift apart: two probes that build
// their own payloads answer questions about two different things while
// appearing to answer one.
//
// Nothing here is production code. The runtime does not emit an image
// escape today; ADR-0089 is Proposed.
package imgpayload

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
)

// Iterm builds an iTerm2 inline image (OSC 1337 File=). args are the
// semicolon-separated size arguments, empty for none.
func Iterm(data []byte, args string) string {
	head := fmt.Sprintf("inline=1;size=%d", len(data))
	if args != "" {
		head += ";" + args
	}
	return "\x1b]1337;File=" + head + ":" + base64.StdEncoding.EncodeToString(data) + "\a"
}

// KittyChunk is the payload limit of one kitty graphics escape.
const KittyChunk = 4096

// Kitty builds a kitty graphics command (APC _G), chunked at 4096
// base64 bytes as the protocol requires. q=2 suppresses the terminal's
// OK/error replies, which would otherwise arrive in the middle of a
// cursor report and corrupt a measurement.
func Kitty(data []byte, args string) string {
	b64 := base64.StdEncoding.EncodeToString(data)
	ctl := "a=T,q=2"
	if args != "" {
		ctl += "," + args
	}
	if len(b64) <= KittyChunk {
		return "\x1b_G" + ctl + ";" + b64 + "\x1b\\"
	}
	var out strings.Builder
	first := true
	for len(b64) > 0 {
		n := min(KittyChunk, len(b64))
		chunk := b64[:n]
		b64 = b64[n:]
		more := "0"
		if len(b64) > 0 {
			more = "1"
		}
		switch {
		case first:
			out.WriteString("\x1b_G" + ctl + ",m=" + more + ";" + chunk + "\x1b\\")
			first = false
		default:
			out.WriteString("\x1b_Gm=" + more + ";" + chunk + "\x1b\\")
		}
	}
	return out.String()
}

// Sixel builds a sixel image (DCS q) of a solid block, bands rows of
// six pixels each. It exists because both ADRs claim a measurement for
// "all three" payload families and only two of them had one — the gap
// an independent verification pass found (2026-09-16).
func Sixel(bands int) string {
	var out strings.Builder
	out.WriteString("\x1bPq")
	out.WriteString(`"1;1;192;`)
	fmt.Fprintf(&out, "%d", bands*6)
	out.WriteString("#0;2;16;43;85#1;2;100;82;25")
	for i := 0; i < bands; i++ {
		out.WriteString("#0")
		out.WriteString(strings.Repeat("~", 24))
		out.WriteString("$#1")
		out.WriteString(strings.Repeat("?", 24))
		out.WriteString("-")
	}
	out.WriteString("\x1b\\")
	return out.String()
}

// TestPNG draws a bordered block with a diagonal, so a scaled or
// stretched image is obvious on screen rather than a guess.
func TestPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill := color.RGBA{R: 0x2a, G: 0x6f, B: 0xdb, A: 0xff}
	edge := color.RGBA{R: 0xff, G: 0xd2, B: 0x3f, A: 0xff}
	const border = 6
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := fill
			switch {
			case x < border || y < border || x >= w-border || y >= h-border:
				c = edge
			case abs(x*h-y*w) < border*max(w, h):
				c = edge
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err) // an in-memory RGBA encode cannot fail
	}
	return buf.Bytes()
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
