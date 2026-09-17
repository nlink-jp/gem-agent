// Command pinprobe measures what rowprobe deliberately excluded: the
// bottom pin, under the condition production actually runs in.
//
// rowprobe reserves rows so nothing scrolls while it compares two cursor
// positions. That is necessary for its method and a deliberate exclusion
// of production's condition, because emit prints at the BOTTOM of the
// screen where every line scrolls.
//
// Nothing here is simulated. It constructs the REAL model (tui.New),
// runs it under the REAL inline Bubble Tea program, and pushes lines
// through the REAL emit path by sending tui.Output, so
// wrapForScrollback, physicalRows and the bottom-hold accounting are the
// production functions. The footer carries a sentinel, so a captured
// screen locates the pin without a human reading it.
//
// TWO THINGS THIS TOOL LEARNED THE HARD WAY, both from independent
// verification passes, and both the reason it is shaped as it is:
//
//  1. THE INSTRUMENT MUST CARRY ITS OWN NUMBERS. The first version
//     printed a fixed block of prose and measured nothing: the figures
//     that reached ADR-0089 came from throwaway shell drivers and from a
//     human reading screenshots, and were preserved nowhere. So -drive
//     runs the whole experiment, reads the screen back with tmux
//     capture-pane, and prints a table it computed; analyze is a pure
//     function, and its test replays real captures kept in testdata.
//     A reader who re-runs this gets numbers, not assurances.
//  2. THE REGIME MUST BE ARRANGED, NOT ASSUMED. The pin's padding is
//     `height - printed - view - 1` and the branch where it is positive
//     is labelled, in production, "screen not full" (internal/tui,
//     bottomHold). A fixed -fill chosen for a 30-row tmux pane put an
//     80-row iTerm2 window in the OTHER regime, and the ADR reported
//     those runs as "full". So the filler is computed from the terminal's
//     own height, -regime names which side is wanted, and every reading
//     prints the gap that shows which side it actually landed on.
//
// What this cannot do: tmux is the only screen reader here, and it does
// not draw the iTerm2 or kitty payloads — under it those are swallowed,
// so their line really does occupy one row and there is nothing to
// measure. (Sixel is different: this tmux renders it, which is why the
// sixel cases move at all.) A drawn OSC 1337 image at the bottom of a
// scrolling screen needs a terminal that both draws and can be read back;
// this tool cannot supply one, and says so rather than reporting the case
// that happened to be measurable.
//
// Usage:
//
//	go run ./tools/pinprobe -drive            # run the experiment, print the table
//	go run ./tools/pinprobe -only SIXEL-12    # one UI run, for a human to watch
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/termimg"
	"github.com/nlink-jp/gem-agent/internal/tui"
	"github.com/nlink-jp/gem-agent/tools/imgpayload"
)

// The footer prints ModelName and ProjectDir every frame, so they are the
// pin's address on a captured screen. Values no ordinary output contains.
// imageMarker tags a case whose text is raw PNG bytes for tui.Image
// rather than a payload to print. A tag beats a second case list.
const imageMarker = "\x00PINPROBE-IMAGE\x00"

const (
	sentinelModel = "PINPROBE~MODEL~SENTINEL"
	sentinelDir   = "/PINPROBE~DIR~SENTINEL"
)

