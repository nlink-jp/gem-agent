package cmd

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

// Review round 3 regressions.

// plainAsk asks nothing on a cancelled context — and leaves stdin
// untouched for the REPL.
func TestPlainAskDeclinesWhenCancelled(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("1\n"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := plainAsk(in, &strings.Builder{})(ctx, "q", []string{"a", "b"}); err != errAskDeclined {
		t.Errorf("err = %v, want decline", err)
	}
	if in.Buffered() != 0 || in.Size() == 0 {
		// nothing must have been consumed: the next line belongs to the REPL
		if b, _ := in.Peek(1); len(b) == 0 {
			t.Error("plainAsk consumed stdin on a cancelled turn")
		}
	}
}

// The child never expands @-references in a model-authored question.
func TestSearchChildDoesNotExpandMentions(t *testing.T) {
	sb := &scriptBackend{responses: []*llm.Response{{Content: "nothing to report"}}}
	reg, _, _, _ := searchSetup(t, sb)
	if _, err := runSearch(t, reg, map[string]any{"question": "what does @pond.txt say?"}); err != nil {
		t.Fatal(err)
	}
	if len(sb.messages) == 0 || len(sb.messages[0]) == 0 {
		t.Fatal("child never called the backend")
	}
	// pond.txt holds "a heron stands in the pond"; had the @ref been
	// expanded, the file's text would ride the child's first message.
	if strings.Contains(sb.messages[0][0].Content, "heron") || len(sb.messages[0][0].Attachments) != 0 {
		t.Errorf("child expanded the @ref from a model-authored question: %q", sb.messages[0][0].Content)
	}
}

// Report truncation is rune-safe: a Japanese report never ends mid-rune.
func TestSearchReportTruncationRuneSafe(t *testing.T) {
	big := strings.Repeat("日本語の長い報告文。", searchReportCap)
	sb := &scriptBackend{responses: []*llm.Response{{Content: big}}}
	reg, _, _, _ := searchSetup(t, sb)
	out, err := runSearch(t, reg, map[string]any{"question": "q"})
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(out) {
		t.Error("truncated report is not valid UTF-8")
	}
	if !strings.Contains(out, "truncated") {
		t.Error("truncation not disclosed")
	}
}

// The startup-notes tee stops recording once frozen.
func TestStartupNotesFreeze(t *testing.T) {
	var out bytes.Buffer
	n := &startupNotes{w: &out}
	n.Write([]byte("warning: one\n"))
	n.freeze()
	for i := 0; i < 100; i++ {
		n.Write([]byte("[tool] x\n"))
	}
	if len(n.lines) != 0 {
		t.Errorf("tee kept %d lines after freeze", len(n.lines))
	}
	if !strings.Contains(out.String(), "[tool] x") {
		t.Error("writes stopped reaching stderr after freeze")
	}
}

// The plain REPL and every prompt that reads stdin share ONE
// bufio.Reader (AGENTS.md gotcha): a second reader strands typed-ahead
// input. Pinned at the source level.
func TestNoSecondStdinReader(t *testing.T) {
	src, err := os.ReadFile("root.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "bufio.NewReader(os.Stdin)") {
		t.Error("root.go wraps os.Stdin in a second bufio.Reader — hand prompts the shared `stdin` instead")
	}
}
