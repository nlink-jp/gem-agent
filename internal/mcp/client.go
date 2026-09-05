package mcp

import (
	"github.com/nlink-jp/gem-agent/internal/bounded"

	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
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

// CallError is a tools/call that did not return a result (ADR-0075 §1):
// Err is either the server's *RPCError — the server's own words,
// delivered as a rejection — or a cause of gem-agent's own (transport,
// timeout, exit, framing). Sent records whether the tools/call request
// with the model's arguments was written to the server: false when the
// server could not be started (its initialize may itself have been
// refused with an RPCError — that is not a rejection of the call) or
// the request could not be written. The adapter reads both facts by
// type, never by matching text.
type CallError struct {
	Server string
	Tool   string
	Err    error
	Sent   bool
}

// startError is an ensureStarted failure: the server could not be
// spawned (Phase ""), or refused initialize or the initialized
// notification. Its text is the one ListTools always showed; CallTool
// hands the adapter the cause under the server's name instead.
type startError struct {
	Server string
	Phase  string
	Err    error
}

func (e *startError) Error() string {
	if e.Phase == "" {
		return fmt.Sprintf("mcp %s: %v", e.Server, e.Err)
	}
	return fmt.Sprintf("mcp %s: %s: %v", e.Server, e.Phase, e.Err)
}

func (e *startError) Unwrap() error { return e.Err }

// cause is the failure without the server's name, the phase kept.
func (e *startError) cause() error {
	if e.Phase == "" {
		return e.Err
	}
	return fmt.Errorf("%s: %w", e.Phase, e.Err)
}

// notSentError marks a request that never reached the server's stdin.
type notSentError struct{ err error }

func (e *notSentError) Error() string { return e.err.Error() }
func (e *notSentError) Unwrap() error { return e.err }

func (e *CallError) Error() string {
	return fmt.Sprintf("mcp %s: %s: %v", e.Server, e.Tool, e.Err)
}

func (e *CallError) Unwrap() error { return e.Err }

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

	mu    sync.Mutex // lifecycle state
	wmu   sync.Mutex // stdin writes (reader goroutine also writes replies)
	stdin io.WriteCloser
	kill  func()
	alive bool
	gen   int // spawn generation; read loops of dead incarnations must not touch newer state
	// endCause says why the current incarnation's read loop ended
	// (scanner error — the frame cap — or plain EOF), for the waiters
	// it fails (ADR-0072 §4.8).
	endCause string
	nextID   int64

	pmu     sync.Mutex
	pending map[int64]chan message
}

// NewStdio creates a client that spawns command with args and extra env.
// The child's stderr is discarded unless GEMAGENT_MCP_STDERR=1 (MCP
// servers log there; in a REPL that noise drowns the conversation).
func NewStdio(name string, cfg ServerConfig, timeout time.Duration, clientVersion string) *Client {
	spawn := func() (io.WriteCloser, io.ReadCloser, func(), error) {
		cmd := exec.Command(cfg.Command, cfg.Args...)
		// Its own process group, killed as a group (ADR-0072 §4.5): a
		// wrapper's child — the server behind an npx or uvx launcher —
		// outlived a timeout that killed only the direct child.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
		var once sync.Once
		kill := func() {
			// Once: shutdown and the read loop's EOF both reach here,
			// and Wait may run only once per process.
			once.Do(func() {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				_ = cmd.Process.Kill()
				go func() { _ = cmd.Wait() }() // reap; also unblocks pipe reads
			})
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
		return &startError{Server: c.name, Err: err}
	}
	c.stdin = stdin
	c.kill = kill
	c.alive = true
	c.endCause = ""
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
		return &startError{Server: c.name, Phase: "initialize", Err: err}
	}
	if err := c.send(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	}, -1); err != nil {
		c.shutdown()
		return &startError{Server: c.name, Phase: "initialized notification", Err: err}
	}
	return nil
}

