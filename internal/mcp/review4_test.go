package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// Review round 4: a server that ends its own stdout (exited on its own,
// or an over-long line ended the scan) is reaped by the incarnation's
// kill, not left as a zombie with both pipes open.
func TestReadLoopEOFReapsTheIncarnation(t *testing.T) {
	killed := make(chan struct{}, 1)
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	spawn := func() (io.WriteCloser, io.ReadCloser, func(), error) {
		return inW, outR, func() {
			select {
			case killed <- struct{}{}:
			default:
			}
			_ = inR.Close()
		}, nil
	}
	c := newClient("t", spawn, time.Second, "test")
	// Drive the incarnation by hand: readLoop is what owns the EOF.
	c.mu.Lock()
	c.kill = func() {
		select {
		case killed <- struct{}{}:
		default:
		}
	}
	c.alive = true
	c.gen++
	gen := c.gen
	c.mu.Unlock()
	done := make(chan struct{})
	go func() { c.readLoop(outR, gen); close(done) }()
	_ = outW.Close() // the server "exits"
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatal("EOF did not reap the incarnation")
	}
	<-done
	c.mu.Lock()
	alive := c.alive
	c.mu.Unlock()
	if alive {
		t.Fatal("client still marked alive after EOF")
	}
	_ = context.Background()
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
	c := newClient("loop", spawn, 5*time.Second, "test")
	defer c.Close()
	_, err := c.ListTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("err = %v, want the repeated-cursor refusal", err)
	}
}
