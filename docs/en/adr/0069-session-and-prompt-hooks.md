# ADR-0069: session-start and prompt-submit hooks — context enters as data, on the same contract as Claude Code

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-04 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a cross-runtime shared knowledge space for concurrent agent sessions (gem-agent and Claude Code alike) needs a per-turn injection point; Claude Code has `SessionStart` and `UserPromptSubmit` hooks, gem-agent has only `PreToolUse` |
| Amends | ADR-0044 §1 (two more events, on the demand that section anticipated) |

## Context

ADR-0044 implemented one hook event and declined the rest: "the one
event with demonstrated demand is PreToolUse, and a capability without
a demonstrated trigger is dead weight. The same mechanism accepts more
events later if demand appears." The demand has appeared. The
organization is designing a shared, machine-local knowledge space that
concurrent agent sessions read and write across runtimes. Its delivery
model is a delta injected at every turn boundary — the only honest
granularity, since nothing can interrupt a model mid-turn — and Claude
Code already offers that boundary as a hook. gem-agent did not.

Measured before designing, against Claude Code 2.1.226 with a capture
script registered through `--settings` (the documented contract was
read afterwards and differs in two places):

- `SessionStart` stdin: `{"session_id", "transcript_path", "cwd",
  "hook_event_name": "SessionStart", "source"}`. `source` was
  `"startup"` on a fresh run and `"resume"` under `--resume`. The
  documentation also lists `permission_mode`; the payload did not carry
  it.
- `UserPromptSubmit` stdin: the same identity fields plus `prompt_id`,
  `permission_mode`, and the typed text under **`prompt`**. The
  documentation names that field `user_input`; the payload says
  `prompt`.
- Output: on exit 0, plain stdout is recorded in the transcript as a
  `hook_success` attachment with the text as its content; a JSON
  object with `hookSpecificOutput.additionalContext` is recorded as a
  `hook_additional_context` attachment. Both reach the model. A JSON
  object without that field injects nothing.
- Blocking: a `UserPromptSubmit` hook that exits 2 stops the turn
  before any model call — the result reads "operation blocked by hook"
  with stderr as the reason, followed by the original prompt — and the
  `{"decision": "block", "reason"}` form does the same. `SessionEnd`
  (and per the documentation `SessionStart`) cannot block: exit 2 is
  reported as a failed hook.
- A `hookSpecificOutput` object on an event that does not define one
  fails Claude Code's output schema validation, reported on stderr.

## Decision

### 1. Two more events, from gem-agent's own config, global only

`[[hooks.session_start]]` and `[[hooks.user_prompt_submit]]` join
`[[hooks.pre_tool_use]]` in `config.toml`, each with a `command` and an
optional `timeout_sec`. A `session_start` entry may carry a `matcher`
that selects the source (`startup`, `resume`, `clear`; `a|b` and `*`
as for tools); a `user_prompt_submit` entry takes none — every prompt
runs it, and a matcher there is a config error rather than a hook that
silently never fires. ADR-0044 §5 stands: no project surface. A
context hook runs on every turn, so a project-level one would be a
cloned repository executing an arbitrary command per turn.
`~/.claude/settings.json` is still not read (ADR-0011).

### 2. The payloads are Claude Code's, and the sources are the ones gem-agent has

Each hook receives one JSON object on stdin with `hook_event_name`,
`session_id`, `transcript_path` and `cwd`, plus `source` for
`SessionStart` or `prompt` for `UserPromptSubmit` — the measured
fields gem-agent can honestly supply. `prompt_id` and
`permission_mode` have no gem-agent equivalent and are omitted rather
than invented. When the session log is disabled, `session_id` and
`transcript_path` are sent empty, so a script sees the same shape every
time.

`session_start` fires once at startup with `source` `startup`, or
`resume` under `--continue`/`--resume`, and again on `/clear` with
`clear` — a cleared conversation is a fresh start to the operator's
scripts. It does not fire after compaction: Claude Code's `compact`
source has no demonstrated consumer here, and firing inside a turn
would raise the question §4 avoids. `user_prompt_submit` fires for
every turn that reaches the model — a typed message, the argv first
message (ADR-0064), a `/skill`-expanded turn, the `-p` prompt — and
not for slash commands or the operator's `!` shell escape, which never
become a turn.

**Addendum (same day, v0.65.1).** The `PreToolUse` payload now carries
`session_id` and `transcript_path` too (empty when the log is
disabled), so a hook that keeps per-session state can tie a call to its
session — agent-board's claim enforcement needed exactly this, and
without it refused the claimant its own file. Claude Code's PreToolUse
carries the same identity fields, so the shape stays one contract.

### 3. Output is context or a verdict; a prompt can be refused, a session start cannot

