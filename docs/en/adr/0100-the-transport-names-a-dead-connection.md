# ADR-0100: The transport names a dead connection, not the silence

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-01) — implemented |
| Date | 2026-11-01 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "`思考中… 105 秒間データなし — 接続が失速している可能性` shows up again and again, while the agent is in fact working" |
| Amends | ADR-0033 §1 (the stall warning), ADR-0056 §1–§2 (the 90s threshold) |
| Relates to | ADR-0021 (retry only before anything is consumed), ADR-0041 §5 (`toolRunning`) |

## Context

ADR-0033 §1 turned the running status into a heartbeat, and gave it
one threshold: after N seconds with no chunk, the line says "no data
for Ns — the connection may be stalled". ADR-0056 moved N from 20s
to 90s. It had measured one source of harmless silence: a Gemini
function call arrives as one whole part, so the wire carries nothing
while the model composes a large argument (about 650 B/s, 40s for
21KB). It also concluded that no client-side signal separates
"composing" from "stalled".

### The field: silence that is not a function call

The operator's session of 2026-11-01 (v0.94.0,
`google/gemini-4-argon`, thinking `high`) has 58 main rounds. Eight of
them took 97–210s. All eight are on prompts of 230k–770k tokens, and
none of them produced a large output:

| Round time | Prompt | Cached | Output | Thoughts |
|---|---|---|---|---|
| 154.1s | 247,652 | 96% | 54 | 58 |
| 210.1s | 236,311 | 0% | 239 | 1,999 |
| 126.6s | 754,197 | 64% | 177 | 483 |
| 101.8s | 768,553 | 0% | 52 | 1,960 |

The session log records how long each round took, not when its first
chunk came. But streaming 54 output and 58 thought tokens does not
take 150s. Almost all of that round was waiting on the server. The
probe below also saw the wait on a 16k-token prompt: the first chunk
came after 90.5s in one run, and in another run nothing came for more
than 180s. The wait is not bounded by any number a threshold could
pick.

### The premise: the transport already checks the connection

gem-agent does not build its HTTP client. `genai.NewClient` hands
that job to `cloud.google.com/go/auth/httptransport`. Its base
transport runs `x/net/http2.ConfigureTransports` with
`ReadIdleTimeout = 31s` (`PingTimeout` stays at its 15s default).
On the toolchain the measurement ran on (go1.27.3), x/net v0.58.0
passes these settings to the standard library's HTTP/2 implementation
(`transport_wrap.go`).

The behaviour is this. When an HTTP/2 connection receives no frame
for 31s, the transport sends a PING. Every frame that arrives restarts
the 31s. When a PING gets no answer within 15s, the transport closes
the connection and the stream fails with
`http2: client connection lost`. So a dead connection is named at
most 46s after the last frame it delivered.

ADR-0056's conclusion holds for the content, but not for the
connection. No signal tells "composing" from "thinking", but one does
tell "the other end is there", and it already runs on every request.
Nobody in this project chose it, and nothing here pins it.

### The measurement

`internal/llm/pinghealth_live_test.go` stated its hypotheses and
refutation criteria in commits 5c85c14 (H1–H3) and 8b624c1 (H4)
before their runs. It calls the shipped `NewVertex` and `ChatStream`.
Its only hook is the dialer of `http.DefaultTransport`, which the
auth library clones. It reads the health check through
`GODEBUG=http2debug=1` and keeps only whitelisted lines, because
that log also carries the bearer token. The token client's transport
is a clone that does not ping, so every health-check line belongs to
the Vertex connection.

| Hypothesis | Runs | Result |
|---|---|---|
| H1 the connection is HTTP/2 | every run | held: health-check lines in each |
| H2 a long silence on a live connection is pinged, every ping answered, and the stream completes | 3 conclusive (about 354k tokens, 0% cached) | held: gaps of 205.4s / 147.2s / 79.1s; health checks sent 6 / 3 / 2; failures 0 |
| H3 a connection frozen after the request fails within 61s, after a health-check failure | 2 | held: 44.0s / 44.0s after the freeze |
| H4 the same, frozen after the response has started (a chunk before the freeze) | 3 (2 conclusive) | held: 46.0s / 46.0s after the freeze |

Not counted:

- One H2 run failed at 31.6s. The grep filter I applied to the test
  output dropped its error text, so the cause is not known. The timing
  fits five exhausted 429 retries, but that is a guess.
