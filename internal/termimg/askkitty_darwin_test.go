package termimg

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A pseudo-terminal pair: the test holds the master and plays the terminal,
// askKitty gets the slave. macOS only, like the runtime; the slave of
// /dev/ptmx is /dev/ttysNNN with the master's minor number, which the first
// round trip below proves rather than assumes.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	if err := unix.IoctlSetInt(m, unix.TIOCPTYGRANT, 0); err != nil {
		t.Fatalf("grantpt: %v", err)
	}
	if err := unix.IoctlSetInt(m, unix.TIOCPTYUNLK, 0); err != nil {
		t.Fatalf("unlockpt: %v", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(m, &st); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("/dev/ttys%03d", unix.Minor(uint64(st.Rdev)))
	s, err := os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open slave %s: %v", name, err)
	}
	master = os.NewFile(uintptr(m), "ptmx")
	t.Cleanup(func() { _ = s.Close(); _ = master.Close() })
	return master, s
}

// terminal plays a terminal on the master side: it reads what the program
// writes and, once it has seen the DA1 request that ends kittyQuery, sends
// answer. It records everything it was sent.
type terminal struct {
	mu   sync.Mutex
	seen []byte
}

func playTerminal(t *testing.T, master *os.File, answer string) *terminal {
	t.Helper()
	term := &terminal{}
	go func() {
		buf := make([]byte, 1024)
		answered := false
		for {
			n, err := master.Read(buf)
			term.mu.Lock()
			term.seen = append(term.seen, buf[:n]...)
			ask := !answered && bytes.Contains(term.seen, []byte("\x1b[c"))
			term.mu.Unlock()
			if ask {
				answered = true
				if answer != "" {
					_, _ = master.WriteString(answer)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return term
}

// readWithin reads from f for at most d, the way the terminal's next owner
// would: whatever arrives is its to have.
func readWithin(t *testing.T, f *os.File, d time.Duration) string {
	t.Helper()
	fd := int(f.Fd())
	var got []byte
	deadline := time.Now().Add(d)
	buf := make([]byte, 256)
	for time.Until(deadline) > 0 {
		ready, err := waitReadable(fd, time.Until(deadline))
		if err != nil {
			t.Fatal(err)
		}
		if !ready {
			break
		}
		n, err := readReady(fd, buf)
		if err != nil && err != errNotReady {
			break
		}
		got = append(got, buf[:n]...)
		if len(got) > 0 {
			// one more short look, for a reply that arrives in two writes
			deadline = time.Now().Add(50 * time.Millisecond)
		}
	}
	return string(got)
}

func TestAskKittyReadsTheVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		want         bool
	}{
		{"graphics accepted, then DA1", "\x1b_Gi=31;OK\x1b\\\x1b[?62;4c", true},
		{"graphics refused, then DA1", "\x1b_Gi=31;ENOTSUPPORTED:nope\x1b\\\x1b[?62;4c", false},
		{"DA1 alone: the terminal does not know the protocol", "\x1b[?1;2c", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave := openPTY(t)
			playTerminal(t, master, tc.answer)
			start := time.Now()
			if got := askKitty(slave, 2*time.Second); got != tc.want {
				t.Errorf("askKitty = %v, want %v", got, tc.want)
			}
			if took := time.Since(start); took > time.Second {
				t.Errorf("a terminal that answered was waited on for %v", took)
			}
		})
	}
}

func TestAskKittyGivesUpOnASilentTerminal(t *testing.T) {
	master, slave := openPTY(t)
	playTerminal(t, master, "")
	start := time.Now()
	if askKitty(slave, 300*time.Millisecond) {
		t.Error("silence was read as a capability")
	}
	if took := time.Since(start); took < 250*time.Millisecond || took > 2*time.Second {
		t.Errorf("gave up after %v, want about 300ms", took)
	}
}

// The property the first version lacked: when askKitty returns, nothing of it
// is still reading the terminal, and nothing of the terminal's answer is left
// for the next reader. Whatever the terminal sends next belongs to whoever
// reads next — in production lipgloss's background query, then the operator's
// keys. Both halves are checked for every verdict, because the two leaks hid
// each other: the stale reader was what swallowed the unread DA1 reply.
func TestAskKittyLeavesTheTerminalToItsNextOwner(t *testing.T) {
	for _, tc := range []struct{ name, answer string }{
		{"graphics accepted", "\x1b_Gi=31;OK\x1b\\\x1b[?62;4c"},
		{"DA1 alone", "\x1b[?1;2c"},
		{"silent terminal", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave := openPTY(t)
			playTerminal(t, master, tc.answer)
			askKitty(slave, 300*time.Millisecond)

			// The probe restored cooked mode; the next owner sets its own.
			// Raw here only so the test reads bytes rather than lines.
			fd := int(slave.Fd())
			raw, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
			if err != nil {
				t.Fatal(err)
			}
			raw.Lflag &^= unix.ICANON | unix.ECHO
			if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, raw); err != nil {
				t.Fatal(err)
			}

			if left := readWithin(t, slave, 150*time.Millisecond); left != "" {
				t.Errorf("the probe left %q unread for the next reader", left)
			}
			const next = "\x1b]11;rgb:ffff/ffff/ffff\a"
			if _, err := master.WriteString(next); err != nil {
				t.Fatal(err)
			}
			if got := readWithin(t, slave, time.Second); got != next {
				t.Errorf("the next reader got %q, want %q: something else is still reading the terminal", got, next)
			}
		})
	}
}

func TestAskKittyErasesTheLineADumbTerminalDirtied(t *testing.T) {
	master, slave := openPTY(t)
	term := playTerminal(t, master, "\x1b[?1;2c")
	askKitty(slave, time.Second)
	time.Sleep(50 * time.Millisecond)
	term.mu.Lock()
	defer term.mu.Unlock()
	if !strings.HasSuffix(string(term.seen), "\r\x1b[2K") {
		t.Errorf("the last thing sent must erase the line; terminal saw %q", term.seen)
	}
}
