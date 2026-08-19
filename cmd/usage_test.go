package cmd

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// usageMock replays scripted responses for the report test.
type usageMock struct{ responses []*llm.Response }

func (m *usageMock) ChatStream(ctx context.Context, system string, msgs []llm.Message, defs []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	if len(m.responses) == 0 {
		return &llm.Response{Content: "x"}, nil
	}
	r := m.responses[0]
	m.responses = m.responses[1:]
	return r, nil
}

func TestUsageReportStatement(t *testing.T) {
	reg, err := tools.New(t.TempDir(), func(ctx context.Context, c string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", c)
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	mb := &usageMock{responses: []*llm.Response{
		{Content: "a", PromptTokens: 10000, OutputTokens: 200, ThoughtTokens: 50, CachedTokens: 8000},
		{Content: "b", PromptTokens: 12000, OutputTokens: 300, CachedTokens: 11000},
	}}
	ag := agent.New(agent.Options{Backend: mb, Registry: reg, Gate: nil, System: "s", MaxTurns: 5})
	ag.SetContextWindow(1_000_000)
	if _, err := ag.Run(context.Background(), "one", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Run(context.Background(), "two", nil); err != nil {
		t.Fatal(err)
	}

	tally := newUsageTally()
	tally.add("summarize_file", "light-model", 600, 40)
	tally.add("summarize_file", "light-model", 400, 30)
	tally.add("web_fetch", "light-model", 900, 60)

	out := usageReport(ag, tally, "main-model", "light-model")
	for _, want := range []string{
		"main loop (main-model):",
		"rounds 2 · prompt 22.0k · output 500",
		"cached 19.0k of prompt (86%)",
		"cache saves cost/latency, not window space",
		"context now 12.0k of 1.0M window (1%)",
		"summarize_file (light-model): 2 calls · prompt 1.0k · output 70",
		"web_fetch (light-model): 1 calls · prompt 900 · output 60",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	// Categories with no activity stay silent — a statement, not a form.
	if strings.Contains(out, "risk checks") || strings.Contains(out, "compaction") {
		t.Errorf("empty categories rendered:\n%s", out)
	}

	// A fresh session is honest about having nothing to report.
	fresh := agent.New(agent.Options{Backend: mb, Registry: reg, System: "s", MaxTurns: 5})
	if out := usageReport(fresh, newUsageTally(), "m", "l"); !strings.Contains(out, "no requests yet") {
		t.Errorf("fresh report = %q", out)
	}
}
