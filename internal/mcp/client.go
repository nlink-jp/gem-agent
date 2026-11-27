package mcp

import (
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/workdir"

	mcpproto "github.com/nlink-jp/mcp"
	"github.com/nlink-jp/mcp/client"

	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// scannerMax bounds one line from the server (ADR-0072 §4.8). The name is
// kept from the hand-written reader because ADR-0090 cites it.
const scannerMax = 10 * 1024 * 1024

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
// with the model's arguments may have reached the server: false when the
// server could not be started (its handshake may itself have been
// refused with an RPCError — that is not a rejection of the call) or
// the request is known not to have been written (ADR-0126 §3). The
// adapter reads both facts by type, never by matching text.
type CallError struct {
	Server string
	Tool   string
	Err    error
	Sent   bool
}

// startError is an ensureStarted failure: the server could not be
// spawned (Phase ""), or could not be opened (Phase "open": the
// server/discover probe and, for a legacy server, initialize and the
// initialized notification). Its text is the one ListTools always showed;
// CallTool hands the adapter the cause under the server's name instead.
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

func (e *CallError) Error() string {
	return fmt.Sprintf("mcp %s: %s: %v", e.Server, e.Tool, e.Err)
}

func (e *CallError) Unwrap() error { return e.Err }

// worded is a failure whose text this package writes — the budget that ran
// out, the reason a server ended — over the module's error, which stays
// reachable with errors.Is (ErrNotSent, ErrLineTooLong, a context error).
type worded struct {
	text string
	err  error
}

func (e *worded) Error() string { return e.text }
func (e *worded) Unwrap() error { return e.err }

// Tool is one tool advertised by an MCP server.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// spawnFunc starts the server process and returns its stdin, stdout, and
// a kill function. Injected so tests can run a fake server over pipes.
// kill may be called more than once.
type spawnFunc func() (io.WriteCloser, io.ReadCloser, func(), error)

// incarnation is one start of the server: the module's Client opened on
// it and the kill that ends its process (ADR-0126 §1). Nothing outlives
// it — a successor gets a Client of its own, so a frame of a dead start
// cannot reach a live one's stdin.
type incarnation struct {
	cl   *client.Client
	kill func()
}

// Client is a stdio MCP client for one server. Calls are synchronous; a
// timed-out or cancelled call kills the child and the next call respawns
// it lazily. The protocol — the server/discover probe, initialize for a
// legacy server, framing, routing answers, refusing the server's
// requests — is nlink-jp/mcp's (organization ADR-030); this type owns
// the process: when it starts, when it is killed, and what its failures
// mean to the runtime.
//
// MCP does define notifications/cancelled (requestId, reason; in the spec
// since 2024-11-05), but the receiver may ignore it — most servers do —
// and nothing acknowledges it either way, so the kill is what reliably
// unblocks the reader. gem-agent does not send it: the kill follows in
// the same step, which leaves a cooperating server no time to act, and a
// server that stopped reading its stdin would park the write, and the
// kill behind it, for the send's whole deadline.
type Client struct {
	name    string
	spawn   spawnFunc
	timeout time.Duration
	// startupTimeout bounds the start — spawn and the module's Open — separately
	// from a tool call's budget. They are different things: a call's budget is
	// how long the work may take, a start's is how long a server may take to
	// say hello, and a server that is slow to greet is usually slow because of
	// something outside this machine (a remote MCP over HTTPS). Cutting it
	// short does not save time overall, because the session then has to
	// reconnect it, so this is a separate number rather than a smaller one.
	startupTimeout time.Duration
	version        string

	// workDir is this session's work directory, attached to every
	// tools/call as request _meta (ADR-021 §9). Empty means attach
	// nothing, which is what a session without a work directory wants.
	workDir string

	// startSem holds one start at a time. A channel rather than a mutex,
	// so a second caller waiting on a start still answers to its own ctx.
	startSem chan struct{}
	mu       sync.Mutex // guards cur, starting
	cur      *incarnation
	// starting is the kill of a start still being opened, so that Close
	// reaches a process that is not yet the current incarnation.
	starting func()
}

// NewStdio creates a client that spawns command with args and extra env.
// The child's stderr is discarded unless GEMAGENT_MCP_STDERR=1 (MCP
// servers log there; in a REPL that noise drowns the conversation).
func NewStdio(name string, cfg ServerConfig, timeout, startupTimeout time.Duration, clientVersion, workDir string) *Client {
	spawn := func() (io.WriteCloser, io.ReadCloser, func(), error) {
		cmd := exec.Command(cfg.Command, cfg.Args...)
		// Its own process group, killed as a group (ADR-0072 §4.5): a
		// wrapper's child — the server behind an npx or uvx launcher —
		// outlived a timeout that killed only the direct child.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// The runtime's own configuration variables reach no child
		// (ADR-0087 §2). A server that wants one takes it from its own
		// `env` block below, where a server's inputs belong.
		cmd.Env = sandbox.ChildEnv(os.Environ())
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
			// Once: a timeout, the watcher and the module's abort of a
			// stuck write can all reach here, and Wait may run only once
			// per process.
			once.Do(func() {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				_ = cmd.Process.Kill()
				go func() { _ = cmd.Wait() }() // reap; also unblocks pipe reads
			})
		}
		return stdin, stdout, kill, nil
	}
	return newClient(name, spawn, timeout, startupTimeout, clientVersion, workDir)
}