func main() {
	drive := flag.Bool("drive", false, "run the whole experiment under tmux and print the measured table")
	only := flag.String("only", "", "run one case by name; empty runs all. Names: "+strings.Join(caseNames(), ", "))
	repeat := flag.Int("repeat", 1, "print the selected case this many times, so an undercount has somewhere to accumulate")
	regime := flag.String("regime", "full", "which side of the pad branch to arrange: full (filler exceeds the screen) or empty (no filler)")
	fill := flag.Int("fill", -1, "override the filler line count; -1 computes it from the terminal height and -regime")
	hold := flag.Duration("hold", 6*time.Second, "keep the UI up this long after the last line, so a capture can be taken")
	rows := flag.Int("rows", 30, "-drive only: tmux pane height")
	cols := flag.Int("cols", 120, "-drive only: tmux pane width")
	flag.Parse()

	if *drive {
		if err := runDriver(*rows, *cols, *repeat); err != nil {
			fmt.Fprintln(os.Stderr, "pinprobe:", err)
			os.Exit(1)
		}
		return
	}
	if err := runUI(*only, *repeat, *regime, *fill, *hold); err != nil {
		fmt.Fprintln(os.Stderr, "pinprobe:", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------
// The reading: a pure function over a captured screen.
// ---------------------------------------------------------------------

// Reading is what one captured screen says. Everything here is counted
// from the capture; nothing is inferred from what the run intended.
type Reading struct {
	Case     string
	Rows     int  // rows in the capture
	LastEnd  int  // 1-based row of the last MARK-<case>-END, 0 when absent
	PinRow   int  // 1-based row of the first footer sentinel, 0 when absent
	Frames   int  // sentinel occurrences: 1 is one painted frame
	Stranded int  // frames left behind: Frames-1, never negative
	Gap      int  // rows between the last END and the pin
	GapOK    bool // both anchors were on screen, so Gap means something
}

// Full reports which side of production's pad branch this screen landed
// on, and refuses to answer when it cannot tell.
//
// The branch is positive-pad = "screen not full". On a CLEAN screen the pad
// shows up as the gap between the last output and the pinned frame, so a
// small gap means full. Two limits, both found by a verification pass and
// both worth stating rather than hiding behind a threshold:
//
//   - Gap is measured to the TOPMOST sentinel, which on a damaged screen is
//     the first STRANDED frame, not the pin. So a negative gap says nothing
//     about the regime — it says frames were stranded above the last
//     output. The first version returned true for any Gap <= 1, which made
//     the check pass vacuously on exactly the screens it existed to police.
//   - Even clean, Gap is pad plus the frame's own height minus one, so the
//     threshold encodes pinprobe's two-row frame (input box + footer). Add
//     a row to the model and a full screen reads as empty.
//
// So: a damaged screen is UNKNOWN, and a clean one is compared against the
// frame height the tool actually renders.
const frameRows = 2 // input box + footer, the frame tui.New renders here

func (r Reading) Full() (full, known bool) {
	if !r.GapOK || r.Stranded > 0 || r.Gap < 0 {
		return false, false
	}
	return r.Gap <= frameRows-1, true
}

// analyze reads one captured screen for one case.
func analyze(caseName string, capture []string) Reading {
	r := Reading{Case: caseName, Rows: len(capture)}
	endMark := "MARK-" + caseName + "-END"
	for i, line := range capture {
		if strings.Contains(line, endMark) {
			r.LastEnd = i + 1
		}
		// Count a frame by EITHER sentinel. The footer is
		// "<model> · ctx … · <dir>", and an image drawn at the left of a
		// stranded frame covers the model sentinel while leaving the dir
		// one at the right intact — measured on iTerm2 2026-09-17, where
		// counting by the model sentinel alone reported a clean pin for a
		// screen carrying three stranded frames. The wider net is the
		// point: an instrument that can be blinded by the very thing it
		// measures reports success.
		if strings.Contains(line, sentinelModel) || strings.Contains(line, sentinelDir) {
			r.Frames++
			if r.PinRow == 0 {
				r.PinRow = i + 1
			}
		}
	}
	if r.Frames > 1 {
		r.Stranded = r.Frames - 1
	}
	if r.LastEnd > 0 && r.PinRow > 0 {
		r.Gap, r.GapOK = r.PinRow-r.LastEnd-1, true
	}
	return r
}

// ---------------------------------------------------------------------
// The driver: arrange the regime, run the UI, read the screen back.
// ---------------------------------------------------------------------

// run builds a child with the runtime's own environment stripped
// (ADR-0087 §2). Every spawn goes through here rather than calling
// exec.Command directly: the architecture test enumerates spawn sites and
// names probes explicitly, and the first version of -drive had five sites
// that each forgot the rule.
func run(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = sandbox.ChildEnv(os.Environ())
	return cmd
}

func runDriver(rows, cols, repeat int) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux is the screen reader and is not installed: %w", err)
	}
	fmt.Printf("pinprobe -drive — tmux pane %dx%d, %d repeat(s) per case\n", cols, rows, repeat)
	fmt.Printf("%-10s %-7s %-6s %-5s %-9s %s\n", "case", "regime", "asked", "got", "stranded", "gap END→pin")
	fmt.Println(strings.Repeat("-", 62))
	for _, regime := range []string{"full", "empty"} {
		for _, c := range caseNames() {
			read, err := oneRun(self, c, regime, rows, cols, repeat)
			if err != nil {
				fmt.Printf("%-10s %-7s FAILED: %v\n", c, regime, err)
				continue
			}
			got := "empty"
			if full, known := read.Full(); !known {
				got = "?"
			} else if full {
				got = "full"
			}
			gap := "n/a"
			if read.GapOK {
				gap = fmt.Sprintf("%d", read.Gap)
			}
			fmt.Printf("%-10s %-7s %-6s %-5s %-9d %s\n", c, regime, regime, got, read.Stranded, gap)
		}
	}
	fmt.Println()
	fmt.Println("stranded counts frames left behind in scrollback: 0 is a clean pin.")
	fmt.Println("'asked' is the regime arranged, 'got' is the one the gap shows it")
	fmt.Println("landed in; they must agree or the row says nothing about the regime.")
	fmt.Println("Under tmux the iTerm2 and kitty payloads are swallowed, so their")
	fmt.Println("rows are a control for the sixel ones, not a measurement of drawing.")
	return nil
}

// oneRun arranges one tmux pane, runs the UI in it, and reads it back.
func oneRun(self, caseName, regime string, rows, cols, repeat int) (Reading, error) {
	const session = "pinprobe-drive"
	_ = run("tmux", "kill-session", "-t", session).Run()
	cmd := fmt.Sprintf("%s -only %s -regime %s -repeat %d -hold 8s",
		self, caseName, regime, repeat)
	if err := run("tmux", "new-session", "-d", "-s", session,
		"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows), cmd).Run(); err != nil {
		return Reading{}, fmt.Errorf("tmux new-session: %w", err)
	}
	defer func() { _ = run("tmux", "kill-session", "-t", session).Run() }()
	time.Sleep(7 * time.Second)
	out, err := run("tmux", "capture-pane", "-t", session, "-p").Output()
	if err != nil {
		return Reading{}, fmt.Errorf("tmux capture-pane: %w", err)
	}
	var capture []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		capture = append(capture, sc.Text())
	}
	return analyze(caseName, capture), nil
}

