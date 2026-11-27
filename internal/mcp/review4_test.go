package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/mcp/client"
)

// Review round 4: a server that ends its own stdout (exited on its own,
// or an over-long line ended the reading) is reaped by the incarnation's
// kill at once, not left as a zombie with both pipes open until the next
// call. The module only reports the end; the watcher kills (ADR-0126 §2).
func TestAnEndedServerIsReapedWithoutAnotherCall(t *testing.T) {
	f := &fakeServer{handler: stdHandler}
	c := newTestClient(f, 2*time.Second)
	defer c.Close()
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	_ = f.outs[0].Close() // the server "exits"
	f.mu.Unlock()
	eventually(t, "reaping the exited server", func() bool { return f.wasKilled(1) })
	if c.live() != nil {
		t.Error("the client still holds the exited start")
	}
}

// Review round 4: ${VAR:-default} is Claude Code's .mcp.json grammar;
// os.ExpandEnv looked up a variable literally named "VAR:-default".
func TestExpandEnvDefaults(t *testing.T) {
	t.Setenv("MCP_R4_SET", "set")
	_ = os.Unsetenv("MCP_R4_UNSET")
	cases := map[string]string{
		"${MCP_R4_SET:-dflt}":   "set",
		"${MCP_R4_UNSET:-dflt}": "dflt",
		"${MCP_R4_UNSET}":       "",
		"pre-${MCP_R4_SET}-x":   "pre-set-x",
		"$MCP_R4_SET":           "set",
	}
	for in, want := range cases {
		if got := expandEnv(in); got != want {
			t.Errorf("expandEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

// ADR-0072 §4.5: a server that repeats a cursor ends the listing with
// an error naming it, not a wait for the timeout.
func TestListToolsRefusesARepeatingCursor(t *testing.T) {
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	spawn := func() (io.WriteCloser, io.ReadCloser, func(), error) {
		go func() {
			sc := bufio.NewScanner(inR)
			for sc.Scan() {
				var msg struct {
					ID     *json.Number `json:"id"`
					Method string       `json:"method"`
				}
				if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil {
					continue
				}
				var result any = map[string]any{"protocolVersion": "1", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "t"}}
				if msg.Method == "tools/list" {
					result = map[string]any{"tools": []any{map[string]any{"name": "x", "description": "d"}}, "nextCursor": "again"}
				}
				resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": result})
				_, _ = outW.Write(append(resp, '\n'))
			}
		}()
		return inW, outR, func() { _ = inR.Close(); _ = outW.Close() }, nil
	}
	c := newClient("loop", spawn, 5*time.Second, 5*time.Second, "test", "")
	defer c.Close()
	_, err := c.ListTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("err = %v, want the repeated-cursor refusal", err)
	}
}

// ADR-0072 §4.8: a response line over the frame cap is reported as
// that, not as "server exited".
func TestOverlongLineIsNamedAsTheCause(t *testing.T) {
	killed := make(chan struct{}, 1)
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	spawn := func() (io.WriteCloser, io.ReadCloser, func(), error) {
		go func() {
			sc := bufio.NewScanner(inR)
			for sc.Scan() {
				var msg struct {
					ID     *json.Number `json:"id"`
					Method string       `json:"method"`
				}
				if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil {
					continue
				}
				if msg.Method == "tools/list" {
					huge := strings.Repeat("x", scannerMax+10)
					_, _ = outW.Write([]byte(`{"jsonrpc":"2.0","id":` + string(*msg.ID) + `,"result":{"tools":[],"pad":"` + huge + "\"}}\n"))
					return
				}
				resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{"protocolVersion": "1", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "t"}}})
				_, _ = outW.Write(append(resp, '\n'))
			}
		}()
		return inW, outR, func() {
			select {
			case killed <- struct{}{}:
			default:
			}
			_ = inR.Close()
			_ = outW.Close()
		}, nil
	}
	c := newClient("big", spawn, 10*time.Second, 10*time.Second, "test", "")
	defer c.Close()
	_, err := c.ListTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "frame cap") {
		t.Fatalf("err = %v, want the frame-cap cause", err)
	}
	if !errors.Is(err, client.ErrLineTooLong) {
		t.Errorf("err = %v — the line limit must be reachable with errors.Is", err)
	}
	// The server that sent the line is still running: it is killed, not
	// left beside its successor.
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Error("the server that sent an over-long line was not killed")
	}
}
