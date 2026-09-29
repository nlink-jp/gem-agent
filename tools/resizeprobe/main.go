// Command resizeprobe measures what narrowing and widening the window do
// to the TUI's screen and scrollback, one ADR-0094 arm at a time, in the
// terminal it runs in.
//
// It follows pinprobe's method: nothing here is simulated. It builds the
// REAL model (tui.New), runs it under the REAL inline Bubble Tea program,
// and prints through the REAL emit path (tui.Output, tui.Image), so
// wrapForScrollback, physicalRows, the bottom hold and the declared image
// box are the production functions. The arm under test is the model's own
// (tui.Options.Shrink), not a copy of it.
//
// A run clears the tab's screen and scrollback, fills the screen past its
// height with numbered lines, types a long draft into the input box, prints
// two pictures where the terminal draws (one pushed into the scrollback,
// one left on the screen), and then asks the operator to narrow the window
// and later to widen it, printing numbered lines after each until what was
// on the screen has scrolled away. Every printed line is numbered or is a
// marker, so a copy of the tab's text can be read by a machine: a blank row
// outside a picture is black space, an unnumbered row is a stale frame, and
// a gap in a numbered series is history the sweep destroyed. -analyze reads
// such a copy. Pictures carry no text and are read by eye; the report says
// which to look at.
//
// -drive runs every arm under tmux and resizes the pane itself. tmux
// reflows, but it does not draw the iTerm2 or kitty pictures, so -drive is
// a control that the arms and the analyzer work — a text reading of tmux,
// not of either terminal the decision is about.
//
// Usage:
//
//	go run ./tools/resizeprobe -arm clear     # one arm, in this terminal
//	pbpaste | go run ./tools/resizeprobe -analyze
//	go run ./tools/resizeprobe -drive         # every arm under tmux
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/termimg"
	"github.com/nlink-jp/gem-agent/internal/tui"
	"github.com/nlink-jp/gem-agent/tools/imgpayload"
)

// Sentinels no ordinary output contains: the footer prints ModelName and
// ProjectDir every frame, and the draft sits in the input box, so a stale
// copy of the frame carries at least one of them.
const (
	sentinelModel = "RESIZEPROBE~MODEL"
	sentinelDir   = "/RESIZEPROBE~DIR"
	sentinelDraft = "DRAFT~SENTINEL"
)

func main() {
	arm := flag.String("arm", "clear", "the shrink arm to run: clear (today), none, erase (ADR-0094)")
	analyzeIn := flag.Bool("analyze", false, "read a copy of the tab's text on stdin and print the readings")
	drive := flag.Bool("drive", false, "run every arm under tmux, resizing the pane, and print the readings")
	yes := flag.Bool("yes", false, "start without asking (the tab's screen and scrollback are cleared)")
	hold := flag.Duration("hold", 2*time.Second, "keep the UI up this long after PROBE-END")
	wait := flag.Duration("wait", 90*time.Second, "how long to wait for each resize before going on without it")
	steps := flag.String("steps", "", "narrow the window ITSELF (CSI 8 t) through these widths instead of asking: "+
		"comma-separated columns, or fit / fit+N / fit-N for the widest line of the frame drawn at the time. "+
		"a terminal that ignores it (tmux does) is reported, and the run asks for a drag instead")
	rows := flag.Int("rows", 30, "-drive only: tmux pane height")
	cols := flag.Int("cols", 120, "-drive only: tmux pane width")
	save := flag.String("save", "", "-drive only: also write each arm's capture to this directory as tmux-<arm>.txt")
	flag.Parse()

	var err error
	switch {
	case *analyzeIn:
		err = printReport(os.Stdout, analyze(readLines(os.Stdin)))
	case *drive:
		err = runDriver(*rows, *cols, *save)
	default:
		var mode tui.ShrinkMode
		if mode, err = parseArm(*arm); err == nil {
			err = runUI(mode, *yes, *hold, *wait, *steps)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "resizeprobe:", err)
		os.Exit(1)
	}
}

