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
attempts (ADR-0033 §2 introduced the retry and its line on screen; the
count was three then). The TUI's status line shows the wait while it
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
   It carries the model asked, the kind of call (`source`, in the usage
   record's words), the attempt about to start and the maximum, the HTTP
   status that failed the attempt before it, the endpoint's status token
   for it, and the backoff in milliseconds. It is written before the
   wait, so the record's time is when the failed attempt came back.
   Every field is always written.
2. **Every model slot reports to the same recorder.** The risk, summary
   and librarian slots are copies of the main backend. They do not share
   its observer or its thinking dial (ADR-0033 §3, ADR-0098 §5), and
   they do share this: a slot's retry is a wait on the same endpoint,
   and a record holding the main loop's alone would read as "no retries"
   for the rest. The one-shot calls behind the web tools, which retry on
   a loop of their own, report to it too.
3. **The call site names the source.** The backend cannot tell a risk
   review from the main loop when both use one model, which is the
   default configuration. Each call site wraps its context with the name
   its usage record uses; the outermost name stands, so the calls the
   file-search child makes carry that tool's name. An architecture test
   fails a model call that does not name itself.
4. **Only tokens are kept from the endpoint's answer.** The status code
   and the status token are read from the API error value. Its message
   is not recorded: it is free text this runtime does not control, an
   upstream's message can quote back what was sent to it, and a
   transport error quotes the request's address.
5. **The record is diagnostic.** It is not conversation state: resume
   skips it, nothing in it reaches the model, and a failed write does
   not touch the call. It follows the transcript in use across `/clear`.
6. **The retry policy keeps its shape; two defects in it are fixed.**
   Which statuses are retried and how many times stay as they were. The
   pre-release review of this record found that the web tools' loop
   decided from the error's text rather than its status, so an error
   that merely quoted "502" was retried, and that the backoff's jitter
   let one first retry in four fire with no wait. Both are corrected,
   because the record would otherwise have written down a cause that was
   a guess and a wait of zero.

ADR-0033 kept thought text out of the transcript (§3) and wrote nothing
down for its other events either; the code and the architecture
reference said all of them were display-only. That now holds for the
heartbeat and for thought text. A retry is the exception this ADR makes.

## Consequences

- A transcript now separates "the call waited on a rate limit" from "the
  endpoint took this long", for every model call the session made, in
  every mode including the two that show nothing on screen.
- Builds before v0.102.0 wrote no such record. In a transcript from one
  of them the absence means "not recorded", not "no retries". A resumed
  session keeps its first header, so the build that wrote a stretch of a
  transcript is the `version` of the `resumed` record before it.
- The record says a retry happened, to which kind of call, and with
  which status. It does not say which quota or threshold was involved:
  that would be in the endpoint's message, which is not kept.
- The transcript does not mark when a model call starts, so the time a
  refused attempt took can be read for a main-loop call that directly
  follows a message and not, in general, for the others. The waits
  between attempts are exact for all of them.
- The recorder was installed by one line of `cmd/root.go`; a test pins
  that line, and the record was produced once by the built binary
  against a local endpoint that answers 429 before the release.

## Alternatives considered

- **A retry count on the `usage` record.** Declined: a call that fails
  after its last attempt writes no usage record, so the retries that
  matter most would be the ones lost; and the usage record's shape is an
  accounting contract other tools read (ADR-0057, ADR-0066).
- **Recording the main loop only.** Declined for the reason in §2.
- **Keeping the endpoint's error text, cut to a length.** The first
  draft did. Declined on review: the organisation's own record of an
  upstream that echoed part of a credential in its error is the reason
  not to pass upstream text through, and a bound on its length does not
  change what it may hold.
- **Recording time to first chunk on every call.** Not taken here. It
  answers a different question — where a slow answer spends its time —
  and is worth asking only after this record shows how much of the
  silence is retries.
- **Telemetry instead of the transcript.** Declined: telemetry is opt-in
  and leaves the machine; the question is asked of sessions that already
  ran, from the files they left.

## References

- `internal/llm/vertex.go` (`SetRetryRecorder`, `retried`,
  `transientStatus`, `retryBackoff`), `internal/llm/types.go`
  (`WithSource`), `internal/llm/web.go` (`generateWithRetry`),
  `cmd/retrylog.go`, `internal/archtest/retrysource_test.go`
- `docs/en/reference/sessions.md` §Retries in the transcript