- One H4 run was void because the relay never froze. The server sent
  fewer than 4KB in 180s while every ping succeeded. That run is the
  H2 kind of silence on a small prompt.
- Several runs got `GOAWAY NO_ERROR "session_timed_out"` 47–80s in.
  The stream in flight was not affected.

The pre-registration capped the blackhole runs at 2. H4 was added
later, with its own pre-registration and 3 runs. The total spend was
about $2.3 (up to $3.0 if the failed run was billed), against the
approved cap of $3.4.

### What this means for the warning

The shipped transport already ends a dead connection's stream at most
46s after its last frame. The 90s warning cannot make that happen
sooner. It can only arrive earlier than the error in one case: the
connection died less than 46s before the 90s mark, after a frame or a
ping answer. In every other case the warning shows while the
connection is alive and answering pings, which in the field is every
case observed. A warning that fires on healthy work teaches the
operator to ignore it, which is the outcome ADR-0056 set out to avoid.

## Decision

### 1. The health check is gem-agent's own setting

`NewVertex` builds the HTTP client instead of leaving that to the
SDK. The goal is not new behaviour. The behaviour above becomes a
choice this project made and a test pins.

- **Base transport.** A clone of `http.DefaultTransport`, with
  `HTTP2 = &http.HTTP2Config{SendPingTimeout: 31 * time.Second,
  PingTimeout: 15 * time.Second}` and `MaxIdleConnsPerHost = 100`, as
  the SDK's base sets. This is the standard library, Go 1.24 and later.
  The clone keeps `ForceAttemptHTTP2`, so ALPN still negotiates h2.
- **Authentication.** `httptransport.NewClient` with that
  `BaseRoundTripper`. The credentials come from
  `credentials.DetectDefault` (cloud-platform scope).
  `X-Goog-User-Project` comes from the credentials' quota project. This
  is the construction genai does when it gets no client. Given a
  client, genai adds no auth of its own.
- **Dependency.** `cloud.google.com/go/auth` becomes a direct
  dependency. It is a first-party Google module and is already in the
  module graph.

The constructor takes the two durations as parameters. An offline
test passes short ones: it runs a local TLS HTTP/2 server, sends a
stream request through genai, and freezes the connection through a
relay. It does this twice, once before the response headers and once
after the first chunk. These are the two paths the error takes (a
wrapped `doRequest` error, and the stream scanner). Both must fail
within the bound with an error that §3 recognises. A unit test pins
the shipped values.

### 2. The stall warning is removed

`stallSeconds`, the warning branch of `heartbeatLine` and `StallFmt`
(en/ja) go. The heartbeat stays as it is:
`1m07s · 34 chunks · last 40s`. The age keeps counting and stays
neutral; it never turns into an accusation. `toolRunning` stays,
because it still keeps a side-call's thoughts out of the thought tail
(ADR-0041 §5).

Nothing replaces the warning on screen. If the operator wants to stop
a turn that is only slow, Ctrl+C is already on the line.

### 3. A lost connection is said in the operator's words

When the stream fails with the transport's lost-connection error, the
turn's error says what happened and what to do next: the connection
to Vertex AI was lost, and sending a message continues from there. The
operator's message and every completed round are already in the
history, so "send it again" would repeat the instruction. The message
does not explain how the loss was detected (ADR-0056: the supplier's
mechanism is not something the operator can act on). The text lives
in uitext (en/ja), and the raw error goes to the session log as a
`connection_lost` record. The match is on the error text, because the
transport exports no sentinel for it. That is why §1's offline test
pins it on both paths.

The file-search child (`agentic_file_search`) is the one other agent
loop. Its error goes to the main model, not to the operator, so a lost
connection there returns the transport's own text with a model-facing
next step (ask again, or search directly). The child keeps no session
log, so that text is the only record of the loss.

genai also prints the stream error itself through the standard
logger (`log.Printf("Error %v", …)` in its stream reader). gem-agent
never redirects that logger, so under the TUI the raw line would be
written over the screen. While the TUI runs, the standard logger
writes to the session log instead, one `library_log` record per line
(genai also logs hints there, such as a response too large to stream,
which would otherwise be lost). The plain REPL and `-p` keep it on
stderr. The
implementation confirms the screen with tmux before and after
(AGENTS.md: TUI rows are measured, not reasoned).

