package cmd

import (
	"strings"
	"testing"
)

// ADR-0055 §2: bounded read, disclosed clip, text only.
func TestReadPipedStdin(t *testing.T) {
	t.Run("plain text passes through", func(t *testing.T) {
		content, warning := readPipedStdin(strings.NewReader(`{"ip":"192.0.2.7"}`))
		if warning != "" || content != `{"ip":"192.0.2.7"}` {
			t.Errorf("content=%q warning=%q", content, warning)
		}
	})
	t.Run("empty attaches nothing", func(t *testing.T) {
		content, warning := readPipedStdin(strings.NewReader(""))
		if content != "" || warning != "" {
			t.Errorf("content=%q warning=%q", content, warning)
		}
	})
	t.Run("over-cap is clipped with the clip disclosed", func(t *testing.T) {
		// Multi-byte runes across the cap boundary: the repair may drop
		// at most a partial rune, never yield invalid text.
		big := strings.Repeat("あ", stdinCap/3+100)
		content, warning := readPipedStdin(strings.NewReader(big))
		if warning != "" {
			t.Fatalf("warning=%q", warning)
		}
		if len(content) > stdinCap+100 {
			t.Errorf("content not clipped: %d bytes", len(content))
		}
		if !strings.Contains(content, "[stdin clipped at 256 KiB") {
			t.Error("clip not disclosed inside the attachment")
		}
	})
	t.Run("NUL bytes are refused as binary", func(t *testing.T) {
		content, warning := readPipedStdin(strings.NewReader("PK\x00\x00zipdata"))
		if content != "" || !strings.Contains(warning, "not UTF-8 text") {
			t.Errorf("content=%q warning=%q", content, warning)
		}
	})
	t.Run("invalid UTF-8 is refused as binary", func(t *testing.T) {
		content, warning := readPipedStdin(strings.NewReader("\xff\xfe\x01binary"))
		if content != "" || warning == "" {
			t.Errorf("content=%q warning=%q", content, warning)
		}
	})
}
