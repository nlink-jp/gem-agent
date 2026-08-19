package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// protocolVersion is the MCP revision this client speaks. Servers
// negotiate: we accept whatever version the server answers with.
const protocolVersion = "2025-06-18"

const (
	scannerInitial = 64 * 1024
	scannerMax     = 10 * 1024 * 1024
)

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// message is a decoded JSON-RPC frame (request, response, or notification).
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.Number    `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Tool is one tool advertised by an MCP server.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// spawnFunc starts the server process and returns its stdin, stdout, and
// a kill function. Injected so tests can run a fake server over pipes.
type spawnFunc func() (io.WriteCloser, io.ReadCloser, func(), error)

// Client is a stdio MCP client for one server. Calls are synchronous; a
// timed-out or cancelled call kills the child (MCP has no cancel
// notification — kill-and-respawn is the only interruption mechanism)
// and the next call respawns it lazily.
type Client struct {
	name    string
	spawn   spawnFunc
	timeout time.Duration
	version string

	mu     sync.Mutex // lifecycle state
	wmu    sync.Mutex // stdin writes (reader goroutine also writes replies)
	stdin  io.WriteCloser
	kill   func()
	alive  bool
	gen    int // spawn generation; read loops of dead incarnations must not touch newer state
	nextID int64

	pmu     sync.Mutex
	pending map[int64]chan message
}

// NewStdio creates a client that spawns command with args and extra env.
// The child's stderr is discarded unless GEMAGENT_MCP_STDERR=1 (MCP
// servers log there; in a REPL that noise drowns the conversation).
func NewStdio(name string, cfg ServerConfig, timeout time.Duration, clientVersion string) *Client {
	spawn := func() (io.WriteCloser, io.ReadCloser, func(), error) {
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		if os.Getenv("GEMAGENT_MCP_STDERR") == "1" {
			cmd.Stderr = os.Stderr
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, nil, fmt.Errorf("start %s: %w", cfg.Command, err)
		}
		kill := func() {
			_ = cmd.Process.Kill()
			go func() { _ = cmd.Wait() }() // reap; also unblocks pipe reads
		}
		return stdin, stdout, kill, nil
	}
	return newClient(name, spawn, timeout, clientVersion)
}

func newClient(name string, spawn spawnFunc, timeout time.Duration, version string) *Client {
	return &Client{
		name:    name,
		spawn:   spawn,
		timeout: timeout,
		version: version,
		pending: map[int64]chan message{},
	}
}

// Name returns the server name from .mcp.json.
func (c *Client) Name() string { return c.name }

// Close kills the server process if running.
func (c *Client) Close() { c.shutdown() }

func (c *Client) shutdown() {
	c.mu.Lock()
	if c.alive {
		c.alive = false
		if c.kill != nil {
			c.kill()
		}
	}
	c.mu.Unlock()
}

// ensureStarted spawns and handshakes if the server is not running.
func (c *Client) ensureStarted(ctx context.Context) error {
	c.mu.Lock()
	if c.alive {
		c.mu.Unlock()
		return nil
	}
	stdin, stdout, kill, err := c.spawn()
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("mcp %s: %w", c.name, err)
	}
	c.stdin = stdin
	c.kill = kill
	c.alive = true
	c.gen++
	gen := c.gen
	c.mu.Unlock()

	go c.readLoop(stdout, gen)

	initParams := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "gem-agent", "version": c.version},
	}
	if _, err := c.rawCall(ctx, "initialize", initParams); err != nil {
		c.shutdown()
		return fmt.Errorf("mcp %s: initialize: %w", c.name, err)
	}
	if err := c.send(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	}, -1); err != nil {
		c.shutdown()
		return fmt.Errorf("mcp %s: initialized notification: %w", c.name, err)
	}
	return nil
}

// readLoop routes responses to pending calls, drops notifications, and
// politely refuses server-initiated requests (unanswered requests could
// hang a server that waits for the reply).
func (c *Client) readLoop(stdout io.ReadCloser, gen int) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, scannerInitial), scannerMax)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // non-JSON noise on stdout; ignore
		}
		switch {
		case msg.Method != "" && msg.ID != nil:
			// Refuse server-initiated requests — but never write from
			// the read loop itself: a blocking write while the peer is
			// not reading deadlocks both directions.
			id := *msg.ID
			go func() {
				// gen-pinned: after a kill-and-respawn this refusal
				// belongs to the dead incarnation and is dropped.
				_ = c.send(map[string]any{
					"jsonrpc": "2.0", "id": id,
					"error": map[string]any{"code": -32601, "message": "method not supported by gem-agent"},
				}, gen)
			}()
		case msg.Method != "":
			// notification — nothing to route
		case msg.ID != nil:
			if id, err := msg.ID.Int64(); err == nil {
				c.pmu.Lock()
				ch := c.pending[id]
				delete(c.pending, id)
				c.pmu.Unlock()
				if ch != nil {
					ch <- msg
				}
			}
		}
	}
	// EOF: the server exited (or we killed it). Fail all waiters — but
	// only if a newer incarnation has not already been spawned: a stale
	// loop draining after kill-and-respawn must not close the pending
	// channels (e.g. the fresh initialize) of its successor. Stale
	// waiters of THIS incarnation are covered by their per-call timeouts.
	c.mu.Lock()
	stale := c.gen != gen
	if !stale {
		c.alive = false
	}
	c.mu.Unlock()
	if stale {
		return
	}
	c.pmu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pmu.Unlock()
}

// send writes one frame to the current incarnation's stdin. gen pins
// the frame to the incarnation that produced it: a stale read loop's
// refusal must be dropped, not injected into a successor's stdin
// mid-handshake (a response with an id the fresh server never issued).
// gen < 0 means "whatever is current". The stdin snapshot is taken
// under mu — reading the field under wmu alone raced ensureStarted's
// write of it (ADR-0021).
func (c *Client) send(v any, gen int) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	stdin, cur, alive := c.stdin, c.gen, c.alive
	c.mu.Unlock()
	if gen >= 0 && gen != cur {
		return nil // stale incarnation's frame: drop
	}
	if stdin == nil || !alive {
		return fmt.Errorf("mcp %s: server not running", c.name)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	// Writing through the snapshot: if a respawn swapped stdin after it
	// was taken, this hits the dead pipe and fails — the safe direction.
	_, err = stdin.Write(append(data, '\n'))
	return err
}

// rawCall issues one request and waits for its response, bounded by the
// per-call timeout. On timeout/cancel the child is killed — the next
// call respawns it.
func (c *Client) rawCall(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	ch := make(chan message, 1)
	c.pmu.Lock()
	c.pending[id] = ch
	c.pmu.Unlock()

	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}, -1); err != nil {
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return nil, err
	}

	tctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	select {
	case msg, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("server exited during %s", method)
		}
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	case <-tctx.Done():
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		// No cancel notification exists in MCP: kill and respawn lazily.
		c.shutdown()
		return nil, fmt.Errorf("%s timed out after %s (server killed; it restarts on the next call)", method, c.timeout)
	}
}

// ListTools fetches the server's tool list (following pagination).
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	if err := c.ensureStarted(ctx); err != nil {
		return nil, err
	}
	var all []Tool
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.rawCall(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("mcp %s: tools/list: %w", c.name, err)
		}
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &page); err != nil {
			return nil, fmt.Errorf("mcp %s: tools/list result: %w", c.name, err)
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// CallTool invokes one tool. The result is always a string for the LLM:
// text content concatenated, non-text content noted, and server-side
// tool failures (isError) prefixed with "error: " rather than dropped.
func (c *Client) CallTool(ctx context.Context, tool string, args map[string]any) (string, error) {
	if err := c.ensureStarted(ctx); err != nil {
		return "", err
	}
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.rawCall(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", fmt.Errorf("mcp %s: %s: %w", c.name, tool, err)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", fmt.Errorf("mcp %s: %s result: %w", c.name, tool, err)
	}
	var b []byte
	for i, item := range out.Content {
		if i > 0 {
			b = append(b, '\n')
		}
		if item.Type == "text" {
			b = append(b, item.Text...)
		} else {
			b = append(b, ("[non-text content: " + item.Type + "]")...)
		}
	}
	text := string(b)
	if text == "" {
		text = "(no content)"
	}
	if out.IsError {
		return "error: " + text, nil
	}
	return text, nil
}
