// Command escprobe measures what an escape sequence in text from outside
// the runtime does to a real terminal once it reaches the TUI (ADR-0093).
//
// Nothing is simulated on the UI side. -ui constructs the REAL model
// (tui.New, the production glamour renderer for the dark theme), runs it
// under the REAL inline Bubble Tea program, starts a turn the way a typed
// line does, and delivers one hostile string on one channel — a streamed
// reply chunk, a live thought, a tool event's detail — exactly as cmd
// sends it — or as the argument an approval dialog shows. -drive runs every case on every channel in its own tmux
// server and reads back what the terminal did, from the terminal: the
// pane title (OSC 0/2), the paste buffers (OSC 52, with set-clipboard on),
// where a marker landed on the captured screen (cursor movement, CR, BS),
// and whether the sequence's body is visible at all — a body the screen
// does not show is one the terminal parsed as a control.
//
// The tmux server is a private one (-L escprobe) with no client attached,
// so an OSC 52 the probe provokes is stored in that server's buffers and
// forwarded nowhere; the operator's own tmux and clipboard are not touched.
//
// What this cannot do: tmux parses kitty APC _G, iTerm2 OSC 1337 and DCS
// and draws none of them, so for those "consumed" means "a terminal
// received a control it would act on", not "a picture was drawn".
//
// Usage:
//
//	go run ./tools/escprobe -drive                     # every case, every channel
//	go run ./tools/escprobe -ui -case OSC0 -channel reply   # one run, to watch
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/tui"
)

const (
	socket   = "escprobe"
	session  = "escprobe"
	pwn      = "ESCPWN"
	pwnB64   = "RVNDUFdO" // base64("ESCPWN")
	readyTag = "ESCPROBE-READY"
)

// probeCase is one hostile string and the reading that says whether the
// terminal acted on it. body is text inside the sequence that a terminal
// which parsed the sequence does not print: seeing it on screen means the
// sequence was shown, not obeyed.
type probeCase struct {
	name    string
	payload string
	body    string
	effect  func(r reading) (acted bool, what string)
}

type reading struct {
	title   string
	buffers string
	screen  []string
}

func has(lines []string, s string) (row, col int, ok bool) {
	for i, l := range lines {
		if j := strings.Index(l, s); j >= 0 {
			return i, len([]rune(l[:j])), true
		}
	}
	return 0, 0, false
}

func consumed(body string) func(reading) (bool, string) {
	return func(r reading) (bool, string) {
		if _, _, ok := has(r.screen, body); ok {
			return false, "body shown as text"
		}
		return true, "parsed as a control (body not on screen)"
	}
}

// titled reads the pane title. Only whether it carries the probe's marker
// is printed: an untouched title is the machine's host name, which does
// not belong in a record.
func titled(r reading) (bool, string) {
	if strings.Contains(r.title, pwn) {
		return true, fmt.Sprintf("pane title set to %q", r.title)
	}
	return false, "pane title untouched"
}

