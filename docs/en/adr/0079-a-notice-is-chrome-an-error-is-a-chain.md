# ADR-0079: A notice is chrome; an error is a chain

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-08, implemented and unreleased) |
| Date | 2026-09-08 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The third pre-release pass for v0.72.0: the notices `internal/agent` writes were moved into the ja/en catalog with no ADR, on the reading that ADR-0029 §3 already covered them. The reviewer's verdict — half defensible, half a policy change — is the reason this exists |
| Amends | ADR-0029 §3 (its cataloged list gains the agent's mid-turn notices, and its discriminator is restated) |

## Context

ADR-0029 §3 is written as two closed lists: what is cataloged (`/help`,
the approval dialog, input chrome, the settings hint, `/auto`, `/clear`
and `/compact` feedback, the startup-safety prompts) and what stays
English (banner labels and `warning:` lines, cobra `--help`,
model-facing text, Go error chains).

Eleven catalog fields now carry what `internal/agent` writes mid-turn.
They were moved on the claim that no ADR was needed, because §3 names
"`/compact` feedback" and auto-compaction is that feedback from the
other trigger.

That reading holds for four of them. It does not hold for the rest — a
content-filter retry, a truncated response, a repeated remote failure, a
transcript-write failure, the round ladder's two continuations, and the
prompt hook's attachment notice — which are on neither list. Those are new surfaces entering the
catalog, and §3 is a closed enumeration. `df2ea5e`, in the same release,
wrote a whole ADR with an `Amends` row for a move of exactly this size.

Worse, the rule written into `internal/uitext`'s package doc to justify
the move does not decide its own membership:

> a sentence carrying the operator's next command is chrome, whatever
> wrote it; a wrapped error chain is not.

`CompactFailedFmt` is `"context compaction failed: %s"` with the cause
from `err.Error()`, and it carries no command at all — the command
arrives separately, on the second consecutive failure, when automatic
compaction gives up. By the stated rule
it belongs on the English side. It is in the catalog. A rule that the
work it justifies does not follow is not the rule that was applied.

## Decision

### 1. The agent's mid-turn notices are cataloged

§3's cataloged list gains: the notices `internal/agent` writes while a
turn runs. That is every call to `Agent.notify`, and the one that
reaches `onNotice` directly. They are the same surface as `/compact`
feedback — the operator reads them in the same scrollback, in the same
session, about the same runtime — and a session that answers in Japanese
when asked and in English when not asked is the drift ADR-0029 exists to
stop.

### 2. The discriminator is authorship, not the presence of a command

The rule that decides membership:

> **gem-agent composes the sentence → catalog. The sentence IS a
> returned `error` → English.**

A cataloged sentence may quote a cause verbatim: the frame is the
runtime's and gets translated, the cause arrives in whatever language it
was written in and is not touched. That is what a `%s` around
`err.Error()` already does, and it is why `CompactFailedFmt` is
correctly cataloged after all — the rule was wrong, not the placement.

What stays English is an `error` value travelling up the stack. Its text
is a chain, its innards come from libraries, and a translated frame
around English innards is the mixing §3 removed. This is why the
turn-ending errors — the round cap, the content-filter block, the media
replay, "no usable response" — are not in the catalog even though they
are unambiguously operator-facing and carry commands.

The presence of a next command is a rule about **wording** — every
operator line should have one — and it was never a rule about
**language**. Conflating the two is what produced a justification that
its own examples contradict.

### 3. What the rule does not yet cover, and is not fixed here

Three composed-by-gem-agent strings render inside the approval dialog —
which §3 lists as cataloged — and stay hardcoded English:
`EscalationReason` (`internal/agent/autoapprove.go`), every
`risk.Verdict.Reason` (`internal/risk`), and the unknown-tool and
unconfined-shell reasons in `internal/agent/decision.go`. By §2's rule
they are catalog text. They are pre-existing, they are not touched by
this release, and a Japanese session still reads English risk reasoning
at the gate.

Named rather than quietly excepted: the rule is what makes them a
violation, and a rule with an unstated exception is the thing §2 was
written to replace. Cataloging them is a change to the risk tier's
vocabulary, not a wiring fix, and it belongs in its own change.

### 4. What does not change

- Banner labels and `warning:` lines stay English (grep-stable output).
- Cobra `--help` stays English.
- Model-facing text stays English, and now has its own section in
  `make labels` so the read-through cannot mistake it for chrome.

## Alternatives considered

- **Leave the five uncataloged and revert them to English** — rejected:
  it restores the two-strings-for-one-event defect for compaction and
  leaves a Japanese session reading English recovery instructions at the
  moment it most needs precision.
- **Catalog the turn-ending errors too** — rejected: an `error` is
  composed by wrapping, and each layer would need a catalog key for a
  frame whose innards stay English. §3's reasoning is unchanged.
- **Say nothing and let the package doc carry it** — this is what was
  done, and the reviewer caught it. A closed enumeration that grows by
  five without a record is how the next reader learns the wrong rule.

## Consequences

- `internal/uitext`'s package doc states the authorship rule, not the
  next-command one.
- ADR-0029 gains an *Amended by* line.
- No code changes: the eleven fields are already cataloged, and this ADR
  records the decision that put them there and corrects the reason.