// ---------------------------------------------------------------------
// The UI run.
// ---------------------------------------------------------------------

func runUI(only string, repeat int, regime string, fill int, hold time.Duration) error {
	lines, notes := cases(only)
	if len(lines) == 0 {
		return fmt.Errorf("no case named %q (have: %s)", only, strings.Join(caseNames(), ", "))
	}
	if repeat > 1 {
		one := lines
		lines = nil
		for i := 0; i < repeat; i++ {
			lines = append(lines, one...)
		}
	}

	// The capability is resolved here, before tea.NewProgram, exactly as
	// the product does it — so an IMAGE case exercises the real drawing
	// path rather than a payload this tool built itself.
	var tty *os.File
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		tty = f
		defer func() { _ = f.Close() }()
	}
	proto := termimg.Resolve("auto", tty, os.Getenv, 2*time.Second)
	model := tui.New(tui.Options{
		Theme:      "notty", // no colors: a capture is compared as text
		ModelName:  sentinelModel,
		ProjectDir: sentinelDir,
		Images:     proto,
	})
	prog := tea.NewProgram(model)

	// The filler is computed from the terminal, not from a constant: a
	// count that fills a 30-row pane leaves an 80-row window in the other
	// regime, and that mistake reached an ADR.
	if fill < 0 {
		fill = 0
		if regime == "full" {
			h := 24
			if _, r, err := termSize(); err == nil && r > 0 {
				h = r
			}
			fill = h + 10
		}
	}

	go func() {
		time.Sleep(700 * time.Millisecond)
		if fill > 0 {
			filler := make([]string, 0, fill)
			for i := 1; i <= fill; i++ {
				filler = append(filler, fmt.Sprintf("FILL %03d — arranging the %s regime", i, regime))
			}
			prog.Send(tui.Output{Lines: filler})
			time.Sleep(400 * time.Millisecond)
		}
		for _, l := range lines {
			if png, ok := strings.CutPrefix(l, imageMarker); ok {
				// The production path end to end: the model decodes,
				// chooses the box, declares it and counts it. Nothing
				// here builds a payload.
				prog.Send(tui.Image{Data: []byte(png), MIME: "image/png"})
				time.Sleep(250 * time.Millisecond)
				continue
			}
			prog.Send(tui.Output{Lines: []string{l}})
			time.Sleep(250 * time.Millisecond)
		}
		time.Sleep(hold)
		prog.Quit()
	}()

	if _, err := prog.Run(); err != nil {
		return err
	}
	// Machine-readable, so a driver OR A HUMAN READING THE SCREEN can
	// audit what was arranged rather than trusting a label. On a terminal
	// with no screen reader the eye is the instrument, and then this line
	// is the only evidence that the regime asked for is the regime the run
	// got: rows is the window the filler was computed against.
	cols, rows, err := termSize()
	if err != nil {
		cols, rows = 0, 0
	}
	fmt.Fprintf(os.Stderr, "PINPROBE-META regime=%s fill=%d rows=%d cols=%d repeat=%d cases=%d\n",
		regime, fill, rows, cols, repeat, len(notes))
	if rows > 0 && regime == "full" && fill < rows {
		fmt.Fprintf(os.Stderr, "PINPROBE-WARN asked for the full regime but printed %d filler lines into a %d-row window: this run is NOT full\n", fill, rows)
	}
	return nil
}