On exit 0, plain stdout is injected context; a JSON object is a
verdict, of which only `hookSpecificOutput.additionalContext` is
context. A `user_prompt_submit` hook refuses the prompt by exit 2 with
the reason on stderr, or by either JSON block form ADR-0044 §3 already
accepts (`hookSpecificOutput.permissionDecision: "deny"`, `decision:
"block"`). A refused prompt is erased: nothing enters the history or
the transcript, no `turn.end` event is emitted, and `Run` returns
`ErrPromptBlocked` with the reason, which the operator sees. The first
block wins, and context other hooks produced for that prompt is
discarded with it. A `session_start` hook that blocks is reported as a
failure and injects nothing — the measured Claude Code semantics.
Everything else — non-zero exit, timeout, unparseable output — fails
open with a notice and no context (ADR-0044 §3, unchanged).

### 4. Injected context rides the data lane, never the system prompt and never the typed input

Hook output is delivered as an attachment on the next user message —
the lane ADR-0055 built for piped stdin: stored beside the typed text
in the transcript, flattened after it on the wire inside the turn's
nonce tag and announced as "quoted as data". Three things follow, and
each is the reason for the choice:

- **The system prompt is untouched.** A session-start hook's output
  therefore does not disturb the byte-stable request prefix the implicit
  cache depends on (ADR-0018), and the startup-snapshot rule of
  ADR-0020 §7 keeps holding: nothing rewrites the prefix mid-session,
  including `/clear`, whose hook output simply rides the first new
  turn.
- **The typed input is untouched.** The input string is the risk
  evaluator's "operator instruction" evidence (ADR-0038/0054),
  trusted precisely because an injection attacker cannot write it. A
  hook's output is code run over whatever that code read — a shared
  store other agents write to, in the triggering design — so it must
  not acquire that standing. Merging it into the prompt would hand the
  evaluator's one clean channel to every writer of that store. A test
  pins the boundary, as `attachdata_test.go` does for stdin.
- **The model is told what it is.** "Attached hook (session_start),
  quoted as data" is a statement of provenance, not a prohibition. The
  triggering design wants exactly this framing: its records are claims
  other sessions made, to be weighed, never directives.

This is stricter than Claude Code, which places hook stdout in the
model's context without such a wrapper. The difference is deliberate
and documented; a hooks block copied from Claude Code still works, its
output simply arrives labelled. Output is capped at 8000 runes per hook
with a visible cut, and the operator sees one line per injection
("session_start hook (startup) attached N bytes of context as data for
the next turn") — a channel that silently shapes every turn would be
the wrong kind of quiet.

### 5. Verified live

One-shot run on 2026-09-04, `--mcp off`, a scratch state root: both
hooks fired with the §2 payloads (checked field by field by the hook
script), the transcript's first user message carried two `hook`
attachments beside the untouched prompt, and the model echoed both
marker strings verbatim. A second run with an exit-2 prompt hook
stopped before any model call: exit status 1, the reason on stderr,
and a transcript holding nothing but the session header.

## Consequences

- The shared knowledge space design can register one script in both
  runtimes: the stdin payload and the output contract are the same, and
  the only difference — data framing — is what that design wants.
- Every turn pays one process spawn per configured
  `user_prompt_submit` hook, on top of the hook's own work; a session
  start pays one per `session_start` hook. Unconfigured, nothing runs.
- A hook that prints on every turn adds up to 8000 runes to every
  turn's context. That is the operator's budget to spend; the cap and
  the notice make the spend visible.
- `Agent.Options` gains `PromptHook`; `Run` may now return
  `ErrPromptBlocked` before recording anything. `slashOutput` takes an
  `onClear` callback so `/clear` can re-run the session-start hooks.
- Two more `[hooks]` entries in the config reference and the shipped
  template; still no settings-panel toggle (ADR-0044: a toggle on a
  control is a bypass).

## Alternatives considered

- **Session-start output into the system prompt** (as an operator
  instruction section) — rejected: it would rewrite the cached prefix
  on `/clear` and would grant code-generated text the AGENTS.md trust
  tier (§4).
- **Plain context, as Claude Code does** — rejected for the typed-input
  channel alone: merging hook output into the prompt string is the
  ADR-0055 hole with a different producer (§4). The data lane already
  existed and already had the boundary test.
- **`compact` as a fourth source** — deferred: no consumer, and a hook
  firing inside a turn would need its own delivery rule (§2).
- **`SessionEnd`** — deferred: the triggering design's claims expire by
  TTL and heartbeat, so a release-on-exit event has no consumer yet.
  Its payload (`reason`: `other` in one-shot mode) was captured with
  the rest and can be added on the same mechanism.

## References

- ADR-0044 (pre-tool hooks; the measured-contract method and the §1 clause this ADR invokes)
- ADR-0055 (piped stdin as a data attachment; the lane and the boundary test reused here)
- ADR-0038 / ADR-0054 (the typed input as the risk evaluator's trusted instruction channel)
- ADR-0018 (the byte-stable request prefix), ADR-0020 §7 (startup snapshot)
- ADR-0064 (the argv first message is a typed turn)
