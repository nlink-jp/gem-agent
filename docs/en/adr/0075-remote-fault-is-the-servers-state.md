# ADR-0075: A remote tool's repeated identical error is the server's state, and the runtime says so

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-06, revised after the independent review in §5) |
| Date | 2026-09-05 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Session 4d6bb685 (2026-09-05): the BigQuery remote MCP server, reached through mcp-bridge, answered ten `execute_sql*` calls with "Required parameter is missing: query" although every call carried `query`; the model spent 56 rounds and 6.5M prompt tokens investigating the runtime it runs in |
| Amends | ADR-0040 (a sibling detector beside the loop guard), ADR-0058 (the intake no longer prefixes `error:` itself), ADR-0060 §3 (a second tool-message field trusted by provenance) |
| Relates to | ADR-0076 (what the same session read on the way, and why that is left alone) |

## Context — what happened

Every fact below is from the transcript or from a replay; nothing is
inferred from the model's own account.

| time (JST) | call | answer |
|---|---|---|
| 23:10:03 | `execute_sql_readonly` | rows |
| 23:10:30, 23:10:43 | two queries with a wrong function name | the SQL errors the model deserved |
| 23:10:54 | a valid query | **"Required parameter is missing: query"** |
| 23:11:13, 23:11:26 | two more valid queries | rows |
| 23:11:39 → 23:14:22 | seven valid queries, `execute_sql` included, `SELECT 1 AS test` included | the same error, every time |
| 23:13:13 | `list_table_ids` (same server) | the table list |
| 23:16:34 → 23:18:47 | eleven queries, now with `dryRun: false` | rows |

- Every one of the ten failed calls carried `query`: the transcript's
  arguments, the gate's detail line, and the code agree. The executor's
  only edit to arguments is removing its own `gem_agent_purpose` field
  (ADR-0047 §2); `mcp.Client.CallTool` marshals the map as it is.
- Replayed an hour later through the same mcp-bridge binary and the
  same token command, the four shapes — no `dryRun`, `dryRun: false`,
  the failing multi-line query verbatim, `SELECT 1 AS test` — all
  return rows. Only an undeclared argument fails, with a different
  message ("Request contains an invalid argument."). The server's own
  schema declares `dryRun` optional. The fault was transient on the
  server side. Nothing on the path records requests — mcp-bridge keeps
  no log by design, gem-agent discards MCP stderr unless
  `GEMAGENT_MCP_STDERR=1` — so the bytes that left cannot be shown;
  the replay is the evidence.
- The "fix" the model found, `dryRun: false`, followed a gap of two
  minutes during which nothing was sent (longer than the bridge's 90 s
  idle-connection timeout). It coincided with the server recovering.

What the model did between the first and the last error, in order:
reworded the query and retried, three times (the ADR-0040 loop guard
keys on identical canonical arguments and never fired); switched to
`execute_sql`, the mutating sibling (the operator approved it); ran
twelve read-lane shell commands, none of which reached a gate — `ps
aux`, `ls`/`grep` of `~/.config/gem-agent/mcp.json`, `cat
~/.config/mcp-bridge/config.json`, `strings` on the bridge binary,
`find` under `~/works`, and a `mcp-bridge run bigquery` of its own
inside the sandbox (which failed on the token command, the read lane
having no network); searched GitHub (the operator denied the second
search); searched the web; then guessed.

## Context — why the runtime let it happen

This is a runtime defect, not a model defect, for three reasons.

1. **The model has rules for two kinds of refused call and none for a
   failed one.** The system prompt says of a gate denial: "a decision,
   not an obstacle — ask how to proceed instead of retrying", and of a
   lane refusal: "needs the lane the refusal names, not a retry". Both
   are triggers delivered in the function response at the moment they
   apply, and both worked in this session (after the 23:08 denial the
   model stopped and asked for the project id). A remote server's
   error has no rule and no trigger. The model filled the gap with the
   path nobody wrote: debugging the runtime. This is the lesson of
   ADR-0062 (a capability without a trigger never fires) and ADR-0063
   (a prohibition breeds a third path) once more: what is missing is a
   trigger, not a prohibition.
