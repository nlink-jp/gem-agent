package main

// -auto: the whole run in a real terminal with nobody at the keyboard.
//
// The operator asked for it (2026-09-29) after the first rounds needed a
// drag at the right moment, a screenshot within 1.5 seconds and a copy of
// the tab: a measurement that depends on a person's timing measures the
// person as much as the terminal, and one run of kitty narrowed past its
// target and back. Here the driver opens a NEW window of the terminal,
// runs the probe in it, resizes the window to exact widths, captures the
// window at fixed moments, reads its whole text including the scrollback,
// and analyzes it. Nothing touches the operator's own windows.
//
// Each terminal is driven through its own documented interface:
//
//   - kitty: a new instance with remote control enabled on the command
//     line, for that instance only (-o allow_remote_control=yes and a
//     private socket), then `kitten @ resize-os-window --unit cells`,
//     `get-text --extent all` and `ls` for the window id.
//   - iTerm2: AppleScript — `create window with default profile command`,
//     a session's `columns` and `contents` (which holds the scrollback:
//     measured, 300 lines back), and the window's id, which is the one
//     screencapture takes.
//
// The first AppleScript call asks macOS for permission to control iTerm2,
// and screencapture needs Screen Recording for whatever runs this; both
// are the operator's to grant.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// terminal is one of the two terminals ADR-0094 decides for.
type terminal interface {
	open(command []string, cols, rows int) error
	resize(cols int) error
	text() (string, error)
	windowID() (int, error)
	close()
}

func newTerminal(name string) (terminal, error) {
	switch name {
	case "kitty":
		return &kittyTerm{}, nil
	case "iterm2":
		return &itermTerm{}, nil
	}
	return nil, fmt.Errorf("-auto: %q is not a terminal this drives (kitty, iterm2)", name)
}

// --- kitty -------------------------------------------------------------

const kittyApp = "/Applications/kitty.app/Contents/MacOS/"

type kittyTerm struct {
	sock string
	proc *exec.Cmd
}

