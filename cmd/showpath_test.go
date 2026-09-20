package cmd

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/termimg"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// showslash_test.go hands the slash handler a fake `show`, so it proves that
// /show keeps the spaces in its argument and stops there — one layer above the
// code that lost them. These tests run the real newShowPath: the path goes in,
// the file's bytes must come out at the screen.
func TestShowPathReadsAPathWithSpaces(t *testing.T) {
	project := realTempDir(t)
	name := "Screenshot 2026-09-21 at 10.00.00.png"
	var want bytes.Buffer
	if err := png.Encode(&want, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, name), want.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, typed := range []string{
		name,
		filepath.Join(project, name),
		`Screenshot\ 2026-09-21\ at\ 10.00.00.png`, // a file dragged into the terminal
		"'" + name + "'",
		"@" + name,
	} {
		var shown []byte
		var mime string
		show := newShowPath(project, func(data []byte, m string) error {
			shown, mime = data, m
			return nil
		})
		out, isErr := show(typed)
		if isErr || out != "" {
			t.Errorf("/show %s: %q (error=%v); the picture is the output", typed, out, isErr)
			continue
		}
		if !bytes.Equal(shown, want.Bytes()) || mime != "image/png" {
			t.Errorf("/show %s: drew %d bytes of %q, want the file", typed, len(shown), mime)
		}
	}
}

func TestShowPathSaysWhyNothingWasShown(t *testing.T) {
	project := realTempDir(t)
	drew := false
	show := newShowPath(project, func([]byte, string) error { drew = true; return nil })

	out, isErr := show("no such shot.png")
	if !isErr || !strings.HasPrefix(out, "not shown:") {
		t.Errorf("a missing file: %q (error=%v)", out, isErr)
	}
	if drew {
		t.Error("something was drawn for a missing file")
	}

	// A file that only claims to be an image never reaches the screen.
	if err := os.WriteFile(filepath.Join(project, "fake.png"), []byte("not really a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, isErr := show("fake.png"); !isErr || drew {
		t.Errorf("a text file named .png: %q (error=%v, drew=%v)", out, isErr, drew)
	}

	var real bytes.Buffer
	if err := png.Encode(&real, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "a b.png"), real.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	failing := newShowPath(project, func([]byte, string) error { return errors.New("terminal cannot draw") })
	if out, isErr := failing("a b.png"); !isErr || !strings.Contains(out, "terminal cannot draw") {
		t.Errorf("a screen that refuses: %q (error=%v)", out, isErr)
	}
}

// realTempDir is t.TempDir() with its symlinks resolved, as the project
// directory is at start-up: on macOS the temp root sits under /var, a link to
// /private/var, and a project path that resolves elsewhere is refused.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestShowImagePromisesWhatTheScreenDraws: internal/tools cannot import the
// drawing package, so it repeats the format list. This is where both are in
// reach.
func TestShowImagePromisesWhatTheScreenDraws(t *testing.T) {
	if tools.ShowImageFormats != termimg.Formats {
		t.Errorf("show_image promises %q; the screen draws %q", tools.ShowImageFormats, termimg.Formats)
	}
	if termimg.MaxBytes != 2<<20 {
		t.Errorf("show_image says \"up to 2 MiB\"; termimg.MaxBytes is %d", termimg.MaxBytes)
	}
}