func parseArm(s string) (tui.ShrinkMode, error) {
	for _, m := range arms() {
		if m.String() == s {
			return m, nil
		}
	}
	return 0, fmt.Errorf("no arm named %q (have: clear, none, erase)", s)
}

func arms() []tui.ShrinkMode {
	return []tui.ShrinkMode{tui.ShrinkClearScreen, tui.ShrinkLeaveAlone, tui.ShrinkEraseFrame}
}

// ---------------------------------------------------------------------
// The reading: a pure function over a copy of the tab's text.
// ---------------------------------------------------------------------

// Zone is one stretch of the run, between two resize markers.
type Zone struct {
	Name     string
	Blank    int      // blank rows outside pictures: black space
	PicRows  []int    // blank rows inside each picture's markers
	Stray    int      // rows that are neither numbered nor a marker
	Frames   int      // stale frame pieces: each draft or footer copy, however many share a row
	Samples  []string // the first few stray rows, as found
	Numbered int
}

// Report is what one copy says.
type Report struct {
	Arm     string
	Found   bool // PROBE-START and PROBE-END were both present
	Zones   []Zone
	Missing map[string][]int // numbered series → numbers absent
	// Printed is what PROBE-END says each series printed. Without it a
	// series that lost its TAIL looked complete: kitty's clear erased the
	// last fifty lines of one in place (2026-09-29) and the report said
	// "none".
	Printed map[string]int
	// Lost names the markers the run printed and the copy does not hold;
	// a lost RESIZE-1 silently merged two zones.
	Lost []string
}

var (
	numbered = regexp.MustCompile(`^([HPAB])(\d{3}) `)
	// The tail of a long numbered line, where a copy keeps the terminal's
	// soft wrap as a line break (tmux capture-pane -J joins it; a
	// terminal's own copy may not). Counting it as stray would report
	// history as a stale frame.
	tail = regexp.MustCompile(`^[~ ]+$`)
	picMark  = regexp.MustCompile(`^PIC-(\d+) (BEGIN|END)`)
	armMark  = regexp.MustCompile(`^PROBE-START arm=(\S+)`)
	protoArg = regexp.MustCompile(` proto=(\S+)`)
	counts   = regexp.MustCompile(`\b([HPAB])=(\d+)`)
	// Anywhere on the row: a terminal can glue the next line onto a stale
	// frame's row (the none arm's AFTER-SHRINK, measured under tmux).
	markers = regexp.MustCompile(`(PIC-\d+ (?:BEGIN|END)|RESIZE-\d|AFTER-SHRINK|AFTER-GROW)`)
)