// readLoop routes responses to pending calls, drops notifications, and
// politely refuses server-initiated requests (unanswered requests could
// hang a server that waits for the reply).
func (c *Client) readLoop(stdout io.ReadCloser, gen int) {
	scanner := bounded.Scanner(stdout, scannerInitial, scannerMax)
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
	cause := "server exited"
	if err := scanner.Err(); err != nil {
		cause = "server stream ended: " + err.Error()
		if errors.Is(err, bufio.ErrTooLong) {
			cause = fmt.Sprintf("server sent a response line over the %d-byte frame cap", scannerMax)
		}
	}
	c.mu.Lock()
	stale := c.gen != gen
	var kill func()
	if !stale {
		c.alive = false
		c.endCause = cause
		kill = c.kill
	}
	c.mu.Unlock()
	if stale {
		return
	}
	// The incarnation is over whichever side ended it: reap the child
	// (a server that exited on its own stayed a zombie with both pipes
	// open — review round 4) and, for an over-long line that ended the
	// scan, kill the still-running server rather than orphan it beside
	// its successor. Idempotent for an exited child.
	if kill != nil {
		kill()
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
		return nil, &notSentError{err: err}
	}

	tctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	select {
	case msg, ok := <-ch:
		if !ok {
			c.mu.Lock()
			cause := c.endCause
			c.mu.Unlock()
			if cause == "" {
				cause = "server exited"
			}
			return nil, fmt.Errorf("%s during %s", cause, method)
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
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		// Bounds on a foreign server's pagination (ADR-0072 §4.5): a
		// cursor that repeats, too many pages, or too many tools ends
		// the listing with the server's name, not a 30-second wait.
		if pages >= maxToolListPages {
			return nil, fmt.Errorf("mcp %s: tools/list ran past %d pages", c.name, maxToolListPages)
		}
		if cursor != "" && seen[cursor] {
			return nil, fmt.Errorf("mcp %s: tools/list repeated cursor %q", c.name, cursor)
		}
		seen[cursor] = true
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
		if len(all) > maxToolListTools {
			return nil, fmt.Errorf("mcp %s: tools/list returned more than %d tools", c.name, maxToolListTools)
		}
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// maxToolListPages / maxToolListTools bound a server's tool listing.
const (
	maxToolListPages = 100
	maxToolListTools = 5000
)

// Content is one block of a tool result. MCP servers may answer with
// more than text — an image, audio, a resource — and this type carries
// each block whole rather than flattening it. Text blocks fill Text;
// binary blocks fill Data (already base64-decoded) and MIME.
type Content struct {
	Type string
	Text string
	Data []byte
	MIME string
}

// CallTool invokes one tool and returns its content blocks in order,
// plus whether the server marked the call failed (isError). The blocks
// are returned whole because the caller, not this client, decides what
// to do with them: a block too large for the model's context and a
// block that is not text at all both have to land somewhere on disk,
// and this package knows nothing about where that is.
func (c *Client) CallTool(ctx context.Context, tool string, args map[string]any) ([]Content, bool, error) {
	if err := c.ensureStarted(ctx); err != nil {
		// Not sent: the call never left. The adapter shows the cause
		// under the server's name, so the start failure is handed over
		// without the name and with its phase (initialize, spawn).
		var se *startError
		if errors.As(err, &se) {
			err = se.cause()
		}
		return nil, false, &CallError{Server: c.name, Tool: tool, Err: err}
	}
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.rawCall(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		var ns *notSentError
		if errors.As(err, &ns) {
			return nil, false, &CallError{Server: c.name, Tool: tool, Err: ns.err}
		}
		return nil, false, &CallError{Server: c.name, Tool: tool, Err: err, Sent: true}
	}
	var out struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, false, &CallError{Server: c.name, Tool: tool, Err: fmt.Errorf("result: %w", err), Sent: true}
	}
	blocks := make([]Content, 0, len(out.Content))
	for _, item := range out.Content {
		b := Content{Type: item.Type, Text: item.Text, MIME: item.MimeType}
		if item.Data != "" {
			// A block that says it carries data and whose data will not
			// decode is reported as text rather than dropped: silently
			// losing a server's answer is the failure this whole change
			// exists to stop.
			raw, derr := base64.StdEncoding.DecodeString(item.Data)
			if derr != nil {
				b.Type = "text"
				b.Text = fmt.Sprintf("[%s content could not be decoded: %v]", item.Type, derr)
			} else {
				b.Data = raw
			}
		}
		blocks = append(blocks, b)
	}
	return blocks, out.IsError, nil
}
