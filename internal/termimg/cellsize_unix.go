//go:build unix

package termimg

import "golang.org/x/sys/unix"

// CellAspect reads a cell's height over its width from the terminal on fd
// with TIOCGWINSZ — an ioctl, not a query written to the terminal, so it is
// safe while Bubble Tea owns stdin (ADR-0092 §4). false when the terminal
// reports no pixel size.
func CellAspect(fd int) (float64, bool) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0, false
	}
	return aspectOf(ws.Row, ws.Col, ws.Ypixel, ws.Xpixel)
}
