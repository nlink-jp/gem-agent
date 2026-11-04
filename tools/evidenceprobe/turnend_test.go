package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The records both runtimes write, in the shapes the probe reads.
var (
	tUser      = `{"kind":"message","data":{"role":"user","content":"save the result as evidence"}}`
	tCalls     = `{"kind":"message","data":{"role":"assistant","content":"Querying first.","tool_calls":[{"name":"mcp__bq__query","args":{"query":"SELECT 1"}}]}}`
	tResult    = `{"kind":"message","data":{"role":"tool","content":"rows"}}`
	tAnswer    = `{"kind":"message","data":{"role":"assistant","content":"Saved to evidence/rows.json."}}`
	tEmptyErr  = `{"kind":"assistant_empty","data":{"finish_reason":"error","retried":true}}`
	tEmptyStop = `{"kind":"assistant_empty","data":{"finish_reason":"stop","retried":false}}`
	tNotice    = `{"kind":"notice","data":{"message":"the model returned an empty response — sending the same request again"}}`
	tStopped   = `{"kind":"round_intervention","data":{"trigger":"loop","decision":false,"source":"stop"}}`
	tContinued = `{"kind":"round_intervention","data":{"trigger":"limit","decision":true,"source":"auto"}}`
	tWindow    = `{"kind":"window_full","data":{"round":7}}`
	tConnLost  = `{"kind":"connection_lost","data":{"round":3,"error":"stream stalled"}}`
	tSession   = `{"kind":"session","data":{}}`
)

// A server that fails is not the model's outcome: the run is replaced,
// as the pre-registration says of an API error. What the model did —
// answer, get stopped by the runtime for its own behaviour, keep
// returning nothing — counts. Measured case: a local server out of GPU
// memory failed 27 of 29 runs, and every one that had said anything
// before failing was counted as a valid run with no evidence.
func TestTurnEndJudgesTheRun(t *testing.T) {
	cases := []struct {
		name    string
		lines   []string
		timeout bool
		end     string
		valid   bool
		failure string
	}{
		{"answer after tools", []string{tSession, tUser, tCalls, tResult, tAnswer}, false, endAnswer, true, ""},
		{"loop guard stops the turn", []string{tUser, tCalls, tStopped, tResult}, false, endGuard, true, ""},
		{"round limit continued, then an answer", []string{tUser, tCalls, tContinued, tResult, tAnswer}, false, endAnswer, true, ""},
		{"window full", []string{tUser, tCalls, tResult, tWindow}, false, endGuard, true, ""},
		{"empty completions, finish reason stop", []string{tUser, tCalls, tResult, tEmptyStop, tNotice, tEmptyStop}, false, endEmpty, true, ""},
		{"empty with error retried, then an answer", []string{tUser, tCalls, tResult, tEmptyErr, tNotice, tAnswer}, false, endAnswer, true, ""},
		{"server ends the completion with error", []string{tUser, tCalls, tResult, tEmptyErr, tNotice}, false, endModelError, false, endModelError},
		{"request after the results failed unrecorded", []string{tUser, tCalls, tResult}, false, endAfterTools, false, endAfterTools},
		{"first request failed", []string{tSession, tUser}, false, endNoModelTurn, false, endNoModelTurn},
		{"connection lost", []string{tUser, tCalls, tResult, tConnLost}, false, endConnection, false, endConnection},
		{"timeout overrides an answer", []string{tUser, tAnswer}, true, endAnswer, false, "timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(p, []byte(strings.Join(c.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var rec record
			readTranscript(p, &rec)
			if rec.End != c.end {
				t.Errorf("end = %q, want %q", rec.End, c.end)
			}
			valid, failure := judgeRun(rec.End, c.timeout)
			if valid != c.valid || failure != c.failure {
				t.Errorf("judgeRun = %v %q, want %v %q", valid, failure, c.valid, c.failure)
			}
		})
	}
}

// No transcript means the runtime never reached the model.
func TestJudgeRunWithoutATranscript(t *testing.T) {
	if valid, failure := judgeRun("", false); valid || failure != "no transcript" {
		t.Errorf("judgeRun = %v %q", valid, failure)
	}
}

// The text the model wrote before a failure is still kept as Final —
// the record shows what it said; validity no longer rests on it.
func TestFinalKeepsTheLastTextBeforeAFailure(t *testing.T) {
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join([]string{tUser, tCalls, tResult, tEmptyErr}, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var rec record
	readTranscript(p, &rec)
	if rec.Final != "Querying first." || rec.End != endModelError {
		t.Errorf("final=%q end=%q", rec.Final, rec.End)
	}
}
