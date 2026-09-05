package cmd

import (
	"github.com/nlink-jp/gem-agent/internal/bounded"

	"fmt"
	"os"
	"os/exec"
	"strings"
)

// clipboardImage captures the clipboard image as PNG bytes via
// osascript — the @clipboard route (ADR-0012). gem-agent is macOS-only
// by design, so the platform dependency costs nothing that was
// promised. The AppleScript writes to a temp file because «class PNGf»
// data cannot cross stdout losslessly.
func clipboardImage() ([]byte, error) {
	tmp, err := os.CreateTemp("", "gem-agent-clipboard-*.png")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(path) }()

	script := fmt.Sprintf(`set f to open for access POSIX file %q with write permission
try
	set eof f to 0
	write (the clipboard as «class PNGf») to f
on error m number n
	close access f
	error m number n
end try
close access f`, path)
	if out, _, err := bounded.CombinedOutput(exec.Command("osascript", "-e", script), 64*1024); err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "PNGf") || strings.Contains(msg, "-1700") {
			return nil, fmt.Errorf("no image on the clipboard (take a screenshot with Cmd+Ctrl+Shift+4 first)")
		}
		return nil, fmt.Errorf("clipboard capture failed: %s", msg)
	}
	f, err := os.Open(path)
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

// clipboardImageCap bounds a clipboard capture: a screenshot, not a
// disk image (ADR-0073 §4 — the read was unbounded).
const clipboardImageCap = 64 << 20