// analyze reads a copy of the tab's text. Only rows between PROBE-START
// and PROBE-END count: below the end is the live frame and the probe's own
// report, above the start whatever the tab held before. The LAST start
// counts: a terminal that ignored the probe's scrollback clear still holds
// the runs before it.
func analyze(lines []string) Report {
	r := Report{Missing: map[string][]int{}, Printed: map[string]int{}}
	marks := map[string]bool{}
	pictures := false
	for i := len(lines) - 1; i >= 0; i-- {
		if armMark.MatchString(strings.TrimRight(ansi.Strip(lines[i]), " \t\r")) {
			lines = lines[i:]
			break
		}
	}
	seen := map[string]map[int]bool{}
	max := map[string]int{}
	var z *Zone
	started, inPic := false, false
	for _, raw := range lines {
		line := strings.TrimRight(ansi.Strip(raw), " \t\r")
		if !started {
			if m := armMark.FindStringSubmatch(line); m != nil {
				started = true
				r.Arm = m[1]
				if p := protoArg.FindStringSubmatch(line); p != nil && p[1] != "none" {
					pictures = true
				}
				r.Zones = append(r.Zones, Zone{Name: "before"})
				z = &r.Zones[len(r.Zones)-1]
			}
			continue
		}
		for _, m := range markers.FindAllStringSubmatch(line, -1) {
			marks[m[1]] = true
		}
		// A zone marker glued behind a stale frame: the frame belongs to
		// the zone before it.
		for _, zm := range []string{"RESIZE-1", "RESIZE-2"} {
			if i := strings.Index(line, zm); i > 0 {
				z.Stray++
				z.Frames += framePieces(line[:i])
				if len(z.Samples) < 3 {
					z.Samples = append(z.Samples, line)
				}
				line = line[i:]
			}
		}
		switch {
		case strings.HasPrefix(line, "PROBE-END"):
			r.Found = true
			for _, c := range counts.FindAllStringSubmatch(line, -1) {
				r.Printed[c[1]], _ = strconv.Atoi(c[2])
			}
		case strings.HasPrefix(line, "RESIZE-1"):
			r.Zones = append(r.Zones, Zone{Name: "shrink"})
			z = &r.Zones[len(r.Zones)-1]
			continue
		case strings.HasPrefix(line, "RESIZE-2"):
			r.Zones = append(r.Zones, Zone{Name: "grow"})
			z = &r.Zones[len(r.Zones)-1]
			continue
		}
		if r.Found {
			break
		}
		switch m := picMark.FindStringSubmatch(line); {
		case m != nil && m[2] == "BEGIN":
			inPic = true
			z.PicRows = append(z.PicRows, 0)
			continue
		case m != nil:
			inPic = false
			continue
		}
		switch {
		case strings.TrimSpace(line) == "":
			if inPic {
				z.PicRows[len(z.PicRows)-1]++
			} else {
				z.Blank++
			}
		case numbered.MatchString(line):
			m := numbered.FindStringSubmatch(line)
			n, _ := strconv.Atoi(m[2])
			if seen[m[1]] == nil {
				seen[m[1]] = map[int]bool{}
			}
			seen[m[1]][n] = true
			if n > max[m[1]] {
				max[m[1]] = n
			}
			z.Numbered++
		case tail.MatchString(line):
			// the rest of a long numbered line
		case strings.HasPrefix(line, "AFTER-") || strings.HasPrefix(line, "PROBE-") || strings.HasPrefix(line, "DRAG INSTEAD"):
			// markers
		default:
			z.Stray++
			z.Frames += framePieces(line)
			if len(z.Samples) < 3 {
				z.Samples = append(z.Samples, line)
			}
		}
	}
	for _, series := range []string{"H", "P", "A", "B"} {
		// What the run says it printed, where it said; below the highest
		// number found otherwise, which cannot see a lost tail.
		top, whole := r.Printed[series]
		if !whole {
			top = max[series] - 1
		}
		for n := 1; n <= top; n++ {
			if !seen[series][n] {
				r.Missing[series] = append(r.Missing[series], n)
			}
		}
	}
	expected := []string{"RESIZE-1", "AFTER-SHRINK", "RESIZE-2", "AFTER-GROW"}
	if pictures {
		expected = append([]string{"PIC-1 BEGIN", "PIC-1 END", "PIC-2 BEGIN", "PIC-2 END"}, expected...)
	}
	for _, m := range expected {
		if !marks[m] {
			r.Lost = append(r.Lost, m)
		}
	}
	return r
}

// ranges prints a sorted list of numbers as runs: P032-P077, H003.
func ranges(series string, ns []int) string {
	var out []string
	for i := 0; i < len(ns); {
		j := i
		for j+1 < len(ns) && ns[j+1] == ns[j]+1 {
			j++
		}
		if i == j {
			out = append(out, fmt.Sprintf("%s%03d", series, ns[i]))
		} else {
			out = append(out, fmt.Sprintf("%s%03d-%s%03d", series, ns[i], series, ns[j]))
		}
		i = j + 1
	}
	return strings.Join(out, " ")
}

// framePieces counts the stale frame copies on one row of a copy. iTerm2
// re-joins a staircase into ONE logical line — four drafts and the next
// marker glued together (measured, 2026-09-29) — so counting rows reported
// one copy where there were four. A footer carries both of its sentinels;
// it counts once.
func framePieces(line string) int {
	footers := strings.Count(line, sentinelModel)
	if d := strings.Count(line, sentinelDir); d > footers {
		footers = d
	}
	return strings.Count(line, sentinelDraft) + footers
}

