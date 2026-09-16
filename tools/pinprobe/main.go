// Command pinprobe measures what rowprobe deliberately excluded: the
// bottom pin, under the condition production actually runs in.
//
// rowprobe reserves rows below the cursor so nothing scrolls while it
// measures — necessary for comparing two cursor positions, and a
// deliberate exclusion of production's condition, because emit prints
// at the BOTTOM of the screen where every line scrolls. An independent
// verification pass (2026-09-16) found ADR-0089 reading rowprobe's
// number as though it covered that case. It does not. This probe
// exists to stop the two being confused again.
//
// Nothing here is simulated. It constructs the REAL model (tui.New),
// runs it under the REAL inline Bubble Tea program, and pushes lines
// through the REAL emit path by sending tui.Output — so
// wrapForScrollback, physicalRows and the bottom-hold accounting are
// the production functions, not copies of them. The footer carries a
// sentinel (ModelName/ProjectDir), so a screen capture can find the pin
// without a human reading it.
//
// OBSERVABILITY, STATED RATHER THAN WORKED AROUND. The recorded rule is
// that a row-arithmetic fix is counted on a real terminal, with tmux
// capture-pane or script, because "lines the model returned" is not
// evidence of what was painted. tmux is the only screen reader here,
// and tmux does not draw inline images: under it the payload is
// swallowed. So this probe measures the ACCOUNTING — what production
// counts an image line as, and whether the pin survives it — and it
// cannot measure a DRAWN image at the bottom of a scrolling screen.
// That case needs a terminal that both draws and can be read back; say
// so rather than reporting the case that happened to be measurable.
//
// Usage (the driver does this):
//
//	tmux new-session -d -x 120 -y 30 'go run ./tools/pinprobe -hold 6s'
//	sleep 3 && tmux capture-pane -p
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nlink-jp/gem-agent/internal/tui"
	"github.com/nlink-jp/gem-agent/tools/imgpayload"
)

// The footer prints ModelName and ProjectDir every frame, so they are
// the pin's address on a captured screen. Values no ordinary output
// could contain.
const (
	sentinelModel = "PINPROBE~MODEL~SENTINEL"
	sentinelDir   = "/PINPROBE~DIR~SENTINEL"
)

func main() {
	hold := flag.Duration("hold", 6*time.Second, "keep the UI up this long after the last line, so a capture can be taken")
	fill := flag.Int("fill", 60, "plain lines printed first, to push the view to the bottom so every later print scrolls")
	report := flag.String("report", "", "write the machine-readable summary here (default: stderr after the UI exits)")
	only := flag.String("only", "", "run one case by name (PLAIN, ITERM-H6, ITERM-H12, KITTY-R6, SIXEL-6); empty runs all")
	repeat := flag.Int("repeat", 1, "print the selected case this many times, so an undercount has somewhere to accumulate")
	flag.Parse()

	lines, notes := cases(*only)
	if *repeat > 1 {
		one := lines
		lines = nil
		for i := 0; i < *repeat; i++ {
			lines = append(lines, one...)
		}
		notes = append(notes, fmt.Sprintf("%-10s repeated %d times", "", *repeat))
	}

	model := tui.New(tui.Options{
		Theme:      "notty", // no colors: a capture is compared as text
		ModelName:  sentinelModel,
		ProjectDir: sentinelDir,
	})
	prog := tea.NewProgram(model)

	go func() {
		// Let the first frame paint before anything is pushed.
		time.Sleep(700 * time.Millisecond)
		filler := make([]string, 0, *fill)
		for i := 1; i <= *fill; i++ {
			filler = append(filler, fmt.Sprintf("FILL %03d — pushing the view to the bottom so every later print scrolls", i))
		}
		prog.Send(tui.Output{Lines: filler})
		time.Sleep(400 * time.Millisecond)
		for _, l := range lines {
			prog.Send(tui.Output{Lines: []string{l}})
			time.Sleep(250 * time.Millisecond)
		}
		time.Sleep(*hold)
		prog.Quit()
	}()

	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "pinprobe:", err)
		os.Exit(1)
	}

	out := summary(notes)
	if *report != "" {
		if err := os.WriteFile(*report, []byte(out), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "pinprobe:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprint(os.Stderr, out)
}

// cases returns the lines to push and a note per line. Each payload is
// bracketed by its own marker lines, so a capture can count the rows
// the payload actually took without trusting anything this tool says.
func cases(only string) (lines []string, notes []string) {
	png := imgpayload.TestPNG(320, 180)
	add := func(name, payload, note string) {
		if only != "" && only != name {
			return
		}
		lines = append(lines, "MARK-"+name+"-BEGIN")
		lines = append(lines, payload)
		lines = append(lines, "MARK-"+name+"-END")
		notes = append(notes, fmt.Sprintf("%-10s %s", name, note))
	}
	add("PLAIN", "a plain line of text, the control",
		"control: one row of text, no escape")
	add("ITERM-H6", imgpayload.Iterm(png, "width=40;height=6;preserveAspectRatio=1"),
		"iTerm2 OSC 1337, declares 6 rows")
	add("ITERM-H12", imgpayload.Iterm(png, "width=40;height=12;preserveAspectRatio=1"),
		"iTerm2 OSC 1337, declares 12 rows")
	add("KITTY-R6", imgpayload.Kitty(png, "f=100,r=6,c=40"),
		"kitty APC _G, declares 6 rows, chunked")
	add("SIXEL-6", imgpayload.Sixel(6),
		"sixel DCS q, 6 bands (36px tall), declares nothing")
	add("SIXEL-12", imgpayload.Sixel(12),
		"sixel DCS q, 12 bands (72px tall), declares nothing")
	add("SIXEL-24", imgpayload.Sixel(24),
		"sixel DCS q, 24 bands (144px tall), declares nothing")
	return lines, notes
}

func summary(notes []string) string {
	var b strings.Builder
	b.WriteString("pinprobe — lines pushed through the real emit path (tui.Output)\n\n")
	for _, n := range notes {
		b.WriteString("  " + n + "\n")
	}
	b.WriteString("\nEach payload is bracketed by MARK-<name>-BEGIN / MARK-<name>-END.\n")
	b.WriteString("On a captured screen the rows BETWEEN a pair are what that payload\n")
	b.WriteString("occupied. Production counts an image line as physicalRows makes it,\n")
	b.WriteString("which floors at 1, so a drawn image of N rows leaves the accounting\n")
	b.WriteString("short by N-1. What that COSTS is not the same everywhere, and the\n")
	b.WriteString("first draft of this text asserted a drift the runs then contradicted:\n")
	b.WriteString("  tmux 3.7c, sixel, screen already full: one frame stranded per\n")
	b.WriteString("    image (3 repeats -> 3), pin above the last output; the PLAIN\n")
	b.WriteString("    control at the same fill is clean.\n")
	b.WriteString("  tmux, same payload, screen NOT yet full: no damage at all.\n")
	b.WriteString("  iTerm2 3.7.2, OSC 1337, screen full, 1 image and 5: nothing\n")
	b.WriteString("    moves — same gap as the no-image control, no frame stranded.\n")
	b.WriteString("So measure the regime you mean. Run the control at the same -fill.\n")
	b.WriteString("\nThe footer carries " + sentinelModel + ". Exactly one occurrence on the\n")
	b.WriteString("captured screen means the frame was painted once; more than one means\n")
	b.WriteString("a frame leaked, which is what a wrong row count looks like.\n")
	return b.String()
}
