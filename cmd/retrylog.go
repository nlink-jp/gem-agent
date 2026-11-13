package cmd

import (
	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/llm"
)

// kindModelRetry is the session-log record of one backoff retry of a
// model call (ADR-0116). Diagnostic: Load skips it.
const kindModelRetry = "model_retry"

// retryRecord is what a model_retry record carries: which model was
// asked and by which kind of call, which attempt is about to start out
// of how many, the HTTP status that failed the one before with the
// endpoint's status token, and how long the backoff waits. The record's
// own time is when that earlier attempt came back, so the time spent on
// a failed attempt and the wait after it can both be read off the
// transcript. Every field is always written: an empty source or status
// is a fact about that retry, not a record from an older build.
func retryRecord(ev llm.RetryEvent) map[string]any {
	return map[string]any{
		"model":    ev.Model,
		"source":   ev.Source,
		"attempt":  ev.Attempt,
		"max":      ev.Max,
		"cause":    ev.Cause,
		"status":   ev.Status,
		"delay_ms": ev.DelayMS,
	}
}

// recordRetries sends every retry of a model call — the main loop's and
// every side call's, they share the recorder — to the session log. A
// retry is otherwise shown in the TUI's status line and gone: a call
// that waited out three rate limits and one the endpoint was slow to
// answer look the same in the transcript, and telling them apart is
// the first step of asking why a session was slow. Best-effort like
// every diagnostic record: a failed write does not touch the call.
func recordRetries(backend *llm.Vertex, log agent.SessionLog) {
	installRetryRecorder(backend.SetRetryRecorder, log)
}

// installRetryRecorder is recordRetries with the backend's setter
// passed in, so a test can fire a retry without a model call.
func installRetryRecorder(install func(func(llm.RetryEvent)), log agent.SessionLog) {
	install(func(ev llm.RetryEvent) {
		_ = log.Log(kindModelRetry, retryRecord(ev))
	})
}