func printReport(w io.Writer, r Report) error {
	if r.Arm == "" {
		return fmt.Errorf("no PROBE-START line: this is not a copy of a resizeprobe run")
	}
	fmt.Fprintf(w, "arm %s", r.Arm)
	if !r.Found {
		fmt.Fprint(w, " — PROBE-END not found: the copy is incomplete, readings below cover what it holds")
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%-7s %-6s %-6s %-6s %-9s %s\n", "zone", "black", "stray", "frames", "numbered", "picture rows")
	for _, z := range r.Zones {
		fmt.Fprintf(w, "%-7s %-6d %-6d %-6d %-9d %v\n", z.Name, z.Blank, z.Stray, z.Frames, z.Numbered, z.PicRows)
	}
	var missing []string
	for _, series := range []string{"H", "P", "A", "B"} {
		if ns := r.Missing[series]; len(ns) > 0 {
			missing = append(missing, fmt.Sprintf("%s (%d)", ranges(series, ns), len(ns)))
		}
	}
	if len(missing) == 0 {
		missing = []string{"none"}
	}
	fmt.Fprintf(w, "missing numbered lines: %s\n", strings.Join(missing, ", "))
	if len(r.Printed) == 0 {
		fmt.Fprintln(w, "  (PROBE-END carries no counts: a lost tail of a series cannot be seen)")
	}
	if len(r.Lost) > 0 {
		fmt.Fprintf(w, "markers lost: %s\n", strings.Join(r.Lost, ", "))
	}
	for _, z := range r.Zones {
		for _, s := range z.Samples {
			fmt.Fprintf(w, "  stray in %s: %q\n", z.Name, s)
		}
	}
	fmt.Fprintln(w, "black: blank rows outside pictures. stray: rows neither numbered nor a marker;")
	fmt.Fprintln(w, "frames: stale draft or footer copies, counted even when several share a row. A missing number is history")
	fmt.Fprintln(w, "the sweep erased. Picture rows are blank in a text copy; compare them across arms.")
	return nil
}

func readLines(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// ---------------------------------------------------------------------
// The UI run.
// ---------------------------------------------------------------------

// sizeLog records every size report the program receives, as the model
// receives it.
type sizeLog struct {
	mu     sync.Mutex
	events []sizeEvent
}

type sizeEvent struct {
	at   time.Time
	w, h int
}

func (s *sizeLog) add(w, h int) {
	s.mu.Lock()
	s.events = append(s.events, sizeEvent{time.Now(), w, h})
	s.mu.Unlock()
}

func (s *sizeLog) last() (sizeEvent, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return sizeEvent{}, 0
	}
	return s.events[len(s.events)-1], len(s.events)
}

func (s *sizeLog) all() []sizeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sizeEvent(nil), s.events...)
}

