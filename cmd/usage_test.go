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
	"github.com/nlink-jp/gem-agent/internal/uitext"
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
	tally.add("summarize_file", "light-model", 600, 40, 0)
	tally.add("summarize_file", "light-model", 400, 30, 0)
	// ADR-0066 §4: the fetched page is most of a web_fetch call and
	// bills as input; the line names it rather than hiding 90% of the
	// tool's input (measured 953 of 1054).
	tally.add("web_fetch", "light-model", 900, 60, 1091)

	out := usageReport(ag, tally, "main-model", "light-model")
	for _, want := range []string{
		"main loop (main-model):",
		"rounds 2 · prompt 22.0k · output 500",
		"cached 19.0k of prompt (86%)",
		"cached 19.0k of prompt (86%)",
		"context now 12.0k of 1.0M window (1%)",
		"summarize_file (light-model): 2 calls · prompt 1.0k · output 70\n",
		"web_fetch (light-model): 1 calls · prompt 900 · output 60 · tool results 1.1k\n",
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

// The exit summary answers "how do I get back" and "what did it cost",
// and nothing else — and stays silent for a session with no
// conversation, whose resume hint would point at a file resume refuses.
func TestExitSummary(t *testing.T) {
	en := uitext.For(uitext.EN)
	lines := exitSummary(agent.UsageStats{Rounds: 3, Prompt: 45200, Output: 3100},
		"20260826-153012", en)
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want session + usage", lines)
	}
	if !strings.Contains(lines[0], "20260826-153012") || !strings.Contains(lines[0], "gem-agent -c") {
		t.Errorf("session line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "3 rounds") || !strings.Contains(lines[1], "45.2k") {
		t.Errorf("usage line = %q", lines[1])
	}

	// No session log: the cost line still prints, the resume hint not.
	lines = exitSummary(agent.UsageStats{Rounds: 1, Prompt: 100, Output: 10}, "", en)
	if len(lines) != 1 || strings.Contains(lines[0], "resume") {
		t.Errorf("no-log lines = %v", lines)
	}

	// Nothing happened: nothing to say.
	if lines := exitSummary(agent.UsageStats{}, "20260826-153012", en); lines != nil {
		t.Errorf("empty session produced a summary: %v", lines)
	}
}

// ADR-0065 §2: a goroutine the floor left behind is named on the way
// out — and only then.
func TestExitSummaryNamesAbandonedCalls(t *testing.T) {
	en := uitext.For(uitext.EN)
	lines := exitSummary(agent.UsageStats{Rounds: 1, Prompt: 10, Output: 5, AbandonedRunning: 2}, "", en)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "2 abandoned tool call(s) still running") {
		t.Errorf("abandoned calls not named:\n%s", joined)
	}
	lines = exitSummary(agent.UsageStats{Rounds: 1, Prompt: 10, Output: 5}, "", en)
	if strings.Contains(strings.Join(lines, "\n"), "abandoned") {
		t.Error("receipt mentions abandoned calls when there are none")
	}
}
