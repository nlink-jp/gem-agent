package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// lostBackend fails every request the way the transport's health
// check does (ADR-0100).
type lostBackend struct{}

func (lostBackend) ChatStream(context.Context, string, []llm.Message, []llm.ToolDef, func(string)) (*llm.Response, error) {
	return nil, fmt.Errorf("%w: %w", llm.ErrConnectionLost, errors.New("doRequest: error sending request: http2: client connection lost"))
}

// A lost connection reaches the operator as what happened and the next
// step, in their language; the transport's text stays in the error
// chain and in the session log (ADR-0100 §3).
func TestConnectionLostIsSaidInTheOperatorsWords(t *testing.T) {
	log := &capturingLog{}
	ja := uitext.For(uitext.JA)
	_, reg := newAgent(t, lostBackend{}, &approveAll{}, 3)
	a := New(Options{Backend: lostBackend{}, Registry: reg, Gate: &approveAll{}, System: "s", MaxTurns: 3, Msgs: ja, Log: log})
	_, err := a.Run(context.Background(), "hi", nil)
	if err == nil {
		t.Fatal("a lost connection ended the turn without an error")
	}
	if err.Error() != ja.ConnectionLost {
		t.Errorf("error = %q, want the catalog's %q", err.Error(), ja.ConnectionLost)
	}
	if !errors.Is(err, llm.ErrConnectionLost) {
		t.Error("the error chain lost ErrConnectionLost")
	}
	found := false
	for i, k := range log.kinds {
		if k != "connection_lost" {
			continue
		}
		found = true
		if !strings.Contains(fmt.Sprint(log.data[i]), "http2: client connection lost") {
			t.Errorf("connection_lost record does not keep the transport's text: %v", log.data[i])
		}
	}
	if !found {
		t.Errorf("no connection_lost record; logged %v", log.kinds)
	}
}

// Any other backend error passes through untouched.
func TestOtherBackendErrorsAreNotConnectionLost(t *testing.T) {
	want := errors.New("vertex AI stream: 403 permission denied")
	a, _ := newAgent(t, errBackend{want}, &approveAll{}, 3)
	_, err := a.Run(context.Background(), "hi", nil)
	if !errors.Is(err, want) || err.Error() != want.Error() {
		t.Errorf("err = %v, want %v unchanged", err, want)
	}
}

type errBackend struct{ err error }

func (b errBackend) ChatStream(context.Context, string, []llm.Message, []llm.ToolDef, func(string)) (*llm.Response, error) {
	return nil, b.err
}
