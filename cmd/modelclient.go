package cmd

import (
	"context"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/llm"
)

// modelClient is what a session needs of its model client: the chat call
// and the two digest calls the web tools make, the thinking dial, the
// slots derived from it, the observer and retry recorder, and the context
// window lookup (ADR-0124 §2.2). The binary's is a *llm.Vertex; a test
// that runs the whole startup passes a scripted one.
type modelClient interface {
	llm.Backend
	webSearcher
	urlFetcher
	thinkingDial
	WithModel(name string) modelClient
	WithThinking(level, key string) modelClient
	SetObserver(fn func(llm.StreamEvent))
	SetRetryRecorder(fn func(llm.RetryEvent))
	ContextWindow(ctx context.Context) (int, error)
}

// vertexClient adapts *llm.Vertex to modelClient. It EMBEDS the client so
// ChatStream is promoted, not forwarded: a forwarding call would be a
// `.ChatStream(` in cmd/ that names no source, which the architecture
// test of ADR-0116 §1 refuses — rightly, since a forward has no source of
// its own to name. Only the two methods that derive a slot are wrapped,
// so the slot is a modelClient too.
type vertexClient struct{ *llm.Vertex }

func (v vertexClient) WithModel(name string) modelClient {
	return vertexClient{v.Vertex.WithModel(name)}
}

func (v vertexClient) WithThinking(level, key string) modelClient {
	return vertexClient{v.Vertex.WithThinking(level, key)}
}

// newModelClient builds the session's model client. It is the one seam
// the startup has for a test (ADR-0124 §2.2): the characterization tests
// replace it with a scripted model and nothing else.
var newModelClient = func(ctx context.Context, cfg *config.Config) (modelClient, error) {
	v, err := llm.NewVertex(ctx, cfg.GCP.Project, cfg.GCP.Location, cfg.Model.Name, cfg.Model.Safety, cfg.Model.Thinking, cfg.TUI.ShowThoughts)
	if err != nil {
		return nil, err
	}
	return vertexClient{v}, nil
}
