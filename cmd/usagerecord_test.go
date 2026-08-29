package cmd

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// capturingLog records the transcript writes of the cmd-side tools.
type capturingLog struct {
	kinds []string
	data  []any
}

func (c *capturingLog) Log(kind string, data any) error {
	c.kinds = append(c.kinds, kind)
	c.data = append(c.data, data)
	return nil
}

func (c *capturingLog) usage(t *testing.T) []session.UsageRecord {
	t.Helper()
	var out []session.UsageRecord
	for i, kind := range c.kinds {
		if kind != session.KindUsage {
			continue
		}
		r, ok := c.data[i].(session.UsageRecord)
		if !ok {
			t.Fatalf("record %d is %T, not a session.UsageRecord", i, c.data[i])
		}
		out = append(out, r)
	}
	return out
}

// ADR-0057: a side call is a model call, so it leaves the same
// accounting record as any other — named by source, priced by the model
// that actually billed. The web tools are the case where those differ:
// web_fetch runs on the light model.
func TestWebToolsLeaveAccountingRecords(t *testing.T) {
	log := &capturingLog{}
	reg, err := tools.New(t.TempDir(), func(ctx context.Context, c string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", c)
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerWebTools(reg, &fakeSearcher{answer: "a"}, &fakeFetcher{digest: "d", status: "ok"},
		"main-model", "light-model", log, newUsageTally()); err != nil {
		t.Fatal(err)
	}
	search, _ := reg.Get("web_search")
	if _, err := search.Run(context.Background(), map[string]any{"query": "q"}); err != nil {
		t.Fatal(err)
	}
	fetch, _ := reg.Get("web_fetch")
	if _, err := fetch.Run(context.Background(), map[string]any{"url": "https://example.com"}); err != nil {
		t.Fatal(err)
	}

	recs := log.usage(t)
	if len(recs) != 2 {
		t.Fatalf("want one accounting record per call, got %d: %+v", len(recs), recs)
	}
	if recs[0].Source != session.UsageWebSearch || recs[0].Model != "main-model" || recs[0].Prompt != 100 {
		t.Errorf("web_search record = %+v", recs[0])
	}
	if recs[1].Source != session.UsageWebFetch || recs[1].Model != "light-model" || recs[1].Prompt != 200 {
		t.Errorf("web_fetch record = %+v", recs[1])
	}

	// The descriptive records keep their diagnostics and lose their
	// token fields: two places to count is a double-counting bug.
	for i, kind := range log.kinds {
		if kind != "web_search" && kind != "web_fetch" {
			continue
		}
		m, ok := log.data[i].(map[string]any)
		if !ok {
			t.Fatalf("%s record is %T", kind, log.data[i])
		}
		if _, dup := m["prompt"]; dup {
			t.Errorf("%s still carries tokens beside the usage record: %v", kind, m)
		}
	}
}

// The one accounting shape, end to end: what the agent writes and what
// the tools write must be the same record, or an aggregator has to know
// which code path spent the tokens.
func TestUsageRecordShapeIsShared(t *testing.T) {
	log := &capturingLog{}
	logUsage(log, session.UsageSummarizeFile, "light-model",
		llm.Usage{Prompt: 10, Output: 2, Thoughts: 3, Cached: 4, Total: 15})
	recs := log.usage(t)
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	want := session.UsageRecord{Source: session.UsageSummarizeFile, Model: "light-model",
		Prompt: 10, Output: 2, Thoughts: 3, Cached: 4, Total: 15}
	if recs[0] != want {
		t.Errorf("record = %+v, want %+v", recs[0], want)
	}
	// A call that spent nothing is not an accounting event.
	logUsage(log, session.UsageWebSearch, "main-model", llm.Usage{})
	if len(log.usage(t)) != 1 {
		t.Error("a zero-token call wrote a record")
	}
}
