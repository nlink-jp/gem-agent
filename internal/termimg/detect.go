package termimg

import (
	"bytes"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// Detect asks the terminal what it can draw, ONCE, and the caller must do
// it before tea.NewProgram (ADR-0089 §7). The reason is raw-mode stdin
// ownership, not protocol decoding: once Bubble Tea owns stdin, a
// terminal's reply to a query arrives in the input box as phantom
// keystrokes — the same hazard that keeps WithAutoStyle out of the Markdown
// renderer.
//
// A reply that never comes means NO capability, never "try again later". A
// terminal report carries no tag, so a reply abandoned on a timeout is not
// lost but misfiled into whatever asks next.
//
// tty may be nil, and every failure returns None: not drawing is always a
// safe answer, and the fallback is what the runtime does today.
func Detect(tty *os.File, env func(string) string, timeout time.Duration) Protocol {
	if p, decided := fromEnv(env); decided {
		return p
	}
	if tty == nil {
		return None
	}
	if !askKitty(tty, timeout) {
		return None
	}
	return Kitty
}

// Resolve turns the operator's `[tui] images` setting into the protocol
// this session will use. "auto" asks (Detect); "off" never draws; a named
// protocol is taken as given, which is the escape hatch for a probe that
// is wrong about a terminal it has never met. An unknown value draws
// nothing rather than guessing — config validation rejects it first, and a
// setting that reached here unvalidated is not a reason to start emitting
// escapes.
func Resolve(setting string, tty *os.File, env func(string) string, timeout time.Duration) Protocol {
	switch setting {
	case "auto":
		return Detect(tty, env, timeout)
	case "iterm":
		return ITerm2
	case "kitty":
		return Kitty
	default: // "off", and anything unrecognised
		return None
	}
}

// fromEnv answers from the environment alone where that is conclusive, so
// the common cases cost no query at all. decided=false means the terminal
// has to be asked.
func fromEnv(env func(string) string) (p Protocol, decided bool) {
	if env == nil {
		env = os.Getenv
	}
	// Inside a multiplexer the answer is off. Passthrough is the
	// multiplexer's configuration, and the one measured rendering a
	// payload stranded a frame for every image.
	if env("TMUX") != "" || strings.HasPrefix(env("TERM"), "screen") {
		return None, true
	}
	if env("KITTY_WINDOW_ID") != "" || env("GHOSTTY_RESOURCES_DIR") != "" ||
		strings.Contains(env("TERM"), "kitty") {
		return Kitty, true
	}
	if env("TERM_PROGRAM") == "iTerm.app" {
		return ITerm2, true
	}
	return None, false
}

// kittyQuery asks the terminal to accept a 1x1 image and report, and then
// asks it for its primary device attributes. i=31 is an arbitrary id echoed
// back; a=q means query only, so nothing is drawn.
//
// The SECOND question is what makes the first one answerable. A terminal
// that does not know the graphics protocol says nothing to it, and silence
// is indistinguishable from slowness, so the only verdict available was the
// timeout — measured at a full 2.001 s on Apple Terminal, on every start,
// with TERM_PROGRAM set and the environment unable to classify it. Every
// VT-compatible terminal answers DA1, so a DA1 reply arriving with no
// graphics reply before it is a definitive no, in milliseconds. The budget
// below is now a backstop for a terminal that answers neither, not the
// mechanism.
const kittyQuery = "\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\\x1b[c"

// askKitty writes the query and reads for a reply, with the discipline the
// probes had to learn: the stream drained before asking, a generous budget
// as the backstop, and silence read as "no" rather than as "ask again".
//
// It reads on the calling goroutine, with select(2) bounding every wait, and
// that is a correctness property rather than a style: when askKitty returns,
// nothing of it is still reading the terminal. The first version read from a
// goroutine, which stayed blocked in tty.Read after the verdict — on macOS
// /dev/tty cannot join kqueue, so it is a blocking descriptor and Close does
// not wake a read that is already in it. The terminal's next input went to
// that reader instead of its owner: measured on Apple Terminal, 3 runs of 3,
// it took the OSC 11 colour reply lipgloss.HasDarkBackground was waiting for
// 300 ms after Close had returned, so "auto" fell back to dark whatever the
// background was; with a fixed theme the same read takes the operator's first
// keystroke instead.
func askKitty(tty *os.File, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	// Raw mode, briefly. The reply carries no newline, so in cooked mode
	// it would sit in the line buffer until the operator pressed Enter and
	// every probe would time out into "no capability". This runs before
	// tea.NewProgram, so nothing else owns the terminal yet; the state is
	// restored before returning, whatever happens.
	fd := int(tty.Fd())
	restore, err := term.MakeRaw(fd)
	if err != nil {
		return false
	}
	defer func() { _ = term.Restore(fd, restore) }()

	// Drain whatever was already buffered: an older reply would otherwise
	// be read as the answer to this question.
	buf := make([]byte, 256)
	for {
		ready, err := waitReadable(fd, 0)
		if err != nil || !ready {
			break
		}
		if n, err := readReady(fd, buf); err != nil || n == 0 {
			break
		}
	}
	if _, err := tty.WriteString(kittyQuery); err != nil {
		return false
	}
	// A terminal that does not understand APC prints the query's body
	// instead of answering it: Apple Terminal left
	// "Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA" on the operator's screen at every
	// start (measured 2026-09-17). The line this dirtied is erased before
	// the terminal is handed to the UI. Nothing above the cursor is
	// touched, and on a terminal that echoed nothing this clears a line
	// that is already blank.
	defer func() { _, _ = tty.WriteString("\r\x1b[2K") }()

	var got []byte
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		ready, err := waitReadable(fd, remaining)
		if err != nil || !ready {
			return false
		}
		n, err := readReady(fd, buf)
		if err == errNotReady {
			continue
		}
		if err != nil || n == 0 {
			return false // hang-up, or the descriptor went away
		}
		got = append(got, buf[:n]...)
		if done, ok := parseKittyReply(got); done {
			return ok
		}
		if len(got) > 4096 {
			return false
		}
	}
}

