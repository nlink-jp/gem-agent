package cmd

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/mcp"
)

type drawn struct {
	data []byte
	mime string
}

// TestIntakeDrawsOnlyWhatItSavedAndDescribed is ADR-0090 §2's one
// condition. A block the response budget refuses is neither saved nor
// listed, and drawing it would put a picture on the operator's screen that
// appears nowhere in the session's record.
func TestIntakeDrawsOnlyWhatItSavedAndDescribed(t *testing.T) {
	work := t.TempDir()
	var got []drawn
	in := newMCPIntake(fixedDir(work), func(data []byte, mime string) {
		got = append(got, drawn{data, mime})
	})

	small := []byte("\x89PNG\r\n\x1a\n pretend this is a picture")
	out := in.render("srv", "shot", []mcp.Content{{Type: "image", Data: small, MIME: "image/png"}})

	if len(got) != 1 {
		t.Fatalf("draw called %d times, want 1", len(got))
	}
	if string(got[0].data) != string(small) || got[0].mime != "image/png" {
		t.Errorf("draw got %d bytes of %q, want the block's own bytes and MIME", len(got[0].data), got[0].mime)
	}
	if !strings.Contains(out, "image saved at") {
		t.Errorf("the model's note is unchanged by drawing; got %q", out)
	}
}

// TestIntakeDoesNotDrawWhatItRefused: a block whose note cannot fit the
// response budget is counted into the leftovers line, not saved, not
// described — and not drawn.
func TestIntakeDoesNotDrawWhatItRefused(t *testing.T) {
	work := t.TempDir()
	var calls int
	in := newMCPIntake(fixedDir(work), func([]byte, string) { calls++ })
	in.cap = 10 // no room for any note

	out := in.render("srv", "shot", []mcp.Content{{Type: "image", Data: []byte("bytes"), MIME: "image/png"}})
	if calls != 0 {
		t.Errorf("drew %d block(s) the budget refused", calls)
	}
	if !strings.Contains(out, "past the response budget") {
		t.Errorf("the refusal is not in the record either: %q", out)
	}
}

// TestIntakeDrawsOnlyImages: a non-image binary block is saved and
// described like any other, and never offered to the screen. The drawing
// side re-checks the bytes (termimg.Measure); this is the cheaper gate in
// front of it.
func TestIntakeDrawsOnlyImages(t *testing.T) {
	work := t.TempDir()
	var calls int
	in := newMCPIntake(fixedDir(work), func([]byte, string) { calls++ })

	out := in.render("srv", "dump", []mcp.Content{{Type: "resource", Data: []byte("not a picture"), MIME: "application/octet-stream"}})
	if calls != 0 {
		t.Errorf("offered a non-image block to the screen %d time(s)", calls)
	}
	if out == "" {
		t.Error("the block should still be saved and described")
	}
}

// TestIntakeWithoutASinkIsUnchanged: every entrance that is not an
// interactive TUI passes nil, and the intake behaves exactly as it did
// before ADR-0090.
func TestIntakeWithoutASinkIsUnchanged(t *testing.T) {
	work := t.TempDir()
	in := newMCPIntake(fixedDir(work), nil)
	out := in.render("srv", "shot", []mcp.Content{{Type: "image", Data: []byte("bytes"), MIME: "image/png"}})
	if !strings.Contains(out, "image saved at") {
		t.Errorf("note changed without a sink: %q", out)
	}
}