func newClient(name string, spawn spawnFunc, timeout, startupTimeout time.Duration, version, workDir string) *Client {
	if startupTimeout <= 0 {
		startupTimeout = timeout
	}
	return &Client{
		name:           name,
		spawn:          spawn,
		timeout:        timeout,
		startupTimeout: startupTimeout,
		version:        version,
		workDir:        workDir,
		startSem:       make(chan struct{}, 1),
	}
}

// Name returns the server name from .mcp.json.
func (c *Client) Name() string { return c.name }

// Close kills the server process if running.
func (c *Client) Close() {
	c.mu.Lock()
	inc, starting := c.cur, c.starting
	c.starting = nil
	c.mu.Unlock()
	if starting != nil {
		starting() // its Open fails, and nothing becomes current
	}
	if inc != nil {
		c.drop(inc)
	}
}

// drop ends one incarnation: it is no longer the current one, and its
// process is killed and reaped. Dropping a start that a successor already
// replaced leaves the successor alone.
func (c *Client) drop(inc *incarnation) {
	c.mu.Lock()
	if c.cur == inc {
		c.cur = nil
	}
	c.mu.Unlock()
	inc.kill()
}

// watch reaps an incarnation as soon as reading from it ends — the server
// exited, or sent a line over the cap — instead of leaving it to the next
// call: an exited server stayed a zombie with both pipes open, and one
// that sent an over-long line kept running beside its successor (review
// round 4; ADR-0072 §4.8). The module only closes Done; the kill is ours.
func (c *Client) watch(inc *incarnation) {
	<-inc.cl.Done()
	c.drop(inc)
}

// live is the current incarnation if reading from it has not ended. One
// that has ended is dropped here, before anything is sent to it, so the
// call starts the server again (ADR-0126 §2).
func (c *Client) live() *incarnation {
	c.mu.Lock()
	inc := c.cur
	c.mu.Unlock()
	if inc == nil {
		return nil
	}
	select {
	case <-inc.cl.Done():
		c.drop(inc)
		return nil
	default:
		return inc
	}
}

