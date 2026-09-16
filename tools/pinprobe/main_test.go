package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAnalyzeAgainstCapturedScreens replays the screens this probe was
// actually run against. They are real tmux 3.7c captures, 120x30, taken
// 2026-09-17 and kept in testdata for one reason: the first version of
// this tool printed a fixed block of prose and measured nothing, so the
// figures that reached ADR-0089 lived only in throwaway shell drivers and
// in a human's reading of screenshots. An independent pass found that the
// document cited an instrument which produced none of its numbers. This
// test is where those numbers now live.
func TestAnalyzeAgainstCapturedScreens(t *testing.T) {
	for _, tc := range []struct {
		file     string
		caseName string
		regime   string // the regime the run arranged
		gap      int
		stranded int
	}{
		// Screen not yet full: the pad absorbs, and the sixel that this
		// tmux renders costs nothing. The plain control is identical,
		// which is what makes the pair a control.
		{"tmux-empty-plain.txt", "PLAIN", "empty", 18, 0},
		{"tmux-empty-sixel12.txt", "SIXEL-12", "empty", 18, 0},
		// Screen full: the pad is gone, and now the payload the counter
		// cannot see costs one stranded frame per image — three images,
		// three frames — while the plain control at the same fill stays
		// clean.
		{"tmux-full-plain.txt", "PLAIN", "full", 1, 0},
		{"tmux-full-sixel12.txt", "SIXEL-12", "full", -16, 3},
	} {
		t.Run(tc.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			got := analyze(tc.caseName, strings.Split(strings.TrimRight(string(b), "\n"), "\n"))
			if !got.GapOK {
				t.Fatalf("both anchors must be on screen: LastEnd=%d PinRow=%d", got.LastEnd, got.PinRow)
			}
			if got.Gap != tc.gap {
				t.Errorf("gap = %d, want %d", got.Gap, tc.gap)
			}
			if got.Stranded != tc.stranded {
				t.Errorf("stranded = %d, want %d", got.Stranded, tc.stranded)
			}
			// The regime must be the one the run arranged. A fixed filler
			// count chosen for a 30-row pane put an 80-row window on the
			// other side of the pad branch, and the ADR reported those
			// runs under the wrong label; this is the guard against
			// repeating it.
			wantFull := tc.regime == "full"
			if got.Full() != wantFull {
				t.Errorf("regime: arranged %q but the gap of %d says full=%v", tc.regime, got.Gap, got.Full())
			}
		})
	}
}

// TestAnalyzeReportsAbsentAnchors: a capture where the markers scrolled
// away must say so rather than produce a number. A gap computed from one
// anchor is the shape of every reading this tool got wrong.
func TestAnalyzeReportsAbsentAnchors(t *testing.T) {
	r := analyze("PLAIN", []string{"nothing here", "or here"})
	if r.GapOK || r.Gap != 0 || r.Frames != 0 {
		t.Errorf("empty capture must be inert, got %+v", r)
	}
	r = analyze("PLAIN", []string{"MARK-PLAIN-END", "no sentinel"})
	if r.GapOK {
		t.Error("an END with no pin must not yield a gap")
	}
}

// TestCaseNamesMatchTheFlagHelp: the -only help string is built from the
// same list the cases come from, so an added case cannot go unnamed. The
// previous version hard-coded five names while defining seven, and a
// reader reproducing a table could not find two of them.
func TestCaseNamesMatchTheFlagHelp(t *testing.T) {
	names := caseNames()
	if len(names) != len(allCases()) {
		t.Fatalf("caseNames() = %d, allCases() = %d", len(names), len(allCases()))
	}
	for _, n := range names {
		lines, notes := cases(n)
		if len(lines) != 3 || len(notes) != 1 {
			t.Errorf("case %q selects %d lines / %d notes, want 3 / 1", n, len(lines), len(notes))
		}
	}
	if lines, _ := cases("NO-SUCH-CASE"); len(lines) != 0 {
		t.Error("an unknown case name must select nothing, so the run can refuse")
	}
}

// TestAnalyzeCountsAFrameTheImageCovered is the iTerm2 run, and the
// instrument defect it exposed. The footer is
// "<model> · ctx … · <dir>"; a stranded frame with an image drawn over
// its left half keeps only the dir sentinel, so counting by the model
// sentinel alone reported a CLEAN pin for a screen carrying three
// stranded frames. An instrument that the thing it measures can blind
// reports success, which is the worst failure available to it.
//
// This fixture is the operator's copied transcript of
//
//	go run ./tools/pinprobe -only ITERM-H12 -regime full -repeat 3
//
// on iTerm2 3.7.2, 180x80, 2026-09-17 — the run whose META line
// (regime=full fill=90 rows=80) is the evidence that the full regime was
// arranged rather than assumed.
//
// ONLY THE FRAME COUNT IS ASSERTED. A copied transcript is not a screen
// capture: the rows an image occupies carry no text, so they are absent
// from the copy, and Gap / Full() computed from it would be geometry the
// source cannot supply. The tmux fixtures above come from capture-pane
// and do carry geometry; this one does not, and saying which is which is
// the whole point of keeping them side by side.
func TestAnalyzeCountsAFrameTheImageCovered(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "iterm2-full-itermh12-transcript.txt"))
	if err != nil {
		t.Fatal(err)
	}
	capture := strings.Split(strings.TrimRight(string(b), "\n"), "\n")

	got := analyze("ITERM-H12", capture)
	if got.Stranded != 3 {
		t.Errorf("stranded = %d, want 3 (one per image, repeat=3)", got.Stranded)
	}
	if got.Frames != 4 {
		t.Errorf("frames = %d, want 4 (three stranded plus the live one)", got.Frames)
	}

	// The defect, pinned: the model sentinel survives exactly once.
	model := 0
	for _, l := range capture {
		if strings.Contains(l, sentinelModel) {
			model++
		}
	}
	if model != 1 {
		t.Fatalf("fixture no longer exhibits the covering: %d model sentinels", model)
	}
	if got.Frames == model {
		t.Error("frames must not be counted by the model sentinel alone: an image covers it")
	}
}
