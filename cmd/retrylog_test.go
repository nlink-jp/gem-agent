package cmd

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
)

type recordedLine struct {
	kind string
	data any
}

type fakeSessionLog struct {
	lines []recordedLine
	err   error
}

func (f *fakeSessionLog) Log(kind string, data any) error {
	f.lines = append(f.lines, recordedLine{kind, data})
	return f.err
}

// The record's field names are what a transcript reader keys on
// (sessions.md): a rename here silently empties every tally built on
// them, so the names are pinned through the JSON a reader would see.
func TestRetryRecordFields(t *testing.T) {
	raw, err := json.Marshal(retryRecord(llm.RetryEvent{Model: "m", Source: "main", Attempt: 3, Max: 5, Cause: "429", Status: "RESOURCE_EXHAUSTED", DelayMS: 1000}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"model": "m", "source": "main", "attempt": 3.0, "max": 5.0, "cause": "429",
		"status": "RESOURCE_EXHAUSTED", "delay_ms": 1000.0}
	if len(got) != len(want) {
		t.Fatalf("record = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("record[%q] = %v, want %v", k, got[k], v)
		}
	}
}

// A retry the backend reports becomes one model_retry line. An empty
// source or status is written as empty, not left out: a reader must be
// able to tell "this retry had none" from a build that wrote no such key.
func TestRetryBecomesAModelRetryRecord(t *testing.T) {
	var fire func(llm.RetryEvent)
	log := &fakeSessionLog{}
	installRetryRecorder(func(fn func(llm.RetryEvent)) { fire = fn }, log)
	fire(llm.RetryEvent{Model: "m", Attempt: 2, Max: 5, Cause: "503", DelayMS: 500})
	if len(log.lines) != 1 || log.lines[0].kind != "model_retry" {
		t.Fatalf("lines = %+v, want one model_retry record", log.lines)
	}
	data, ok := log.lines[0].data.(map[string]any)
	if !ok || data["cause"] != "503" || data["model"] != "m" {
		t.Fatalf("record data = %#v", log.lines[0].data)
	}
	for _, key := range []string{"source", "status"} {
		if v, present := data[key]; !present || v != "" {
			t.Errorf("record[%q] = %v (present %v), want an empty string that is there", key, v, present)
		}
	}
}

// The names this package's own one-shot calls give themselves are the
// usage record's names for the same calls: a transcript reader joins the
// two on them.
func TestWebSourcesAreTheUsageSources(t *testing.T) {
	if llm.SourceWebSearch != session.UsageWebSearch || llm.SourceWebFetch != session.UsageWebFetch {
		t.Errorf("llm sources %q, %q; usage sources %q, %q", llm.SourceWebSearch, llm.SourceWebFetch, session.UsageWebSearch, session.UsageWebFetch)
	}
}

// The recorder reaches the transcript only through one line of root.go.
// Without it every retry is still shown and none is kept, and no other
// test notices: the recorder is optional by design. The line is the
// thing under test (the pattern of TestTUIOptionsWiring).
func TestRetryRecorderIsWired(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "root.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "recordRetries" && len(call.Args) == 2 {
			if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == "backend" {
				found = true
			}
		}
		return true
	})
	if !found {
		t.Fatal("root.go does not call recordRetries(backend, …): retries would be shown and never recorded")
	}
}