func termSize() (cols, rows int, err error) {
	out, err := run("stty", "-f", "/dev/tty", "size").Output()
	if err != nil {
		return 0, 0, err
	}
	_, err = fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &rows, &cols)
	return cols, rows, err
}

// ---------------------------------------------------------------------
// The cases.
// ---------------------------------------------------------------------

type probeCase struct {
	name    string
	payload func() string
	note    string
}

func allCases() []probeCase {
	png := func() []byte { return imgpayload.TestPNG(320, 180) }
	return []probeCase{
		{"PLAIN", func() string { return "a plain line of text, the control" },
			"control: one row of text, no escape"},
		{"ITERM-H6", func() string { return imgpayload.Iterm(png(), "width=40;height=6;preserveAspectRatio=1") },
			"iTerm2 OSC 1337, declares 6 rows — swallowed by tmux, so a control here"},
		{"ITERM-H12", func() string { return imgpayload.Iterm(png(), "width=40;height=12;preserveAspectRatio=1") },
			"iTerm2 OSC 1337, declares 12 rows — swallowed by tmux, so a control here"},
		{"KITTY-R6", func() string { return imgpayload.Kitty(png(), "f=100,r=6,c=40") },
			"kitty APC _G, declares 6 rows, single chunk at this size"},
		{"SIXEL-6", func() string { return imgpayload.Sixel(6) },
			"sixel DCS q, 36px tall, declares nothing — this tmux renders it"},
		{"SIXEL-12", func() string { return imgpayload.Sixel(12) },
			"sixel DCS q, 72px tall, declares nothing — this tmux renders it"},
		{"SIXEL-24", func() string { return imgpayload.Sixel(24) },
			"sixel DCS q, 144px tall, declares nothing — this tmux renders it"},
		{"IMAGE", func() string { return imageMarker + string(imgpayload.TestPNG(320, 180)) },
			"a real PNG through the PRODUCTION path: tui.Image -> drawImage -> declared box"},
	}
}

func caseNames() []string {
	var out []string
	for _, c := range allCases() {
		out = append(out, c.name)
	}
	return out
}

// cases returns the lines to push and a note per case. Each payload is
// bracketed by its own markers, so a capture can count the rows it took
// without trusting anything this tool says.
func cases(only string) (lines []string, notes []string) {
	for _, c := range allCases() {
		if only != "" && only != c.name {
			continue
		}
		lines = append(lines, "MARK-"+c.name+"-BEGIN", c.payload(), "MARK-"+c.name+"-END")
		notes = append(notes, c.note)
	}
	return lines, notes
}