// waitFor waits until a report satisfies ok, then until no report has
// arrived for settle — a drag delivers many. It returns the settled size
// and false when no report satisfied ok within limit.
func (s *sizeLog) waitFor(ok func(w int) bool, settle, limit time.Duration) (sizeEvent, bool) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if e, _ := s.last(); ok(e.w) {
			for {
				e, _ = s.last()
				if time.Since(e.at) >= settle {
					return e, true
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	e, _ := s.last()
	return e, false
}

func runUI(mode tui.ShrinkMode, yes bool, hold, wait time.Duration, steps string) error {
	plan, err := parseSteps(steps)
	if err != nil {
		return err
	}
	if !yes {
		fmt.Printf("resizeprobe, arm %q (ADR-0094).\n", mode)
		fmt.Println("It CLEARS this tab's screen and scrollback, so run it in a tab you do not need.")
		fmt.Println("It will ask you to narrow the window, then to widen it again.")
		fmt.Print("Size the window as you normally use it, then press Enter to start: ")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			return fmt.Errorf("no answer on stdin: %w", err)
		}
	}
	// Home, erase the screen, then the saved lines — in that order, so a
	// terminal that moves the screen into the scrollback on ED 2 has it
	// erased by ED 3 straight after. A run is read from a known start.
	fmt.Print("\x1b[H\x1b[2J\x1b[3J")

	// The capability is resolved before tea.NewProgram, as the product
	// does it (ADR-0089 §7), so the pictures take the real drawing path.
	var tty *os.File
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		tty = f
		defer func() { _ = f.Close() }()
	}
	proto := termimg.Resolve("auto", tty, os.Getenv, 2*time.Second)

	sweep := tui.NewSweepWriter(os.Stdout)
	model := tui.New(tui.Options{
		Theme:      "notty", // no colours: a copy is compared as text
		ModelName:  sentinelModel,
		ProjectDir: sentinelDir,
		Images:     proto,
		Shrink:     mode,
		Sweep:      sweep,
	})
	sizes := &sizeLog{}
	prog := tea.NewProgram(model, tea.WithOutput(sweep),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if s, ok := msg.(tea.WindowSizeMsg); ok {
				sizes.add(s.Width, s.Height)
			}
			return msg
		}))

	var notes []string
	go func() {
		notes = script(prog, sweep, sizes, proto, mode, plan, wait, hold)
		prog.Quit()
	}()
	if _, err := prog.Run(); err != nil {
		return err
	}

	flushes, sweeps := sweep.Stats()
	fmt.Println()
	fmt.Printf("RESIZEPROBE-META arm=%s proto=%s flushes=%d sweeps=%d\n", mode, protoName(proto), flushes, sweeps)
	for _, n := range notes {
		fmt.Println("RESIZEPROBE-NOTE", n)
	}
	if arms := sweep.Arms(); len(arms) > 0 {
		fmt.Println("sweeps armed (width re-wrapped to, K computed, the drawn frame's line widths):")
		for i, a := range arms {
			fmt.Printf("  %3d  width %d  K %d  cells %v\n", i+1, a.Width, a.K, a.Cells)
		}
	}
	fmt.Println("size reports (the model receives these):")
	events := sizes.all()
	for i, e := range events {
		gap := ""
		if i > 0 {
			gap = fmt.Sprintf(" +%dms", e.at.Sub(events[i-1].at).Milliseconds())
		}
		fmt.Printf("  %3d  %dx%d%s\n", i+1, e.w, e.h, gap)
	}
	fmt.Println()
	fmt.Println("Now read the result, scrolling up from here:")
	if proto != termimg.None {
		fmt.Println("  1. PIC-1 (pushed into the scrollback before the narrowing) and PIC-2 (on the")
		fmt.Println("     screen when you narrowed): is each one kept, black, or gone? Scroll to see.")
	} else {
		fmt.Println("  1. No pictures: this terminal was not detected as iTerm2 or kitty.")
	}
	fmt.Println("  2. Text: copy the whole tab and run the analyzer on it —")
	fmt.Println("     iTerm2: Edit > Select All, Copy, then: pbpaste | go run ./tools/resizeprobe -analyze")
	fmt.Println("  3. What you saw while narrowing and widening, in your own words.")
	return nil
}

