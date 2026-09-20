//go:build !unix

package termimg

import (
	"errors"
	"time"
)

// No terminal is asked on a platform this runtime does not target: the probe
// reads "no capability", which is always a safe answer.
var (
	errNoTTYRead = errors.New("termimg: terminal queries are not supported on this platform")
	errNotReady  = errors.New("termimg: nothing to read yet")
)

func waitReadable(int, time.Duration) (bool, error) { return false, errNoTTYRead }
func readReady(int, []byte) (int, error)            { return 0, errNoTTYRead }