2. **The model cannot tell whose words the error is.** A transport
   failure and a server-reported `isError` both begin `error:`; the
   first happens to name the server and tool, the second does not, and
   neither says who is speaking or whether the arguments arrived. The
   runtime knows both: the executor knows the provenance and knows it
   passed the arguments through untouched.
3. **The one detector that exists cannot see this signal.** The loop
   guard keys on identical arguments, by design — iterating on a syntax
   error looks the same from outside and must not trip it. The signal
   that was present here — the same error text for different arguments —
   is exactly what the loop guard ignores, and it is the strongest
   evidence that the arguments are not the cause. Counting it is
   mechanical; the model does it badly across reworded calls.

## Decision

### 1. Provenance is in the rendering: whose words the error is

A failed MCP call has one of three provenances, and the review (§5)
found the third hiding inside the second: the server's `isError`
result; the server's JSON-RPC error object (`-32602 Invalid params`
and kin — the server's words too, delivered as an error, not a
result); and a runtime failure (transport, timeout, exit, framing).
The MCP adapter returns every failure as a typed value,
`*tools.RemoteError{Server, Tool, Kind, Text, Sent}`, through `Run`'s
error return, and `mcp.Client.CallTool` wraps every failure of its own
in a typed `*mcp.CallError` — including a server that would not start —
so the adapter can tell an `*mcp.RPCError` from a transport cause
without matching text. `Sent` is the client's fact whether the call's
arguments were written to the server at all: a rejection is the server
refusing a call that was sent; an `RPCError` answering `initialize` is
the server refusing to start, and the call is incomplete and unsent
(pre-release review, §5). The executor detects the value
with `errors.As` — the ADR-0040 rule for `RoundLimitError` — and
renders it, keeping the `error:` prefix the audit outcome and the
attach guards test:

- result: `error: MCP server "bigquery" answered execute_sql_readonly
  with an error:` followed by the server's text;
- rejection: `error: MCP server "bigquery" rejected the call to
  execute_sql_readonly: rpc error -32602: …`;
- runtime: `error: gem-agent could not complete execute_sql_readonly
  on MCP server "bigquery": <cause>`.

The intake (`mcpIntake.render`) keeps its budget and spill duties and
stops prefixing `error:` itself (ADR-0058 amended). The remote tool's
name appears inside these texts, which travel wrapped as data like any
result.

### 2. A fault counter in the executor, per tool, on identical error text

- Key: the registry tool name (server + tool). Count: consecutive
  failed results of that tool with byte-identical text. Any other
  outcome of the same tool — rows, a different error — resets it; a
  denied or interrupted call is not an outcome of the server and leaves
  it alone. Threshold `mcpFaultThreshold = 3`, the loop guard's number,
  so the ladder has one rung length.
- Per tool, not per server: `list_table_ids` succeeded in the middle of
  the `execute_sql` fault; the observed granularity is the tool.
- Identical text, not any error: the model's own iteration on a syntax
  error produces a different text each time and must not trip it; a
  server fault produces the same text whatever the arguments.
  Accepted limit: an error that embeds a request id or a timestamp
  differs each time and will not trip it — the round ladder stays its
  ceiling. Measured after release, not designed around.
- Per turn, like the loop guard's state: both start fresh with every
  turn (`Run`), so an operator who has read the report and says "try
  again" is not answered by the note on the first call. `/clear`
  begins a new turn and therefore a fresh count.
- Independent of the loop guard: three identical calls still trip
  ADR-0040 first; three reworded calls with one answer trip this.

### 3. At the threshold the runtime speaks — outside the nonce tag, by provenance

`llm.Message` gains a tool-role field `runtime_note` (additive, exactly
like `denial`), set in one function — `Agent.Run`, where the tool
message is built — when the counter reaches the threshold, and again on
every further identical error with the updated count.
`wrapToolMessages` appends it after the wrapped server text, the
mechanism it already uses for the attachment note. The runtime's words
are gem-agent's, at the trust level of the system prompt; the server's
text stays wrapped as data. Recognizing the note by content is rejected
for the ADR-0060 §3 reason: a server that returns a note-shaped string
must not ride unwrapped.

