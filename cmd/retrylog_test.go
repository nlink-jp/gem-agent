package cmd

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
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
	raw, err := json.Marshal(retryRecord(llm.RetryEvent{Model: "m", Attempt: 3, Max: 5, Cause: "429", Error: "Error 429, Resource exhausted.", DelayMS: 1000}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"model": "m", "attempt": 3.0, "max": 5.0, "cause": "429",
		"error": "Error 429, Resource exhausted.", "delay_ms": 1000.0}
	if len(got) != len(want) {
		t.Fatalf("record = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("record[%q] = %v, want %v", k, got[k], v)
		}
	}
}

// recordRetries is the wiring: a retry the backend reports becomes one
// model_retry line, and a session log that cannot be written does not
// reach the model call (the recorder has no error to return).
func TestRecordRetriesWritesModelRetry(t *testing.T) {
	var fire func(llm.RetryEvent)
	install := func(fn func(llm.RetryEvent)) { fire = fn }

	log := &fakeSessionLog{}
	installRetryRecorder(install, log)
	fire(llm.RetryEvent{Model: "m", Attempt: 2, Max: 5, Cause: "503", DelayMS: 500})
	if len(log.lines) != 1 || log.lines[0].kind != "model_retry" {
		t.Fatalf("lines = %+v, want one model_retry record", log.lines)
	}
	data, ok := log.lines[0].data.(map[string]any)
	if !ok || data["cause"] != "503" || data["model"] != "m" {
		t.Errorf("record data = %#v", log.lines[0].data)
	}

	broken := &fakeSessionLog{err: errors.New("disk full")}
	installRetryRecorder(install, broken)
	fire(llm.RetryEvent{Model: "m", Attempt: 2, Max: 5, Cause: "429"}) // must not panic
	if len(broken.lines) != 1 {
		t.Errorf("a failing log was not attempted: %+v", broken.lines)
	}
}
