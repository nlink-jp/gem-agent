package cmd

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// ADR-0111: the command rides as the operator's words, and what it
// printed reaches the model inside the data tag.
func TestDirectShellOutputReachesTheModelAsData(t *testing.T) {
	reg, err := tools.New(t.TempDir(), func(ctx context.Context, command string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", command)
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	backend := &scriptBackend{responses: []*llm.Response{{Content: "ok"}}}
	ag := agent.New(agent.Options{Backend: backend, Model: "m", Registry: reg, MaxTurns: 1})

	out := runDirectShell(context.Background(), reg, ag, "echo OUTPUT-MARKER")
	if !strings.Contains(out, "OUTPUT-MARKER") {
		t.Fatalf("the operator did not get the output: %q", out)
	}
	if _, err := ag.Run(context.Background(), "next", nil); err != nil {
		t.Fatal(err)
	}
	var sent string
	for _, m := range backend.messages[0] {
		if m.Role == llm.RoleUser && strings.HasPrefix(m.Content, session.ShellContextPrefix) {
			sent = m.Content
		}
	}
	if sent == "" {
		t.Fatalf("the shell message was not sent: %+v", backend.messages[0])
	}
	if !strings.Contains(sent, "$ echo OUTPUT-MARKER") {
		t.Errorf("the command is missing: %q", sent)
	}
	before, after, ok := strings.Cut(sent, "quoted as data:")
	if !ok || strings.Contains(strings.ReplaceAll(before, "echo OUTPUT-MARKER", ""), "OUTPUT-MARKER") {
		t.Errorf("the output is outside the data lane: %q", sent)
	}
	if !strings.Contains(after, "OUTPUT-MARKER") || !strings.HasPrefix(strings.TrimSpace(after), "<") {
		t.Errorf("the output is not wrapped: %q", after)
	}
}
