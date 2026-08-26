# ADR-0048: learning that fires on real usage — server-scoped MCP rules, and counting the answers people actually gave

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-26 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator, using v0.46.0 on another machine: an auto-mode session escalated roughly 25 times, every one of them approved, and `/learn` afterwards proposed nothing at all |

## Context

ADR-0045 shipped `/learn` and it worked on its own tests, its own E2E,
and nothing else. The first real session produced zero proposals from
25 approvals. Two independent causes, both mine.

**Reproduced before diagnosing** (synthetic transcript, 25 escalations
each approved in one session): `ApproveSessions=1`, `ModelEscalated=25`,
proposals `0`.

### Cause 1: session-collapsed votes throw away real decisions

ADR-0045 §5 counts one vote per session per key, because a session
allowlist (`a`) turns one keystroke into any number of recorded
approvals. That reasoning is sound and the implementation of it is not:
the gate records `approved` identically whether the operator pressed
`y` for the twenty-fifth time or pressed `a` once (`internal/approve`
and `internal/tui` both consume the allowlist inside the gate). Unable
to tell one keystroke from twenty-five, I collapsed all of them — so
the *measurement* of "the human decided this repeatedly" throws away
exactly the repetitions it is trying to measure.

### Cause 2: MCP friction has a shape frequency cannot see

The operator's escalations were "almost all MCP, spread across
different tools", concentrated in a few servers. That is what
investigative work looks like here: 19 configured servers, 136 tools,
and a triage chain calls `asn-lookup`, then `whois-lookup`, then
`abuse-lookup` — each tool once or twice.

Per-tool frequency learning cannot reach that. The friction is not
"the same question repeatedly"; it is "a different question every
time, each asked once". No threshold over a per-tool counter is both
safe and reachable: three sessions is unreachable, one approval is a
standing permission bought with a single keystroke.

### What the record already knows, and what it does not

`auto_decision` carries the ladder's verdicts and `gate_decision` the
operator's answers, both keyed. Neither says whether a `gate_decision`
came from a keystroke or from the allowlist. And ADR-0045's §Context
promised backfill from "escalated, then ran" for transcripts written
before `gate_decision` existed; the implementation never did it, so
every pre-v0.46.0 session contributes nothing. That gap is recorded
here rather than quietly fixed, because it is why the very first real
run had less evidence than it looked like it should.

## Decision

### 1. The gate reports how it answered

`Approver.Approve` returns `Answer{Approved, FromAllowlist}` instead of
a bool. `gate_decision` records `source: "operator" | "allowlist"`.

With that distinction, §5's collapse applies only where it was
justified: **allowlist answers collapse to one vote per session per
key; operator keystrokes count individually.** Twenty-five typed `y`s
are twenty-five decisions, because that is what they are.

The threshold becomes: propose `"never"` for a key with **≥ 5 operator
approvals or ≥ 3 approving sessions, and no denial anywhere**. Either
route, one bar; the session route survives for the case where an
operator answers the same thing across days.

### 2. MCP proposals are per server, in the global scope

For `mcp__` tools the proposal is `mcp__<server>__*` — the ADR-0008
wildcard, not a new vocabulary — written to the **global** `[tools]`
table of the machine-owned policy file.

Two arguments carry this, and both are about MCP being unlike a shell
command.

**Server scope is the honest unit.** A `never` on one lookup tool while
its five siblings still ask does not describe any decision an operator
makes: they trust *the server*, whose tools they chose to install. The
per-tool rule is a rule about an accident of which tool the model
reached for first.

**Global scope is the honest scope.** ADR-0045 §4 refused a global
command table because `make build` is a different program in every
repository — the Makefile is the project's. That argument does not
transfer: an MCP server comes from `~/.config/gem-agent/mcp.json`, its
binary and behaviour are identical in every project, and a hostile
clone cannot introduce one (a project's `.mcp.json` is behind the
ADR-0023 trust gate). Making the operator re-learn `asn-lookup` per
project would be re-answering a question whose answer cannot vary.

**Servers from a project's `.mcp.json` are excluded** — those *are*
project-supplied, and `mcp.Merge` already labels the scope.

Threshold: **≥ 2 distinct tools of that server approved, and no denial
of any of its tools**. Tool *diversity* is the right counter here and
it is structurally immune to the allowlist problem that caused Cause 1:
the allowlist is keyed by tool name, so one `a` can only ever add one
tool to the count.

### 3. A server proposal discloses everything it will cover

This rule grants more than the evidence for it: `mcp__urlscan-lookup__*`
covers `scan_url`, which sends a URL to a third party, on the strength
of having approved two read-only lookups. That is not a reason to
refuse the rule — the operator wrote these servers and knows what they
do, which is exactly the knowledge ADR-0008 exists to let them write
down. It is a reason to never let them agree to it blind.

So the proposal lists **every tool the rule would cover**, split into
the ones already approved and the ones **not yet used**, with the
server's own description of each (ADR-0046: a claim, attributed). A
long list is clipped with the count of what was hidden, never
silently.

### 4. Per-tool MCP proposals are withdrawn

MCP keys no longer produce per-tool, project-scoped proposals. Two
proposal shapes for the same call would either duplicate or contradict
each other, and §2's argument says the per-tool project rule was the
wrong shape to begin with. Shell commands (project, command key) and
built-in tools (project, tool name) are unchanged: for those, ADR-0045
§4's reasoning still holds.

### 5. Backfill stays unimplemented, and is now written down as such

Recovering decisions from pre-v0.46.0 transcripts means pairing an
`auto_decision` to the tool result that followed it, positionally,
without a recorded key. ADR-0045 named this the strongest evidence
class and the implementation skipped it; rather than half-doing it
under time pressure, it is deferred with its cost stated. If the
records accumulated from here prove insufficient, it returns as its
own ADR.

## Consequences

- The operator's reported session now produces proposals: a few
  servers with several approved tools each clear §2's bar.
- Learned MCP rules apply everywhere, which is a real widening
  compared to ADR-0045 — bounded by: the operator confirms each one
  with the full covered-tool list in front of them, pre-tool hooks
  still run, and `/settings` shows and removes them.
- `Approve` changing shape touches both gate implementations and every
  test double. Worth it: without the distinction, the safety argument
  for collapsing votes and the feature's ability to fire are in direct
  conflict.
- Testing lesson, recorded because it caused this: ADR-0045's E2E
  seeded three sessions to satisfy its own threshold, which measured
  the mechanism and not its reachability. A feature that learns from
  usage has to be tested against a transcript shaped like real usage —
  here, one session, many one-off tools. The reproduction in §Context
  is kept as a regression test.
