package cmd

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/mcpfilter"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// stubServer stands in for one MCP server process and counts what the
// connect path did to it: a toggle of another server must leave it
// untouched, a function toggle must re-list without respawning, and a
// server toggled off must be closed.
type stubServer struct {
	stubCaller
	tools  []string
	lists  int
	closed int
}

func (s *stubServer) ListTools(context.Context) ([]mcp.Tool, error) {
	s.lists++
	var out []mcp.Tool
	for _, n := range s.tools {
		out = append(out, mcp.Tool{Name: n, InputSchema: map[string]any{"type": "object"}})
	}
	return out, nil
}

func (s *stubServer) Close() { s.closed++ }

func reconnectHarness(t *testing.T) (*tools.Registry, *mcpInventory) {
	t.Helper()
	reg, err := tools.New(t.TempDir(), func(ctx context.Context, c string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/true")
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	inv := &mcpInventory{
		Servers:    []string{"a", "b"},
		Offered:    map[string][]string{},
		Scopes:     map[string]string{"a": "global", "b": "global"},
		configured: map[string]bool{"a": true, "b": true},
		complete:   true,
		summary:    map[string]string{},
		registered: map[string][]string{},
	}
	return reg, inv
}

func filterOf(t *testing.T, entries ...string) mcpfilter.Filter {
	t.Helper()
	f, err := mcpfilter.Build(entries, mcpfilter.PolicyScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func registered(reg *tools.Registry, names ...string) []string {
	var out []string
	for _, n := range names {
		if _, ok := reg.Get(n); ok {
			out = append(out, n)
		}
	}
	return out
}

// The field report: an arrow key on one server's row reconnected every
// server. Now the other server is neither closed nor re-listed, and its
// tools stay registered under the same names.
func TestReconnectTouchesOnlyTheNamedServer(t *testing.T) {
	reg, inv := reconnectHarness(t)
	a := &stubServer{stubCaller: stubCaller{name: "a"}, tools: []string{"x", "y"}}
	b := &stubServer{stubCaller: stubCaller{name: "b"}, tools: []string{"z"}}
	ctx := context.Background()
	var warn bytes.Buffer
	for _, s := range []*stubServer{a, b} {
		if !attachMCPServer(ctx, s, reg, &warn, filterOf(t), inv) {
			t.Fatalf("%s did not attach: %s", s.name, warn.String())
		}
	}
	bLists, bClosed := b.lists, b.closed

	kept := reconnectMCPServer(ctx, "a", a, func() mcpServer { t.Fatal("a is running; nothing to start"); return nil },
		reg, &warn, filterOf(t, "a/x"), inv)

	if kept != a {
		t.Fatalf("a running server toggled at the function level is reused, got %v", kept)
	}
	if b.lists != bLists || b.closed != bClosed {
		t.Errorf("b was touched: lists %d→%d closed %d→%d", bLists, b.lists, bClosed, b.closed)
	}
	if got := registered(reg, "mcp__a__x", "mcp__a__y", "mcp__b__z"); strings.Join(got, ",") != "mcp__a__y,mcp__b__z" {
		t.Errorf("registered after the toggle: %v", got)
	}
	if !reg.Excluded("mcp__a__x") {
		t.Error("the excluded function is not noted for the transcript")
	}
	if a.lists != 2 || a.closed != 0 {
		t.Errorf("a function toggle should re-list once without a respawn: lists=%d closed=%d", a.lists, a.closed)
	}
	if inv.summary["a"] != "a [global] (1 of 2 tools)" {
		t.Errorf("summary = %q", inv.summary["a"])
	}
}

// Off closes the one process and records the prefix; on starts one
// process and brings the tools back. The other server's line and tools
// survive both.
func TestReconnectServerOffClosesItAndOnStartsIt(t *testing.T) {
	reg, inv := reconnectHarness(t)
	a := &stubServer{stubCaller: stubCaller{name: "a"}, tools: []string{"x"}}
	b := &stubServer{stubCaller: stubCaller{name: "b"}, tools: []string{"z"}}
	ctx := context.Background()
	var warn bytes.Buffer
	attachMCPServer(ctx, a, reg, &warn, filterOf(t), inv)
	attachMCPServer(ctx, b, reg, &warn, filterOf(t), inv)

	kept := reconnectMCPServer(ctx, "a", a, nil, reg, &warn, filterOf(t, "a"), inv)
	if kept != nil {
		t.Fatalf("an excluded server is not kept, got %v", kept)
	}
	if a.closed != 1 {
		t.Errorf("the excluded server's process was not closed: closed=%d", a.closed)
	}
	if _, ok := reg.Get("mcp__a__x"); ok {
		t.Error("the excluded server's tool is still declared")
	}
	if !reg.Excluded("mcp__a__anything") {
		t.Error("the whole server is not noted by prefix")
	}
	if _, ok := inv.Offered["a"]; ok {
		t.Error("a server that is not running still offers functions to the panel")
	}
	if got := inv.summaryLines(); len(got) != 2 || !strings.Contains(got[0], "not started — excluded") || !strings.HasPrefix(got[1], "b [global] (1 tools)") {
		t.Errorf("summary lines = %q", got)
	}

	started := 0
	fresh := &stubServer{stubCaller: stubCaller{name: "a"}, tools: []string{"x"}}
	kept = reconnectMCPServer(ctx, "a", nil, func() mcpServer { started++; return fresh }, reg, &warn, filterOf(t), inv)
	if kept != fresh || started != 1 {
		t.Fatalf("turning a server on starts exactly one process: kept=%v started=%d", kept, started)
	}
	if _, ok := reg.Get("mcp__a__x"); !ok {
		t.Error("the tool did not come back")
	}
	if reg.Excluded("mcp__a__x") {
		t.Error("yesterday's exclusion survived the reconnect")
	}
	if b.closed != 0 || b.lists != 1 {
		t.Errorf("b was touched: closed=%d lists=%d", b.closed, b.lists)
	}
	if warn.Len() != 0 {
		t.Errorf("nothing went wrong, but the panel would be told: %q", warn.String())
	}
}

// A stale entry is reported against the whole inventory, whichever
// server the toggle named — and only as a warning line, which is what
// the panel keeps.
func TestReconnectReportsAStaleEntry(t *testing.T) {
	reg, inv := reconnectHarness(t)
	a := &stubServer{stubCaller: stubCaller{name: "a"}, tools: []string{"x"}}
	ctx := context.Background()
	var warn bytes.Buffer
	attachMCPServer(ctx, a, reg, &warn, filterOf(t), inv)

	// Named against the server that listed: a function of one that
	// never listed proves nothing (ADR-0077 §2).
	reconnectMCPServer(ctx, "a", a, nil, reg, &warn, filterOf(t, "a/nope"), inv)
	if got := reloadWarnings(warn.String()); !strings.Contains(got, "nope") {
		t.Errorf("the stale entry is not reported: %q", warn.String())
	}
}

// The names a reconnect removes are the ones the server's attach
// recorded, not everything under its prefix. The prefix has two edges
// that a transcript record could live with and a removal cannot: a
// neighbour named "<server>__*" shares it, and a name near the 64-char
// cap is truncated past it (pre-release review).
func TestReconnectRemovesTheServersOwnNamesNotAPrefix(t *testing.T) {
	ctx := context.Background()

	t.Run("a neighbour sharing the prefix keeps its tools", func(t *testing.T) {
		reg, inv := reconnectHarness(t)
		inv.Servers = []string{"foo", "foo__bar"}
		inv.Scopes["foo__bar"], inv.configured["foo__bar"] = "global", true
		foo := &stubServer{stubCaller: stubCaller{name: "foo"}, tools: []string{"x", "w"}}
		bar := &stubServer{stubCaller: stubCaller{name: "foo__bar"}, tools: []string{"y"}}
		var warn bytes.Buffer
		attachMCPServer(ctx, foo, reg, &warn, filterOf(t), inv)
		attachMCPServer(ctx, bar, reg, &warn, filterOf(t), inv)

		reconnectMCPServer(ctx, "foo", foo, nil, reg, &warn, filterOf(t, "foo/x"), inv)
		if _, ok := reg.Get("mcp__foo__bar__y"); !ok {
			t.Error("toggling foo took foo__bar's tool with it")
		}
		if bar.closed != 0 || bar.lists != 1 {
			t.Errorf("foo__bar was touched: closed=%d lists=%d", bar.closed, bar.lists)
		}
		if got := registered(reg, "mcp__foo__x", "mcp__foo__w"); strings.Join(got, ",") != "mcp__foo__w" {
			t.Errorf("foo's own tools after the toggle: %v", got)
		}
	})

	t.Run("a name truncated past its prefix is still removed", func(t *testing.T) {
		reg, inv := reconnectHarness(t)
		long := strings.Repeat("s", 50)
		inv.Servers = []string{long}
		inv.Scopes[long], inv.configured[long] = "global", true
		srv := &stubServer{stubCaller: stubCaller{name: long}, tools: []string{"function_one", "function_two"}}
		var warn bytes.Buffer
		if !attachMCPServer(ctx, srv, reg, &warn, filterOf(t), inv) {
			t.Fatalf("did not attach: %s", warn.String())
		}
		if n := mcpToolName(long, "function_one"); strings.HasPrefix(n, mcpToolPrefix(long)) {
			t.Fatalf("test premise: %q should be truncated past its prefix", n)
		}

		kept := reconnectMCPServer(ctx, long, srv, nil, reg, &warn, filterOf(t, long+"/function_one"), inv)
		if kept != srv {
			t.Fatalf("re-registration failed and the server was dropped: %s", warn.String())
		}
		if _, ok := reg.Get(mcpToolName(long, "function_one")); ok {
			t.Error("the excluded function is still declared")
		}
		if _, ok := reg.Get(mcpToolName(long, "function_two")); !ok {
			t.Error("the kept function is gone")
		}
		if warn.Len() != 0 {
			t.Errorf("warnings: %q", warn.String())
		}
	})
}