func (k *kittyTerm) open(command []string, cols, rows int) error {
	// A unix socket path is limited to about 104 bytes; the user temp dir
	// is short enough, a scratch directory under it often is not.
	tmp, err := run("getconf", "DARWIN_USER_TEMP_DIR").Output()
	if err != nil {
		return fmt.Errorf("user temp dir: %w", err)
	}
	k.sock = strings.TrimSpace(string(tmp)) + fmt.Sprintf("rpk-%d.sock", os.Getpid())
	args := append([]string{
		"-o", "allow_remote_control=yes", "--listen-on", "unix:" + k.sock,
		"-o", "remember_window_size=no",
		"-o", fmt.Sprintf("initial_window_width=%dc", cols),
		"-o", fmt.Sprintf("initial_window_height=%dc", rows),
	}, command...)
	k.proc = run(kittyApp+"kitty", args...)
	if err := k.proc.Start(); err != nil {
		return fmt.Errorf("start kitty: %w", err)
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(k.sock); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("kitty did not open its control socket %s", k.sock)
}

func (k *kittyTerm) remote(args ...string) ([]byte, error) {
	return run(kittyApp+"kitten", append([]string{"@", "--to", "unix:" + k.sock}, args...)...).Output()
}

func (k *kittyTerm) resize(cols int) error {
	_, err := k.remote("resize-os-window", "--width", strconv.Itoa(cols), "--unit", "cells")
	return err
}

func (k *kittyTerm) text() (string, error) {
	out, err := k.remote("get-text", "--extent", "all")
	return string(out), err
}

func (k *kittyTerm) windowID() (int, error) {
	out, err := k.remote("ls")
	if err != nil {
		return 0, err
	}
	var ls []struct {
		PlatformWindowID int `json:"platform_window_id"`
	}
	if err := json.Unmarshal(out, &ls); err != nil || len(ls) == 0 {
		return 0, fmt.Errorf("kitty ls: %v", err)
	}
	return ls[0].PlatformWindowID, nil
}

func (k *kittyTerm) close() {
	if k.proc != nil && k.proc.Process != nil {
		_ = k.proc.Process.Kill()
		_ = k.proc.Wait()
	}
	_ = os.Remove(k.sock)
}

// --- iTerm2 ------------------------------------------------------------

type itermTerm struct{ id int }

func osascript(script string) (string, error) {
	out, err := run("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("osascript: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// asQuote makes s an AppleScript string literal.
func asQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// shQuote makes s one word for the shell iTerm2 hands its command to.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (t *itermTerm) open(command []string, cols, rows int) error {
	words := make([]string, len(command))
	for i, w := range command {
		words[i] = shQuote(w)
	}
	out, err := osascript(fmt.Sprintf(`tell application "iTerm2"
	set w to (create window with default profile command %s)
	tell current session of current tab of w
		set columns to %d
		set rows to %d
	end tell
	return id of w
end tell`, asQuote("/bin/sh -c "+shQuote(strings.Join(words, " "))), cols, rows))
	if err != nil {
		return err
	}
	t.id, err = strconv.Atoi(strings.TrimSpace(out))
	return err
}

func (t *itermTerm) session() string {
	return fmt.Sprintf(`tell application "iTerm2" to tell current session of current tab of window id %d to `, t.id)
}

func (t *itermTerm) resize(cols int) error {
	_, err := osascript(t.session() + fmt.Sprintf("set columns to %d", cols))
	return err
}

func (t *itermTerm) text() (string, error) { return osascript(t.session() + "get contents") }

func (t *itermTerm) windowID() (int, error) { return t.id, nil }

func (t *itermTerm) close() {
	_, _ = osascript(fmt.Sprintf(`tell application "iTerm2" to close window id %d`, t.id))
}

// --- the run ------------------------------------------------------------

var widestMark = regexp.MustCompile(`widest frame line (\d+)`)

// runAuto drives one arm in a new window of the named terminal and writes
// what it measured under out.
// pace is the pause between the driver's narrowing steps. A second apart
// the probe sees each step settle; tens of milliseconds apart is a drag,
// where the terminal is already at the next width while the program is
// still painting for the last.
//
// drag > 0 narrows with the MOUSE instead (drag.swift): the window's right
// edge pulled in to dragTo of its width over that long, so the terminal
// reflows continuously as it does under a hand.
func runAuto(name string, arm string, shrink string, pace, drag, coalesce time.Duration, dragTo float64, cols, rows int, out string) error {
	// Everything the window's probe would refuse is refused here, before a
	// window opens: a probe that stops on a bad argument inside the window
	// left the driver waiting two minutes for a marker (2026-09-29, an arm
	// passed as "erase short").
	if _, err := parseArm(arm); err != nil {
		return err
	}
	if theme != "notty" && theme != "dark" && theme != "light" {
		return fmt.Errorf("-theme %q: notty, dark or light", theme)
	}
	if draftLen != "long" && draftLen != "short" {
		return fmt.Errorf("-draft %q: long or short", draftLen)
	}
	plan, err := parseSteps(shrink)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		plan = []step{{offset: cols * 2 / 3}}
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	dir := filepath.Join(out, fmt.Sprintf("%s-%s-%s", name, arm, time.Now().Format("150405")))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	term, err := newTerminal(name)
	if err != nil {
		return err
	}
	// -settle outlasts the pause between the driver's steps, so the probe
	// sees one narrowing however many steps it takes; -linger keeps the
	// window, and the report printed after the program exits, readable.
	command := []string{self, "-arm", arm, "-yes", "-wait", "60s", "-settle", "4s",
		"-hold", "1s", "-linger", "120s", "-trace", filepath.Join(absOr(dir), "trace.txt"),
		"-coalesce", coalesce.String(), "-theme", theme, "-draft", draftLen}
	if err := term.open(command, cols, rows); err != nil {
		return err
	}
	defer term.close()

	shot := func(label string) {
		id, err := term.windowID()
		if err == nil {
			path := filepath.Join(dir, label+".png")
			err = run("screencapture", "-x", "-o", "-l", strconv.Itoa(id), path).Run()
			if err == nil {
				fmt.Println("  screenshot:", path)
				return
			}
		}
		fmt.Printf("  screenshot %s failed: %v\n", label, err)
	}
	waitText := func(marker string, limit time.Duration) (string, error) {
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			s, err := term.text()
			if err == nil && strings.Contains(s, marker) {
				return s, nil
			}
			// The probe in the window stopped: say why, now.
			if i := strings.Index(s, "resizeprobe: "); err == nil && i >= 0 {
				line, _, _ := strings.Cut(s[i:], "\n")
				return "", fmt.Errorf("the probe in the window stopped: %s", strings.TrimSpace(line))
			}
			time.Sleep(300 * time.Millisecond)
		}
		return "", fmt.Errorf("%s did not appear within %s", marker, limit)
	}

	fmt.Printf("resizeprobe -auto %s — arm %s, %dx%d, results in %s\n", name, arm, cols, rows, dir)
	s, err := waitText("RESIZE-1", 120*time.Second)
	if err != nil {
		return err
	}
	widest := 0
	if m := widestMark.FindStringSubmatch(s); m != nil {
		widest, _ = strconv.Atoi(m[1])
	}
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
		plan = nil
	}
	var targets []int
	for _, st := range plan {
		w := st.resolve([]int{widest})
		targets = append(targets, w)
		if err := term.resize(w); err != nil {
			return fmt.Errorf("resize to %d: %w", w, err)
		}
		time.Sleep(pace)
	}
	fmt.Printf("  widest frame line %d; narrowed through %v\n", widest, targets)
	time.Sleep(1500 * time.Millisecond) // inside the probe's 4 s settle: the screen the narrowing left
	shot("2-after-narrowing")

	if _, err := waitText("RESIZE-2", 60*time.Second); err != nil {
		return err
	}
	if err := term.resize(cols); err != nil {
		return fmt.Errorf("resize back to %d: %w", cols, err)
	}
	time.Sleep(2500 * time.Millisecond)
	shot("3-after-widening")

	s, err = waitText("RESIZEPROBE-META", 90*time.Second)
	if err != nil {
		return err
	}
	shot("4-end")
	if err := os.WriteFile(filepath.Join(dir, "text.txt"), []byte(s), 0o644); err != nil {
		return err
	}
	lines := strings.Split(s, "\n")
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "RESIZEPROBE-") || strings.Contains(l, "  width ") && strings.Contains(l, "  K ") {
			fmt.Println(" ", strings.TrimRight(l, " "))
		}
	}
	return printReport(os.Stdout, analyze(lines))
}

// absOr makes a path absolute for a child whose working directory is the
// terminal's, not ours; on failure it returns the path as given.
func absOr(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
