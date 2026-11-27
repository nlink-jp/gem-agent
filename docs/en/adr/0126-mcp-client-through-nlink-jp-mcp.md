# ADR-0126: The MCP client opens through nlink-jp/mcp — both eras, with the shape the runtime sees unchanged

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-27) |
| Date | 2026-11-27 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Organization ADR-030 (dual-era support for MCP 2026-07-28; clients come after the relays) and the release of the shared module nlink-jp/mcp v0.1.0. Both relays (mcp-bridge v0.4.1, slack-mcp-extender v0.6.0) are migrated |
| Amends | ADR-0072 §4 (where the client's robustness is implemented; the promises stay) |

## Context

### The client today

`internal/mcp/client.go` is a client of its own for one stdio MCP server at a time.

- The server is started when first needed and opened with `initialize` (`2025-06-18`) and
  `notifications/initialized`.
- A call that times out or is cancelled kills the child (its whole process group); the next call starts it
  again. `notifications/cancelled` is not sent.
- When the server ends (its stdout reaches EOF) or a line goes over the limit, the reader kills and reaps the
  child on the spot: an ended server is not left a zombie, and a server that sent an over-long line is not kept
  alive beside its successor (review round 4; ADR-0072 §4.8).
- Writes are bounded by a deadline: a server that stops reading its stdin cannot wedge the client, and on expiry
  the child is killed, which releases the write (review 2026-09-08, F-01). Requests from the server are refused
  with `-32601`, and that write carries the call deadline too (60 seconds by default).
- `tools/list` stops at 100 pages, 5000 tools, or a repeated cursor (ADR-0072 §4.5).
- Each `tools/call` carries the work directory in `_meta["jp.nlink/work_dir"]` (organization ADR-021 §9).
- Failures travel as types. The runtime reads `CallError.Sent` (whether the request reached the server) and
  `*RPCError` (the server's own refusal) by type, never by text (ADR-0075 §1). A call made while the server is not
  running fails with "server not running" and counts as not sent.

### What is missing

1. **It speaks the legacy era only.** 2026-07-28 removed `initialize`; this client cannot reach a modern-only
   server (organization ADR-030, Context).
2. **It holds the handshake itself.** ADR-030 §6 asks that nothing outside the module handle `initialize` or
   `server/discover`. Copies of the same specification have misread it across the organization (the version
   echo, naming 2025-03-26).

### The peers (measured on 2026-11-27, recorded)

A dual-era probe client was run against all 30 servers in `~/.config/gem-agent/mcp.json`. 29 answered
`server/discover` within 3–261 ms and fell back to the legacy era (versions 2024-11-05, 2025-06-18,
2025-11-25); Obsidian (through mcp-bridge v0.4.1) opened in the modern era, 2026-07-28. No violations. The
probe adds less than 0.3 seconds to any server's start.

## Decision

### 1. The inside becomes nlink-jp/mcp's `client`; the shape seen from outside does not change

The public names of `internal/mcp` (`NewStdio`; `Client`'s `Name`, `ListTools`, `CallTool`, `Close`;
`CallError`, `RPCError`, `Content`, `Tool`, `WithBudget`) stay. The runtime (`cmd`) does not change. The
inside is replaced:

- Each start builds `client.NewLineConn(stdout, stdin, kill, client.LineOptions{MaxLine: scannerMax})` and opens
  it with `client.Open`. `kill` is today's: SIGKILL to the process group, then reaping; it may be called any number
  of times. The line limit stays at 10 MiB and the constant keeps its name, `scannerMax` (ADR-0090 cites it by
  name).
- `Open` probes with `server/discover` and, for a legacy peer, sends `initialize` (`2025-11-25`) and
  `notifications/initialized` (module detailed design §4.2). The era does not change for the life of that start.
- `github.com/nlink-jp/mcp` is added as a dependency (an organization module written on the standard library
  alone).

### 2. Start, kill, reaping and restart stay as they are

- A server is started when first needed, and `startupTimeout` covers the start and `Open` as a whole (the probe
  and, for a legacy peer, the handshake; today's budget rule; `WithBudget` stays).
- A call that times out or is cancelled kills that start and drops it; the next call starts the server again (the
  promise of ADR-0072). `notifications/cancelled` is not sent (`CancelNotice: false`, today's decision).
- **Each start gets one watcher.** When `Client.Done()` closes (the server ended, a line went over the limit, a read
  failed), the watcher calls `kill` at once, reaping the child, and marks that start not running. The module only
  closes `Done`; it does not kill the child, so today's promise (an ended server is not left until the next call;
  ADR-0072 §4.8) is kept by this watcher. Why the start ended comes from `Client.Err()`.
- Before a call, the start's `Done()` is checked. If it is closed, the call starts the server again without
  sending anything to the old one.

### 3. Failures keep their types and meanings

- `CallError.Sent` is `false` only when the request is known not to have reached the server: when
  `errors.Is(err, client.ErrNotSent)` (the module's promise that the request never reached the peer), and when the
  start had already ended before the call (§2's check; today's "server not running"). When the start ends during
  the call (`client.ErrClosed`), whether the request arrived is unknown, so `Sent` is `true` (the safe side, as
  today).
- A JSON-RPC error from the server (the module's `*mcp.Error`) is copied into today's `*RPCError` (same `Code`
  and `Message`). How the runtime tells a refusal (ADR-0075 §1) does not change.
- A failed start still travels as `startError`. Its phase names change from `"initialize"` and
  `"initialized notification"` to `"open"` (the probe and, for a legacy peer, the handshake).
- **Wording**: what this package writes ("timed out after <budget>", "server not running", the phase names) stays
  as it is. A failure from the module is wrapped with the module's wording (a repeated cursor, a line over the
  limit). A line over the limit can be told by `errors.Is(err, client.ErrLineTooLong)`, not by its text. The runtime
  reads types (ADR-0075 §1), so a change of wording changes no decision.

### 4. Listing and calling tools

- `ListTools` uses the module's `ListTools`. The bounds (100 pages, 5000 tools, a repeated cursor) are the
  module's and equal today's values. Each tool's `inputSchema` is returned as today's `map[string]any`.
- `CallTool` uses the module's `CallTool`. The work directory goes in `meta` as `jp.nlink/work_dir` (in the modern
  era the module adds the version and the rest to `_meta`). The result's `content` is read into today's
  `Content` (data such as images is base64-decoded; data that will not decode becomes a text block saying so).

### 5. Requests from the server

The module handles them. A legacy peer's `ping` gets `{}` and anything else `-32601`; nothing is written for a
modern peer's requests (by the specification a server writes no requests on stdio). Three things differ from
today, and all three are accepted:

- `ping` gets the `{}` the specification asks for, not today's `-32601`.
- The refusal's wording is the module's.
- Answers are written one at a time from the module's queue, and what overflows the queue is dropped (module
  detailed design §4.4). The deadline for writing an answer is kept at today's length by passing the call deadline
  (60 seconds by default) as `RefuseTimeout`. On expiry the write is released, and a wedged server is killed by the
  call deadline as today.

### 6. Not adopted

Roots, Sampling, Logging, `subscriptions/listen`, the tasks extension and `input_required` (organization ADR-030
§4). A modern result of `input_required` comes back from the module as `ErrUnknownResultType` and is a failure of
that call.

### 7. The tests' stand-in servers (and ADR-030 §6)

The tests of `internal/mcp` need stand-ins for misbehaving peers (one that stops reading stdin, exits midway, sends
over-long lines, sends requests, keeps returning pages, returns errors), and those stand-ins write the legacy
handshake by hand. The tests of a well-behaved peer move to the module's probe server (`probe.Serve`) and write no
handshake. The misbehaving stand-ins remain. They are what ADR-030 §6 calls "test code that stands in for a peer",
but §6 lists its exemptions by path, and the only gem-agent path it names today is `tools/`.

**Dependency (the decision of the organization ADR's owner):** ADR-030 §6 needs an amendment naming
`internal/mcp/*_test.go` among its exemptions. The decision is asked for together with this ADR's approval. Without
it, the design goes back to adding misbehaving stand-ins to the module's probe server (a change to the module).

The stand-ins answer `server/discover` with `-32601`, as the 29 measured legacy servers do. If they did not, the
probe would wait out the module's 5-second default and use up the tests' start budget (300 ms to 3 seconds).

## Detailed design

| Component | Change | Oracle |
|---|---|---|
| `internal/mcp/client.go` | The hand-written reading, sending and handshake become one module `Client` and one watcher per start. The public names and the promises of their behaviour stay | ADR-0072, 0075; module detailed design §4 |
| `internal/mcp` (tests) | The stand-ins change as §7 says, and the tests that touch internals are rewritten to check from outside (Test design) | §1–§7 |
| `go.mod` | Adds `github.com/nlink-jp/mcp` | §1 |
| ADR line citations | ADRs citing lines of `client.go` (0089, 0090, 0091) are pointed at the lines of the same logic (`scripts/remap-adr-citations.py`, by hand where it cannot remap). `scannerMax`, which 0090 cites by name, stays | — |

Bounds: a line of 10 MiB (as today). The tool listing's bounds are the module's (today's values). The probe waits
the module's default (5 seconds), inside `startupTimeout`. Answers to the server's requests are written within the
call deadline (60 seconds by default).

## Test design

- **Today's tests** (`client_test`, `review4_test`, `send_stall_test`, `generation_test`, `config_test`) pass after
  the changes below. They then confirm that the deadline against a server that stops reading stdin, the server's
  exit, the line limit, requests from the server, the page bounds and the `Sent` distinction are the same after the
  replacement.
  - The stand-ins answer `server/discover` with `-32601` (§7).
  - Tests of the handshake order become "`server/discover` → for a legacy peer `initialize` →
    `notifications/initialized`"; the phase wording becomes `open`.
  - Tests that touch internals (`send`, `readLoop`, `gen`, `alive`) — the two in `generation_test` and
    `TestReadLoopEOFReapsTheIncarnation` — are rewritten to check the same promises from outside: an old start's
    writes never reach its successor (structural now, with one `Client` per start, but checked by recording what the
    successor receives after a restart), and an ended server is reaped without waiting for the next call.
  - Tests of wording keep checking what this package writes ("timed out", the budget's length); what comes from the
    module is checked with `errors.Is(err, client.ErrLineTooLong)` (the line limit) or the module's wording (a
    repeated cursor).
- **Watcher**: when the server ends by itself, and when it sends an over-long line, the child is reaped without a
  further call (`kill` is called and no process is left).
- **`Sent`**: a call after the start ended is `Sent=false` (it starts the server again without sending). A call
  whose write ran out of time is `Sent=false`; a call that ran out of time waiting for the answer is `Sent=true`.
- **Requests from the server**: a legacy `ping` gets `{}` and anything else `-32601`, and the answer is written
  within the call deadline.
- **Probe matrix**: the module's three probe servers (legacy only, dual-era, modern only) on this client's start
  function (pipes). Expected results: the specification's compatibility matrix (a dual-era client: legacy, modern,
  modern). In every pair `ListTools` and `CallTool` (the probe's `probe_echo` tool) work, and in the modern era the
  work directory rides in `_meta`.
- **Restart**: a call after a timeout opens a new start, probing again.
- **Real peers**: start with a model the run cannot reach, and record the opening and listing of the 30 registered
  servers (no model cost). Obsidian (through the relay) opens in the modern era, the rest in the legacy era.

## Development plan

1. Approval of this ADR (en and ja), together with lagent ADR-0056 (the twins keep one shape), and the decision on
   §7's dependency (the amendment of ADR-030 §6).
2. Dependency and the client's replacement (§1–§5). Done when: today's tests (with Test design's changes) pass with
   `-race`.
3. Tests of the watcher, `Sent`, requests from the server, the probe matrix and restart. Done when: all pass.
4. ADR line citations, README en/ja, AGENTS.md, CHANGELOG.
5. Real-peer check and one independent pre-release pass (against this ADR, together with lagent).
6. Release (minor: 0.110.0).

## Consequences

- gem-agent reaches modern-only servers too, and legacy servers as before.
- MCP handshake code leaves gem-agent's own code; updates arrive through the module. It stays in the tests'
  stand-ins (§7).
- Every start adds one probe round trip (measured: under 0.3 seconds).
- Failures that come from the module carry the module's wording.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Add the probe to today's client | Leaves the copy of the handshake that ADR-030 §6 forbids; a misreading like the version echo would happen again in another copy |
| Change the shape the runtime sees to the module's too | The wiring in `cmd`, the failure types and the tests would change; the migration would spread over the whole runtime. Keeping the outer shape costs nothing |
| Remember the probe's result across restarts of a server | A MAY in the specification; the 30 servers measured probe in under 0.3 seconds, not worth a mechanism |
| Leave reaping an ended server to the next call | Brings back the zombie and orphan defects fixed in review round 4 |
| Add the misbehaving stand-ins to the module's probe server | Puts the tests' convenience into the module's public parts; stand-ins are the kind of code §6 exempts in the first place |

## References

- Organization ADR-030 (§4, §6), ADR-025, ADR-021
- nlink-jp/mcp v0.1.0: reference manual (`client`), detailed design §4
- ADR-0072 (whole-code review, round 4), ADR-0075 (a remote fault is the server's state), review 2026-09-08 (F-01)
- lagent ADR-0056 (the same migration)
