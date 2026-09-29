package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestAnalyzeAgainstCapturedRuns replays real runs: `resizeprobe -drive
// -save testdata`, tmux 3.7c, a 120x30 pane narrowed to 80 and widened
// back, 2026-09-29. They are text readings of tmux, which draws no iTerm2
// or kitty picture — the control that the arms and this analyzer work, not
// a reading of either terminal ADR-0094 is deciding for. What they show:
// tmux keeps the cleared screen in its history, so today's clear leaves a
// copy of the frame (draft and footer) in the scrollback; leaving it alone
// strands the draft's first wrapped row; erasing the frame's rows leaves
// nothing. No arm puts black space into tmux's history or loses a line.
func TestAnalyzeAgainstCapturedRuns(t *testing.T) {
	for _, tc := range []struct {
		arm          string
		stray, frame int // in the shrink zone
	}{
		{"clear", 2, 2},
		{"none", 1, 1},
		{"erase", 0, 0},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "tmux-"+tc.arm+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		r := analyze(strings.Split(string(raw), "\n"))
		if r.Arm != tc.arm || !r.Found {
			t.Fatalf("%s: arm %q, found %v", tc.arm, r.Arm, r.Found)
		}
		if len(r.Zones) != 3 {
			t.Fatalf("%s: %d zones, want before/shrink/grow", tc.arm, len(r.Zones))
		}
		shrink := r.Zones[1]
		if shrink.Stray != tc.stray || shrink.Frames != tc.frame {
			t.Errorf("%s: shrink stray %d frames %d, want %d %d (%q)", tc.arm, shrink.Stray, shrink.Frames, tc.stray, tc.frame, shrink.Samples)
		}
		for _, z := range r.Zones {
			if z.Blank != 0 {
				t.Errorf("%s: %s zone has %d blank rows", tc.arm, z.Name, z.Blank)
			}
			if z.Name != "shrink" && z.Stray != 0 {
				t.Errorf("%s: %s zone has %d stray rows", tc.arm, z.Name, z.Stray)
			}
		}
		if len(r.Missing) != 0 {
			t.Errorf("%s: missing %v", tc.arm, r.Missing)
		}
	}
}

// Every reading the analyzer reports, on a transcript built to hold one of
// each — including the things it must NOT count.
func TestAnalyzeCountsEachReading(t *testing.T) {
	lines := []string{
		"whatever the tab held before",
		"",
		"\x1b[1mPROBE-START arm=erase proto=kitty size=100x30\x1b[0m",
		"H001 history",
		"H002 history",
		"PIC-1 BEGIN (pushed into the scrollback before the narrowing)",
		"", "", "", // the picture's rows: blank in a text copy
		"PIC-1 END",
		"H004 history", // H003 missing: history the sweep erased
		"RESIZE-1: NARROW the window now",
		"",                                      // black space
		"┃ DRAFT~SENTINEL type type",            // a stale frame
		"RESIZEPROBE~MODEL · ctx –/– · total 0", // and its footer
		"─── a border fragment",                 // stray, not a sentinel
		"AFTER-SHRINK size=66x30",
		"A001 ~~~~ ~~~~",
		"RESIZE-2: WIDEN the window again now",
		"AFTER-GROW size=100x30",
		"B001 history",
		"PROBE-END",
		"┃ DRAFT~SENTINEL the live frame, below the end",
		"",
	}
	r := analyze(lines)
	if r.Arm != "erase" || !r.Found {
		t.Fatalf("arm %q found %v", r.Arm, r.Found)
	}
	before, shrink, grow := r.Zones[0], r.Zones[1], r.Zones[2]
	if before.Blank != 0 || len(before.PicRows) != 1 || before.PicRows[0] != 3 || before.Stray != 0 {
		t.Errorf("before: %+v", before)
	}
	if shrink.Blank != 1 || shrink.Stray != 3 || shrink.Frames != 2 || shrink.Numbered != 1 {
		t.Errorf("shrink: %+v", shrink)
	}
	if grow.Blank != 0 || grow.Stray != 0 || grow.Numbered != 1 {
		t.Errorf("grow: %+v", grow)
	}
	if got := r.Missing["H"]; len(got) != 1 || got[0] != 3 {
		t.Errorf("missing = %v, want H003", r.Missing)
	}

	var out bytes.Buffer
	if err := printReport(&out, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "map[H:[3]]") {
		t.Errorf("the report does not name the missing line:\n%s", out.String())
	}
}

func TestReportRefusesATextThatIsNotARun(t *testing.T) {
	if err := printReport(&bytes.Buffer{}, analyze([]string{"hello", "world"})); err == nil {
		t.Error("a copy with no PROBE-START must be refused, not reported as clean")
	}
	r := analyze([]string{"PROBE-START arm=none", "H001 history"})
	var out bytes.Buffer
	if err := printReport(&out, r); err != nil || !strings.Contains(out.String(), "PROBE-END not found") {
		t.Errorf("an incomplete copy must say so: %v\n%s", err, out.String())
	}
}

// The long history lines re-wrap on a narrowing only if they are long, and
// they must never soft-wrap at the width they are printed at.
func TestNumberedLinesFitTheirWidth(t *testing.T) {
	for _, w := range []int{40, 80, 120} {
		if got := ansi.StringWidth(numberedLine("H", 5, w)); got > w-1 || got < w-8 {
			t.Errorf("width %d: a long line is %d cells, want just under the width", w, got)
		}
		if got := numberedLine("H", 4, w); got != "H004 history" {
			t.Errorf("a short line is %q", got)
		}
		if d := draftText(w); !strings.HasPrefix(d, sentinelDraft) || ansi.StringWidth(d) > w-6 {
			t.Errorf("width %d: draft %d cells", w, ansi.StringWidth(d))
		}
	}
}
