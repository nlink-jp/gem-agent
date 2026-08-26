# ADR-0045: transcript-driven approval-rule learning — `/learn` proposes, the operator decides

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-26 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: auto mode runs on a fixed evaluation — can it learn from the operator's decision history? Direction set in review: rule sets are built per project (the opposite would be dangerous), and learning runs as an explicit `/` command over the recorded transcripts, proposing rules. Follow-up: the model tier's verdicts on MCP calls visibly wobble — an equal friction source |

## Context

The auto-approve ladder (ADR-0004) is fixed: a pure-function rule tier
(Safe/Review/Block), a model tier consulted only for Review (approve ∧
confidence ≥ 0.8), and the human gate. ADR-0038 added one adaptive
element — the operator's typed instruction rides along as evidence —
but nothing in the ladder ever changes shape from experience. The
operator answers the same question about `go test` for the hundredth
time with the same `y`.

MCP calls are the other half of the friction, and the operator reports
it directly: the rule tier classifies every `mcp__` call Review
(effects unknown to it), so each call rides on the model tier — whose
verdicts wobble. The same lookup tool is auto-approved on one call and
escalated on the next, and an escalation the operator cannot predict
is the same friction as the hundredth `go test` prompt with worse
ergonomics: it interrupts an investigation chain mid-flight.

Feedback channels exist, but each carries exactly one decision: `a`
(session allowlist), `p` (persisted per-tool policy, ADR-0009), and the
hand-written ADR-0008 tables. The accumulated record of what the
operator actually decided — dozens of sessions of it — is written down
and then never read.

Two structural facts shape any learning design:

**The signal is one-sided.** A human decision exists only for calls
that were escalated. A wrong auto-approval produces no correction
signal at all. Learning from this history can therefore only reduce
over-escalation (friction); it cannot improve the safety side, and it
must not be allowed to degrade it.

**The labels are trustworthy; the calls are not.** The decision itself
(y/n) is operator input — the one channel a prompt-injection attacker
cannot write (the ADR-0038 argument). But the call being decided was
authored by the model, which tool output can influence. Any matching
that is looser than deterministic equality hands an attacker a way to
dress a hostile call as a previously-approved pattern.

What the transcripts already hold, measured against the current code:

- **Denials**: a tool-role message whose `Content` equals the exact
  `deniedResult` constant. Hook denials carry the distinct
  `denied by a pre-tool hook: ` prefix and are excluded — they are not
  operator decisions.
- **Auto-mode verdicts**: `auto_decision` diagnostic records
  (name, approved, tier, reason, model). Calls these approved are the
  ladder's decisions, not the operator's, and are excluded from
  endorsement counts.
- **Operator approvals**, in two shapes. An `auto_decision` record
  with `approved=false` followed by a real result: the ladder
  escalated and the operator said yes — the exact shape of the MCP
  wobble, and the strongest evidence class. And a mutating call with a
  real result and no `auto_decision` record (non-auto mode) — where
  `y`, the session allowlist, and a `"never"` policy in force at the
  time are retroactively indistinguishable. §7 removes this ambiguity
  going forward; for backfill it is mitigated by deduplicating
  proposals against current policy.

## Decision

### 1. `/learn` — operator-invoked, never ambient

Learning runs when the operator types `/learn`, and at no other time.
The command scans **this project's transcripts only** (the ADR-0022
layout plus legacy flat files whose header names this project),
aggregates the operator's decisions, and presents rule proposals for
per-item confirmation. Nothing changes until a proposal is accepted.

A background learner that shifted approval behavior silently would be
an unauditable loosening channel — the exact thing the ADR-0008
asymmetry (tightening is free, loosening is an explicit operator act)
exists to prevent. Keeping the operator the author of every rule is
the design, not a limitation.

### 2. Deterministic extraction — no model reads the transcripts

The learning pass parses structured records only: tool-call names and
arguments, tool-role results compared against exact constants, and
`auto_decision` records. It never feeds transcript text to a model.

Transcripts are full of attacker-influenceable prose — tool outputs,
file contents, web pages. A model that read them and then proposed
policy would be a prompt-injection-to-policy pipeline: the persistence
vector ADR-0020 §4 closes for memory, rebuilt one layer up.
Deterministic aggregation is immune by construction — the only inputs
that carry weight are the operator's own recorded decisions.

### 3. Aggregation keys are syntactic facts

- **Non-shell tools** (MCP included): the tool name — the ADR-0008
  vocabulary, directly. Arguments are deliberately not part of the
  key: for a lookup-style tool the risk lives in what the tool does,
  not in which indicator it is asked about.
