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
