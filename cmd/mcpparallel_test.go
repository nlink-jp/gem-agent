package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// slowServer answers tools/list after a delay, standing in for a server that
// goes over the network.
type slowServer struct {
	stubCaller
	delay time.Duration
}

func (s *slowServer) Close() {}

func (s *slowServer) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}, nil
}

// TestListingOverlaps pins the startup shape: the servers are listed at once,
// so startup costs the slowest one rather than the sum. Measured on a real
// 25-server configuration (2026-09-14), two servers went over the network —
// GitHub's remote MCP at ~1.9s and a Slack proxy at ~1.3s — and the other
// twenty-three answered in under 20ms each, so serially those two were the
// whole of startup and a slow day at either landed on it in full.
func TestListingOverlaps(t *testing.T) {
	const (
		each = 200 * time.Millisecond
		n    = 5
	)
	servers := make([]mcpServer, n)
	for i := range servers {
		servers[i] = &slowServer{stubCaller: stubCaller{name: fmt.Sprintf("s%d", i)}, delay: each}
	}

	start := time.Now()
	listed := make([]mcpListing, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Add(1)
		go func(i int, s mcpServer) {
			defer wg.Done()
			listed[i] = listMCPServer(context.Background(), s, 10*time.Second)
		}(i, s)
	}
	wg.Wait()
	elapsed := time.Since(start)

	if elapsed > each*2 {
		t.Errorf("listing %d servers of %v took %v — they are not overlapping", n, each, elapsed)
	}
	for i, l := range listed {
		if l.err != nil {
			t.Errorf("server %d: %v", i, l.err)
		}
	}
}

// The order the operator configured has to survive the overlap: the catalog,
// the registry and the warnings all read in that order, and only the waiting
// is concurrent.
func TestAttachOrderSurvivesParallelListing(t *testing.T) {
	reg, err := tools.New(t.TempDir(), func(ctx context.Context, c string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/true")
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"a", "b", "c"}
	inv := &mcpInventory{
		Servers:    names,
		Offered:    map[string][]string{},
		Scopes:     map[string]string{"a": "global", "b": "global", "c": "global"},
		configured: map[string]bool{"a": true, "b": true, "c": true},
		complete:   true,
		summary:    map[string]string{},
		registered: map[string][]string{},
	}
	var warn strings.Builder
	listed := make([]mcpListing, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			// Reverse delays: the last server finishes listing first.
			d := time.Duration(len(names)-i) * 20 * time.Millisecond
			listed[i] = listMCPServer(context.Background(),
				&slowServer{stubCaller: stubCaller{name: n}, delay: d}, 10*time.Second)
		}(i, n)
	}
	wg.Wait()

	for _, l := range listed {
		if !attachListedMCPServer(l, reg, &warn, filterOf(t), inv, nil) {
			t.Fatalf("%s did not attach", l.client.Name())
		}
	}
	got := inv.summaryLines()
	if len(got) != len(names) {
		t.Fatalf("summary = %v, want one line per server", got)
	}
	for i, n := range names {
		if !strings.HasPrefix(got[i], n+" ") {
			t.Errorf("summary[%d] = %q, want it to be %q — configured order must survive", i, got[i], n)
		}
	}
}
