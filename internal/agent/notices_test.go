package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// The same event printed Japanese when the operator asked for it and
// English when the runtime decided: /compact went through the catalog
// and auto-compaction did not. A notice carrying the operator's next
// command is chrome, whatever triggered it (ADR-0029 §3).
func TestNoticesFollowTheConfiguredLanguage(t *testing.T) {
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/true")
		}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var notices []string
	a := New(Options{Registry: reg, Gate: &approveAll{}, MaxTurns: 1,
		Backend: &mockBackend{responses: []*llm.Response{{Content: "a summary"}}},
		Msgs:    uitext.For(uitext.JA), OnNotice: func(s string) { notices = append(notices, s) }})

	// Through a real notice site, not through the catalog field: the
	// first version of this test printed a.msgs.CompactNothingFmt and
	// asserted it was Japanese, which is a restatement of New's
	// assignment — removing a.msgs from compact.go would have left it
	// green (pre-release review).
	a.autoCompact, a.compactAtPct = true, 80
	a.window, a.lastPrompt = 1000, 950
	for i := 0; i < 12; i++ {
		a.history = append(a.history, llm.Message{Role: llm.RoleUser, Content: "x"})
	}
	a.maybeAutoCompact(context.Background())

	if len(notices) != 1 {
		t.Fatalf("notices = %v — the auto-compaction path wrote nothing", notices)
	}
	if strings.Contains(notices[0], "context reached") {
		t.Errorf("a Japanese session got the English catalog: %q", notices[0])
	}
	if !strings.Contains(notices[0], "要約") {
		t.Errorf("the notice is not the Japanese one: %q", notices[0])
	}
}

// Nothing wired means English sentences, not empty format strings —
// every test in this package constructs an Agent without a catalog.
func TestNoCatalogMeansEnglish(t *testing.T) {
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/true")
		}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a := New(Options{Registry: reg, Gate: &approveAll{}, MaxTurns: 1})
	if a.msgs == nil || a.msgs.CompactNothingFmt == "" {
		t.Fatal("an agent with no catalog has no notice text")
	}
	if !strings.Contains(a.msgs.RemoteFaultFmt, "/mcp reload") {
		t.Errorf("the remote-fault notice lost its command: %q", a.msgs.RemoteFaultFmt)
	}
}

// The round limit and the loop guard are different events. A loop waved
// through, announced as a round count, told the operator nothing about
// the repeat that triggered it — and the number it printed was the hard
// cap, which reads as headroom the turn does not have.
func TestContinuedNoticeNamesItsTrigger(t *testing.T) {
	a := &Agent{msgs: uitext.For(uitext.EN)}

	limit := a.continuedNotice("round-limit", "", 20)
	if !strings.Contains(limit, "20") || strings.Contains(limit, "repeated") {
		t.Errorf("round-limit notice = %q", limit)
	}
	loop := a.continuedNotice("loop", "shell_exec: ls -la", 20)
	if !strings.Contains(loop, "repeated") || !strings.Contains(loop, "ls -la") {
		t.Errorf("loop notice = %q — it must name the call that repeated", loop)
	}
	if strings.Contains(loop, "20") {
		t.Errorf("loop notice quotes a round number that did not trigger it: %q", loop)
	}
}