The note states what the runtime measured and names the action — the
shape of the denial rule this model already follows. It names the tool
by its registry name (`mcp__bigquery__execute_sql_readonly`), the
identifier the model already holds unwrapped in every request, never
the server-supplied name. Where the server spoke (result or rejection):

> gem-agent: MCP server "bigquery" has answered
> mcp__bigquery__execute_sql_readonly with this same error 3 times in a
> row. gem-agent sent each call's arguments to the server exactly as
> you wrote them, removing only its own gem_agent_purpose field. Tell
> the user what you asked and what the server answered, and ask how to
> proceed.

Where gem-agent could not complete a call it had sent:

> gem-agent: 3 calls in a row to mcp__bigquery__execute_sql_readonly
> could not be completed, each failing the same way (the result above
> says how). gem-agent sent each call's arguments exactly as you wrote
> them. Tell the user what you asked and what happened, and ask how to
> proceed.

Where the call never reached the server (the server would not start,
or could not be written to), the note claims no sending:

> gem-agent: 3 calls in a row to mcp__bigquery__execute_sql_readonly
> could not be completed — each failed before the call reached the
> server (the result above says how). Tell the user what you asked and
> what happened, and ask how to proceed.

Neither note says whose fault it is. The runtime measured two facts —
the arguments left unchanged, the answer repeated — and the review (§5)
showed the first draft's verdict ("not a fault in your arguments or in
this runtime") outran both: a model that misnames a required parameter
consistently gets the same server error for every rewording, and a
timeout is `[mcp].call_timeout_sec`, gem-agent's own setting. The
action is the same either way, and it is the one the denial rule asks
for.

No prohibition is written. ADR-0063 measured what a prohibition does:
it generalises and breeds a fourth path. The trigger and the named
action are the whole instruction.

In `-p` there is nobody to ask; the model's report ends the turn, as a
denial does (ADR-0060). In `--auto` the operator is present and reads
the report. The transcript gets an `mcp_fault` record `{server, tool,
kind, sent, count, round, error}` (error clipped) at the threshold and on
every further hit, so the note's effect can be measured after release
(§5, decision point 2); the operator sees one notice per streak at the
threshold. Telemetry is unchanged: `tool.call` already carries
`outcome=error` per call.

### 4. What is not done, and why

- **No runtime retry.** A transparent retry hides the server's state,
  and the model's own retry is the retry.
- **No classification of error text** (transient versus permanent, the
  server's fault versus the model's). Text is an unbounded domain; the
  server that could have classified did not, and the runtime measured
  only what §3 says.
- **No hard stop.** The ladder escalates, never kills (ADR-0040): the
  note, then the round ladder.
- **No prompt change.** The trigger is delivered in the function
  response at the moment it applies — the one slot that reaches the
  model mid-turn (ADR-0012, ADR-0060 §2).

### 5. Independent review (2026-09-06) and revisions

A code-verified review by a fresh-context verifier, with every Seatbelt
and behaviour claim re-run, produced these changes to the first draft:

- **A third provenance.** `rawCall` returns a server's JSON-RPC error
  object through the error return, so the draft's two shapes would have
  rendered `-32602 Invalid params` as "gem-agent could not complete".
  §1 now has three kinds, typed end to end.
- **The note carries the registry name**, not the remote tool's: the
  unwrapped position is the one ADR-0060 §3 reserved for gem-agent's
  and the operator's words, and a server's tool name is a third
  party's string.
- **The note asserts only what was measured** — see §3. The draft's
  "not a fault in your arguments or in this runtime" was neither
  measured nor always true.
- **Per-turn lifetime, stated.** The draft said the counter is cleared
  "with the loop guard's state at `/clear`"; the loop guard's state is
  cleared at every turn, not at `/clear`. §2 says per turn.
- **The architecture test covers both fields.** The draft said an
  archtest pins `RuntimeNote` "as `Denial` is"; no such test existed
  for `Denial` — only a behaviour test. `TestProvenanceFieldsAreSetOnce`
  now pins both to `Agent.Run`.
- **Facts corrected.** `--auto` has an operator present (the draft
  grouped it with `-p`); mcp-bridge's pass-through nature is its
  README's stated scope, not its ADR-0001 (which is about
  pre-registered OAuth clients); a transport failure and a
  server-reported error were distinguishable by shape already — what
  was missing was the provenance said out loud.
