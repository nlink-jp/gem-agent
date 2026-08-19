package mcp

import (
	"strings"
	"sync"
	"testing"
)

// fakeStdin records what reaches one incarnation's stdin.
type fakeStdin struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (f *fakeStdin) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(p)
}
func (f *fakeStdin) Close() error { return nil }
func (f *fakeStdin) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.String()
}

// ADR-0021: a stale incarnation's frame (the read loop's refusal of a
// server-initiated request, racing a kill-and-respawn) is dropped, not
// injected into the successor's stdin mid-handshake.
func TestStaleGenerationSendIsDropped(t *testing.T) {
	c := newClient("x", nil, 0, "test")
	gen1 := &fakeStdin{}
	gen2 := &fakeStdin{}

	c.mu.Lock()
	c.stdin, c.alive, c.gen = gen1, true, 1
	c.mu.Unlock()
	if err := c.send(map[string]any{"id": 1, "from": "gen1"}, 1); err != nil {
		t.Fatal(err)
	}

	// Respawn: generation 2 takes over.
	c.mu.Lock()
	c.stdin, c.gen = gen2, 2
	c.mu.Unlock()

	// A refusal produced by gen 1's read loop arrives late.
	if err := c.send(map[string]any{"id": 7, "from": "stale-gen1"}, 1); err != nil {
		t.Fatalf("stale send must be a silent drop, got %v", err)
	}
	// A current-generation frame still goes through.
	if err := c.send(map[string]any{"id": 2, "from": "gen2"}, -1); err != nil {
		t.Fatal(err)
	}

	if got := gen2.String(); strings.Contains(got, "stale-gen1") {
		t.Errorf("stale frame reached the new incarnation's stdin: %q", got)
	}
	if !strings.Contains(gen2.String(), "gen2") {
		t.Error("current-generation frame did not reach stdin")
	}
	if !strings.Contains(gen1.String(), `"from":"gen1"`) {
		t.Errorf("gen1 frame missing: %q", gen1.String())
	}
}

// A send with no live server errors instead of dereferencing nil stdin.
func TestSendWithoutServerFailsClosed(t *testing.T) {
	c := newClient("x", nil, 0, "test")
	if err := c.send(map[string]any{"id": 1}, -1); err == nil {
		t.Error("send with no server must error")
	}
}
