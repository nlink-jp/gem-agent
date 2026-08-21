# ADR-0033: Turn observability — heartbeat, retries, thoughts

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-21 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the agent sometimes sits on "thinking…" with no response for a very long time, and nothing on screen says what is happening |

## Context

During a turn the status line showed a spinner and the static word
"thinking…". Three situations rendered identically behind it:

1. the model is healthily thinking (thought chunks ARE arriving — a
   high thinking level can spend minutes before the first visible
   token);
2. the connection has stalled and nothing is arriving at all;
3. the stream failed and the client is sitting in a silent
   exponential-backoff retry (up to 3 attempts, 15s max backoff — over
   half a minute of deliberate silence with no indication).

The operator cannot distinguish "wait" from "interrupt and retry", and
"what is it doing?" has no answer on screen at all.

## Decision

### 1. A stream heartbeat, always on

The backend reports every received chunk to an observer; the TUI's
running status becomes live:

    thinking… 1m07s · 34 chunks · last 0s

— elapsed since the turn started, chunks received, seconds since the
last one. Thought-only and metadata-only chunks count too: liveness is
the question, not visibility. When nothing has arrived for 20s the
line switches to the warning style and says so — a stalled connection
now LOOKS different from a thinking model, and the operator knows
Ctrl+C is on the table. No automatic timeout is added: long thinking
is legitimate, and the display plus the operator beats a timer that
kills real work.

### 2. Retries are visible

The backoff loop reports each scheduled retry; the status line shows
`retry 2/3 (429) — waiting 4s` instead of silence. Deliberate waiting
must not be indistinguishable from a hang (the "silent deliberate
states" lesson).

### 3. Thought summaries, displayed live and kept ephemeral

With `[tui].show_thoughts = true` (the default), the request asks for
thought summaries (`IncludeThoughts`) and the TUI streams them into
the live area in the dim style, replaced as the real answer starts.
This is the direct answer to "what is happening": the model narrates
its own progress.

Two boundaries keep it honest and cheap:

- **Ephemeral by design.** Thought text is displayed and dropped —
  never written to the transcript, never replayed in the history. The
  stored shape stays exactly what ADR-0018/0021 measured: thought
  parts carry their signatures only. (Measured live: a multi-round
  tool turn and a resume both run clean with summaries requested but
  text stripped from the replay.)
- **Main loop only.** Side-calls (risk review, compaction, summaries,
  web digests) and the summary model never request thoughts — nobody
  is watching those streams.

`show_thoughts = false` restores the quiet spinner; the heartbeat and
retry visibility stay — they are correctness observability, not
decoration. Plain-REPL and one-shot modes get none of this: their
output goes to pipes.

### 4. Status strings join the language catalog

The running-state strings ("thinking…", "waiting for the tool…",
"compacting…", "interrupting…", the heartbeat and retry formats) had
never entered the ADR-0029 catalogs. They do now, in both languages —
they are exactly the operator-facing chrome the catalog exists for.

## Consequences

- "Is it thinking or is it stuck?" is answerable at a glance, and the
  answer updates once a second.
- Requesting thought summaries changes the request config; the reply
  path was measured unchanged (signatures-only replay accepted).
  Summaries ride the same thought-token budget the thinking level
  already spends.
- A truly dead TCP connection still only surfaces as a growing "last
  Ns" — the operator decides when to interrupt. Accepted: any
  automatic cut-off would also cut off legitimate long thoughts.
