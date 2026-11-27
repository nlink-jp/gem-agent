package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ADR-0021, from outside (ADR-0126): a dead start's frame — the refusal
// of a request that server sent just before it was killed — never reaches
// the successor's stdin mid-handshake. Each start has a module Client of
// its own, so this holds by construction; the test records what the
// successor received to keep it so.
func TestAStartsFramesNeverReachItsSuccessor(t *testing.T) {
	f := &fakeServer{}
	f.handler = func(f *fakeServer, method string, _ json.RawMessage) ([]string, any, bool) {
		if method == "tools/call" && f.spawnCount() == 1 {
			// A request of its own, then no answer to the call.
			return []string{`{"jsonrpc":"2.0","id":999,"method":"sampling/createMessage","params":{}}`}, nil, false
		}
		r, ok := stdResult(method)
		return nil, r, ok
	}
	c := newTestClient(f, 300*time.Millisecond)
	defer c.Close()

	if _, err := callText(t, c, "check_ip", nil); err == nil {
		t.Fatal("the first start's call must time out")
	}
	if _, err := callText(t, c, "check_ip", nil); err != nil {
		t.Fatalf("the call after the restart: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // room for a late frame to land

	f.mu.Lock()
	defer f.mu.Unlock()
	for i, m := range f.received {
		if f.receivedBy[i] == 2 && m.Method == "" && m.ID != nil && m.ID.String() == "999" {
			t.Errorf("the first start's answer to request 999 reached the second start: %+v", m)
		}
	}
}

// A call made after the server ended is never written to the ended start:
// the call starts the server again. When that start fails, the call was
// not sent (ADR-0126 §3; today's "server not running").
func TestACallAfterTheServerEndedStartsItAgain(t *testing.T) {
	f := &fakeServer{handler: stdHandler}
	c := newTestClient(f, 2*time.Second)
	defer c.Close()

	if _, err := callText(t, c, "check_ip", nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	_ = f.outs[0].Close() // the server exits
	f.mu.Unlock()
	eventually(t, "reaping the ended start", func() bool { return f.wasKilled(1) })

	if _, err := callText(t, c, "check_ip", nil); err != nil {
		t.Fatalf("the call after the server ended: %v", err)
	}
	if f.spawnCount() != 2 {
		t.Errorf("spawns = %d, want a second start", f.spawnCount())
	}

	f.mu.Lock()
	_ = f.outs[1].Close()
	f.failSpawn = true
	f.mu.Unlock()
	eventually(t, "reaping the second start", func() bool { return f.wasKilled(2) })
	_, _, err := c.CallTool(context.Background(), "check_ip", nil)
	var ce *CallError
	if !errors.As(err, &ce) || ce.Sent {
		t.Errorf("err = %v — want a CallError with Sent=false when no server could be started", err)
	}
}

// A call that ran out of time waiting for its answer may have reached the
// server, and is reported as sent (ADR-0126 §3). The one whose write never
// finished is pinned in send_stall_test.
func TestACallThatTimedOutWaitingWasSent(t *testing.T) {
	f := &fakeServer{}
	f.handler = func(f *fakeServer, method string, params json.RawMessage) ([]string, any, bool) {
		if method == "tools/call" {
			return nil, nil, false
		}
		return stdHandler(f, method, params)
	}
	c := newTestClient(f, 200*time.Millisecond)
	defer c.Close()
	_, _, err := c.CallTool(context.Background(), "check_ip", nil)
	var ce *CallError
	if !errors.As(err, &ce) || !ce.Sent {
		t.Fatalf("err = %v — want a sent CallError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v — the deadline must stay reachable with errors.Is", err)
	}
	eventually(t, "killing the start whose call timed out", func() bool { return f.wasKilled(1) })
}