- **Decision points resolved.** Threshold 3 (one rung length with the
  loop guard, and the false-exoneration risk grows as the threshold
  falls). No middle rung in v1; the `mcp_fault` record carries the
  round so the note's effect can be measured first. Runtime failures
  are counted, under their own note.

A second independent pass over the implementation, before release,
added:

- **`Sent`.** `CallTool` returned a start failure unwrapped, so a server
  refusing `initialize` with a JSON-RPC error rendered as "rejected the
  call" and, after three restarts, the note said the arguments had been
  sent — they had never left. Every failure of the client now travels in
  `CallError` with `Sent`; a rejection requires a sent call; the
  incomplete note has a variant that claims no sending. Tests cover the
  start refusal, the unwritten request and the undecodable result.
- **Wording.** The approval reference and the RFP said "exactly two
  tool results skip the wrap"; skill bodies skip it too (ADR-0010), and
  the note does not unwrap the result — it is appended after the
  wrapped result. Both now say so.
- **Resume.** The provenance fields come back from the transcript
  through `json.Unmarshal`, which the architecture test does not see.
  The transcript lives in the state directory, outside every lane's
  write reach and outside the file tools' roots, so this is the class
  ADR-0060 §3 already accepted for `denial`, not a new one — recorded
  here so the next reader does not rediscover it.
- Recorded, not changed: a start failure's cause still carries the
  phase (`initialize: rpc error …`); the operator notice is English-only
  and outside `make labels`' walk, like every agent-level notice; the
  CHANGELOG cites the ADR, as every entry since 0.1.0 does.

## Alternatives considered

- **A prompt rule about repeated errors** — rejected. A rule without a
  trigger does not fire (ADR-0062, measured), and a prohibition breeds
  a third path (ADR-0063, measured).
- **A per-server counter** — rejected: a healthy sibling tool resets it
  (observed at 23:13:13).
- **Keying the counter on arguments as well** — rejected: that is the
  loop guard; this detector exists for the case it cannot see.
- **Asking mcp-bridge to annotate upstream errors** — rejected. The
  bridge is a pass-through by its stated scope (no governance layer,
  no audit log), and the fact the model needs — "gem-agent sent what
  you wrote" — is one only this runtime can assert.
- **Closing the read lane so the investigation cannot happen** — that
  is ADR-0076's subject, decided the other way; and it would not have
  stopped the retries, the escalation to `execute_sql`, the GitHub
  search or the web search.

## Consequences

- `tools.RemoteError` (three kinds) and `mcp.CallError`; the MCP
  adapter returns the first, `CallTool` the second; the executor
  renders the three shapes of §1 and the intake stops prefixing
  `error:`.
- `llm.Message.RuntimeNote` (`runtime_note`); transcript schema version
  unchanged (additive). An old build resuming a new transcript ignores
  the field: the note is missing from the replay, safety unaffected.
- The counter lives in the agent beside the loop guard's per-turn
  state and starts fresh with every turn.
- The `mcp_fault` transcript record (`sent` included) and one operator
  notice per streak. A resumed transcript restores both provenance
  fields as written — the state directory is outside every lane's and
  every file tool's reach (§5).
- Tests: the counter (reset on rows and on a different text, untouched
  by a denial, fires at 3 and again at 4, per tool); the wrap (the note
  rides outside the tag exactly when the field is set; a tool result
  whose text is the note stays wrapped); the three renderings and their
  `errors.As` detection; the adapter's mapping of `isError`, RPC error
  and transport failure; the `mcp_fault` record; a replay of the
  4d6bb685 shape — three reworded calls, one answer — ending with the
  note; `TestProvenanceFieldsAreSetOnce` in `internal/archtest`.
- Docs: README (approval paragraph, both languages), sessions reference
  (the record and the field), approval reference (the second trusted
  field), architecture reference (the wrap), tools and integration
  references (the three shapes and the note), the RFP's security layer
  3, AGENTS.md gotchas, CHANGELOG; *Amended by* lines under ADR-0040,
  ADR-0058 and ADR-0060.
