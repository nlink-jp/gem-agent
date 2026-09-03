package cmd

import (
	"io"
	"strings"
	"testing"
	"time"
)

// ADR-0067: a piped stdin that closes promptly is read silently; one
// that stays open past the grace is announced exactly once and still
// read to EOF.

// fastGrace is generous on purpose: the read finishes in microseconds,
// so the select returns at once and the grace costs no test time —
// but a starved goroutine on a loaded machine must not fire a
// spurious notice.
const fastGrace = 500 * time.Millisecond

func TestReadPipedStdinNoticing_PromptEOFStaysSilent(t *testing.T) {
	notices := 0
	content, warning := readPipedStdinNoticing(strings.NewReader("ready data"), fastGrace, func() { notices++ })
	if notices != 0 {
		t.Fatalf("notice fired %d times on an immediately-closed reader, want 0", notices)
	}
	if content != "ready data" || warning != "" {
		t.Fatalf("content=%q warning=%q, want the data and no warning", content, warning)
	}
}

func TestReadPipedStdinNoticing_EmptyPromptEOFStaysSilent(t *testing.T) {
	notices := 0
	content, warning := readPipedStdinNoticing(strings.NewReader(""), fastGrace, func() { notices++ })
	if notices != 0 || content != "" || warning != "" {
		t.Fatalf("notices=%d content=%q warning=%q, want 0/empty/empty (the < /dev/null shape)", notices, content, warning)
	}
}

func TestReadPipedStdinNoticing_SlowPipeIsAnnouncedOnceAndStillRead(t *testing.T) {
	pr, pw := io.Pipe()
	notices := 0
	var firedAt time.Time
	start := time.Now()
	go func() {
		// A producer that is slow, not idle: writes well past the
		// grace (15×, so a starved timer still fires first) and then
		// closes. The read must survive the notice intact.
		time.Sleep(300 * time.Millisecond)
		_, _ = pw.Write([]byte("late data"))
		_ = pw.Close()
	}()
	content, warning := readPipedStdinNoticing(pr, 20*time.Millisecond, func() { notices++; firedAt = time.Now() })
	if notices != 1 {
		t.Fatalf("notice fired %d times, want exactly 1", notices)
	}
	if firedAt.Sub(start) < 20*time.Millisecond {
		t.Fatalf("notice fired after %v, before the %v grace", firedAt.Sub(start), 20*time.Millisecond)
	}
	if content != "late data" || warning != "" {
		t.Fatalf("content=%q warning=%q after the notice, want the late data and no warning", content, warning)
	}
}

func TestReadPipedStdinNoticing_IdlePipeClosedEmptyAfterNotice(t *testing.T) {
	pr, pw := io.Pipe()
	notices := 0
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = pw.Close() // the idle-pipe shape, eventually released
	}()
	content, warning := readPipedStdinNoticing(pr, 20*time.Millisecond, func() { notices++ })
	if notices != 1 || content != "" || warning != "" {
		t.Fatalf("notices=%d content=%q warning=%q, want 1/empty/empty", notices, content, warning)
	}
}

// ADR-0067 §2: an announced wait is seen to end; a silent read stays
// silent; a warning already says nothing was attached.
func TestStdinOutcomeLine(t *testing.T) {
	cases := []struct {
		name             string
		content, warning string
		waited           bool
		want             string
	}{
		{"content after a silent read", "abc", "", false, "[stdin: 3 bytes attached as data]"},
		{"content after an announced wait", "abcd", "", true, "[stdin: 4 bytes attached as data]"},
		{"empty after an announced wait", "", "", true, "[stdin: ended empty — nothing attached]"},
		{"empty after a silent read (the < /dev/null shape)", "", "", false, ""},
		{"binary skipped after an announced wait — the warning already says so", "", "piped stdin is not UTF-8 text, nothing attached", true, ""},
	}
	for _, c := range cases {
		if got := stdinOutcomeLine(c.content, c.warning, c.waited); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStdinWaitMessageNamesBothRemedies(t *testing.T) {
	for _, want := range []string{"close the pipe", "< /dev/null", "2s"} {
		if !strings.Contains(stdinWaitMessage, want) {
			t.Errorf("stdin wait message lacks %q: %s", want, stdinWaitMessage)
		}
	}
	if stdinWaitNotice != 2*time.Second {
		t.Errorf("stdinWaitNotice = %v, but the message promises 2s", stdinWaitNotice)
	}
}