// parseKittyReply reads the terminal's answer to kittyQuery. done says the
// whole answer has arrived; ok says it included an acceptance.
//
// The whole answer ends with the DA1 reply, because DA1 is the last thing the
// query asks and every terminal answers it. Stopping at the graphics reply —
// as the first version did — left the DA1 reply in the input queue for
// whatever read the terminal next; it went unnoticed only because the stale
// reader described above swallowed it. A terminal that answers neither is
// what the caller's deadline is for.
//
// Bytes that are neither reply (a key pressed during start-up, a late answer
// to someone else's query) are skipped rather than taken as a verdict.
func parseKittyReply(got []byte) (done, ok bool) {
	da1 := bytes.Index(got, []byte("\x1b[?"))
	if da1 < 0 {
		return false, false
	}
	end := da1 + 3
	for end < len(got) && (got[end] == ';' || (got[end] >= '0' && got[end] <= '9')) {
		end++
	}
	if end >= len(got) {
		return false, false // DA1 still arriving
	}
	if got[end] != 'c' {
		// Some other private-mode report; keep waiting for DA1 behind it.
		if next := bytes.Index(got[end:], []byte("\x1b[?")); next >= 0 {
			return parseKittyReply(got[end+next:])
		}
		return false, false
	}
	// The graphics reply, if there is one, precedes DA1.
	before := got[:da1]
	g := bytes.Index(before, []byte("\x1b_G"))
	if g < 0 {
		return true, false
	}
	st := bytes.Index(before[g:], []byte("\x1b\\"))
	if st < 0 {
		return true, false
	}
	return true, bytes.Contains(before[g:g+st], []byte(";OK"))
}
