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
	in := make(chan byte, 4096)
	go func() {
		defer close(in)
		buf := make([]byte, 64)
		for {
			n, err := tty.Read(buf)
			for i := 0; i < n; i++ {
				in <- buf[i]
			}
			if err != nil {
				return
			}
		}
	}()
	// Drain whatever was already buffered: an older reply would otherwise
	// be read as the answer to this question.
	for drained := false; !drained; {
		select {
		case <-in:
		default:
			drained = true
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
	deadline := time.After(timeout)
	for {
		select {
		case b, ok := <-in:
			if !ok {
				return false
			}
			got = append(got, b)
			if done, ok := parseKittyReply(got); done {
				return ok
			}
			if len(got) > 256 {
				return false
			}
		case <-deadline:
			return false
		}
	}
}

// parseKittyReply reads the terminal's answer to kittyQuery. done says the
// reply is complete; ok says it was an acceptance. A terminal that does not
// know the protocol answers nothing at all, which the caller times out.
func parseKittyReply(got []byte) (done, ok bool) {
	if !bytes.HasPrefix(got, []byte("\x1b_G")) {
		// Some other escape arrived first; this is not our reply.
		if len(got) >= 3 {
			return true, false
		}
		return false, false
	}
	i := bytes.Index(got, []byte("\x1b\\"))
	if i < 0 {
		return false, false
	}
	return true, bytes.Contains(got[:i], []byte(";OK"))
}