// ensureStarted returns the running incarnation, spawning and opening one
// if there is none.
func (c *Client) ensureStarted(ctx context.Context) (*incarnation, error) {
	select {
	case c.startSem <- struct{}{}:
	case <-ctx.Done():
		return nil, &startError{Server: c.name, Err: fmt.Errorf("waiting for another start: %w", ctx.Err())}
	}
	defer func() { <-c.startSem }()
	if inc := c.live(); inc != nil {
		return inc, nil
	}
	stdin, stdout, kill, err := c.spawn()
	if err != nil {
		return nil, &startError{Server: c.name, Err: err}
	}
	c.mu.Lock()
	c.starting = kill
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.starting = nil
		c.mu.Unlock()
	}()

	// One budget for the whole greeting, and the only one: the probe and, for
	// a legacy server, the handshake all run under it (WithBudget), so the
	// per-call timeout neither shortens a start that was given longer nor puts
	// its own number on the start's error.
	hctx, hcancel := WithBudget(ctx, c.startupTimeout)
	defer hcancel()

	conn, err := client.NewLineConn(stdout, stdin, kill, client.LineOptions{MaxLine: scannerMax})
	if err != nil {
		kill()
		return nil, &startError{Server: c.name, Phase: "open", Err: err}
	}
	cl, err := client.Open(hctx, conn, client.Options{
		Client: mcpproto.Implementation{Name: "gem-agent", Version: c.version},
		// Answers to the server's requests are written within the call
		// deadline, as the hand-written refusal was (ADR-0126 §5).
		RefuseTimeout: c.timeout,
		CancelNotice:  false,
	})
	if err != nil {
		kill()
		return nil, &startError{Server: c.name, Phase: "open", Err: c.failure(hctx, nil, "open", err)}
	}
	inc := &incarnation{cl: cl, kill: kill}
	c.mu.Lock()
	if c.starting == nil {
		// Close ran while this start was being opened.
		c.mu.Unlock()
		kill()
		return nil, &startError{Server: c.name, Phase: "open", Err: errors.New("closed while starting")}
	}
	c.cur = inc
	c.mu.Unlock()
	go c.watch(inc)
	return inc, nil
}

// failure turns an error from the module into the one this package has
// always returned: the server's JSON-RPC error as *RPCError, a deadline as
// the budget that ran out (the incarnation killed, to restart on the next
// call), the end of the server as its reason. ctx is the context the
// request ran under; inc is nil for a start, which its caller kills.
func (c *Client) failure(ctx context.Context, inc *incarnation, method string, err error) error {
	var rpc *mcpproto.Error
	if errors.As(err, &rpc) {
		return &RPCError{Code: rpc.Code, Message: rpc.Message}
	}
	if ctxErr := ctx.Err(); ctxErr != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		// Kill and respawn lazily. notifications/cancelled is not sent
		// first — the receiver may ignore it and acknowledges nothing, so
		// the kill would follow regardless (see the Client comment).
		if inc != nil {
			c.drop(inc)
		}
		if errors.Is(ctxErr, context.Canceled) {
			return err // the caller gave up: its own cancellation, not a timeout report
		}
		budget := budgetOf(ctx, c.timeout)
		if errors.Is(err, client.ErrNotSent) {
			// The write never finished: a server that stops reading its
			// stdin fills the pipe and parks the writer (review 2026-09-08,
			// F-01). The module's LineConn killed the child to release it.
			return &worded{fmt.Sprintf("writing to mcp %s timed out after %s "+
				"(the server stopped reading its stdin; it was killed and restarts on the next call)",
				c.name, budget), err}
		}
		return &worded{fmt.Sprintf("%s timed out after %s (server killed; it restarts on the next call)", method, budget), err}
	}
	if errors.Is(err, client.ErrClosed) {
		cause := "server exited"
		if errors.Is(err, client.ErrLineTooLong) {
			cause = fmt.Sprintf("server sent a response line over the %d-byte frame cap", scannerMax)
		} else if inc != nil {
			if end := inc.cl.Err(); end != nil && !errors.Is(end, io.EOF) {
				cause = "server stream ended: " + end.Error()
			}
		}
		return &worded{fmt.Sprintf("%s during %s", cause, method), err}
	}
	return err
}

// budgetKey marks a context whose time budget a caller has already stated.
type budgetKey struct{}

