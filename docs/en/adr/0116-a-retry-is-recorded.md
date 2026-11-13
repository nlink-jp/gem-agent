# ADR-0116: A retry is recorded — the transcript tells a wait from a slow answer

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-13) |
| Date | 2026-11-13 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: asked where a session's time and cost go. The transcripts showed main-model calls taking one to two minutes at large context, and could not say whether any of that was waiting on a rate limit |
| Relates to | ADR-0033 (turn observability: a retry is shown, and nothing was kept), ADR-0057 (the usage record), ADR-0071 (`/clear` opens a new transcript), ADR-0100 (the `connection_lost` record) |

## Context

A model call that meets a 429 or a 5xx waits and tries again, up to five
attempts (ADR-0033 §2). The TUI's status line shows the wait while it
lasts. Nothing is written: the transcript holds the message before the
call and the `usage` record after it, and the seconds between them are
the answer's whole cost in time.

Two different things fill those seconds. The endpoint may be slow to
answer a large request, or it may have refused the request one or more
times and the runtime waited between attempts. The provider's
pay-as-you-go documentation describes a per-organisation throughput
threshold above which traffic is served only when capacity is spare, and
names higher latency and 429 as what the excess meets. A single request
from a long session can be a large share of such a threshold by itself.
Whether that is what makes a given session slow is a question about
retries, and the record that would answer it does not exist. The plain
REPL and one-shot mode do not show a retry at all.

## Decision

1. **Every backoff retry of a model call writes a `model_retry` record.**
   It carries the model asked, the attempt about to start and the
   maximum, the status that failed the attempt before it, that attempt's
   error text within 300 runes, and the backoff in milliseconds. The
   record's time is when the failed attempt came back, so the time an
   attempt took and the wait after it are both readable.
2. **Every model slot reports to the same recorder.** The risk, summary
   and librarian slots are copies of the main backend. They do not share
   its observer or its thinking dial (ADR-0033 §3, ADR-0098 §5), and
   they do share this: a slot's retry is a wait on the same endpoint,
   and a record holding the main loop's alone would read as "no retries"
   for the rest. The one-shot calls behind the web tools, which retry on
   a loop of their own, report to it too.
3. **The record is diagnostic.** It is not conversation state: resume
   skips it, nothing in it reaches the model, and a failed write does
   not touch the call. It follows the transcript in use across `/clear`.
4. **The retry policy does not change.** Which errors are retried, how
   many times and after how long stay as they were.

ADR-0033 said of its events that nothing in them enters the transcript.
That boundary now holds for the heartbeat and for thought text, which
stay display-only; a retry is the exception this ADR makes.

## Consequences

- A transcript now separates "the call waited on a rate limit" from "the
  endpoint took this long", for every model the session used, in every
  mode including the two that show nothing on screen.
- A transcript written by an earlier build has no such records. Their
  absence there means "not recorded", not "no retries"; a reader tells
  the two apart by the build in the session header.
- The endpoint's error text enters the session log. It is third-party
  text in a diagnostic record, as an MCP server's error is in
  `mcp_fault`, and it is bounded.
- The record says a retry happened and what the endpoint said. It does
  not say which quota or threshold was involved unless the endpoint's
  text does.

## Alternatives considered

- **A retry count on the `usage` record.** Declined: a call that fails
  after its last attempt writes no usage record, so the retries that
  matter most would be the ones lost; and the usage record's shape is an
  accounting contract other tools read (ADR-0057, ADR-0066).
- **Recording the main loop only.** Declined for the reason in §2.
- **Recording time to first chunk on every call.** Not taken here. It
  answers a different question — where a slow answer spends its time —
  and is worth asking only after this record shows how much of the
  silence is retries.
- **Telemetry instead of the transcript.** Declined: telemetry is opt-in
  and leaves the machine; the question is asked of sessions that already
  ran, from the files they left.

## References

- `internal/llm/vertex.go` (`SetRetryRecorder`, `retried`),
  `internal/llm/web.go` (`generateWithRetry`), `cmd/retrylog.go`
- `docs/en/reference/sessions.md` §Retries in the transcript
