# ADR-0080: what arming auto mode delegates — a declared scope, and a refusal the gate cannot answer

| Field | Value |
|-------|-------|
| Status | **Proposed** (2026-09-08) — the first ADR in this set that is not Accepted; written to be decided, not to record a decision already made |
| Date | 2026-09-08 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | A review of MITL auto mode, 2026-09-08: an ordinary in-project edit is approved by the rule tier without any check against what the operator asked for (A-01); the only channel carrying the operator's intent to the evaluator is the current turn's input, clipped (A-02); and a call the model tier refused can still be run by a session allowlist entry answered earlier (A-03) |
| Relates to | ADR-0004 (the ladder this amends the edges of), ADR-0008 (per-tool policy — `always` and `never`), ADR-0035 (the allowlist's tool-name granularity), ADR-0038 and ADR-0054 (the instruction reaching the evaluator, and in every round), ADR-0046 (an MCP tool's self-description as evidence), ADR-0050 (the risk rulebook — guidance, never instructions), ADR-0053 (`--auto` and `--allow` as one-shot controls), ADR-0073 (lanes: what bounds a shell command), ADR-0077 (the operator declares what a session has; §1's refusal to make built-ins selectable) |

## Context — three findings, one shape

The ladder answers *is this call dangerous?* Nothing in it answers *is
this call inside what the operator authorised for this session?* The
three findings are that question arriving from three directions.

**A-01. The rule tier's `safe` ends the ladder.** `write_file` and
`edit_file` on a path inside the project, that is not one of the
persistent files, are `safe` — and `decideAuto` returns approved
without consulting the model tier at all. So a session opened with
"review only, do not write anything" has no layer that stops an
ordinary edit: the main model is asked not to propose one, and if it
proposes one anyway the approval layers agree it is fine. *Where a file
may be edited* and *whether this session may edit it* are different
questions, and satisfying the first currently skips the second.

**A-02. The evaluator's picture of intent is one turn wide.** The
instruction handed to the model tier is `a.turnInput` — the text the
operator typed for *this* turn — clipped to `riskInstructionCap`
(2000 runes). A turn that says "続けて" carries nothing; a prohibition
in the last paragraph of a long brief is clipped away, because the clip
keeps the head. Attachments and history are deliberately excluded, and
that exclusion is right: they are the channels an injection reaches.
But it leaves the operator's standing constraints with no channel at
all.

**A-03. A prior grant outranks the model's objection.** When the model
tier refuses, the call goes to the gate. The gate is the operator *or*
the session allowlist, and only a must-prompt call — Block-tier, the
`operator` lane, unconfined shell, an `"always"` policy — is the
operator's unconditionally. So an `a` typed once about one call can
answer a later call the evaluator has just objected to. This is widest
for MCP, where the rule tier cannot reach the server's effects and `a`
covers any later arguments to the same tool name.

**The shape they share.** The ladder has no representation of a
standing operator constraint. The operator's intent exists only as
prose, reaches only the model tier, only for the calls that get that
far, and only for the current turn — while prior *grants* are durable
session state. Permission persists; prohibition does not. Every one of
the three findings is that asymmetry seen from a different side.

## Context — why "check it before the safe verdict" is not enough

The obvious repair is to test an explicit setting ahead of
`risk.Classify`, and refuse a write when the session is read-only. On
its own that is theatre. ADR-0077 §1 already refused to make built-ins
selectable, and gave the reason: *removing a reviewable built-in pushes
the same work into `shell_exec` and shell redirection — less
reviewable, not more contained.* A read-only mode that stops
`write_file` and leaves the `write` lane available has not made the
session read-only; it has made it write through a less legible door.

The corollary is that the enforcement point is the **lane**, not the
tool. ADR-0073 already put the kernel there, and the `read` lane is the
one boundary in this system verified at startup against probes that
must fail. A session that may not write is a session whose shell may
only run in the `read` lane — which is a claim the kernel makes, not
one gem-agent asserts.

## Decision

### 1. A session scope, declared by the operator, evaluated before the ladder

One new config section and one flag. The scope is a setting the
operator writes or passes, never a thing inferred from what they typed.

```toml
# ~/.config/gem-agent/config.toml, or the project's
[scope]
# "project" (today's behaviour, the default) or "off"
writes = "off"
```

`--read-only` is the per-run form, the way `--auto` is for the ladder
(ADR-0053). Project scope beats global scope, and the more restrictive
of the two wins — a project that says `"off"` is not loosened by a
global `"project"`, because a scope whose layers can widen it is not a
scope.

With `writes = "off"`:

- `write_file`, `edit_file`, `save_memory` and `delete_memory` are
  refused before any gate, the way an excluded MCP function is
  (ADR-0077 §1). The refusal names the scope, so the model can report
  it rather than retrying around it.
- `shell_exec` may only run in the `read` lane. A call declaring
  `write` or `operator` is refused, not escalated. The read lane's
  profile is what makes this true; the refusal is what makes it legible.
- Every MCP tool asks, as it does today (ADR-0008's default) — the rule
  tier cannot tell a read tool from a write tool on someone else's
  server, so the scope cannot either. `[mcp] exclude` (ADR-0077) is the
  operator's instrument there, and this ADR does not duplicate it.

### 2. Out of scope is a refusal, never an escalation

An out-of-scope call must not reach the gate. The gate can be answered
by the session allowlist (§4), so a scope that escalates is a scope any
earlier `a` can spend. It must also not be a Block-tier verdict: Block
means *the operator has to look at this*, and the operator has already
said no — asking again is a worse answer than refusing.

This is ADR-0077's "declared but refused" argument at the level of an
effect rather than a tool: a prohibition re-read every round is a state
whose default reading drifts, while a refusal is a fact the round has
to route around.

### 3. Path-scoped writes are named here and not built here

`writes = "paths"` with a `writable` list is the shape operators will
ask for next, and it is deliberately not decided in this ADR. The
reason is §"why before the safe verdict is not enough": the file tools'
check is the easy half, and it is worth nothing until the `write`
lane's Seatbelt profile is generated from the same list. Until those
two are one function — the way `sandbox.PersistentFiles` is already
read by both the profile and the file tools' verdict — a path scope
would be a setting the shell walks around, which is worse than not
having it, because it reads like a boundary.

`writes = "off"` has no such gap: the `read` lane already exists, is
already verified at startup, and is already the profile a shell command
can be held to.

### 4. A model-tier refusal is must-prompt for that call

When the model tier ran and returned a usable verdict that did not
approve, the resulting gate call is must-prompt: the session allowlist
does not answer it, and the operator sees it.

The `a` the operator typed was an answer about a call they were shown.
The evaluator is objecting to *these arguments*, which the operator has
not seen — most sharply for MCP tools, where `a` covers any later
arguments to the same name. Letting the older, coarser answer beat the
newer, specific objection inverts which of the two knew more.

This only ever asks more. `"never"` still skips the gate entirely
(ADR-0008), because that is the operator's standing decision about a
tool rather than a per-call grant; `"always"` is unchanged; and an
operator who wants the old behaviour answers `y`.

**An evaluation failure is not a refusal.** A transport error, an
unparseable verdict or a confidence outside 0–1 escalates as it does
today, and the allowlist may answer it. Fail-closed is the right
instinct and this is the exception to it: a flaky network would
otherwise convert every allowlisted call in a long run into a prompt,
which trains the operator to clear prompts rather than read them. The
two cases are now distinguishable in the transcript — an
`auto_decision` written after this review carries `confidence` and
`evaluator_model`, and a failure carries neither — so the split can be
measured instead of assumed.

### 5. The declared scope reaches the evaluator; prose stays evidence

The evaluator payload gains the session scope as a mechanical line,
present on every evaluation and never clipped. A "続けて" turn then
still carries the operator's standing constraint, because the
constraint was never prose.

The typed instruction keeps exactly its current role — evidence inside
the isolation wrap, per ADR-0038 and ADR-0054 — with one change:
`riskInstructionCap` clips the **middle**, keeping the head and the
tail. A prohibition is written where a person writes one, which is at
the end, and a head-only clip drops precisely the sentence the
evaluator most needs. The clip marker stays, so the evaluator is told
something was removed.

**Constraints are never extracted from prose by a model.** No layer
reads the operator's text and derives a rule from it. The proposer
would be authoring its own cage, which is ADR-0020 §4's objection to
model-approved memory writes and ADR-0050's reason for keeping the
rulebook as guidance; and a derived constraint is a derived
*permission* the moment the derivation is wrong in the loosening
direction. What is not declared is not enforced, and this ADR says so
plainly rather than implying a coverage the code does not have.

## Alternatives considered

- **Add `write_file` and `edit_file` to ADR-0077's `[mcp] exclude`
  style of set.** Rejected by ADR-0077 §1's own reasoning: the work
  moves into `shell_exec` and redirection. The scope has to be an
  effect, not a tool list.
- **Make the scope a Block-tier verdict.** Rejected in §2: Block asks
  the operator, and the operator has already answered.
- **Derive constraints from the conversation with a model pass.**
  Rejected in §5. It is also the design that fails most quietly: it
  looks like coverage.
- **Carry the last N turns of operator text to the evaluator.**
  Rejected: it grows the payload on every call, makes the constraint's
  lifetime depend on how much was typed since, and still leaves
  enforcement to a model's reading. The declared scope does the job
  with a fixed cost.
- **A model refusal revokes the allowlist entry for the session.**
  Rejected: one objection about one set of arguments would spend a
  grant the operator made deliberately. Must-prompt for that call is
  the narrowest thing that fixes the inversion.
- **Argument-scoped allowlist entries** (`a` on a tool *and* an
  argument shape). Not decided here. ADR-0035 chose tool-name
  granularity, and an argument-shaped key has no stable form across MCP
  servers; `"always"` remains the instrument for a tool whose arguments
  matter.

## Consequences

- Two behaviour changes reach existing operators: an `a` no longer
  answers a call the model tier refused (§4), and — only when a scope
  is declared — some calls are refused that used to run. Both ask more
  and permit less. CHANGELOG entries belong under *Changed*.
- `save_memory` and `delete_memory` sit in the same class as §4: they
  escalate rather than prompt, so one `a` covers a session's later
  saves. ADR-0020 §6 says "always escalate" and means the tool policy
  to be the deliberate relaxation. Whether they should be must-prompt
  is deliberately left out of this ADR.
- The measurement this ADR's §4 split relies on is the record shipped
  alongside this review, not a harness. A fixed evaluation set per
  model version — false approvals on dangerous calls, needless prompts
  on ordinary ones, latency, cost — remains unbuilt and is what would
  turn `minConfidence` from a constant into a calibrated one.
