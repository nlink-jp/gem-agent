package cmd

import (
	"path/filepath"
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

// TestIntakeDoesNotDrawWhatItCouldNotSave is the other half of ADR-0090 §2:
// saved AND described. A write that fails still produces a note — and that
// note can be SHORTER than the success note, which carries a full path — so
// a caller inferring the outcome from the budget verdict concluded "saved"
// and drew a picture with no file behind it. The operator would be looking
// at something the session's record does not contain, and `view_image` on
// the path the model was given would find nothing.
func TestIntakeDoesNotDrawWhatItCouldNotSave(t *testing.T) {
	var calls int
	// A work directory that cannot be written to: the write fails, the
	// note says so, and nothing is drawn.
	in := newMCPIntake(fixedDir(filepath.Join(t.TempDir(), "no-such-dir")), func([]byte, string) { calls++ })

	out := in.render("srv", "shot", []mcp.Content{{Type: "image", Data: []byte("bytes"), MIME: "image/png"}})
	if calls != 0 {
		t.Errorf("drew %d block(s) whose bytes never reached disk", calls)
	}
	if !strings.Contains(out, "could not be saved") {
		t.Errorf("the failure is not in the record either: %q", out)
	}
}

// TestDrawnAndDescribedUseOnePredicate: the note calls a block an image and
// the screen draws one on the same test. Two predicates could disagree —
// the draw gate keyed on the MIME prefix while the note keyed on Type — and
// then a picture on screen is recorded as "[resource content saved at …]",
// or a block the record calls an image is silently never drawn.
func TestDrawnAndDescribedUseOnePredicate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		block     mcp.Content
		wantDrawn bool
		wantNote  string
	}{
		{"image type, image mime", mcp.Content{Type: "image", Data: []byte("bytes"), MIME: "image/png"}, true, "image saved at"},
		{"image type, no mime at all", mcp.Content{Type: "image", Data: []byte("bytes")}, true, "image saved at"},
		{"resource type wearing an image mime", mcp.Content{Type: "resource", Data: []byte("bytes"), MIME: "image/png"}, false, "resource content saved at"},
	} {
		var calls int
		in := newMCPIntake(fixedDir(t.TempDir()), func([]byte, string) { calls++ })
		out := in.render("srv", "shot", []mcp.Content{tc.block})
		if drawn := calls > 0; drawn != tc.wantDrawn {
			t.Errorf("%s: drawn=%v, want %v", tc.name, drawn, tc.wantDrawn)
		}
		if !strings.Contains(out, tc.wantNote) {
			t.Errorf("%s: note %q does not contain %q", tc.name, out, tc.wantNote)
		}
	}
}
