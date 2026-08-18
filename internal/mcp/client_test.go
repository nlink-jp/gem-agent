package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer speaks newline-delimited JSON-RPC over pipes, standing in
// for a spawned MCP server process.
type fakeServer struct {
	mu       sync.Mutex
	spawns   int
	received []message
	// handler returns raw lines to emit before the response, the result
	// payload, and whether to respond at all.
	handler func(f *fakeServer, method string, params json.RawMessage) (pre []string, result any, respond bool)
}

func (f *fakeServer) spawn() (io.WriteCloser, io.ReadCloser, func(), error) {
	f.mu.Lock()
	f.spawns++
	f.mu.Unlock()

	inR, inW := io.Pipe()   // client stdin -> server
	outR, outW := io.Pipe() // server -> client stdout

	go func() {
		scanner := bufio.NewScanner(inR)
		scanner.Buffer(make([]byte, 64*1024), scannerMax)
		for scanner.Scan() {
			var msg message
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}
			f.mu.Lock()
			f.received = append(f.received, msg)
			f.mu.Unlock()
			if msg.ID == nil || msg.Method == "" {
				continue // notification, or the client answering our request
			}
			pre, result, respond := f.handler(f, msg.Method, msg.Params)
			for _, line := range pre {
				fmt.Fprintln(outW, line)
			}
			if !respond {
				continue
			}
			resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": result})
			_, _ = outW.Write(append(resp, '\n'))
		}
	}()

	kill := func() {
		inW.Close()
		outW.Close()
		inR.Close()
		outR.Close()
	}
	return inW, outR, kill, nil
}

func (f *fakeServer) spawnCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawns
}

func stdResult(method string) (any, bool) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{},
			"serverInfo":      map[string]any{"name": "fake", "version": "0"},
		}, true
	case "tools/list":
		return map[string]any{"tools": []map[string]any{{
			"name":        "check_ip",
			"description": "Check an IP.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"ip": map[string]any{"type": "string"}}},
		}}}, true
	case "tools/call":
		return map[string]any{"content": []map[string]any{{"type": "text", "text": "tool-result"}}}, true
	}
	return nil, false
}

func stdHandler(f *fakeServer, method string, params json.RawMessage) ([]string, any, bool) {
	result, ok := stdResult(method)
	return nil, result, ok
}

func newTestClient(f *fakeServer, timeout time.Duration) *Client {
	return newClient("fake", f.spawn, timeout, "test")
}

func TestListAndCall(t *testing.T) {
	f := &fakeServer{handler: stdHandler}
	c := newTestClient(f, 2*time.Second)
	defer c.Close()

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "check_ip" || tools[0].InputSchema["type"] != "object" {
		t.Fatalf("tools = %+v", tools)
	}

	out, err := c.CallTool(context.Background(), "check_ip", map[string]any{"ip": "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "tool-result" {
		t.Errorf("out = %q", out)
	}

	// Handshake order: initialize request, then initialized notification.
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.received) < 2 || f.received[0].Method != "initialize" || f.received[1].Method != "notifications/initialized" {
		t.Errorf("handshake frames wrong: %+v", f.received[:2])
	}
}

func TestIsErrorPrefixed(t *testing.T) {
	f := &fakeServer{}
	f.handler = func(_ *fakeServer, method string, _ json.RawMessage) ([]string, any, bool) {
		if method == "tools/call" {
			return nil, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "lookup failed"}},
				"isError": true,
			}, true
		}
		r, ok := stdResult(method)
		return nil, r, ok
	}
	c := newTestClient(f, 2*time.Second)
	defer c.Close()

	out, err := c.CallTool(context.Background(), "check_ip", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "error: lookup failed" {
		t.Errorf("out = %q", out)
	}
}

func TestNotificationAndServerRequestInterleaved(t *testing.T) {
	f := &fakeServer{}
	f.handler = func(_ *fakeServer, method string, _ json.RawMessage) ([]string, any, bool) {
		if method == "tools/call" {
			pre := []string{
				`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"noise"}}`,
				`{"jsonrpc":"2.0","id":999,"method":"sampling/createMessage","params":{}}`,
			}
			r, ok := stdResult(method)
			return pre, r, ok
		}
		r, ok := stdResult(method)
		return nil, r, ok
	}
	c := newTestClient(f, 2*time.Second)
	defer c.Close()

	out, err := c.CallTool(context.Background(), "check_ip", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "tool-result" {
		t.Errorf("out = %q (notification/server-request should be transparent)", out)
	}

	// The server-initiated request must receive a -32601 refusal — an
	// unanswered request could hang a server that waits for it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, m := range f.received {
			if m.ID != nil && m.Method == "" && m.Error != nil && m.Error.Code == -32601 {
				f.mu.Unlock()
				return
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("server request never received the -32601 refusal")
}

func TestTimeoutKillsThenRespawns(t *testing.T) {
	f := &fakeServer{}
	f.handler = func(f *fakeServer, method string, _ json.RawMessage) ([]string, any, bool) {
		if method == "tools/call" && f.spawnCount() == 1 {
			return nil, nil, false // first incarnation hangs on calls
		}
		r, ok := stdResult(method)
		return nil, r, ok
	}
	c := newTestClient(f, 300*time.Millisecond)
	defer c.Close()

	_, err := c.CallTool(context.Background(), "check_ip", nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if f.spawnCount() != 1 {
		t.Fatalf("spawns = %d before respawn", f.spawnCount())
	}

	out, err := c.CallTool(context.Background(), "check_ip", nil)
	if err != nil {
		t.Fatalf("call after respawn failed: %v", err)
	}
	if out != "tool-result" || f.spawnCount() != 2 {
		t.Errorf("out = %q, spawns = %d (lazy respawn expected)", out, f.spawnCount())
	}
}

func TestLargeToolOutput(t *testing.T) {
	big := strings.Repeat("x", 200*1024) // beyond the scanner's initial buffer
	f := &fakeServer{}
	f.handler = func(_ *fakeServer, method string, _ json.RawMessage) ([]string, any, bool) {
		if method == "tools/call" {
			return nil, map[string]any{"content": []map[string]any{{"type": "text", "text": big}}}, true
		}
		r, ok := stdResult(method)
		return nil, r, ok
	}
	c := newTestClient(f, 5*time.Second)
	defer c.Close()

	out, err := c.CallTool(context.Background(), "check_ip", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != big {
		t.Errorf("large output corrupted: len=%d", len(out))
	}
}

func TestToolListPagination(t *testing.T) {
	f := &fakeServer{}
	f.handler = func(_ *fakeServer, method string, params json.RawMessage) ([]string, any, bool) {
		if method == "tools/list" {
			var p struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(params, &p)
			if p.Cursor == "" {
				return nil, map[string]any{
					"tools":      []map[string]any{{"name": "a", "inputSchema": map[string]any{"type": "object"}}},
					"nextCursor": "page2",
				}, true
			}
			return nil, map[string]any{
				"tools": []map[string]any{{"name": "b", "inputSchema": map[string]any{"type": "object"}}},
			}, true
		}
		r, ok := stdResult(method)
		return nil, r, ok
	}
	c := newTestClient(f, 2*time.Second)
	defer c.Close()

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "a" || tools[1].Name != "b" {
		t.Errorf("paginated tools = %+v", tools)
	}
}
