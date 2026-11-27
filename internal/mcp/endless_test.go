package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/mcp/client"
)

// ADR-0072 §4.8 against a server that writes without ever writing a
// newline: the line cap still ends the start at once and kills the
// server, rather than the reader discarding until a newline that never
// comes and the call waiting out its deadline (pre-release review of the
// move to nlink-jp/mcp; the module's v0.2.0 LineConn).
func TestAnEndlessLineEndsTheStartAtTheCap(t *testing.T) {
	killed := make(chan struct{}, 1)
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	spawn := func() (io.WriteCloser, io.ReadCloser, func(), error) {
		go func() {
			sc := bufio.NewScanner(inR)
			for sc.Scan() {
				var msg message
				if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil {
					continue
				}
				switch msg.Method {
				case "server/discover":
					refuseDiscover(outW, *msg.ID)
				case "tools/list":
					chunk := []byte(strings.Repeat("x", 64<<10))
					for {
						if _, err := outW.Write(chunk); err != nil {
							return // killed
						}
					}
				default:
					r, _ := stdResult(msg.Method)
					resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": r})
					_, _ = outW.Write(append(resp, '\n'))
				}
			}
		}()
		return inW, outR, func() {
			select {
			case killed <- struct{}{}:
			default:
			}
			_ = inR.Close()
			_ = outR.Close()
		}, nil
	}
	c := newClient("endless", spawn, 30*time.Second, 30*time.Second, "test", "")
	defer c.Close()

	start := time.Now()
	_, err := c.ListTools(context.Background())
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v — the cap did not end the reading", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "frame cap") || !errors.Is(err, client.ErrLineTooLong) {
		t.Fatalf("err = %v, want the frame-cap cause", err)
	}
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Error("the server writing an endless line was not killed")
	}
}
