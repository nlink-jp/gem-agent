package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nlink-jp/mcp/probe"
)

// lockedBuffer is a bytes.Buffer safe for the probe server's log writer and
// the test reading it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// received is every line the server read, in order.
func (b *lockedBuffer) received(t *testing.T) []string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	sc.Buffer(make([]byte, 64*1024), scannerMax)
	for sc.Scan() {
		var e struct {
			Dir  string `json:"dir"`
			Line string `json:"line"`
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("probe log line: %v", err)
		}
		if e.Dir == "in" {
			lines = append(lines, e.Line)
		}
	}
	return lines
}

// probeSpawn runs the module's probe server of kind over pipes, as this
// client's start function would run a process (ADR-0126, Test design).
func probeSpawn(kind probe.ServerKind, log io.Writer, spawns *int) spawnFunc {
	return func() (io.WriteCloser, io.ReadCloser, func(), error) {
		*spawns++
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			_ = probe.Serve(ctx, kind, inR, outW, probe.ServerOptions{Log: log})
			_ = outW.Close()
		}()
		var once sync.Once
		kill := func() {
			once.Do(func() {
				cancel()
				_ = inW.Close()
				_ = inR.Close()
				_ = outW.Close()
				_ = outR.Close()
			})
		}
		return inW, outR, kill, nil
	}
}

// The specification's compatibility matrix for a dual-era client: a legacy
// server opens in the legacy era, a dual-era and a modern-only server in the
// modern one. In every pair the tool listing and a call work, and the work
// directory rides in the call's _meta.
func TestTheProbeMatrix(t *testing.T) {
	cases := []struct {
		kind   probe.ServerKind
		modern bool
	}{
		{probe.ServerLegacy, false},
		{probe.ServerDual, true},
		{probe.ServerModernOnly, true},
	}
	for _, tc := range cases {
		t.Run(tc.kind.String(), func(t *testing.T) {
			log := &lockedBuffer{}
			spawns := 0
			c := newClient("probe", probeSpawn(tc.kind, log, &spawns), 5*time.Second, 5*time.Second, "test", "/session/work")
			defer c.Close()

			tools, err := c.ListTools(context.Background())
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			found := false
			for _, tool := range tools {
				if tool.Name == "probe_echo" && tool.InputSchema["type"] == "object" {
					found = true
				}
			}
			if !found {
				t.Fatalf("tools = %+v, want probe_echo with its schema", tools)
			}
			out, err := callText(t, c, "probe_echo", map[string]any{"text": "hello"})
			if err != nil || out != "hello" {
				t.Fatalf("probe_echo = %q, %v", out, err)
			}

			lines := log.received(t)
			if len(lines) == 0 || !strings.Contains(lines[0], `"server/discover"`) {
				t.Fatalf("the first line the server read was not the probe: %v", lines)
			}
			sawInitialize := false
			var call string
			for _, l := range lines {
				if strings.Contains(l, `"method":"initialize"`) {
					sawInitialize = true
				}
				if strings.Contains(l, `"method":"tools/call"`) {
					call = l
				}
			}
			if sawInitialize == tc.modern {
				t.Errorf("initialize sent = %v against a %s server", sawInitialize, tc.kind)
			}
			var p struct {
				Params struct {
					Meta map[string]any `json:"_meta"`
				} `json:"params"`
			}
			if err := json.Unmarshal([]byte(call), &p); err != nil {
				t.Fatalf("tools/call line %q: %v", call, err)
			}
			if p.Params.Meta["jp.nlink/work_dir"] != "/session/work" {
				t.Errorf("_meta = %v, want the work directory", p.Params.Meta)
			}
			if _, has := p.Params.Meta["io.modelcontextprotocol/protocolVersion"]; has != tc.modern {
				t.Errorf("_meta = %v — the protocol version rides there in the modern era only", p.Params.Meta)
			}
		})
	}
}

