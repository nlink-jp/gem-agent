# ADR-0056: The stall warning cried wolf — a function call arrives whole

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-30 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "the model is clearly still working, but around an edit_file / write_file call the status line says the connection may be stalled" |
| Amends | ADR-0033 §1 (the single 20s threshold), ADR-0041 §5 |

## Context

ADR-0033 §1 gave the running status a heartbeat and ONE threshold: 20
seconds with no chunk turns the line into "no data for Ns — the
connection may be stalled". ADR-0041 §5 closed three holes in it, all
about silence AFTER a tool call had started (`toolRunning`,
side-call chunks, turn boundaries). The reported case is the window
BEFORE the call arrives — while the model is composing it.

Two live measurements (gemini-3.7-flash, `internal/llm`, `-tags live`)
settle what is happening:

`chunkgap_live_test.go` — one `write_file` with 21,761 bytes of
content:

    t=  2.4s chunk #1        t=  9.4s chunk #4
    t= 42.5s chunk #5   ← 33.1s of silence
    RESULT: total=42.8s chunks=6 maxGap=33.1s contentBytes=21761

`wirebytes_live_test.go` — the same request with the HTTP response
body tapped, to ask whether a byte-level heartbeat is possible at all:

    t=  7.1s read #3 590 bytes
    t= 47.5s read #4 1024 bytes   ← 40.4s, NOT ONE BYTE read
    t= 47.5s reads #5..#10        ← ~37KB flushed at once

So:

- Gemini emits a function call as **one whole part**. There is no
  partial-argument streaming to observe — the four early chunks are
  thought summaries, and then nothing until the finished call.
- The silence is **not** observable as liveness at any layer available
  to the client: not chunks, not response bytes. "Composing a large
  argument" and "the TCP connection died" are byte-for-byte identical
  from here.
- The silence scales with the argument — ~650 B/s in the measurement,
  so a 100KB file is minutes of it.

The 20s threshold therefore accuses the model of being dead during
exactly the work gem-agent exists to do: writing and editing files.
A warning that fires on healthy work is worse than no warning — the
operator learns to ignore the one signal that means "Ctrl+C is on the
table".

## Decision

### 1. The threshold moves: `stallSeconds` 20 → 90

Nothing is added to the screen. The heartbeat keeps showing what it
always showed — `1m07s · 34 chunks · last 40s` — and only past 90s
does it turn into the warning. Suppression while `toolRunning` is
untouched (ADR-0041 §5), and no automatic timeout is introduced
(ADR-0033).

A first attempt put the cause on screen: a dim line under the status
reading "a large write/edit sends nothing until it is complete".
**Rejected on operator review** — "that is the backend's business; the
operator reads it and thinks *so what?*". They cannot act on how
Gemini frames a part, and a status bar that explains our supply chain
is a status bar explaining itself. Supplier constraints belong in
comments, ADRs and commit messages; the screen gets the threshold that
follows from them. (House rule, learned expensively on weather-lens.)

One string does change: `StallFmt` carried its own
`(Ctrl+C interrupts)` while `CtrlCHint` renders immediately after it,
so the warning line was a duplicate that truncated mid-word at 80
columns. The copy is gone and the English shortens to "the stream may
be stalled"; a regression test pins the hint at 80 columns in both
languages.

### 2. Why 90 seconds

2.5× the measured benign silence for a ~21KB argument, which covers
the file sizes a turn actually writes. It is a judgement, not a
derivation: the honest cost is that a genuinely dead connection now
goes unnamed for 70 seconds longer. Accepted, because the age itself
(`last 84s`) is on screen and counting from the first second, and
Ctrl+C never stopped being available. Delaying the accusation costs
the operator information they can already see; making it falsely
costs them the signal itself.

### 3. `/riskbook learn` sets its flag AFTER `beginTurnStats`

The learning pass suppressed the detector with `m.toolRunning = true`
and then called `beginTurnStats()`, which resets that flag — so a pass
sitting on a human's answer warned about a connection it was not
using. Ordering fixed, with a regression test; the `!command` path
already had it right.

### 4. The measurements stay in the tree

Both live tests are kept as the evidence for the numbers above, and as
the check on the assumption: if Gemini ever streams partial
function-call arguments, `chunkGap` collapses and the quiet tier
becomes dead code that can be removed. `wirebytes_live_test.go`
promotes `golang.org/x/oauth2` to a direct (test-only) dependency —
the ADC token source has to survive an injected `HTTPClient`.

## Consequences

- The status line stops lying about the most common long turn, and
  says nothing new to do it: one constant, not one more string.
- A dead connection is named at 90s instead of 20s. No automatic
  timeout is added — ADR-0033's refusal to kill long work stands.
- No new signal was invented: a byte-level heartbeat was considered
  and **measured impossible**, which is why the fix is a threshold
  rather than a mechanism.
- If a future model streams partial function-call arguments, the
  measurement collapses and 90s can come back down — the live tests
  are what would notice.