- **`shell_exec`**: the command key is the first token, extended by the
  second token iff it has subcommand shape (`^[a-z][a-z0-9-]*$` — not a
  flag, not a path). `go test`, `make build`, `git status` keep two
  tokens; `ls -la` and `touch newfile.txt` reduce to their head.
- **No key is derived** — at learn time *and* at match time — for
  commands that are not plain: multi-segment commands (`|`, `;`, `&&`,
  `||`, `&`), dynamic construction (`$(`, backticks, `${`, `eval`), or
  any redirection. A key that names two tokens must be the whole truth
  of what runs (the agent-skeleton finding). Learner and matcher share
  one derivation function so they cannot drift apart.
- Two further exclusions the implementation added, for the same reason:
  a head containing a path separator (`./deploy.sh`,
  `/usr/local/bin/make`) names a *file*, whose contents can change
  under a key that says nothing about them, while a bare name resolves
  through the operator's own PATH; and an environment-assignment
  prefix (`FOO=bar make build`) changes what the command does without
  appearing in the key.

Semantic or fuzzy similarity is rejected outright: it is precisely the
loose matching §Context warns about.

### 4. New vocabulary: per-command policy, project scope only

`[projects."<path>".commands]` in the machine-owned `policy.toml` maps
a command key to `"never"` / `"always"` — the same two words as
ADR-0008, no third vocabulary. Semantics are identical to ADR-0008:

- `"never"` skips the gate and the model tier in all modes, but does
  **not** lift the rule tier's Block floor, and pre-tool hooks
  (ADR-0044) still run first.
- `"always"` is the tightening floor: always ask, in every mode.
- A command that fails §3's plainness test matches no rule and takes
  the normal ladder.

The two tables combine in `Policy.ForCall` by two rules, in order.
**`"always"` from either table wins**: an operator who pinned
`shell_exec = "always"` said every shell call is theirs to see, and a
learned rule — which only ever means "I approved this repeatedly" —
must not take that back; in the other direction a learned `"always"`
tightens a blanket `shell_exec = "never"`, and tightening is free
(ADR-0008). **Otherwise the command entry answers**, being the more
specific statement about this call; an entry exists only because the
operator confirmed it, so it is their decision either way.

A **global commands table deliberately does not exist** (operator
direction). `make build` being safe in one repository says nothing
about another; a `"never"` learned in a trusted project, applied
globally, would auto-run inside the next hostile clone. Per-tool
global policy (ADR-0008) is unchanged.

The vocabulary lives in machine-owned `policy.toml` only. Extending
the hand-written `[approval]` tables can follow if demand appears —
a capability without a demonstrated trigger is dead weight (ADR-0044).

### 5. Proposal thresholds — v1 constants, counted per session

**Votes are counted per session, not per call.** The first draft said
"≥ 5 approvals across ≥ 2 sessions"; implementing it surfaced that a
session allowlist (`a`) turns one keystroke into any number of
approvals, so five "approvals" can be one decision the operator made
once — and repeating a command within a session is the same
inflation without the allowlist. Collapsing each session to one vote
per key and outcome removes both, and needs no new plumbing to tell
the allowlist apart from a typed `y`.

- Propose `"never"` for a key approved in **≥ 3 separate sessions with
  no denial anywhere**, whose recorded examples do not classify Block
  today.
- Propose `"always"` for a key denied in **≥ 2 sessions** — tightening
  gets the lower bar, in the Block-pattern spirit (a generous match
  costs one prompt).
- Never proposed: `save_memory` / `delete_memory`. Frequency evidence
  is invalid where the risk lives in per-call content — twelve
  harmless saves say nothing about the thirteenth (ADR-0020 §4).
  Command keys aggregate calls with stable semantics; memory writes
  are the opposite.
- MCP proposals get no semantic filter: the learner cannot tell a
  lookup (risk per tool) from a messenger (risk per call). That
  judgment is what the confirmation step asks of the operator — who,
  unlike any classifier, knows what the server does. This is
  ADR-0008's original rationale applied: the policy table is the place
  to write down what only the operator knows.
- Skipped: keys already covered by current policy in either scope, and
  keys whose recorded examples classify Block — `"never"` does not lift
  that floor, so the rule would change nothing.

The draft also excluded rule-tier **Safe** keys as friction-free. That
was an auto-mode assumption: with auto off, Safe-tier calls do reach
the gate, and the operator answering them repeatedly is exactly the
friction this removes. Dropped.

Constants, not config: there is no evidence yet that tuning is needed,
and a knob here would be a knob on how readily approvals are given
away.

### 6. Confirmation: evidence, then one decision per rule

