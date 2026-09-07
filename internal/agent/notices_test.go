package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

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
		Msgs: uitext.For(uitext.JA), OnNotice: func(s string) { notices = append(notices, s) }})

	a.compactFailures = 0
	a.notify(strings.TrimSpace(a.msgs.CompactNothingFmt))

	if len(notices) != 1 {
		t.Fatalf("notices = %v", notices)
	}
	if strings.Contains(notices[0], "context is at") {
		t.Errorf("a Japanese session got the English catalog: %q", notices[0])
	}
	if !strings.Contains(notices[0], "/clear") {
		t.Errorf("the next command did not survive translation: %q", notices[0])
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
