// Command rowprobe answers one question and nothing else: when an
// inline-image escape DECLARES a height in cells, does the terminal
// consume exactly that many rows?
//
// The answer decides whether inline graphics can live in this TUI at
// all. emit (internal/tui/model.go) prints a line and counts its
// physical rows, and the bottom pinning rests on that count. An image
// payload measures ZERO cells wide — ansi.StringWidth returns 0 for
// OSC 1337, APC _G and DCS sixel alike (measured against x/ansi
// v0.11.6, the version this module pins) — so the counter cannot
// measure an image. It can only be TOLD. If the terminal honours the
// declaration, the count is exact by construction and the pinning
// holds; if it does not, there is no number to hand the counter and
// the whole approach is dead.
//
// Method: reserve N+3 rows below the cursor (print newlines, then move
// back up — this forces the scroll BEFORE the measurement, so nothing
// scrolls during it), ask the terminal where the cursor is (CPR,
// ESC[6n), write the payload, ask again. Every case is drawn, so the
// screen doubles as the visual check: the numbers and the picture are
// the same run.
//
// THE CURSOR DELTA IS NOT THE ROW COUNT. Measured (iTerm2 3.7.2): an
// image leaves the cursor on its LAST row, at the column just past the
// declared box — never on a fresh row below. So the delta between the
// two reports is one less than the rows the image occupies, and reading
// the delta as the count makes a terminal that honours every
// declaration exactly look like one that honours none. consumedRows
// does the correction, and its test carries the measurement that
// justifies it.
//
// A LOST REPLY IS FATAL, NEVER SKIPPED. Measured on iTerm2 3.7.2: after
// an image payload the terminal answers ESC[6n late — later than the
// 700ms the first version of this probe allowed. That version gave up
// and moved to the next case, and every number after that point was
// garbage: a CPR reply carries no tag, so an abandoned reply is not
// lost, it is MISFILED — the next query reads the previous query's
// answer and reports some other case's cursor. (The run showed it
// exactly: 11 queries sent, 5 read, and the 6 late replies landed on
// the shell prompt after exit as ";41R;1R;41R;1R;1R;1R", zsh having
// eaten each ESC[<row>.) So the budget is seconds, not milliseconds,
// the stream is drained before each query and before exit, and a query
// that truly goes unanswered stops the run instead of poisoning it.
//
// The report goes to stdout, the drawing to /dev/tty, so
//
//	go run ./tools/rowprobe > dist/rowprobe.txt
//
// keeps the table while the images stay on screen.
//
// Usage: go run ./tools/rowprobe [-proto auto|iterm|kitty]
//
//	[-timeout 5s] [-settle 250ms]
//
// Requires a real terminal: it opens /dev/tty and needs a reply to
// CPR. Terminal.app answers CPR but draws no image (every case will
// read 0 rows, reported as INCONCLUSIVE rather than as a verdict).
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/nlink-jp/gem-agent/tools/imgpayload"
)

// probeCase is one payload to draw and measure. declared is the row
// count the payload states; 0 means it states none, and then the
// observed count is information rather than a verdict.
type probeCase struct {
	name     string
	w, h     int // source image size in pixels
	iterm    string
	kitty    string
	declared int
	prefix   string // text on the same line, before the payload
	suffix   string // text on the same line, after it
}

// cases cover the shapes the TUI would actually emit: a box the image
// fits by width, the same box with a tall image (fits by height), the
// stretched variant, the extremes (1 row, 12 rows), an undeclared
// image, and an image with text around it on one line — emit prints
// whole lines, so that last one is not academic.
var cases = []probeCase{
	{name: "no size declared", w: 320, h: 180, iterm: "", kitty: "f=100", declared: 0},
	{name: "height=6 only", w: 320, h: 180, iterm: "height=6", kitty: "f=100,r=6", declared: 6},
	{name: "40x6 box, aspect kept (wide img)", w: 320, h: 180, iterm: "width=40;height=6;preserveAspectRatio=1", kitty: "f=100,r=6,c=40", declared: 6},
	{name: "40x6 box, aspect kept (tall img)", w: 180, h: 320, iterm: "width=40;height=6;preserveAspectRatio=1", kitty: "f=100,r=6,c=40", declared: 6},
	{name: "40x6 box, stretched", w: 320, h: 180, iterm: "width=40;height=6;preserveAspectRatio=0", kitty: "f=100,r=6,c=40", declared: 6},
	{name: "height=1", w: 320, h: 180, iterm: "height=1", kitty: "f=100,r=1", declared: 1},
	{name: "40x12 box, aspect kept", w: 320, h: 180, iterm: "width=40;height=12;preserveAspectRatio=1", kitty: "f=100,r=12,c=40", declared: 12},
	{name: "text + image + text on one line", w: 320, h: 180, iterm: "height=6", kitty: "f=100,r=6", declared: 6, prefix: "before ", suffix: " after"},
}

