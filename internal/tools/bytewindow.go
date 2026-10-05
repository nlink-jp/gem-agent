package tools

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"unicode/utf8"

	"github.com/nlink-jp/gem-agent/internal/bounded"
)

// byteWindow is read_file's position window (ADR-0096 §2). A line window
// cannot reach the tail of one long line — a spilled tool result is
// typically a single line of JSON past readCap — so the tool that the
// spill notice names could not read what was spilled.
type byteWindow struct {
	set       bool
	offset    int64 // negative counts from the end
	hasOffset bool
	length    int
}

// byteWindowArgs reads offset/length. It does not reuse intArg: that
// treats 0 and negatives as absent, and here 0 is the first byte and a
// negative offset is the point of the feature.
func byteWindowArgs(args map[string]any) (byteWindow, error) {
	var w byteWindow
	off, hasOff, err := exactIntArg(args, "offset")
	if err != nil {
		return w, err
	}
	length, hasLen, err := exactIntArg(args, "length")
	if err != nil {
		return w, err
	}
	if !hasOff && !hasLen {
		return w, nil
	}
	if intArg(args, "start_line") > 0 || intArg(args, "end_line") > 0 {
		return w, errors.New("offset/length read by bytes and start_line/end_line by lines — pass one or the other")
	}
	w.set, w.offset, w.hasOffset = true, off, hasOff
	switch {
	case !hasLen:
		// The inline limit, not readCap: reading a spilled result back
		// 200 KB at a time would put it inline after all.
		w.length = OutputCap
	case length <= 0:
		return w, fmt.Errorf("length must be positive (got %d)", length)
	case length > readCap:
		w.length = readCap
	default:
		w.length = int(length)
	}
	return w, nil
}

// exactIntArg reports a whole-number argument, distinguishing absent
// from zero. A fractional value is refused rather than rounded.
func exactIntArg(args map[string]any, key string) (int64, bool, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false, nil
	}
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return 0, false, fmt.Errorf("%s must be a whole number", key)
	}
	return int64(f), true, nil
}

// readBytes returns the window and a note naming the bytes actually
// returned. Offsets past either end are clamped and the note says so.
// The window is moved to rune boundaries — a start inside a UTF-8
// sequence advances, an end inside one retreats — so a Japanese text is
// never shown with a broken character at either edge; the note states
// the moved bounds, which are what a following call should continue
// from.
func readBytes(f *os.File, w byteWindow) (string, error) {
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", errors.New("this is a directory — use list_files")
	}
	size := st.Size()
	start, clamped := w.offset, ""
	if start < 0 {
		start += size
	}
	if start < 0 {
		start, clamped = 0, fmt.Sprintf(" (offset %d is before the start; read from 0)", w.offset)
	} else if start > size {
		start, clamped = size, fmt.Sprintf(" (offset %d is past the end)", w.offset)
	}
	end := start + int64(w.length)
	if end > size {
		end = size
	}
	buf := make([]byte, end-start)
	n, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	buf = buf[:n]
	end = start + int64(n)
	// Leading continuation bytes belong to a rune that began before the
	// window; at most UTFMax-1 of them, so a binary file is not eaten.
	if start > 0 {
		skip := 0
		for skip < len(buf) && skip < utf8.UTFMax-1 && !utf8.RuneStart(buf[skip]) {
			skip++
		}
		buf, start = buf[skip:], start+int64(skip)
	}
	if end < size {
		kept := bounded.TrimIncompleteRune(buf)
		if len(kept) == 0 && len(buf) > 0 {
			// A window shorter than the one rune it starts in would
			// return nothing and a continuation from its end would
			// never advance: widen it to that rune instead.
			more := make([]byte, utf8.UTFMax)
			m, _ := f.ReadAt(more, start)
			if _, w := utf8.DecodeRune(more[:m]); w > len(buf) {
				kept = more[:w]
			}
		}
		end += int64(len(kept) - len(buf))
		buf = kept
	}
	return string(buf) + fmt.Sprintf("\n[bytes %d–%d of %d%s]", start, end, size, clamped), nil
}
