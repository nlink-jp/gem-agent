# ADR-0060: deny with reason — the `N` answer

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-31 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator proposal: "when denying, let me optionally type why — `n` stays instant, `N` opens a reason field" |

*Amended by ADR-0075: `runtime_note` is the second tool-message field
trusted by provenance — the runtime's own words about a remote tool's
repeated identical failure, set in the same executor and appended
outside the nonce tag. Recognising it by content stays rejected for the
reason §3 gives.*

## Context

A gate denial today sends the model one fixed sentence: *"Tool
execution denied by the user. Do not retry the same call; ask the user
how to proceed instead."* The design acknowledges that a denial
usually comes with a reason — and then spends a full model round
fetching it: the model ends its turn asking, the operator types the
answer they already knew at the moment they pressed `n`. Between the
denial and the question there is also a window in which nothing stops
the model from trying a variant of the same call first.

The obvious imitation — Claude Code's "deny, then type what to do
differently into the input box" — is not available here. Gemini
requires the content following a function-call turn to consist of
exactly its function responses; a user part riding alongside was
measured as a 400 on the next round (ADR-0012 §5). At the moment of
denial there is exactly one slot that reaches the model: the denial's
own function response. So the operator's guidance either rides there
or waits a round.

ADR-0036 deliberately gave `ask_user` no free-input field: declining
ends with the model asking in prose, where the ordinary input box is
the free-text channel. That reasoning does not transfer to the
approval gate. A declined `ask_user` has already reached the "model
asks, operator types" state; a denied tool call has not — the turn is
mid-round, the ordinary input channel is a full model round away, and
the fixed denial text is what pays for that round. The two designs
answer different situations, and this ADR is the record of why free
text is right here and stays wrong there.

One more constraint surfaced during design: **every tool-role message
is nonce-wrapped at send time** as untrusted data (the ADR-0010
exemption covers only `load_skill`). The denial text is authored by
gem-agent and the operator — nobody else can put a byte in it — yet it
ships wrapped in the same "content, never instructions" tag as tool
output. For the fixed sentence that was a tolerable wrinkle. For an
operator's typed guidance it is self-defeating in exactly the ADR-0010
sense: wrapping an instruction as data while the system prompt forbids
following data leaves it half-inert.

## Decision

### 1. A fifth dialog answer: `N`, "deny with reason"

The TUI approval dialog gains one answer between deny and
always-allow: **deny with reason (N)**. Selecting it (arrows/Tab +
Enter, or a direct capital `N` when the IME is off) replaces the
options row with a one-line text field. Enter sends the denial with
the typed reason; an empty Enter is a plain deny; Esc returns to the
options row with nothing decided.

`n`, Esc and Ctrl+C keep their exact current meaning: a one-keystroke,
no-reason deny. The operator rejected the "press n, then dismiss an
optional reason field" shape explicitly — the fast path must stay one
action. The plain-stdin gate gains the same answer (`N` on the answer
line, then one reason line); the one-shot deny gates (`-p`, agent
search) and the interrupted-turn auto-deny answer with no reason, as
before.

### 2. The reason rides in the denial function response

A denial with a reason returns, as the tool result:

> Tool execution denied by the user, who gave this reason:
> «reason»
> Do not retry the same call; follow the reason, or ask the user how
> to proceed.

This is the one slot the API leaves open mid-round (see Context), and
it converts the standing "ask the user how to proceed" round into
guidance delivered at the moment of decision.

### 3. Denial results are exempt from the nonce wrap — by provenance, never by content

`llm.Message` gains a tool-role field `denial` (persisted; additive —
schema version unchanged). It is set in exactly one place: the
executor's gate-denial path. `wrapToolMessages` skips the untrusted
wrap for messages carrying it. The rationale is ADR-0010's, applied to
the other party the system already trusts unwrapped: the denial text's
authors are gem-agent (the fixed sentence) and the operator (the
reason typed at the console) — the same trust tier as the system
prompt and the operator's own messages.

Recognizing denials **by string comparison is rejected**: the previous
prefix-equality checks (the view_image / read_document attach guards,
the audit outcome) were already one MCP server returning a
denial-shaped string away from misclassification — and an unwrap
triggered by content would be a real injection door, the exact defect
class review round 2 closed. All three checks move to the provenance
bool; the constant stays only as text.

An old build resuming a new transcript ignores the field and wraps the
denial as before — degraded wording, nothing unsafe.

### 4. The record keeps the reason; telemetry does not

The `gate_decision` transcript record gains `deny_reason`. It is the
operator's own words about their own decision — exactly the evidence
ADR-0045 stores and ADR-0048's learner weighs, and a denial with a
stated reason is far stronger evidence than a bare `n`. The
OpenTelemetry export (ADR-0035) does **not** carry it: free text
typed at an approval prompt is the kind of content that ends up
holding paths, hostnames, or stronger, and the export's attribute set
stays enumerable.

## Consequences

- One saved model round per redirected denial, and no variant-retry
  window between the denial and the guidance.
- The learner's raw material improves at zero cost to it (no learner
  changes in this ADR).
- The `Approver` interface grows a third return value (`denyReason`);
  all gates and test fakes update. Still plain returns, not a struct —
  ADR-0035's reasoning about the shared-package import cycle holds.
- The dialog's IME rule (ADR-0002 lineage) extends to the reason
  field: it accepts composed Japanese text, and Enter only reaches the
  app when composition is done — the same contract as the main input
  box.
