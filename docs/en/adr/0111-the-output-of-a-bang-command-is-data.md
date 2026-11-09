# ADR-0111: the output of a `!` command is data

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-09) — implemented |
| Date | 2026-11-09 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | A repository audit (code-audit, 2026-11-09), and the operator's decision after reviewing it |
| Relates to | ADR-0055 (piped stdin as data), ADR-0073 (the operator lane), ADR-0110 (a pipe without `-p` is the operator's own input) |

## Context

A `!` command's output joined the history as one user-role string opening "I ran this shell command myself:". The send-time wrap covers tool results and attachments, so this output was the one place program output reached the model unmarked, in the operator's voice, on every later round and after resume. The project's rule is that untrusted data is wrapped.

This is not ADR-0110's case. There the operator composes a stream of input, and the specification says it is their input. Here they ask to see what a program printed; that the printed text should count as their words is nobody's decision.

## Decision

The command stays text in the operator's voice. The output is a text attachment on the same message (`kind "shell output"`, `ref "!"`), so the wrap, the transcript, resume and compaction are inherited from the attachment path. The approval gates and the risk evaluator are untouched: the evaluator reads only the turn's input.

## Consequences

- `!git status` followed by "fix that" still works: the model reads the output through the wrap.
- Session listings still show the command; the message keeps its prefix and `$ ` line.
- Sessions recorded before this keep their old messages, unwrapped, on resume.

## Alternatives considered

- **Leave it, as the operator's own act** — rejected by the operator: running a command is not vouching for what it prints.
- **Wrap the whole message** — rejected: the command is the operator's words, and it would be marked as text not to follow.
