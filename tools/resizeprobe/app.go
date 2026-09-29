package main

// -app: the built binary itself, driven like an operator would drive it.
//
// Every other mode runs the TUI model inside this probe. That is the
// product's model, renderer and writer, but not its wiring: cmd builds the
// program, hands the model its writer and chooses the output. The operator
// asked for the binaries themselves to be tested the same way, with nobody
// at the keyboard (2026-09-29), rather than by hand and a report.
//
// The driver opens a new window of the terminal running the binary in a
// scratch project with an isolated config (no MCP servers, a model that is
// never called), and types into it through the terminal's own interface:
// `!` commands print the numbered lines and the markers the analyzer reads,
// `/show` draws the pictures, a draft sits in the input box while the window
// is narrowed — by the terminal's interface or by a mouse drag — and widened
// back. The footer carries the model name, so a stale copy of the frame is
// counted by the same sentinel the probe's own runs use. The binary's echo
// of what was typed ("! …", "> …") is not history and is skipped.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/nlink-jp/gem-agent/tools/imgpayload"
)

func ansiWidth(s string) int { return ansi.StringWidth(s) }

// appConfig is the isolated config each binary starts with: nothing of the
// operator's settings, no MCP servers, a model name that is the footer
// sentinel and is never called.
func appConfig(flavor string) (string, error) {
	switch flavor {
	case "gem-agent":
		return `[gcp]
project = "resizeprobe-placeholder"
location = "global"
[model]
name = "` + sentinelModel + `"
[mcp]
enabled = false
[telemetry]
enabled = false
[tui]
theme = "` + theme + `"
`, nil
	case "lagent":
		return `[llm]
provider = "openai"
base_url = "http://127.0.0.1:9/v1"
model = "` + sentinelModel + `"
[model]
context_window = 32768
[mcp]
enabled = false
[tui]
theme = "` + theme + `"
`, nil
	}
	return "", fmt.Errorf("-app %q: gem-agent or lagent", flavor)
}

// seriesScript prints one numbered series — every fifth line as wide as
// the caller asks — from a file in the project, so the `!` line the binary
// echoes stays one row: a long echo wraps, and its tail is not history.
const seriesScript = `p=$1; n=$2; t=$3; i=1
while [ $i -le $n ]; do
  if [ $((i%5)) -eq 0 ]; then
    printf '%s%03d' $p $i; j=0
    while [ $j -lt $t ]; do printf ' ~~~~'; j=$((j+1)); done; printf '\n'
  else printf '%s%03d history\n' $p $i; fi
  i=$((i+1))
done
`

// seriesCommand is the `!` line printing one series, every fifth line as
// wide as fits a window cols wide.
func seriesCommand(prefix string, count, cols int) string {
	tildes := (cols - 8) / 5
	if tildes < 1 {
		tildes = 1
	}
	return fmt.Sprintf("!sh series.sh %s %d %d", prefix, count, tildes)
}

// hasLine reports whether any line of the terminal's text starts with p —
// the binary's output, not its echo of the command that printed it.
func hasLine(text, p string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(l, " "), p) {
			return true
		}
	}
	return false
}

