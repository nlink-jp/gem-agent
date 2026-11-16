package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/genai"
)

func TestThinkingLevelMapping(t *testing.T) {
	cases := map[string]genai.ThinkingLevel{
		"":        "",
		"minimal": genai.ThinkingLevelMinimal,
		"low":     genai.ThinkingLevelLow,
		"medium":  genai.ThinkingLevelMedium,
		"high":    genai.ThinkingLevelHigh,
		// ADR-0118 §2: the SDK has no constant for this one, so the wire
		// string itself is what the test holds.
		"xhigh": "XHIGH",
	}
	for in, want := range cases {
		if got := ThinkingLevel(in); got != want {
			t.Errorf("ThinkingLevel(%q) = %q, want %q", in, got, want)
		}
	}
}

// ADR-0025 §2: the summary model must not inherit the main model's
// thinking level.
func TestWithModelDoesNotInheritThinking(t *testing.T) {
	v := testVertex("main", "high")
	sub := v.WithModel("light")
	if sub.Thinking() != "" {
		t.Errorf("WithModel inherited thinking level %q", sub.Thinking())
	}
}

// ADR-0098 §5-6: the live dial moves the main backend and everything
// holding that same pointer, and never a copy WithModel or WithThinking
// made — at startup or after the turn.
func TestSetThinkingMovesOnlyTheMainDial(t *testing.T) {
	main := testVertex("main", "high")
	summary := main.WithModel("light")
	slot := main.WithModel("judge").WithThinking("low", "[model].risk_thinking")
	levelOnly := main.WithThinking("medium", "[model].risk_thinking")
	riding := main // the model tier and librarian without a slot

	main.SetThinking("low")
	if main.Thinking() != "low" || riding.Thinking() != "low" {
		t.Errorf("main = %q, riding = %q, want low", main.Thinking(), riding.Thinking())
	}
	if summary.Thinking() != "" || slot.Thinking() != "low" || levelOnly.Thinking() != "medium" {
		t.Errorf("a copy moved with the main dial: summary %q, slot %q, level-only %q",
			summary.Thinking(), slot.Thinking(), levelOnly.Thinking())
	}
	// A copy taken AFTER the change still gets its own level.
	if got := main.WithModel("light2").Thinking(); got != "" {
		t.Errorf("WithModel after SetThinking inherited %q", got)
	}
	main.SetThinking("")
	if main.Thinking() != "" {
		t.Errorf("default did not clear the level: %q", main.Thinking())
	}
}

// ADR-0098 §7: a level set live is fixed live, and the hint says how;
// a configured one still names its key.
func TestThinkingHintNamesWhereTheLevelCameFrom(t *testing.T) {
	base := errors.New("Error 400, Thinking level is unsupported")
	main := testVertex("main", "minimal")
	if got := thinkingHint(base, main.thinkingNow()).Error(); !strings.Contains(got, `"minimal"`) ||
		!strings.Contains(got, "/thinking default") || !strings.Contains(got, "[model].thinking") {
		t.Errorf("configured hint = %q", got)
	}
	// A slot is not moved by /thinking, so its hint names only its key.
	slot := main.WithModel("judge").WithThinking("minimal", "[model].risk_thinking")
	if got := thinkingHint(base, slot.thinkingNow()).Error(); !strings.Contains(got, `[model].risk_thinking = "minimal"`) || strings.Contains(got, "/thinking") {
		t.Errorf("slot hint = %q", got)
	}
	main.SetThinking("minimal")
	got := thinkingHint(base, main.thinkingNow())
	if !strings.Contains(got.Error(), "/thinking default") || !strings.Contains(got.Error(), `"minimal"`) {
		t.Errorf("live hint = %q", got)
	}
	if !errors.Is(got, base) {
		t.Error("the hint dropped the API error from the chain")
	}
}

// The dial is read by side calls while the UI writes it (run with -race).
func TestSetThinkingIsSafeForConcurrentUse(t *testing.T) {
	main := testVertex("main", "high")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); main.SetThinking("low") }()
		go func() { defer wg.Done(); _ = main.thinkingNow() }()
	}
	wg.Wait()
}

// ADR-0118 §2: the level the SDK has no name for still goes on the wire
// as the string the API reads, and the model's own default sends no
// level at all.
func TestXHighReachesTheWire(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`+"\n\n")
	}))
	t.Cleanup(srv.Close)
	v := localVertex(t, srv, "m")
	hist := []Message{{Role: RoleUser, Content: "hi"}}

	if _, err := v.ChatStream(context.Background(), "", hist, nil, nil); err != nil {
		t.Fatalf("default level: %v", err)
	}
	v.SetThinking("xhigh")
	if _, err := v.ChatStream(context.Background(), "", hist, nil, nil); err != nil {
		t.Fatalf("xhigh: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if strings.Contains(bodies[0], "thinkingLevel") {
		t.Errorf("the default level sent a level: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], `"thinkingLevel":"XHIGH"`) {
		t.Errorf("xhigh did not reach the wire as XHIGH: %s", bodies[1])
	}
}