// result is what one case measured.
type result struct {
	c        probeCase
	rows     int // rows the cursor advanced (NOT the occupancy: see consumedRows)
	consumed int // rows the image actually occupies
	endCol   int // column the cursor ended on
	width    int // what ansi.StringWidth makes of the payload
	bytes    int // payload size
	waited   time.Duration
	scrolled bool // the screen scrolled during the measurement: rows is a floor, not a count
	skipped  string
}

func main() {
	proto := flag.String("proto", "auto", "image protocol: auto, iterm, kitty")
	timeout := flag.Duration("timeout", 5*time.Second, "how long to wait for one cursor report before giving up on the run")
	settle := flag.Duration("settle", 250*time.Millisecond, "pause after writing an image before asking where the cursor is")
	flag.Parse()

	if err := run(*proto, *timeout, *settle); err != nil {
		fmt.Fprintln(os.Stderr, "rowprobe:", err)
		os.Exit(1)
	}
}

func run(proto string, timeout, settle time.Duration) error {
	if proto == "auto" {
		proto = detectProto()
	}
	if proto != "iterm" && proto != "kitty" {
		return fmt.Errorf("unknown protocol %q (want iterm or kitty)", proto)
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open /dev/tty: %w (run this in a terminal, not a pipe)", err)
	}
	defer func() { _ = tty.Close() }()

	fd := int(tty.Fd())
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return fmt.Errorf("terminal size: %w", err)
	}

	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	restore := func() { _ = term.Restore(fd, old) }
	defer restore()
	// Ctrl+C in raw mode would leave the terminal unusable.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		restore()
		os.Exit(130)
	}()

	t := &tt{f: tty, in: startReader(tty), rows: rows, cols: cols, timeout: timeout, settle: settle}

	t.writeStr(fmt.Sprintf("\r\n=== rowprobe: %s, %dx%d cells ===\r\n", proto, cols, rows))
	results := make([]result, 0, len(cases))
	var aborted string
	for _, c := range cases {
		r, fatal := t.measure(c, proto)
		results = append(results, r)
		if fatal != nil {
			aborted = fatal.Error()
			break
		}
	}
	t.writeStr("\r\n")
	// Swallow anything still in flight so late replies do not land on
	// the shell prompt after this process is gone.
	t.drainFor(600 * time.Millisecond)

	restore()
	signal.Stop(sig)
	report(proto, cols, rows, results, aborted, t.sent, t.got)
	if aborted != "" {
		return fmt.Errorf("run aborted: %s", aborted)
	}
	return nil
}

// detectProto guesses from the environment only. A real integration
// must ask the terminal (and must ask BEFORE Bubble Tea owns stdin);
// this is a probe, and a wrong guess here costs a flag, not a bug.
func detectProto() string {
	if strings.Contains(os.Getenv("TERM"), "kitty") || os.Getenv("KITTY_WINDOW_ID") != "" {
		return "kitty"
	}
	if os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return "kitty"
	}
	return "iterm"
}

// tt is the terminal under measurement.
type tt struct {
	f          *os.File
	in         <-chan byte
	rows, cols int
	timeout    time.Duration
	settle     time.Duration
	sent, got  int // cursor reports asked for, and answered in time
}

func (t *tt) writeStr(s string) { _, _ = t.f.WriteString(s) }

