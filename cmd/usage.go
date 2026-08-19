package cmd

import (
	"fmt"
	"strings"
	"sync"

	"github.com/nlink-jp/gem-agent/internal/agent"
)

// usageTally accumulates the token spend of tools that run on their own
// backends (summarize_file, web_search, web_fetch) — per-category
// accounting for /usage (ADR-0019). In memory on purpose: a display
// command must not reread a transcript that may hold megabytes of
// base64 images just to add integers.
type usageTally struct {
	mu      sync.Mutex
	order   []string
	entries map[string]*tallyEntry
}

type tallyEntry struct {
	model                 string
	calls, prompt, output int
}

func newUsageTally() *usageTally {
	return &usageTally{entries: map[string]*tallyEntry{}}
}

func (u *usageTally) add(tool, model string, prompt, output int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	e, ok := u.entries[tool]
	if !ok {
		e = &tallyEntry{model: model}
		u.entries[tool] = e
		u.order = append(u.order, tool)
	}
	e.calls++
	e.prompt += prompt
	e.output += output
}

// usageReport renders the /usage statement.
func usageReport(ag *agent.Agent, tally *usageTally, mainModel, summaryModel string) string {
	s := ag.Usage()
	var b strings.Builder

	fmt.Fprintf(&b, "main loop (%s):\n", mainModel)
	if s.Rounds == 0 {
		b.WriteString("  no requests yet\n")
	} else {
		fmt.Fprintf(&b, "  rounds %d · prompt %s · output %s · thoughts %s\n",
			s.Rounds, humanTok(s.Prompt), humanTok(s.Output), humanTok(s.Thoughts))
		cachePct := 0.0
		if s.Prompt > 0 {
			cachePct = 100 * float64(s.Cached) / float64(s.Prompt)
		}
		fmt.Fprintf(&b, "  cached %s of prompt (%.0f%%) — cache saves cost/latency, not window space\n",
			humanTok(s.Cached), cachePct)
		window := "unknown window"
		if s.Window > 0 {
			window = fmt.Sprintf("%s window (%.0f%%)", humanTok(s.Window), 100*float64(s.LastPrompt)/float64(s.Window))
		}
		fmt.Fprintf(&b, "  context now %s of %s\n", humanTok(s.LastPrompt), window)
	}
	if s.RiskCalls > 0 {
		fmt.Fprintf(&b, "auto-approve risk checks (%s): %d calls · prompt %s · output %s\n",
			mainModel, s.RiskCalls, humanTok(s.RiskPrompt), humanTok(s.RiskOutput))
	}
	if s.CompactCalls > 0 {
		fmt.Fprintf(&b, "compaction (%s): %d calls · prompt %s · output %s\n",
			mainModel, s.CompactCalls, humanTok(s.CompactPrompt), humanTok(s.CompactOutput))
	}

	tally.mu.Lock()
	for _, tool := range tally.order {
		e := tally.entries[tool]
		fmt.Fprintf(&b, "%s (%s): %d calls · prompt %s · output %s\n",
			tool, e.model, e.calls, humanTok(e.prompt), humanTok(e.output))
	}
	tally.mu.Unlock()

	_ = summaryModel
	return b.String()
}

// humanTok renders a token count compactly.
func humanTok(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