// script is the run, sent to the program from outside its event loop.
func script(prog *tea.Program, sweep *tui.SweepWriter, sizes *sizeLog, proto termimg.Protocol, mode tui.ShrinkMode,
	plan []step, wait, hold time.Duration) (notes []string) {
	say := func(lines ...string) {
		prog.Send(tui.Output{Lines: lines})
		time.Sleep(120 * time.Millisecond)
	}
	printed := map[string]int{}
	series := func(prefix string, count, width int) {
		printed[prefix] = count
		batch := make([]string, 0, 10)
		for i := 1; i <= count; i++ {
			batch = append(batch, numberedLine(prefix, i, width))
			if len(batch) == cap(batch) || i == count {
				say(batch...)
				batch = batch[:0]
			}
		}
	}

	time.Sleep(800 * time.Millisecond) // the first size report and the first frame
	start, _ := sizes.last()
	w, h := start.w, start.h
	if w <= 0 || h <= 0 {
		w, h = 80, 24
		notes = append(notes, "no size report before the start; assumed 80x24")
	}
	say(fmt.Sprintf("PROBE-START arm=%s proto=%s size=%dx%d", mode, protoName(proto), w, h))
	prog.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(draftText(w))})
	series("H", h+10, w)

	if proto != termimg.None {
		png := imgpayload.TestPNG(320, 180)
		say("PIC-1 BEGIN (pushed into the scrollback before the narrowing)")
		prog.Send(tui.Image{Data: png, MIME: "image/png"})
		time.Sleep(250 * time.Millisecond)
		say("PIC-1 END")
		series("P", h, w)
		say("PIC-2 BEGIN (on the screen when the window narrows)")
		prog.Send(tui.Image{Data: png, MIME: "image/png"})
		time.Sleep(250 * time.Millisecond)
		say("PIC-2 END")
	}

	var got sizeEvent
	ok, driven := false, false
	if len(plan) > 0 {
		say("RESIZE-1: the probe narrows the window itself — do not touch it")
		driven = true
		for _, st := range plan {
			target := st.resolve(sweep.DrawnCells())
			cur, _ := sizes.last()
			_ = sweep.Inject(fmt.Sprintf("\x1b[8;%d;%dt", cur.h, target))
			if got, ok = sizes.waitFor(func(x int) bool { return x == target }, 800*time.Millisecond, 3*time.Second); !ok {
				notes = append(notes, fmt.Sprintf("asked for width %d and the terminal did not resize (it ignores CSI 8 t, or refused): falling back to a drag", target))
				driven = false
				break
			}
		}
	}
	if !driven {
		if len(plan) > 0 {
			// RESIZE-1 was already printed; a second would open a second zone.
			say("DRAG INSTEAD: the terminal did not resize itself — NARROW the window now, to about two-thirds, and wait")
		} else {
			say("RESIZE-1: NARROW the window now, to about two-thirds of its width, and wait")
		}
		got, ok = sizes.waitFor(func(x int) bool { return x > 0 && x < w }, 1500*time.Millisecond, wait)
		if !ok {
			notes = append(notes, "no narrowing within the wait: the shrink zone measures nothing")
		}
	}
	say(fmt.Sprintf("AFTER-SHRINK size=%dx%d", got.w, got.h))
	series("A", 2*got.h, got.w)

	narrow := got.w
	if driven {
		say("RESIZE-2: the probe widens the window back itself")
		_ = sweep.Inject(fmt.Sprintf("\x1b[8;%d;%dt", got.h, w))
	} else {
		say("RESIZE-2: WIDEN the window again now, and wait")
	}
	got, ok = sizes.waitFor(func(x int) bool { return x > narrow }, 1500*time.Millisecond, wait)
	if !ok {
		notes = append(notes, "no widening within the wait: the grow zone measures nothing")
	}
	say(fmt.Sprintf("AFTER-GROW size=%dx%d", got.w, got.h))
	series("B", 2*got.h, got.w)

	say(fmt.Sprintf("PROBE-END printed H=%d P=%d A=%d B=%d", printed["H"], printed["P"], printed["A"], printed["B"]))
	time.Sleep(hold)
	return notes
}

// step is one width -steps asks for: a column count, or the widest drawn
// frame line plus an offset ("fit" — the width at which that line exactly
// fills a row, the case iTerm2 was seen to leave a stale draft at).
type step struct {
	fit    bool
	offset int // added to the fit width; the width itself when !fit
}

func (s step) resolve(drawn []int) int {
	if !s.fit {
		return s.offset
	}
	widest := 0
	for _, c := range drawn {
		if c > widest {
			widest = c
		}
	}
	return widest + s.offset
}

func parseSteps(spec string) ([]step, error) {
	if spec == "" {
		return nil, nil
	}
	var out []step
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		switch {
		case tok == "fit":
			out = append(out, step{fit: true})
		case strings.HasPrefix(tok, "fit+") || strings.HasPrefix(tok, "fit-"):
			n, err := strconv.Atoi(tok[3:])
			if err != nil {
				return nil, fmt.Errorf("-steps: %q is not fit+N or fit-N", tok)
			}
			out = append(out, step{fit: true, offset: n})
		default:
			n, err := strconv.Atoi(tok)
			if err != nil || n < 20 {
				return nil, fmt.Errorf("-steps: %q is not a width of 20 columns or more, nor fit", tok)
			}
			out = append(out, step{offset: n})
		}
	}
	return out, nil
}