func cases() []probeCase {
	moved := func(mark string) func(reading) (bool, string) {
		return func(r reading) (bool, string) {
			row, col, ok := has(r.screen, mark)
			if !ok {
				return true, mark + " not on screen"
			}
			if row == 2 && col == 59 {
				return true, fmt.Sprintf("cursor moved: %s at row %d col %d", mark, row+1, col+1)
			}
			return false, fmt.Sprintf("%s in the text flow (row %d col %d)", mark, row+1, col+1)
		}
	}
	return []probeCase{
		{"OSC0", "\x1b]0;" + pwn + "\a", "]0;", titled},
		{"OSC2-ST", "\x1b]2;" + pwn + "\x1b\\", "]2;", titled},
		{"OSC52", "\x1b]52;c;" + pwnB64 + "\a", "]52;", func(r reading) (bool, string) {
			return strings.Contains(r.buffers, pwn), "paste buffers " + fmt.Sprintf("%q", clip(r.buffers))
		}},
		{"C1-OSC", "\u009d0;" + pwn + "\u009c", "0;" + pwn, titled},
		{"C1-OSC-8bit", "\x9d0;" + pwn + "\x9c", "0;" + pwn, titled},
		{"CSI-CUP", "\x1b[3;60HCUPMARK", "", moved("CUPMARK")},
		{"C1-CSI-CUP", "\u009b3;60HC1MARK", "", moved("C1MARK")},
		{"C1-CSI-8bit", "\x9b3;60HC8MARK", "", moved("C8MARK")},
		{"CSI-ED2", "\x1b[2J", "[2J", func(r reading) (bool, string) {
			if _, _, ok := has(r.screen, readyTag); ok {
				return false, "screen above the reply intact"
			}
			return true, "screen above the reply erased"
		}},
		// The payload rides inside "before … after", so a CR returns to
		// the start of "before" and a BS steps back into "abcdef".
		{"CR", "abcdef\rXY", "", func(r reading) (bool, string) {
			if _, _, ok := has(r.screen, "before abcdef"); ok {
				return false, "no overwrite"
			}
			// Bubble Tea erases to the end of the line after writing it,
			// so what the CR returned over is gone, not overwritten.
			return true, "text before the CR hidden"
		}},
		{"BS", "abcdef\b\b\bXY", "", func(r reading) (bool, string) {
			if _, _, ok := has(r.screen, "abcXY after"); ok {
				return true, "overwrote backwards (abcXY after)"
			}
			return false, "no overwrite"
		}},
		// An OSC that is never terminated: the terminal stays in the
		// string state and what follows is not drawn. The approval
		// channel is the one this matters on — the rest of a command.
		{"OSC-open", "\x1b]0; ; rm -rf HIDDENMARK", "", func(r reading) (bool, string) {
			if _, _, ok := has(r.screen, "HIDDENMARK"); ok {
				return false, "the text after it is shown"
			}
			return true, "the text after it is not on screen"
		}},
		// Character references: text with no control in it, which the
		// Markdown renderer decodes into one after the ingress has seen it.
		// Only the flushed reply renders Markdown.
		{"ENT-OSC0", "&#27;]0;" + pwn + "&#7;", "", titled},
		{"ENT-OSC52", "&#x1b;]52;c;" + pwnB64 + "&#x07;", "", func(r reading) (bool, string) {
			return strings.Contains(r.buffers, pwn), "paste buffers " + fmt.Sprintf("%q", clip(r.buffers))
		}},
		{"ENT-CR", "abcdef&#13;XY", "", func(r reading) (bool, string) {
			if _, _, ok := has(r.screen, "before abcdef"); ok {
				return false, "no overwrite"
			}
			return true, "text before the CR hidden"
		}},
		{"OSC8", "\x1b]8;;https://example.invalid/\x1b\\LINKTEXT\x1b]8;;\x1b\\", "]8;;", consumed("]8;;")},
		{"OSC1337", "\x1b]1337;File=inline=1:QUFBQQ==\a", "1337;File", consumed("1337;File")},
		{"APC-kitty", "\x1b_Gf=100,a=T;QUFBQQ==\x1b\\", "_Gf=100", consumed("Gf=100")},
		{"DCS", "\x1bPq#0;2;0;0;0\x1b\\", "q#0;2", consumed("q#0;2")},
	}
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

var channels = []string{"live", "reply", "thought", "tool", "approval"}

func main() {
	drive := flag.Bool("drive", false, "run every case on every channel under a private tmux server and print what the terminal did")
	ui := flag.Bool("ui", false, "run one case in this terminal")
	caseName := flag.String("case", "OSC0", "case name for -ui")
	channel := flag.String("channel", "reply", strings.Join(channels, " | "))
	hold := flag.Duration("hold", 3*time.Second, "keep the UI up this long after the payload")
	flag.Parse()
	switch {
	case *drive:
		if err := runDriver(); err != nil {
			fmt.Fprintln(os.Stderr, "escprobe:", err)
			os.Exit(1)
		}
	case *ui:
		if err := runUI(*caseName, *channel, *hold); err != nil {
			fmt.Fprintln(os.Stderr, "escprobe:", err)
			os.Exit(1)
		}
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func find(name string) (probeCase, bool) {
	for _, c := range cases() {
		if c.name == name {
			return c, true
		}
	}
	return probeCase{}, false
}

// runUI delivers the payload on one channel, through a turn the UI
// believes is running — the phase every one of these messages arrives in.
func runUI(name, channel string, hold time.Duration) error {
	c, ok := find(name)
	if !ok {
		return fmt.Errorf("no case %q", name)
	}
	var prog *tea.Program
	start := make(chan struct{}, 1)
	model := tui.New(tui.Options{
		Theme:        "dark",
		ModelName:    "escprobe",
		ProjectDir:   "/escprobe",
		InitialInput: "go",
		StartTurn:    func(context.Context, string) { start <- struct{}{} },
	})
	prog = tea.NewProgram(model)
	go func() {
		<-start
		prog.Send(tui.Output{Lines: []string{readyTag}})
		time.Sleep(300 * time.Millisecond)
		text := "before " + c.payload + " after"
		switch channel {
		case "live", "reply":
			prog.Send(tui.TextDelta(text))
		case "thought":
			prog.Send(tui.StreamUpdate{Kind: "thought", Thought: text})
		case "tool":
			prog.Send(tui.ToolCall{Name: "shell_exec", Detail: text})
		case "approval":
			// Left unanswered: the dialog is what is on screen.
			prog.Send(tui.ApprovalRequest{Tool: "shell_exec", Detail: text,
				Resp: make(chan tui.ApprovalAnswer, 1)})
		}
		time.Sleep(500 * time.Millisecond)
		if channel == "reply" {
			prog.Send(tui.TurnDone{})
		}
		time.Sleep(hold)
		prog.Quit()
	}()
	_, err := prog.Run()
	return err
}

func tmux(args ...string) *exec.Cmd {
	cmd := exec.Command("tmux", append([]string{"-L", socket}, args...)...)
	cmd.Env = sandbox.ChildEnv(os.Environ())
	return cmd
}

func runDriver() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux is the screen reader and is not installed: %w", err)
	}
	fmt.Println("escprobe -drive — private tmux server, 120x30, set-clipboard on")
	fmt.Printf("%-11s %-8s %-6s %s\n", "case", "channel", "acted", "reading")
	fmt.Println(strings.Repeat("-", 80))
	actedTotal := 0
	for _, c := range cases() {
		for _, ch := range channels {
			r, err := oneRun(self, c.name, ch)
			if err != nil {
				fmt.Printf("%-11s %-8s FAILED: %v\n", c.name, ch, err)
				continue
			}
			acted, what := c.effect(r)
			mark := "no"
			if acted {
				mark = "YES"
				actedTotal++
			}
			fmt.Printf("%-11s %-8s %-6s %s\n", c.name, ch, mark, what)
		}
	}
	fmt.Println()
	fmt.Printf("%d of %d deliveries reached the terminal as a control it acted on.\n",
		actedTotal, len(cases())*len(channels))
	return nil
}

func oneRun(self, name, channel string) (reading, error) {
	_ = tmux("kill-server").Run()
	defer func() { _ = tmux("kill-server").Run() }()
	// start-server first so the option exists before the pane's program
	// writes anything.
	cmd := fmt.Sprintf("%s -ui -case %s -channel %s -hold 3s; sleep 5", self, name, channel)
	if err := tmux("new-session", "-d", "-s", session, "-x", "120", "-y", "30", "sh", "-c", "sleep 0.3; "+cmd).Run(); err != nil {
		return reading{}, fmt.Errorf("new-session: %w", err)
	}
	if err := tmux("set-option", "-g", "set-clipboard", "on").Run(); err != nil {
		return reading{}, fmt.Errorf("set-clipboard: %w", err)
	}
	time.Sleep(3500 * time.Millisecond)
	title, _ := tmux("display-message", "-p", "-t", session, "#{pane_title}").Output()
	var bufs strings.Builder
	if names, err := tmux("list-buffers", "-F", "#{buffer_name}").Output(); err == nil {
		for _, n := range strings.Fields(string(names)) {
			b, _ := tmux("show-buffer", "-b", n).Output()
			bufs.Write(b)
		}
	}
	screen, err := tmux("capture-pane", "-p", "-t", session).Output()
	if err != nil {
		return reading{}, fmt.Errorf("capture-pane: %w", err)
	}
	return reading{
		title:   strings.TrimSpace(string(title)),
		buffers: bufs.String(),
		screen:  strings.Split(string(screen), "\n"),
	}, nil
}