// measure draws one case and returns how many rows it cost. A non-nil
// second return means the reply stream can no longer be trusted and
// the run must stop.
func (t *tt) measure(c probeCase, proto string) (result, error) {
	png := imgpayload.TestPNG(c.w, c.h)
	var payload string
	switch proto {
	case "kitty":
		payload = imgpayload.Kitty(png, c.kitty)
	default:
		payload = imgpayload.Iterm(png, c.iterm)
	}
	r := result{c: c, width: ansi.StringWidth(payload), bytes: len(payload)}

	// Reserve room BELOW the cursor so the screen does not scroll while
	// the image is drawn — a scroll during the measurement would make
	// the two cursor positions incomparable.
	need := c.declared + 3
	if c.declared == 0 {
		need = 20
	}
	if need > t.rows-4 {
		need = t.rows - 4
	}
	if need < 4 {
		r.skipped = "terminal too short"
		return r, nil
	}

	t.writeStr(fmt.Sprintf("\r\n[%s] declared=%s\r\n", c.name, declaredStr(c.declared)))
	t.writeStr(strings.Repeat("\n", need))
	t.writeStr(fmt.Sprintf("\x1b[%dA\r", need))

	row1, _, err := t.cpr()
	if err != nil {
		r.skipped = err.Error()
		return r, err
	}
	t.writeStr(c.prefix + payload + c.suffix)
	// Give the terminal time to take the image in. It answers the next
	// query only when its parser reaches it, and on iTerm2 that is well
	// after the write returns.
	time.Sleep(t.settle)
	start := time.Now()
	row2, col2, err := t.cpr()
	if err != nil {
		r.skipped = err.Error()
		return r, err
	}
	r.waited = time.Since(start)

	r.rows = row2 - row1
	r.endCol = col2
	r.consumed = consumedRows(r.rows, r.endCol)
	// At the last row the count is a floor: the screen scrolled and the
	// starting row moved up with it.
	r.scrolled = row2 >= t.rows
	// Step past the reserved block so the next case starts clean.
	t.writeStr(fmt.Sprintf("\x1b[%d;1H", min(row1+need, t.rows)))
	return r, nil
}

// cpr asks the terminal for the cursor position and parses the reply.
// Anything already buffered is discarded first: a reply carries no tag,
// so a stale one would be read as this query's answer.
func (t *tt) cpr() (row, col int, err error) {
	t.drain()
	t.sent++
	t.writeStr("\x1b[6n")
	var buf []byte
	deadline := time.After(t.timeout)
	for {
		select {
		case b, ok := <-t.in:
			if !ok {
				return 0, 0, fmt.Errorf("tty closed while waiting for cursor report")
			}
			buf = append(buf, b)
			if b == 'R' {
				if _, e := fmt.Sscanf(string(buf), "\x1b[%d;%dR", &row, &col); e != nil {
					return 0, 0, fmt.Errorf("unparsable cursor report %q", string(buf))
				}
				t.got++
				return row, col, nil
			}
			if len(buf) > 64 {
				return 0, 0, fmt.Errorf("cursor report did not terminate")
			}
		case <-deadline:
			return 0, 0, fmt.Errorf("no cursor report in %s: the reply stream is out of step and every later number would be misfiled", t.timeout)
		}
	}
}