Each proposal is shown with its evidence — approved/denied counts,
session count, two or three example calls the operator has already
seen and approved, and how the ladder handled the key historically
(rule tier, model approved / escalated counts). That last line makes
the wobble visible: "model tier approved 12, escalated 9" is the case
for replacing a per-call judgment with a deterministic rule. MCP
proposals additionally show the tool's current self-description,
clipped (ADR-0046 §4) — the operator judging "lookup or messenger?"
should not have to recall it. Each proposal is answered y/n in the
approval-dialog grammar.
Accepted proposals are written via `MutatePolicyFile` and take effect
immediately; `/settings` shows them with `policy.toml (project)`
provenance, editable and deletable like any other entry.

### 7. `gate_decision` records make future learning exact

Alongside the existing telemetry emit, the agent writes a
`gate_decision` diagnostic record to the transcript: tool name,
decision, `must_prompt`, the **aggregation key**, and a clipped
`detail` for evidence. `auto_decision` carries the key too. `Load`
already skips unknown kinds, so no schema bump; both records are
diagnostic and invisible to resume.

Recording the key rather than deriving it later does two things.
The learner never has to pair a decision back to a call — with several
calls per round, that pairing is exactly the kind of positional
guesswork that goes wrong silently. And a key derived by a future
build cannot retroactively re-interpret an old decision: the record
says what this build would have matched.

The record does **not** distinguish a typed `y` from the session
allowlist, and does not need to: §5's per-session counting already
removes the difference between one 'a' and many prompts. Backfill over
old transcripts uses the §Context reconstruction; precision improves
as these records accumulate.

### 8. The declared purpose is not part of any of this

ADR-0047 gives every gated call a model-written `gem_agent_purpose`.
It is stripped before the command key is derived (it is not part of
what runs), it is not aggregated, and it is not shown as evidence in
§6. The evidence for "should this command stop asking?" is what runs
and how the operator answered — not the proposer's account of why it
wanted to. Showing a stream of model-written justifications while
asking for a standing rule would put the proposer's voice into a
decision that exists to record the operator's.

## Rejected alternatives

- **Ambient learning / automatic threshold adjustment** — silently
  loosening approval behavior violates the ADR-0008 asymmetry and
  re-enacts the v0.39.0 lesson that self-approval is not a defense.
- **History as evidence in the model tier** (the ADR-0038 shape, fed
  with past decisions): adapts silently and audits poorly; the
  operator chose explicit, inspectable rules. May return as a future
  ADR if command-rule coverage proves insufficient.
- **Semantic similarity matching** — a poisoning surface (§3).

## Consequences

- Friction falls only where the operator has already voted repeatedly;
  a genuinely novel command still asks. That bound is the point.
- Learned rules are ordinary policy: visible with provenance,
  deletable, and subordinate to the Block floor and pre-tool hooks.
- The scan decodes records partially (kind first, then only the fields
  needed), skipping attachment payloads — a transcript full of base64
  images costs little.
- Backfill imprecision (y vs allowlist vs policy-at-the-time) is named
  in §Context, mitigated by policy dedup, and decays as
  `gate_decision` records accumulate.
- Tests: the key-derivation table (including every §3 exclusion),
  extraction against synthetic transcripts, the shared learner/matcher
  function, threshold logic (including the per-session collapse), the
  gate-side effects of a learned rule (skips the gate, stays scoped to
  its key, does not lift the Block floor, does not match a compound
  line), and the policy write path.
- Surface mechanics: `/learn` runs like a turn rather than a slash
  command in the TUI. Its dialogs are answered on the Bubble Tea event
  loop, and a synchronous slash handler calling into that loop
  deadlocks on an unbuffered channel — so it takes the `/compact`
  shape (async starter, completion via `TurnDone`). The plain REPL,
  which owns stdin on the dispatching goroutine, runs it inline. In
  `-p` it is unreachable, which matches §1: there is no operator to
  ask.
- Measured end to end (isolated `GEMAGENT_STATE_DIR`, real binary,
  pty): three seeded sessions approving `go test` produced one
  proposal, the dialog showed it with its evidence, accepting wrote
  `[projects."…".commands] "go test" = "never"`, and in a fresh
  session the model's `go test ./...` ran with **no gate_decision
  record at all** — the gate was never consulted. The operator's own
  policy file was untouched throughout.
- A verification lesson worth keeping: the first E2E reported a
  failure because its expect pattern (`approval`) matched the banner's
  "approval policy:" line, not a dialog. The check was rewritten to
  assert on the transcript's absence of a `gate_decision` record —
  ground truth rather than screen text.
