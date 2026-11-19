package cmd

// Driving the TUI from a test (ported from lagent ADR-0054 §2.6): the characterization
// child runs as a TUI under a private tmux server — a real terminal the
// test types into and reads back. Bubble Tea's behaviour inside Update,
// the class of the /clear freeze lagent's ADR-0054 calls Found 1, shows
// only there. Skipped where tmux
// is absent.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type tuiRun struct {
	t    *testing.T
	sock string
}

// startTUI runs the session as a TUI in a 110×30 tmux pane and waits for
// its footer.
func startTUI(t *testing.T, e charEnv, turns []scriptTurn, args ...string) *tuiRun {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed: the TUI cannot be driven here")
	}
	scriptPath := filepath.Join(e.root, "script.json")
	raw, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	argv, err := json.Marshal(append([]string{"--config", e.config}, args...))
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GEMAGENT_") || k == "HOME" || k == "LANG" || k == "LC_ALL" || k == "TMUX" || k == "TERM" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+e.home,
		"LANG=C",
		"TERM=xterm-256color",
		charChildEnv+"=1",
		charScriptEnv+"="+scriptPath,
		charRecordEnv+"="+filepath.Join(e.root, "calls.jsonl"),
		charArgsEnv+"="+string(argv),
	)
	var w strings.Builder
	fmt.Fprintf(&w, "#!/bin/sh\ncd %s || exit 1\nexec env -i", shellQuote(e.project))
	for _, kv := range env {
		fmt.Fprintf(&w, " %s", shellQuote(kv))
	}
	fmt.Fprintf(&w, " %s '-test.run=^$'\n", shellQuote(self))
	wrapper := filepath.Join(e.root, "tui.sh")
	if err := os.WriteFile(wrapper, []byte(w.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	// The temporary root ends in a counter that every test shares; its
	// parent is unique to this test run.
	r := &tuiRun{t: t, sock: "gem-agent-test-" + filepath.Base(filepath.Dir(e.root)) + "-" + filepath.Base(e.root)}
	if err := r.tmux("new-session", "-d", "-s", "s", "-x", "110", "-y", "30", wrapper); err != nil {
		t.Fatalf("tmux: %v", err)
	}
	// The server's socket file outlives kill-server; it is ours, by the
	// name we gave it, so it goes too.
	sockPath, _ := exec.Command("tmux", "-L", r.sock, "display-message", "-p", "#{socket_path}").Output()
	t.Cleanup(func() {
		_ = r.tmux("kill-server")
		if p := strings.TrimSpace(string(sockPath)); strings.HasSuffix(p, "/"+r.sock) {
			_ = os.Remove(p)
		}
	})
	_ = r.tmux("set-option", "-t", "s", "remain-on-exit", "on")
	if !r.waitFor("scripted-main", 20*time.Second) {
		t.Fatalf("the TUI did not start:\n%s", r.screen())
	}
	return r
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (r *tuiRun) tmux(args ...string) error {
	return exec.Command("tmux", append([]string{"-L", r.sock}, args...)...).Run()
}

func (r *tuiRun) screen() string {
	out, _ := exec.Command("tmux", "-L", r.sock, "capture-pane", "-p", "-J", "-t", "s").Output()
	return string(out)
}

// scrollback is the pane's whole history, what scrolled off included.
func (r *tuiRun) scrollback() string {
	out, _ := exec.Command("tmux", "-L", r.sock, "capture-pane", "-p", "-J", "-S", "-", "-t", "s").Output()
	return string(out)
}

func (r *tuiRun) dead() bool {
	out, _ := exec.Command("tmux", "-L", r.sock, "display-message", "-p", "-t", "s", "#{pane_dead}").Output()
	return strings.TrimSpace(string(out)) == "1"
}

// typeLine types text and presses Enter.
func (r *tuiRun) typeLine(text string) {
	r.t.Helper()
	if err := r.tmux("send-keys", "-t", "s", "-l", text); err != nil {
		r.t.Fatal(err)
	}
	if err := r.tmux("send-keys", "-t", "s", "Enter"); err != nil {
		r.t.Fatal(err)
	}
}

// waitFor polls the screen until text shows or the time runs out.
func (r *tuiRun) waitFor(text string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(r.screen(), text) {
			return true
		}
		if r.dead() {
			return strings.Contains(r.screen(), text)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func (r *tuiRun) waitExit(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r.dead() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// A session in the TUI: a turn, /clear, /quit — each answered, and two
// transcripts left behind.
func TestTUITurnClearQuit(t *testing.T) {
	e := newCharEnv(t)
	r := startTUI(t, e, []scriptTurn{{Text: "Hello."}}, "--no-sandbox")
	r.typeLine("hi")
	if !r.waitFor("Hello.", 10*time.Second) {
		t.Fatalf("the turn did not answer:\n%s", r.screen())
	}
	r.typeLine("/clear")
	if !r.waitFor("history cleared", 10*time.Second) {
		t.Fatalf("/clear did not answer:\n%s", r.screen())
	}
	r.typeLine("/quit")
	if !r.waitExit(10 * time.Second) {
		t.Fatalf("/quit did not end the session:\n%s", r.screen())
	}
	if n := strings.Count(readTranscripts(t, filepath.Join(e.home, ".local", "state", "gem-agent", "sessions")), "--- transcript "); n != 2 {
		t.Fatalf("want two transcripts (before and after /clear), got %d", n)
	}
}

// A session_start hook that prints (lagent ADR-0054 Found 1, fixed here
// before it was found there): a session_start hook that prints makes /clear report
// the attached context, and the report went to the program from inside
// Update — where a Send never returns. /clear must answer, and so must
// the command typed after it.
func TestTUIClearWithAPrintingSessionStartHook(t *testing.T) {
	e := newCharEnvWith(t, `
[[hooks.session_start]]
command = "echo context-from-hook"
`)
	r := startTUI(t, e, nil, "--no-sandbox")
	r.typeLine("/clear")
	if !r.waitFor("history cleared", 10*time.Second) {
		t.Fatalf("/clear did not answer — the TUI is frozen:\n%s", r.screen())
	}
	if !r.waitFor("session_start hook (clear) attached", 5*time.Second) {
		t.Fatalf("the hook's notice is not in /clear's output:\n%s", r.screen())
	}
	r.typeLine("/version")
	if !r.waitFor("darwin", 10*time.Second) {
		t.Fatalf("the command after /clear did not answer:\n%s", r.screen())
	}
}

// ADR-0124 addendum, Found 4 (lagent ADR-0054): a persistent file
// changed during a TUI session is named at /clear. The note was written
// to the terminal while the TUI held the screen, and the next redraw
// covered it; it is the command's output now.
func TestTUIClearNamesAChangedPersistentFile(t *testing.T) {
	e := newCharEnv(t)
	r := startTUI(t, e, nil, "--no-sandbox")
	if err := os.WriteFile(filepath.Join(e.project, "AGENTS.md"), []byte("# Project rules\n\nChanged during the session.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.typeLine("/clear")
	if !r.waitFor("history cleared", 10*time.Second) {
		t.Fatalf("/clear did not answer:\n%s", r.screen())
	}
	if !r.waitFor("this session added or changed: AGENTS.md", 5*time.Second) {
		t.Fatalf("the persistent-file note is not on the screen:\n%s", r.screen())
	}
}
