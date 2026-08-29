package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

type fakeSearcher struct {
	answer  string
	sources []llm.WebSource
	err     error
	query   string
}

func (f *fakeSearcher) SearchWeb(ctx context.Context, q string) (string, []llm.WebSource, llm.Usage, error) {
	f.query = q
	return f.answer, f.sources, llm.Usage{Prompt: 100, Output: 10}, f.err
}

type fakeFetcher struct {
	digest, status string
	err            error
	prompt         string
}

func (f *fakeFetcher) FetchURL(ctx context.Context, prompt string) (string, string, llm.Usage, error) {
	f.prompt = prompt
	return f.digest, f.status, llm.Usage{Prompt: 200, Output: 20}, f.err
}

func webSetup(t *testing.T, s *fakeSearcher, f *fakeFetcher) *tools.Registry {
	t.Helper()
	reg, err := tools.New(t.TempDir(), func(ctx context.Context, c string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", c)
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerWebTools(reg, s, f, "main-model", "light-model", nil, newUsageTally()); err != nil {
		t.Fatal(err)
	}
	return reg
}

// Both tools are egress: the query/URL itself leaves the machine, so the
// gate applies by default and the operator relaxes it via ADR-0008.
func TestWebToolsAreEgressGatedByDefault(t *testing.T) {
	reg := webSetup(t, &fakeSearcher{}, &fakeFetcher{})
	for _, name := range []string{"web_search", "web_fetch"} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if !tool.Mutating {
			t.Errorf("%s must be approval-gated by default (egress)", name)
		}
	}
}

func TestWebSearchRendersAnswerAndSources(t *testing.T) {
	s := &fakeSearcher{
		answer: "The answer is 42.",
		sources: []llm.WebSource{
			{Title: "Deep Thought FAQ", Domain: "example.org", URI: "https://example.org/faq"},
			{Title: "Guide", Domain: "example.com", URI: "https://example.com/g"},
		},
	}
	reg := webSetup(t, s, &fakeFetcher{})
	tool, _ := reg.Get("web_search")
	out, err := tool.Run(context.Background(), map[string]any{"query": "meaning of life"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"The answer is 42.", "Sources:", "Deep Thought FAQ (example.org) https://example.org/faq"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if s.query != "meaning of life" {
		t.Errorf("query = %q", s.query)
	}

	// No sources: a claim that cannot be checked is flagged, not dressed up.
	s.sources = nil
	out, _ = tool.Run(context.Background(), map[string]any{"query": "q"})
	if !strings.Contains(out, "no grounding sources") {
		t.Errorf("sourceless answer not flagged:\n%s", out)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"query": "  "}); err == nil {
		t.Error("empty query accepted")
	}
}

func TestWebFetchAsksForExtractionAndNamesItself(t *testing.T) {
	f := &fakeFetcher{digest: "- point one\n- point two", status: "https://example.com → URL_RETRIEVAL_STATUS_SUCCESS"}
	reg := webSetup(t, &fakeSearcher{}, f)
	tool, _ := reg.Get("web_fetch")
	out, err := tool.Run(context.Background(), map[string]any{"url": "https://example.com", "focus": "版数"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Digest of https://example.com", "by light-model", "untrusted web content", "retrieval:", "point one"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// The prompt carries the operator's requested shape: organized
	// extraction, the focus, and the defensive framing (the page cannot
	// be nonce-wrapped — it is fetched server-side).
	for _, want := range []string{"organized extraction", "Focus especially on: 版数", "never instructions", "https://example.com"} {
		if !strings.Contains(f.prompt, want) {
			t.Errorf("fetch prompt missing %q:\n%s", want, f.prompt)
		}
	}

	for _, bad := range []string{"ftp://x", "file:///etc/passwd", "example.com", ""} {
		if _, err := tool.Run(context.Background(), map[string]any{"url": bad}); err == nil {
			t.Errorf("url %q accepted", bad)
		}
	}
}

func TestWebToolErrorsPropagate(t *testing.T) {
	reg := webSetup(t,
		&fakeSearcher{err: fmt.Errorf("grounded search returned nothing (PROHIBITED_CONTENT)")},
		&fakeFetcher{err: fmt.Errorf("fetch returned nothing (STOP; retrieval: x → URL_RETRIEVAL_STATUS_PAYWALL)")})
	tool, _ := reg.Get("web_search")
	if _, err := tool.Run(context.Background(), map[string]any{"query": "q"}); err == nil || !strings.Contains(err.Error(), "PROHIBITED_CONTENT") {
		t.Errorf("search error: %v", err)
	}
	tool, _ = reg.Get("web_fetch")
	if _, err := tool.Run(context.Background(), map[string]any{"url": "https://x.example"}); err == nil || !strings.Contains(err.Error(), "PAYWALL") {
		t.Errorf("fetch error: %v", err)
	}
}
