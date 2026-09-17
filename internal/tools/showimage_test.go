package tools

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePNG(t *testing.T, dir, name string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestShowImageHandsTheBytesToTheScreen is ADR-0091 §2: the tool's effect
// is that the OPERATOR sees the picture, so what it must do is deliver
// bytes — not attach them to the conversation, which is view_image's job.
func TestShowImageHandsTheBytesToTheScreen(t *testing.T) {
	r := newRegistry(t)
	writePNG(t, r.projectDir, "chart.png")
	var gotData []byte
	var gotMIME string
	r.SetShowImage(func(data []byte, mime string) error {
		gotData, gotMIME = data, mime
		return nil
	})

	out, err := run(t, r, "show_image", map[string]any{"path": "chart.png"})
	if err != nil {
		t.Fatalf("show_image: %v", err)
	}
	if len(gotData) == 0 || gotMIME != "image/png" {
		t.Errorf("screen received %d bytes of %q", len(gotData), gotMIME)
	}
	if !strings.Contains(out, "shown to the operator") {
		t.Errorf("result does not say it was shown: %q", out)
	}
}

// TestShowImageSaysWhyItCouldNotShow: the operator's screen stays silent on
// refusal (ADR-0089), so the tool result is the only place the reason
// exists — and a model told nothing would report a picture that is not
// there.
func TestShowImageSaysWhyItCouldNotShow(t *testing.T) {
	r := newRegistry(t)
	writePNG(t, r.projectDir, "chart.png")

	// No screen at all: every entrance that is not an interactive TUI.
	out, err := run(t, r, "show_image", map[string]any{"path": "chart.png"})
	if err != nil {
		t.Fatalf("show_image without a screen returned an error: %v", err)
	}
	if !strings.Contains(out, "no screen") {
		t.Errorf("result does not name the reason: %q", out)
	}

	// A screen that refuses — no protocol, too large, not drawable.
	r.SetShowImage(func([]byte, string) error { return errors.New("this terminal cannot draw inline images") })
	out, err = run(t, r, "show_image", map[string]any{"path": "chart.png"})
	if err != nil {
		t.Fatalf("a refusing screen must not be an error: %v", err)
	}
	if !strings.Contains(out, "not shown") || !strings.Contains(out, "cannot draw") {
		t.Errorf("result does not carry the screen's reason: %q", out)
	}
}

// TestShowImageKeepsTheFileToolConfinement: the tool reads like every other
// file tool. This is the whole argument of ADR-0091 §2 — a model-named path
// reaches the screen only because a TOOL opens it, where the confinement
// and the path judging already are.
func TestShowImageKeepsTheFileToolConfinement(t *testing.T) {
	r := newRegistry(t)
	r.SetShowImage(func([]byte, string) error { return nil })
	outside := filepath.Join(t.TempDir(), "outside.png")
	writePNG(t, filepath.Dir(outside), "outside.png")

	for _, tc := range []struct{ name, path string }{
		{"absolute path outside the project", outside},
		{"traversal out of the project", "../outside.png"},
	} {
		if _, err := run(t, r, "show_image", map[string]any{"path": tc.path}); err == nil {
			t.Errorf("%s: show_image escaped the project", tc.name)
		}
	}

	// And a file that is not an image is refused before anything is sent.
	if err := os.WriteFile(filepath.Join(r.projectDir, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "show_image", map[string]any{"path": "notes.txt"}); err == nil {
		t.Error("show_image drew a text file")
	}
}

// TestShowImageAndViewImageDifferOnlyInAudience pins the pair ADR-0091 §2
// describes. Both are read-only, both are judged on their path, both run in
// the cage; what differs is who sees the picture.
func TestShowImageAndViewImageDifferOnlyInAudience(t *testing.T) {
	r := newRegistry(t)
	for _, name := range []string{ShowImageName, ViewImageName} {
		tool, ok := r.Get(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if tool.Mutating {
			t.Errorf("%s is marked mutating", name)
		}
		if !ChildTool(name) {
			t.Errorf("%s does not run in the sandboxed child — its read would not be adjudicated", name)
		}
	}
	show, _ := r.Get(ShowImageName)
	view, _ := r.Get(ViewImageName)
	if !strings.Contains(show.Description, "OPERATOR") {
		t.Error("show_image's description must lead with its audience")
	}
	if strings.Contains(view.Description, "OPERATOR") {
		t.Error("view_image's description now claims the operator's screen")
	}
}