// A call after a timeout opens a new start, and that start probes again.
func TestARestartProbesAgain(t *testing.T) {
	f := &fakeServer{}
	f.handler = func(f *fakeServer, method string, _ json.RawMessage) ([]string, any, bool) {
		if method == "tools/call" && f.spawnCount() == 1 {
			return nil, nil, false
		}
		r, ok := stdResult(method)
		return nil, r, ok
	}
	c := newTestClient(f, 300*time.Millisecond)
	defer c.Close()
	if _, err := callText(t, c, "check_ip", nil); err == nil {
		t.Fatal("the first call must time out")
	}
	if _, err := callText(t, c, "check_ip", nil); err != nil {
		t.Fatalf("the call after the restart: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var second []string
	for i, m := range f.received {
		if f.receivedBy[i] == 2 && m.Method != "" {
			second = append(second, m.Method)
		}
	}
	if len(second) < 3 || second[0] != "server/discover" || second[1] != "initialize" || second[2] != "notifications/initialized" {
		t.Errorf("the second start received %v, want the probe and the handshake first", second)
	}
}

// A legacy server's ping gets the empty result the specification asks for;
// any other request of the server's is refused with -32601 (ADR-0126 §5).
func TestPingIsAnsweredAndOtherRequestsRefused(t *testing.T) {
	f := &fakeServer{}
	f.handler = func(_ *fakeServer, method string, _ json.RawMessage) ([]string, any, bool) {
		if method == "tools/call" {
			r, ok := stdResult(method)
			return []string{
				`{"jsonrpc":"2.0","id":501,"method":"ping"}`,
				`{"jsonrpc":"2.0","id":502,"method":"roots/list"}`,
			}, r, ok
		}
		r, ok := stdResult(method)
		return nil, r, ok
	}
	c := newTestClient(f, 2*time.Second)
	defer c.Close()
	if _, err := callText(t, c, "check_ip", nil); err != nil {
		t.Fatal(err)
	}
	answer := func(id string) *message {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := range f.received {
			m := f.received[i]
			if m.Method == "" && m.ID != nil && m.ID.String() == id {
				return &m
			}
		}
		return nil
	}
	eventually(t, "answering both of the server's requests", func() bool { return answer("501") != nil && answer("502") != nil })
	if ping := answer("501"); ping.Error != nil || string(ping.Result) != "{}" {
		t.Errorf("ping answer = %+v, want the result {}", *ping)
	}
	if other := answer("502"); other.Error == nil || other.Error.Code != -32601 {
		t.Errorf("roots/list answer = %+v, want -32601", *other)
	}
}

// The answer to a server's request is written within the call deadline, as
// the hand-written refusal was — not the module's 5-second default
// (ADR-0126 §5). A server that reads its stdin again after 5.5 seconds
// still gets its answer.
func TestAnAnswerIsWrittenWithinTheCallDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past the module's default refusal deadline")
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	got := make(chan string, 1)
	spawn := func() (io.WriteCloser, io.ReadCloser, func(), error) {
		go func() {
			sc := bufio.NewScanner(inR)
			sc.Buffer(make([]byte, 64*1024), scannerMax)
			for sc.Scan() {
				var msg message
				if json.Unmarshal(sc.Bytes(), &msg) != nil {
					continue
				}
				switch {
				case msg.Method == "server/discover":
					refuseDiscover(outW, *msg.ID)
				case msg.Method == "initialize":
					r, _ := stdResult("initialize")
					resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": r})
					_, _ = outW.Write(append(resp, '\n'))
				case msg.Method == "tools/list":
					// Ask something, then stop reading stdin for longer
					// than the module's default deadline.
					_, _ = fmt.Fprintln(outW, `{"jsonrpc":"2.0","id":77,"method":"ping"}`)
					r, _ := stdResult("tools/list")
					resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": r})
					_, _ = outW.Write(append(resp, '\n'))
					time.Sleep(5500 * time.Millisecond)
				case msg.Method == "" && msg.ID != nil && msg.ID.String() == "77":
					got <- string(msg.Result)
				}
			}
		}()
		return inW, outR, func() { _ = inR.Close(); _ = outW.Close() }, nil
	}
	c := newClient("slowreader", spawn, 30*time.Second, 30*time.Second, "test", "")
	defer c.Close()
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r != "{}" {
			t.Errorf("ping answer = %q, want {}", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the answer to the server's ping never arrived")
	}
}
