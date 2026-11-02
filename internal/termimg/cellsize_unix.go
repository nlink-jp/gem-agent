//go:build unix

package termimg

import "golang.org/x/sys/unix"

// CellSize reads a cell's size in pixels from the terminal on fd with
// TIOCGWINSZ — an ioctl, not a query written to the terminal, so it is
// safe while Bubble Tea owns stdin (ADR-0092 §4, ADR-0103 §3). false when
// the terminal reports no pixel size.
func CellSize(fd int) (Cell, bool) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return Cell{}, false
	}
	return cellOf(ws.Row, ws.Col, ws.Ypixel, ws.Xpixel)
}