func runApp(name, flavor, bin, shrink string, pace, drag time.Duration, dragTo float64, cols, rows int, out string) error {
	cfgText, err := appConfig(flavor)
	if err != nil {
		return err
	}
	if draftLen != "long" && draftLen != "short" {
		return fmt.Errorf("-draft %q: long or short", draftLen)
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return err
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("-bin: %w (build it with make build)", err)
	}
	plan, err := parseSteps(shrink)
	if err != nil {
		return err
	}
	if len(plan) == 0 && drag == 0 {
		plan = []step{{offset: cols * 2 / 3}}
	}
	ver, _ := run(bin, "--version").Output()
	dir := absOr(filepath.Join(out, fmt.Sprintf("%s-app-%s-%s", name, flavor, time.Now().Format("150405"))))
	proj := filepath.Join(dir, "project")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		return err
	}
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte(cfgText), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(proj, "pic.png"), imgpayload.TestPNG(320, 180), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(proj, "series.sh"), []byte(seriesScript), 0o644); err != nil {
		return err
	}
	term, err := newTerminal(name)
	if err != nil {
		return err
	}
	command := []string{"/bin/sh", "-c", fmt.Sprintf("cd %s && exec %s --config %s --mcp off",
		shQuote(proj), shQuote(bin), shQuote(cfg))}
	if err := term.open(command, cols, rows); err != nil {
		return err
	}
	defer term.close()
	fmt.Printf("resizeprobe -auto %s -app %s — %s, %dx%d, results in %s\n", name, flavor,
		strings.TrimSpace(string(ver)), cols, rows, dir)

	shot := func(label string) {
		id, err := term.windowID()
		if err == nil {
			err = run("screencapture", "-x", "-o", "-l", strconv.Itoa(id), filepath.Join(dir, label+".png")).Run()
		}
		if err != nil {
			fmt.Printf("  screenshot %s failed: %v\n", label, err)
		}
	}
	wait := func(line string, limit time.Duration) (string, error) {
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			if s, err := term.text(); err == nil && hasLine(s, line) {
				return s, nil
			}
			time.Sleep(300 * time.Millisecond)
		}
		return "", fmt.Errorf("%q did not appear within %s", line, limit)
	}
	do := func(input, until string) error {
		if err := term.send(input + "\r"); err != nil {
			return fmt.Errorf("send %q: %w", input, err)
		}
		_, err := wait(until, 60*time.Second)
		return err
	}

	if _, err := wait(sentinelModel, 30*time.Second); err != nil {
		return fmt.Errorf("the binary did not come up: %w", err)
	}
	h := rows
	printed := map[string]int{"H": h + 10, "P": h, "A": 2 * h, "B": 2 * h}
	steps := []struct{ in, until string }{
		{fmt.Sprintf("!echo 'PROBE-START arm=app-%s proto=%s size=%dx%d'", flavor, name, cols, rows), "PROBE-START"},
		{seriesCommand("H", printed["H"], cols), fmt.Sprintf("H%03d", printed["H"])},
		{"!echo 'PIC-1 BEGIN (pushed into the scrollback before the narrowing)'", "PIC-1 BEGIN"},
		{"/show pic.png", "> /show"},
		{"!echo 'PIC-1 END'", "PIC-1 END"},
		{seriesCommand("P", printed["P"], cols), fmt.Sprintf("P%03d", printed["P"])},
		{"!echo 'PIC-2 BEGIN (on the screen when the window narrows)'", "PIC-2 BEGIN"},
		{"/show pic.png", "PIC-2 BEGIN"},
		{"!echo 'PIC-2 END'", "PIC-2 END"},
		{"!echo 'RESIZE-1: the driver narrows the window'", "RESIZE-1"},
	}
	for _, st := range steps {
		if err := do(st.in, st.until); err != nil {
			return err
		}
	}
	draft := sentinelDraft + " a short draft"
	if draftLen == "long" {
		draft = draftText(cols)
	}
	if err := term.send(draft); err != nil {
		return err
	}
	time.Sleep(time.Second)
	shot("1-before")

	if drag > 0 {
		id, err := term.windowID()
		if err != nil {
			return err
		}
		res, err := run("swift", "tools/resizeprobe/drag.swift", strconv.Itoa(id),
			strconv.FormatFloat(dragTo, 'f', 3, 64), strconv.Itoa(int(drag.Milliseconds()))).CombinedOutput()
		if err != nil {
			return fmt.Errorf("mouse drag (run from the repository root): %v: %s", err, strings.TrimSpace(string(res)))
		}
		fmt.Printf("  dragged the right edge to %.2f of the width over %s: %s\n", dragTo, drag, strings.TrimSpace(string(res)))
	}
	narrowest := cols
	for _, st := range plan {
		w := st.resolve([]int{ansiWidth(draft) + 2})
		if err := term.resize(w); err != nil {
			return fmt.Errorf("resize to %d: %w", w, err)
		}
		if w < narrowest {
			narrowest = w
		}
		time.Sleep(pace)
	}
	time.Sleep(1500 * time.Millisecond)
	shot("2-after-narrowing")

	// The draft goes, then the history goes on at the narrower width.
	if err := term.send("\x15"); err != nil {
		return err
	}
	for _, st := range []struct{ in, until string }{
		{"!echo AFTER-SHRINK", "AFTER-SHRINK"},
		{seriesCommand("A", printed["A"], narrowest), fmt.Sprintf("A%03d", printed["A"])},
		{"!echo 'RESIZE-2: the driver widens the window'", "RESIZE-2"},
	} {
		if err := do(st.in, st.until); err != nil {
			return err
		}
	}
	if err := term.resize(cols); err != nil {
		return fmt.Errorf("resize back to %d: %w", cols, err)
	}
	time.Sleep(2500 * time.Millisecond)
	shot("3-after-widening")
	for _, st := range []struct{ in, until string }{
		{"!echo AFTER-GROW", "AFTER-GROW"},
		{seriesCommand("B", printed["B"], cols), fmt.Sprintf("B%03d", printed["B"])},
		{fmt.Sprintf("!echo 'PROBE-END printed H=%d P=%d A=%d B=%d'", printed["H"], printed["P"], printed["A"], printed["B"]), "PROBE-END"},
	} {
		if err := do(st.in, st.until); err != nil {
			return err
		}
	}
	shot("4-end")
	s, err := term.text()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "text.txt"), []byte(s), 0o644); err != nil {
		return err
	}
	_ = term.send("\x03\x03") // an empty prompt's Ctrl+C quits
	return printReport(os.Stdout, analyze(strings.Split(s, "\n")))
}