### 4. The probe stays in the tree

`pinghealth_live_test.go` (H1–H4) stays as the record of the
measurement. It is the only thing that checks what the offline test
cannot: that the real endpoint answers pings during its long silences.
It runs only by hand and costs money, so it is a record, not a gate.
The gate is §1's offline test.

### 5. lagent keeps its warning

lagent has the same warning (`stallSeconds = 90`, the same
`StallFmt`). Its premise is different. It talks to a local
OpenAI-compatible server over plain HTTP/1.1, which has no PING, and
it does not go through the auth library. Removing its warning would
remove the only sign of a hung local server. Whether lagent gets a
liveness check of its own is a separate question with a separate
measurement. This ADR changes gem-agent only.

## Alternatives considered

- **Raise the threshold again.** The measured silence runs from 79s to
  over 200s and grows with the context, so no number fits. Any number
  above about 46s also adds little, since the transport settles a dead
  connection by then.
- **No warning before the first chunk of a round.** This removes most
  of the field cases. But it still accuses a live stream mid-round
  (function calls, ADR-0056), and it keeps a claim that the transport
  already settles.
- **Reword the warning** ("waiting for the server"). The operator
  rejected this kind of status line in ADR-0056: the supplier's timing
  is not something the operator can act on.
- **Leave the client to the SDK and only remove the warning.** This is
  the smallest change. But the warning's removal is safe only because
  of the health check, and nothing here would notice if a dependency
  update dropped it. A dead connection would then hang with no warning
  and no error.
- **Keep the SDK's construction and pin it with an offline test.** The
  31s/15s cannot be shortened from outside, so the test would take 46s
  or more of real time. It would also need the TLS roots and the token
  endpoint redirected. And the setting would still be the library's
  choice.
- **Shorter ping intervals.** Not measured. Detection within 46s of the
  last frame is well inside what the operator already tolerated at 90s.
  Changing the numbers would turn a pinned observation into a new
  decision with no evidence behind it.

## Consequences

- Silence no longer produces a warning. The heartbeat age shows how
  long the wait has been. What happens to a dead connection does not
  change: it ends the turn with an error at most 46s after its last
  frame, as it already did. It now does so with a message that says
  what to do. The error path is the same for `-p` and the plain REPL,
  which never had the warning.
- ADR-0033's refusal of an automatic timeout still holds. The health
  check does not end a long stream on a live connection (H2 measured
  205s). It ends only a connection that does not answer.
- Not decided here: retrying a lost connection automatically when no
  chunk was consumed. ADR-0021 would allow it, but it adds a second
  wait to a failure the operator has not seen yet. That is a separate
  decision.

### Residuals

- The ping proves that the front end is there, not the model behind
  it. A backend that holds a stream open and never answers would keep
  a turn waiting. The heartbeat age still shows it, and Ctrl+C ends
  it. Nothing in the measurements points to this case.
- The base transport no longer goes through the auth library's
  `defaultBaseTransport`. The client-certificate (mTLS / S2A) setup it
  applies from the environment is therefore not carried for API calls,
  although the token exchange still gets it. On an mTLS-enforced
  setup, the two would take different paths. gem-agent authenticates
  with ADC and uses neither. If that ever changes, this construction
  has to be revisited.

### Surfaces to sweep in the implementation

`internal/tui/model.go`, `internal/uitext/uitext.go`, the TUI tests
that rewind the clock past `stallSeconds` (`observability_test.go`,
`review3_test.go`), the comments in `cmd/root.go`, `cmd/agentsearch.go`
and `internal/tui/msgs.go`, the header comment of
`pinghealth_live_test.go` (it describes the SDK-built client) and the
log line of `chunkgap_live_test.go` (`stallSeconds=20`), AGENTS.md ("A
silent stream is not a dead stream", and the `StallFmt` gotcha),
README.md / README.ja.md (feature line),
`docs/{en,ja}/reference/interface*.md` (heartbeat states),
`docs/{en,ja}/reference/architecture*.md` (observer line and failure
table), the INDEX files, and ADR-0033 / ADR-0056 (amendment notes).
Outside the repository: the workspace memory and the
`nlink-jp/knowledge` llm-integration entry (en/ja) that state "no
signal at any layer, raise the threshold".