// numberedLine is short, except every fifth, which is as long as the
// hard wrap allows at this width: those are the history rows that
// re-wrap when the window narrows.
func numberedLine(prefix string, n, width int) string {
	s := fmt.Sprintf("%s%03d ", prefix, n)
	if n%5 != 0 {
		return s + "history"
	}
	for ansi.StringWidth(s)+5 <= width-1 {
		s += "~~~~ "
	}
	return strings.TrimRight(s, " ")
}

// draftText fills most of the input box, so the frame's own line re-wraps
// when the window narrows — the staircase is the frame's, not history's.
func draftText(width int) string {
	s := sentinelDraft
	for ansi.StringWidth(s)+6 <= width-6 {
		s += " type"
	}
	return s
}

func protoName(p termimg.Protocol) string {
	switch p {
	case termimg.ITerm2:
		return "iterm2"
	case termimg.Kitty:
		return "kitty"
	default:
		return "none"
	}
}

// ---------------------------------------------------------------------
// The driver: every arm under tmux, the pane resized by the driver.
// ---------------------------------------------------------------------

// run builds a child with the runtime's own environment stripped
// (ADR-0087 §2); the architecture test holds every spawn site to it.
func run(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = sandbox.ChildEnv(os.Environ())
	return cmd
}

func runDriver(rows, cols int, save string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux is the screen reader and is not installed: %w", err)
	}
	narrow := cols * 2 / 3
	fmt.Printf("resizeprobe -drive — tmux pane %dx%d, narrowed to %d and widened back\n", cols, rows, narrow)
	fmt.Println("tmux draws no iTerm2 or kitty picture: these are text readings of tmux, a control.")
	for _, arm := range arms() {
		fmt.Println()
		lines, err := driveOne(self, arm, rows, cols, narrow)
		if err != nil {
			fmt.Printf("arm %s FAILED: %v\n", arm, err)
			continue
		}
		if save != "" {
			path := filepath.Join(save, "tmux-"+arm.String()+".txt")
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				return fmt.Errorf("save capture: %w", err)
			}
		}
		if err := printReport(os.Stdout, analyze(lines)); err != nil {
			fmt.Printf("arm %s: %v\n", arm, err)
		}
	}
	return nil
}

func driveOne(self string, arm tui.ShrinkMode, rows, cols, narrow int) ([]string, error) {
	const session = "resizeprobe-drive"
	_ = run("tmux", "kill-session", "-t", session).Run()
	cmd := fmt.Sprintf("%s -arm %s -yes -hold 8s -wait 20s", self, arm)
	if err := run("tmux", "new-session", "-d", "-s", session,
		"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows), cmd).Run(); err != nil {
		return nil, fmt.Errorf("tmux new-session: %w", err)
	}
	defer func() { _ = run("tmux", "kill-session", "-t", session).Run() }()

	steps := []struct {
		marker string
		width  int
	}{{"RESIZE-1", narrow}, {"RESIZE-2", cols}, {"PROBE-END", 0}}
	for _, st := range steps {
		if err := waitForText(session, st.marker, 30*time.Second); err != nil {
			return nil, err
		}
		if st.width > 0 {
			time.Sleep(300 * time.Millisecond)
			if err := run("tmux", "resize-window", "-t", session, "-x", fmt.Sprint(st.width)).Run(); err != nil {
				return nil, fmt.Errorf("tmux resize-window: %w", err)
			}
		}
	}
	time.Sleep(time.Second)
	out, err := run("tmux", "capture-pane", "-t", session, "-p", "-J", "-S", "-").Output()
	if err != nil {
		return nil, fmt.Errorf("tmux capture-pane: %w", err)
	}
	return readLines(strings.NewReader(string(out))), nil
}

func waitForText(session, text string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		out, err := run("tmux", "capture-pane", "-t", session, "-p", "-J", "-S", "-").Output()
		if err == nil && strings.Contains(string(out), text) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%s did not appear within %s", text, limit)
}
