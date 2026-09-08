package mcp

// Regression: a server that stops reading its stdin parks the writer in
// stdin.Write, holding wmu. The per-call deadline used to start after
// that write returned, so nothing supervised it and nothing killed the
// child — the call, and every other call to that server, hung for the
// process's life (review 2026-09-08, F-01).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// stallServer completes the handshake and then never reads its stdin
// again, the way a wedged server does. Its stdin is an unbuffered
// io.Pipe, so the next frame written to it blocks in Write.
type stallServer struct {
	mu     sync.Mutex
	spawns int
}

func (s *stallServer) spawn() (io.WriteCloser, io.ReadCloser, func(), error) {
	s.mu.Lock()
	s.spawns++
	s.mu.Unlock()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stop := make(chan struct{})
	var once sync.Once

	go func() {
		scanner := bufio.NewScanner(inR)
		scanner.Buffer(make([]byte, scannerInitial), scannerMax)
		for scanner.Scan() {
			var msg message
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}
			if msg.Method == "initialize" && msg.ID != nil {
				result, _ := stdResult("initialize")
				resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": result})
				_, _ = outW.Write(append(resp, '\n'))
				continue
			}
			if msg.Method == "notifications/initialized" {
				<-stop // handshake done: stop draining stdin, stay alive
				return
			}
		}
	}()

	kill := func() {
		once.Do(func() { close(stop) })
		_ = inW.Close()
		_ = inR.Close() // unblocks the parked Write, as killing the child does
		_ = outW.Close()
		_ = outR.Close()
	}
	return inW, outR, kill, nil
}

func (s *stallServer) spawnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawns
}

// within runs fn and fails the test if it has not returned by limit,
// rather than letting the whole package hang to its own deadline.
func within(t *testing.T, limit time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s did not return within %s", what, limit)
	}
}

func TestCallTimesOutWhenServerStopsReadingStdin(t *testing.T) {
	s := &stallServer{}
	c := newClient("stalled", s.spawn, 300*time.Millisecond, "test")
	defer c.Close()

	var err error
	start := time.Now()
	within(t, 5*time.Second, "a call to a server that stopped reading stdin", func() {
		_, err = callText(t, c, "check_ip", nil)
	})
	if err == nil {
		t.Fatal("a call whose request could not be written returned no error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %s for a 300ms timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error does not say it timed out: %v", err)
	}

	// The request never reached the server, so the adapter must be able
	// to say so: a frame cut short carries no terminating newline.
	var ce *CallError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not a *CallError: %v", err)
	}
	if ce.Sent {
		t.Error("Sent = true for a request that was never written")
	}

	// The child was killed, so the next call gets a fresh one.
	if s.spawnCount() != 1 {
		t.Fatalf("spawns = %d before the retry", s.spawnCount())
	}
	within(t, 5*time.Second, "the call after the wedged server was killed", func() {
		_, _ = callText(t, c, "check_ip", nil)
	})
	if s.spawnCount() != 2 {
		t.Errorf("spawns = %d — the wedged server was not killed", s.spawnCount())
	}
}

// wmu is held across the parked write, so a second caller waits on the
// lock rather than on the pipe. Its deadline has to cover that wait too.
func TestConcurrentCallsAreNotHeldByAStalledWrite(t *testing.T) {
	s := &stallServer{}
	c := newClient("stalled", s.spawn, 300*time.Millisecond, "test")
	defer c.Close()

	// Get past the handshake once, so all four calls race on the write.
	within(t, 5*time.Second, "the first call", func() {
		_, _ = callText(t, c, "check_ip", nil)
	})

	var wg sync.WaitGroup
	errs := make([]error, 4)
	within(t, 10*time.Second, "four concurrent calls", func() {
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, _, errs[i] = c.CallTool(context.Background(), "check_ip", nil)
			}(i)
		}
		wg.Wait()
	})
	// Returning at all is the property under test — that is what wmu
	// held forever used to prevent. How each call ended depends on which
	// one won the lock and whether it landed on a fresh incarnation, so
	// only the shape of the failure is asserted here; the first test
	// pins the not-sent report deterministically.
	for i, err := range errs {
		if err == nil {
			continue // this one landed on a fresh incarnation
		}
		var ce *CallError
		if !errors.As(err, &ce) {
			t.Errorf("call %d: error is not a *CallError the adapter can report: %v", i, err)
		}
	}
}

// A caller that gives up while its request is stuck in the pipe gets
// its own cancellation back, not a timeout report.
func TestCancelDuringStalledWriteReturnsCancellation(t *testing.T) {
	s := &stallServer{}
	c := newClient("stalled", s.spawn, 30*time.Second, "test") // long: cancel must be what ends it
	defer c.Close()

	within(t, 5*time.Second, "the handshake call", func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _, _ = c.CallTool(ctx, "check_ip", nil)
	})

	ctx, cancel := context.WithCancel(context.Background())
	var err error
	within(t, 5*time.Second, "a cancelled call", func() {
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		_, _, err = c.CallTool(ctx, "check_ip", nil)
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