// WithBudget bounds ctx by d, unless the work is already running under a
// stated budget — then that one stands. A budget is a property of the piece
// of work, said once by whoever owns it: the start's is startupTimeout,
// an ordinary call's is timeout. Layering the second on the first did two
// wrong things at once. A start given longer than a call was cut at the
// call's number, silently; and every timeout was reported under c.timeout,
// so a start that gave up at its own 30 s said "timed out after 1m0s"
// and sent the operator to the wrong setting.
//
// Exported because the start-up listing in cmd states the budget for the whole
// greeting — spawn, open and the first tools/list — from outside this
// package. Stated with a plain context.WithTimeout it fired first, and the
// tools/list that ran out of it still reported the per-call number.
func WithBudget(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, stated := ctx.Value(budgetKey{}).(time.Duration); stated {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(context.WithValue(ctx, budgetKey{}, d), d)
}

// budgetOf is the budget ctx runs under, for saying which one ran out.
func budgetOf(ctx context.Context, fallback time.Duration) time.Duration {
	if d, ok := ctx.Value(budgetKey{}).(time.Duration); ok {
		return d
	}
	return fallback
}

// ListTools fetches the server's tool list. The module follows the
// pagination within its bounds, which are this client's (ADR-0072 §4.5):
// a cursor that repeats, more than 100 pages, or more than 5000 tools ends
// the listing with the server's name, not a 30-second wait. One budget
// covers the whole listing.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	inc, err := c.ensureStarted(ctx)
	if err != nil {
		return nil, err
	}
	tctx, cancel := WithBudget(ctx, c.timeout)
	defer cancel()
	list, err := inc.cl.ListTools(tctx)
	if err != nil {
		err = c.failure(tctx, inc, "tools/list", err)
		var w *worded
		var rpc *RPCError
		if errors.As(err, &w) || errors.As(err, &rpc) {
			return nil, fmt.Errorf("mcp %s: tools/list: %w", c.name, err)
		}
		// The module's own wording already names tools/list, under its
		// "mcp: " prefix; the server's name takes that place.
		return nil, &worded{fmt.Sprintf("mcp %s: %s", c.name, strings.TrimPrefix(err.Error(), "mcp: ")), err}
	}
	tools := make([]Tool, 0, len(list.Tools))
	for _, t := range list.Tools {
		tool := Tool{Name: t.Name, Description: t.Description}
		if len(t.InputSchema) > 0 {
			if err := json.Unmarshal(t.InputSchema, &tool.InputSchema); err != nil {
				return nil, fmt.Errorf("mcp %s: tools/list result: tool %q: inputSchema: %w", c.name, t.Name, err)
			}
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

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
	inc, err := c.ensureStarted(ctx)
	if err != nil {
		// Not sent: the call never left. The adapter shows the cause
		// under the server's name, so the start failure is handed over
		// without the name and with its phase (open, spawn).
		var se *startError
		if errors.As(err, &se) {
			err = se.cause()
		}
		return nil, false, &CallError{Server: c.name, Tool: tool, Err: err}
	}
	if args == nil {
		args = map[string]any{}
	}
	var meta map[string]any
	if c.workDir != "" {
		// Schema-blind: the key says where this session can read files
		// back, and a server that does not know it ignores it. A server
		// that does uses it only when the call left work_dir out, so the
		// model's own argument always wins.
		meta = map[string]any{workdir.MetaKey: c.workDir}
	}
	tctx, cancel := WithBudget(ctx, c.timeout)
	defer cancel()
	res, err := inc.cl.CallTool(tctx, tool, args, meta)
	if err != nil {
		// ErrNotSent is the module's promise that the request never
		// reached the server. Anything else may have: a server that ended
		// mid-call is reported as sent, the safe side (ADR-0126 §3).
		sent := !errors.Is(err, client.ErrNotSent)
		return nil, false, &CallError{Server: c.name, Tool: tool, Err: c.failure(tctx, inc, "tools/call", err), Sent: sent}
	}
	blocks := make([]Content, 0, len(res.Content))
	for _, raw := range res.Content {
		var item struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, false, &CallError{Server: c.name, Tool: tool, Err: fmt.Errorf("result: %w", err), Sent: true}
		}
		b := Content{Type: item.Type, Text: item.Text, MIME: item.MimeType}
		if item.Data != "" {
			// A block that says it carries data and whose data will not
			// decode is reported as text rather than dropped: silently
			// losing a server's answer is the failure this whole change
			// exists to stop.
			decoded, derr := base64.StdEncoding.DecodeString(item.Data)
			if derr != nil {
				b.Type = "text"
				b.Text = fmt.Sprintf("[%s content could not be decoded: %v]", item.Type, derr)
			} else {
				b.Data = decoded
			}
		}
		blocks = append(blocks, b)
	}
	return blocks, res.IsError, nil
}
