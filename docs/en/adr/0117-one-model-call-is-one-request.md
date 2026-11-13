# ADR-0117: One model call is one request — the SDK's automatic continuation is off

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-14) |
| Date | 2026-11-14 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: asked whether the Vertex AI SDK should be brought up to date. The update compiled and passed every test unchanged — and changes, by default, how many requests one call makes |
| Relates to | ADR-0057 (a usage record per model call), ADR-0116 (a retry is recorded), ADR-0006 (compaction fires on the prompt size), ADR-0100 (the connection's health check is this runtime's) |

## Context

gem-agent used `google.golang.org/genai` v1.71.0. v1.77.0 is current.
Moving to it changes one line of `go.mod`; the code compiles and the test
suite passes as it stood.

Between the two, v1.74.0 gave `GenerateContent` and `GenerateContentStream`
an *automatic continuation*, on unless a request turns it off. When a
response ends with a continuation token — finish reason `CONTINUATION`, or
a checkpoint token followed by a broken stream — the SDK sends the same
request again with the token, as many times as it takes, and hands the
caller one stream. Its own documentation says what follows from that:
timeouts, retries and billing apply to each request, and the usage it
reports is added up across them.

The provider's guide says when the endpoint offers a continuation: when a
request asks for more output than one response may carry (65,536 tokens
streamed, 32,768 otherwise), and, in a stream, at checkpoints from 32,768
output tokens on. gem-agent sets no output limit, and the longest answer
in the operator's recorded sessions is under 20,000 tokens, so with the
default left on nothing would differ today. It would differ on the first
long answer, silently.

Three things in this runtime assume a call is one request:

- **The usage record** (ADR-0057) is one per call, and its prompt count is
  read as the size of the conversation: the context meter shows it and
  compaction fires on it (ADR-0006). A count added up over several
  requests is neither. The provider's guide warns harness authors of
  exactly this.
- **The retry record** (ADR-0116) exists so that a request sent again is
  written down. A re-send inside the SDK would be one this runtime never
  sees.
- **The retry policy** refuses to retry once any output has been consumed,
  because a second attempt would duplicate it. The SDK's continuation
  resumes a stream that has already delivered output.

## Decision

1. **Update the SDK to v1.77.0.** It is the provider's own module (the
   supply-chain rule's first-party case), and staying behind buys nothing.
2. **Turn the automatic continuation off on every request**
   (`AutomaticContinuation: false`), so a model call stays exactly one
   request. A response that ends with a continuation is returned as it
   is, with its own finish reason and its own usage, and the turn loop
   treats it as any other answer that did not end in `STOP`.
3. **Hold it with tests.** Against a local endpoint that offers a
   continuation, a call makes one request — and a control shows the SDK's
   default making two, so the tests cannot go quiet if the SDK changes.
   A source check requires every request configuration in the package to
   set it.
4. **Answers longer than one response are not supported, as before.** If
   they are ever wanted, the continuation belongs in this runtime's own
   loop, with a usage record per request and a record of each re-send —
   a decision of its own.

## Consequences

- Nothing observable changes with this release: the SDK is newer and its
  one new default that would have changed behaviour is declined.
- The accounting contract (one usage record, one request) and the retry
  record's promise (every re-send is written) keep holding without
  depending on how long an answer is.
- An answer that would have needed a continuation ends where the first
  response ends. That was already so with v1.71.0.
- Two things the provider's documents describe for its newest models are
  not in this SDK version and so are not reachable by updating it: the
  setting that decides what happens when earlier turns of a conversation
  are edited, and the highest thinking level of one model.

## Alternatives considered

- **Leave the default on.** Declined for the three reasons in Context: it
  works until the first long answer and then miscounts and re-sends
  without a trace.
- **Stay on v1.71.0.** Declined: the gap only grows, and the next update
  would bring the same default with more around it.
- **Adopt continuation now, recorded properly.** Not taken: no recorded
  session has needed it, and it is a feature with its own accounting
  questions, not a dependency update.

## References

- `internal/llm/vertex.go` (`oneRequest`), `internal/llm/web.go`,
  `internal/llm/onerequest_test.go`
- `google.golang.org/genai` v1.77.0: `GenerateContentConfig.AutomaticContinuation`,
  `continuation.go`
