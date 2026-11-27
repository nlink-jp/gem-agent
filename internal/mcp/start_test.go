package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// slowHello is a stand-in whose initialize takes hold long, so a test can
// act while a start is being opened.
func slowHello(hold time.Duration) *fakeServer {
	f := &fakeServer{}
	f.handler = func(f *fakeServer, method string, params json.RawMessage) ([]string, any, bool) {
		if method == "initialize" {
			time.Sleep(hold)
		}
		return stdHandler(f, method, params)
	}
	return f
}

// Close while a start is being opened kills that start's process, and the
// start does not become current afterwards: nothing is left running.
func TestCloseDuringAStartKillsIt(t *testing.T) {
	f := slowHello(300 * time.Millisecond)
	c := newClient("slowhello", f.spawn, 2*time.Second, 2*time.Second, "test", "")
	done := make(chan error, 1)
	go func() {
		_, err := c.ListTools(context.Background())
		done <- err
	}()
	eventually(t, "the start beginning", func() bool { return f.spawnCount() == 1 })
	c.Close()
	if err := <-done; err == nil {
		t.Error("a start closed while opening must fail")
	}
	if !f.wasKilled(1) {
		t.Error("the process of the start that was closed was not killed")
	}
	if c.live() != nil {
		t.Error("the closed start became current")
	}
}

// A second caller waiting on another caller's start gives up at its own
// deadline, not the start's.
func TestAStartWaitAnswersToTheCallersContext(t *testing.T) {
	f := slowHello(time.Second)
	c := newClient("slowhello", f.spawn, 3*time.Second, 3*time.Second, "test", "")
	defer c.Close()
	go func() { _, _ = c.ListTools(context.Background()) }()
	eventually(t, "the first start beginning", func() bool { return f.spawnCount() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := c.CallTool(ctx, "check_ip", nil)
	if elapsed := time.Since(start); elapsed > 600*time.Millisecond {
		t.Errorf("the waiting call took %v for its 100ms", elapsed)
	}
	var ce *CallError
	if !errors.As(err, &ce) || ce.Sent || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v — want a not-sent CallError carrying the caller's deadline", err)
	}
}
