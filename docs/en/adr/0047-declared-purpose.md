# ADR-0047: model-declared purpose on gated calls — the operator sees why, not only what

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-26 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: approval was requested for a `cp` into a temporary directory — apparently staging a file for a Slack upload — and nothing said so. "Was it trying to run this silently? Was the motivation only in the thinking?" |

## Context

The approval dialog answers two questions and leaves the third one
unanswered. It shows the tool name, the arguments (the whole command
line for `shell_exec`), and — when auto mode escalated — the reason the
call is being asked about. So the operator learns **what will run** and
**why they are being asked**. They do not learn **why the agent wants
it**.

For a self-explanatory call that gap is invisible. For `cp
/some/report.csv /var/folders/…/T/x/` it is the whole decision: the
command is trivially safe in isolation and completely opaque in
purpose.

The motivation is not missing — it is unreachable. Measured across the
45 session transcripts on this machine:

| assistant turns | count |
|---|---|
| total | 413 |
| carrying tool calls | 349 |
| of those, **also carrying a text part** | **1** |
| text only (final answers) | 64 |

Gemini 3 essentially never writes a preamble as a *text* part when it
calls a tool (the single exception was `ask_user`, a tool whose whole
job is to talk to the human). It writes it as a **thought summary**,
and thought text is structurally transient here:

- it is display-only by ADR-0033 — the transcript stores thought
  *signatures* only, because that is the shape replay was measured
  with, and putting the text back fails the next round with 400;
- the live tail is cleared the instant a round ends in a tool call
  (`model.go`, `case ToolCall`), which is exactly the moment the
  operator starts needing it;
- the tail renders only in the running phase, so the approval dialog —
  a different phase — never shows it at all.

So the agent is, from the operator's seat, silent about intent. And
because thought text is never persisted, the transcript cannot answer
"why did it try that?" after the fact either. There is nowhere in the
system where the intent of a call is written down.

## Decision

**Every approval-gated tool takes a required `purpose` argument: one
sentence, in the operator's language, saying why this call is needed
now.**

This is the "teach the model the format" branch of the project's rule
about model output (never silently correct it): the agent does not
infer intent from the command, it gives the model a slot to declare it
and shows the declaration to the human.

### 1. Injected centrally, scoped by `Mutating`

The parameter is added where `llm.ToolDef`s are built from the
registry, not in each tool definition. Built-in tools and MCP tools get
identical treatment, no tool file repeats the boilerplate, and a newly
loaded MCP server is covered automatically.

Scope is the static `Mutating` flag, not the live per-tool policy. The
advertised schema must not change mid-session: the request prefix has
to stay byte-identical for the implicit cache (ADR-0018), and a policy
change is a runtime event.

### 2. `purpose` is gem-agent's field, not the tool's contract

It is stripped from the arguments before `Run`. No MCP server ever
receives an argument its schema did not declare, and no built-in tool
has to know the field exists. The full arguments — purpose included —
stay in the history and the transcript, because that is what the model
actually emitted and what replay requires.

### 3. Self-declaration is never evidence

The purpose is model-authored text written by the same party that
proposed the call. It is displayed to a human and used for nothing
else:

- **stripped from the risk-evaluation payload.** Otherwise the model
  tier reads the proposer's own justification and is persuaded by it —
  the evaluator-is-the-proposer failure ADR-0020 §4 already refused for
  memory writes, in a place where it would apply to every gated call.
- **inert to the rule tier.** `risk.Classify` reads named arguments
  (`path`, `command`) only; a test pins that adding a purpose cannot
  move a verdict in either direction.
- **excluded from the loop signature.** The loop guard compares
  canonical argument JSON; if the purpose were part of it, a model that
  re-words its justification each round would defeat the guard while
  repeating the identical call.

### 4. A missing purpose is surfaced, not punished

The field is `required` in the schema — that is how the model is told
it is mandatory — but a call that arrives without one still runs. The
dialog and the audit event say *(no purpose declared)* instead.

Rejecting the call would invent a new failure mode at the worst
possible moment (an approval prompt the operator cannot satisfy), for
an annotation that is not a safety control. The absence is itself
information: an operator looking at an undeclared `cp` now knows the
model skipped a required field.

### 5. Where it shows up

- the approval dialog, on its own accented line above the escalation
  reason;
- the `⚙ tool` event line in the conversation, so approved-and-run
  calls leave the same trace;
- the `tool.call` audit event as its own `purpose` attribute
  (ADR-0035), so the log answers the question later;
- the session transcript, already, as part of the call's arguments.

## Consequences

- One short string per gated call. The token cost is real and small;
  the schema change re-warms the implicit cache once, on upgrade.
- The operator can read a stream of `⚙` lines as a narrative instead of
  a list of commands.
- `purpose` is model-authored and can be wrong, vague, or flattering.
  It must never gate anything — §3 is the rule that keeps it honest,
  and it is pinned by tests rather than by convention.

## Alternatives considered

**Keep the thought tail in scrollback.** Display-only, safe (no history
change), and cheap. Rejected as the primary fix: thought summaries are
verbose, not scoped to one call, cannot appear in the dialog's phase,
and never reach the transcript. It remains available as an independent
display option.

**Instruct the model to write a text preamble before calling a tool.**
Measured: 1 text part in 349 tool-calling turns. Gemini 3 routes
preamble into thoughts, and a prompt-only instruction with no
structural slot does not fire — the same "capability without a trigger"
pattern recorded when memory saves never happened on their own.

**Derive intent from the command.** That is inference about model
behaviour performed by code — the exact move ADR-0043 removed from the
diagram path. The model knows why; ask it.