// drain empties whatever has already arrived, without waiting.
func (t *tt) drain() {
	for {
		select {
		case _, ok := <-t.in:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// drainFor keeps draining for d, to catch replies still in flight.
func (t *tt) drainFor(d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-t.in:
			if !ok {
				return
			}
		case <-deadline:
			return
		}
	}
}

// startReader drains the tty on its own goroutine so a missing reply
// times out instead of blocking, and so a late reply is consumed
// rather than stolen from the next read.
func startReader(f *os.File) <-chan byte {
	ch := make(chan byte, 4096)
	go func() {
		defer close(ch)
		buf := make([]byte, 64)
		for {
			n, err := f.Read(buf)
			for i := 0; i < n; i++ {
				ch <- buf[i]
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// consumedRows turns the measured cursor delta into the rows the image
// occupies. An image leaves the cursor on its own last row (measured on
// iTerm2 3.7.2: the end column is never 1), so that row counts too. A
// cursor left at column 1 advanced to a fresh row instead, which is
// what happens when nothing was drawn at all.
func consumedRows(delta, endCol int) int {
	if endCol > 1 {
		return delta + 1
	}
	return delta
}

func declaredStr(n int) string {
	if n == 0 {
		return "(none)"
	}
	return fmt.Sprintf("%d rows", n)
}

// report prints the table and the verdict. The verdict is narrow on
// purpose: it answers whether a DECLARED height can be trusted, and
// says nothing about how the image looked.
func report(proto string, cols, rows int, rs []result, aborted string, sent, got int) {
	fmt.Printf("rowprobe — %s protocol, terminal %dx%d cells, TERM=%s TERM_PROGRAM=%s %s\n",
		proto, cols, rows, os.Getenv("TERM"), os.Getenv("TERM_PROGRAM"), os.Getenv("TERM_PROGRAM_VERSION"))
	if os.Getenv("TMUX") != "" {
		fmt.Println("NOTE: running inside tmux — passthrough rules apply and the numbers below are tmux's, not the terminal's.")
	}
	fmt.Printf("cursor reports: %d asked, %d answered in time\n", sent, got)
	fmt.Println()
	fmt.Printf("%-34s %-10s %-9s %-9s %-8s %-10s %-9s %s\n", "case", "declared", "occupies", "cursor d", "end col", "ansi width", "cpr wait", "payload")
	fmt.Println(strings.Repeat("-", 112))
	var mismatched, ok, undeclared int
	// Only a payload ALONE on its line can testify that the terminal
	// drew: text beside it advances the cursor past column 1 by itself,
	// so consumedRows credits the line with a row that the text, not the
	// image, occupies (measured — a terminal that drew nothing still
	// reported 1 row for the "text + image + text" case).
	drew := false
	for _, r := range rs {
		if r.consumed > 0 && r.c.prefix == "" && r.c.suffix == "" {
			drew = true
		}
	}
	for _, r := range rs {
		if r.skipped != "" {
			fmt.Printf("%-34s %-10s FAILED: %s\n", trunc(r.c.name, 34), declaredStr(r.c.declared), r.skipped)
			continue
		}
		obs := fmt.Sprintf("%d rows", r.consumed)
		if r.scrolled {
			obs += "+"
		}
		fmt.Printf("%-34s %-10s %-9s %-9d %-8d %-10d %-9s %d B\n",
			trunc(r.c.name, 34), declaredStr(r.c.declared), obs, r.rows, r.endCol, r.width,
			r.waited.Round(time.Millisecond).String(), r.bytes)
		switch {
		case r.c.declared == 0:
			undeclared++
		case r.consumed == r.c.declared && !r.scrolled:
			ok++
		default:
			mismatched++
		}
	}
	fmt.Println()
	fmt.Println("occupies is the row count; cursor d is the raw delta, one less, because the cursor")
	fmt.Println("  stays on the image's last row. '+' means the screen scrolled: the count is a floor.")
	fmt.Println("ansi width is what x/ansi v0.11.6 reports for the payload — the number physicalRows would count.")
	fmt.Println("cpr wait is how long the terminal took to answer AFTER the settle pause.")
	fmt.Println()
	switch {
	case aborted != "":
		fmt.Println("VERDICT: ABORTED — a cursor report never arrived.")
		fmt.Println("  Cases above the failure are sound; nothing below it was attempted, on")
		fmt.Println("  purpose: an unanswered CPR desynchronizes the reply stream and every")
		fmt.Println("  later number would belong to a different case. Raise -timeout and")
		fmt.Println("  -settle and run again.")
	case ok+mismatched == 0:
		fmt.Println("VERDICT: nothing measurable. No case declared a height that could be checked.")
	case !drew:
		fmt.Println("VERDICT: INCONCLUSIVE - this terminal drew nothing.")
		fmt.Println("  Every case cost zero rows, which is what happens when the payload is")
		fmt.Println("  swallowed rather than rendered: the terminal does not implement this")
		fmt.Println("  protocol, or something between here and it (a multiplexer, a remote")
		fmt.Println("  shell) filtered the escape. It says nothing about whether a declared")
		fmt.Println("  height is honoured - rerun in a terminal that draws (iTerm2 for")
		fmt.Println("  -proto iterm, kitty or Ghostty for -proto kitty).")
	case mismatched == 0 && ok > 0:
		fmt.Printf("VERDICT: every declared height was honoured exactly (%d/%d).\n", ok, ok+mismatched)
		fmt.Println("  Row accounting can be closed by DECLARATION: the emitter states the height,")
		fmt.Println("  emit adds it to the count, and the bottom pin stays exact — with no")
		fmt.Println("  dependence on the image's aspect ratio, which the declared box overrides.")
		fmt.Println("  The cursor is left on the image's last row, so a following newline opens")
		fmt.Println("  the next row rather than adding one.")
	default:
		fmt.Printf("VERDICT: %d of %d declared heights were NOT honoured.\n", mismatched, ok+mismatched)
		fmt.Println("  Declaration is not enough on its own — the mismatching rows above say which")
		fmt.Println("  shapes drift, and a count that can drift cannot carry the bottom pin.")
	}
	if undeclared > 0 {
		fmt.Printf("  %d case(s) declared no height; their observed rows are information, not a verdict.\n", undeclared)
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
