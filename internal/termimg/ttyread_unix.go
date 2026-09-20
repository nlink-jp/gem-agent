//go:build unix

package termimg

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// waitReadable reports whether fd has input to read within d (d <= 0 polls).
// select(2), not poll(2): macOS's poll does not support /dev/tty.
func waitReadable(fd int, d time.Duration) (bool, error) {
	if d < 0 {
		d = 0
	}
	deadline := time.Now().Add(d)
	for {
		var set unix.FdSet
		set.Zero()
		set.Set(fd)
		tv := unix.NsecToTimeval(d.Nanoseconds())
		n, err := unix.Select(fd+1, &set, nil, nil, &tv)
		if err == unix.EINTR {
			if d = time.Until(deadline); d < 0 {
				d = 0
			}
			continue
		}
		if err != nil {
			return false, err
		}
		return n > 0, nil
	}
}

// errNotReady is a read that found nothing after all: not end of input, and
// not a failure. The caller goes back to waiting.
var errNotReady = errors.New("termimg: nothing to read yet")

// readReady reads what select said is there. It is a plain read(2) on the
// descriptor, bypassing os.File: a read that cannot outlive its caller is the
// point of this file.
func readReady(fd int, buf []byte) (int, error) {
	for {
		n, err := unix.Read(fd, buf)
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN {
			return 0, errNotReady
		}
		if n < 0 {
			n = 0
		}
		return n, err
	}
}
