package cmd

import (
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
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "PNGf") || strings.Contains(msg, "-1700") {
			return nil, fmt.Errorf("no image on the clipboard (take a screenshot with Cmd+Ctrl+Shift+4 first)")
		}
		return nil, fmt.Errorf("clipboard capture failed: %s", msg)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no image on the clipboard")
	}
	return data, nil
}
