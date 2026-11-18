package cmd

import (
	"github.com/nlink-jp/gem-agent/internal/bounded"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/statedir"

	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// clipboardImage captures the clipboard image as PNG bytes via
// osascript — the @clipboard route (ADR-0012). gem-agent is macOS-only
// by design, so the platform dependency costs nothing that was
// promised. The AppleScript writes to a temp file because «class PNGf»
// data cannot cross stdout losslessly.
func clipboardImage() ([]byte, error) {
	path, done, err := clipboardTemp()
	if err != nil {
		return nil, err
	}
	defer done()

	script := fmt.Sprintf(`set f to open for access POSIX file %q with write permission
try
	set eof f to 0
	write (the clipboard as «class PNGf») to f
on error m number n
	close access f
	error m number n
end try
close access f`, path)
	capture := exec.Command("osascript", "-e", script)
	// A child this runtime spawns, so the rule that governs the others
	// governs it too (ADR-0087 §2, amended).
	capture.Env = sandbox.ChildEnv(os.Environ())
	if out, _, err := bounded.CombinedOutput(capture, 64*1024); err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "PNGf") || strings.Contains(msg, "-1700") {
			return nil, fmt.Errorf("no image on the clipboard (take a screenshot with Cmd+Ctrl+Shift+4 first)")
		}
		return nil, fmt.Errorf("clipboard capture failed: %s", msg)
	}
	f, err := bounded.OpenRegularPath(path)
	if err != nil {
		return nil, err
	}
	data, more, err := bounded.ReadAll(f, clipboardImageCap)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	if more {
		return nil, fmt.Errorf("clipboard image is larger than %s — shrink it, or attach the file with @<path>", humanBytes(clipboardImageCap))
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no image on the clipboard (take a screenshot with Cmd+Ctrl+Shift+4 first)")
	}
	return data, nil
}

// clipboardTemp makes the file the capture is written into, and
// returns its path and what removes it. It is made in the runtime's own
// state directory, which no lane writes. It used to be made in the
// temporary directory, which every write lane writes: osascript runs
// outside the sandbox and writes into the path it is given, so a
// process in a lane that put a link under that name had the clipboard
// written through it into a file no lane may write (the design review
// of ADR-0121 reproduced it under the real profile).
func clipboardTemp() (path string, done func(), err error) {
	root, err := statedir.Root()
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(root, "clipboard")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	tmp, err := os.CreateTemp(dir, "capture-*.png")
	if err != nil {
		return "", nil, err
	}
	path = tmp.Name()
	_ = tmp.Close()
	return path, func() { _ = os.Remove(path) }, nil
}

// clipboardImageCap bounds a clipboard capture: a screenshot, not a
// disk image (ADR-0073 §4 — the read was unbounded).
const clipboardImageCap = 64 << 20
