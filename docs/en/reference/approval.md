# Approval, sandbox, and safety

The layers between a model-proposed action and its execution, from the
per-call dialog to the Seatbelt sandbox — and the startup gates that
run before anything loads.

## Operator pre-tool hooks (ADR-0044)

Before any gate below — in every mode, auto-approve included —
operator-configured hooks get the call first. Each
`[[hooks.pre_tool_use]]` entry in the global config runs its command
with the call as Claude Code PreToolUse JSON on stdin:

```toml
[[hooks.pre_tool_use]]
matcher = "shell_exec"           # exact name, "a|b", or "*"
command = "python3 /Users/you/.claude/hooks/guard-recursive-write.py"
timeout_sec = 10                 # optional; 0 = default (10)
```

The contract is Claude Code's, measured against the org's installed
guard rather than taken from documentation — a Claude Code guard
script is registered here **unchanged**. A hook denies via stdout
JSON (`permissionDecision: "deny"` with the reason, the form the org
guard actually emits) or exit code 2 with the reason on stderr; the
guard reads `tool_input.command`, which is also the name of
`shell_exec`'s argument. Matchers accept Claude Code names too
(`Bash` matches `shell_exec`; `Write`/`Edit`/`Read` likewise).

A deny is a **deterministic floor**: the ladder below, the model
tier, and the session allowlist never see the call, and the reason is
returned to the model as the tool result, so it corrects and retries
(verified live: the org's relative-path guard denied a `sed -i`
inside gem-agent, the full reason reached the model, and the file
stayed untouched). Anything short of an explicit deny — a crash, a
timeout, unparseable output — proceeds with a warning: hooks only
ever tighten, and a broken guard script must not brick the fallback
tool. Hooks cover the model's calls only; the operator's own
`!command` escape does not pass through them.

## Per-call approval (MITL)

Mutating tools ask before running (the dialog itself is described in
[interface](interface.md)). `y` allows once, `a` allows the tool for
the session, `p` persists never-ask to the policy file (`p` is a TUI
answer; the plain-stdin gate used when the TUI is off offers `y`/`n`/`a`
only), deny fails closed. `a` never covers the dangerous cases: Block-tier calls (sudo,
recursive deletes, credential paths, …) and tools pinned to `"always"`
by policy keep asking regardless (ADR-0021).

## Auto-approve mode (ADR-0004)

Off by default. shift+tab (or `/auto`) toggles it; the status line
shows `⚡auto` while on. **shift+tab also works while a turn is
running**, so a long agent loop that started in manual mode can be
switched over without waiting — the change applies from the next tool
call. Each mutating call then goes through:

1. **Rule tier** (no model call): *safe* → runs; *blocked* → always
   asks (`rm -rf`, `sudo`, `git push`, download-piped-to-shell, disk
   writes, credential paths, anything outside the project…);
   *uncertain* → tier 2.
2. **Model tier**: a separate evaluation round judges the proposed call
   (delivered to it as nonce-wrapped untrusted data, with no tools
   available). It must both approve *and* be confident, or the call
   asks.

**Memory writes never reach tier 2.** `save_memory` and `delete_memory`
are Review-tier, so they would take the *uncertain* branch — but they
are excluded from auto-approval outright and always ask, whatever the
evaluation would have said (ADR-0020 §6). The evaluator is the same
party that proposed the write, so it cannot be the defence against a
poisoned tool result talking the agent into remembering an instruction;
memory is a persistence vector, and what the agent remembers is the
operator's call. The per-tool policy remains the way to relax that on
purpose.

Anything that fails — model error or a malformed verdict — asks. (An
*unknown* tool never reaches the gate at all: the dispatcher rejects it
with an error before approval is consulted, so it also never runs.) The blocked tier is a hard floor the model cannot override, and
the sandbox applies in every mode.

For the **first three rounds of a turn**, the model tier also sees the
request you typed (ADR-0038) — quoted as evidence inside the same
isolation wrap, clipped; never your attachments, never the
conversation. Alignment with your request supports approval; a call
that contradicts it, or serves directions found in file contents
rather than your request, escalates with the contradiction named
(live-measured: a `make build` your instruction explicitly forbade
escalated where the call-only view approves it). Later rounds run the
conventional call-only evaluation byte-identically — deep in a turn,
calls legitimately serve sub-goals your request never names. The
context reaches only calls that reach the model tier: Safe-tier calls
stay rule-approved as before, and Block is decided before the model is
consulted.

For **MCP calls**, the model tier also sees the tool's
self-description — the description the server publishes in its tool
listing (ADR-0046) — quoted as untrusted evidence inside the same
isolation wrap, clipped, read live from the registry. Without it the
evaluator guesses semantics from the tool name alone, which is where
verdicts wobbled call to call. The prompt weighs it as a claim, not a
fact: honest "read-only, fully offline" semantics support approval,
arguments that contradict the description escalate, and a description
that argues for its own approval is itself escalation evidence
(live-measured: a lobbying description was escalated and named as an
injection attempt, while the same call with an honest read-only
description approved).

Both outcomes are explained: auto-approved calls print their reason,
and an escalated call's dialog carries a `⚠` line naming the tier that
objected and why — `blocked by rule (always asks): …` for the
deterministic floor, `escalated by risk review: …` for a model
judgment, and `auto-approve escalated: …` for a call the ladder never
put to the model, which today means a memory write.

Note the MCP boundary: MCP tools are approval-gated and Mutating by
definition, but the rule tier cannot judge a foreign server's tool, so
they can never reach the Block floor — in auto mode the model tier may
pass routine calls (judging with the server-published description as
evidence, ADR-0046), and a session `a` covers the tool for any later
arguments. Pin `"always"` in the policy where that trade is wrong.

## Per-tool approval policy (ADR-0008)

Every MCP tool asks on every call, because gem-agent cannot know what a
server's tool does. You do — so you can say so:

```toml
# ~/.config/gem-agent/config.toml
[approval.tools]
"mcp__tor-exit-lookup__*" = "never"   # a read-only lookup server
"shell_exec"              = "always"  # even in auto-approve mode
```

`"never"` skips the gate in every mode, `"always"` gates in every mode
(auto-approve cannot lift it, and neither can a session `a`), and an
unset tool keeps the default. A trailing `*` matches a whole MCP
server. Resolution is **scope first, then specificity** (ADR-0021): a
project rule beats any global rule for the tools it matches; within one
scope, exact names win over wildcards. A bare `"*"` is rejected —
switching off every gate at once should not be reachable by a
one-character entry.

**`"never"` is not "run anything."** For a tool whose effect varies per
call — `shell_exec` — the rule tier's blocked patterns still ask.

A project can carry its own policy in `<project>/.gem-agent.toml` (see
`gem-agent.example.project.toml`), and **direction matters**:

| From a project file | Honoured |
|---|---|
| `"always"` — more approvals | always |
| `"never"` — fewer approvals | only if the project is listed in `[approval].trusted_projects` in *your* config |

A checked-out repository is not necessarily something you wrote, and
cloning one must not be able to switch the gate off. Ignored entries
are named at startup, with the line to add if you do want them.

One useful consequence: `-p` one-shot mode denies mutating tools
because nothing can answer a prompt there, but a tool set to `"never"`
was never going to ask — so it runs. A read-only MCP lookup with a
`"never"` policy is usable in a pipeline.

Changes made through `/settings` or the `p` answer persist to the
machine-owned `~/.config/gem-agent/policy.toml`; concurrent instances
write it through a locked read-modify-write, so two sessions cannot
clobber each other's decisions.

## Sandbox (ADR-0001)

`shell_exec` (and `!` commands) run wrapped in macOS sandbox-exec:
file writes restricted to the project directory, the scratch dirs
(`TMPDIR`, `/private/tmp`) and `/dev`, enforced by Seatbelt and covered
by a real enforcement test.
`--no-sandbox` disables the wrapper (debugging only). The sandbox
applies in every approval mode.

## Startup safety (ADR-0023)

Two gates run before anything loads:

- **Broad roots ask first.** Launched in `/`, your home directory, or
  an ancestor of it, gem-agent explains that file tools and sandboxed
  shell writes would span that entire tree and asks before starting
  (default: no). Non-interactive runs (`-p`, pipes) are refused there
  outright.
- **A new project must be trusted once.** The first launch in a project
  that provides agent-facing files lists what it provides —
  instruction files (injected as *your* instructions), `.mcp.json`
  (each server entry starts a child process), `.claude/skills` — and
  asks whether to trust it (default: no). The answer persists per
  project in the machine-owned `policy.toml`
  (`trust = "granted" | "declined"`; delete the key to be asked
  again). Declining still starts the session: the project's own files
  are simply not loaded, and the banner says so. Ancestor instruction
  files and all global configuration are outside the gate — a clone
  cannot plant files in directories you own above it. Projects listed
  in `[approval].trusted_projects` are trusted without asking.
  Non-interactive runs in an undecided project run bare (nothing of
  the project's loaded, note on stderr, nothing recorded) so
  read-only `-p` pipelines over fresh clones keep working.

## Untrusted-content isolation (ADR-0018)

Tool output is isolated with nonce XML tags (nlk/guard;
session-scoped in the main loop, per-call in side-calls) — content
returned by tools is framed as data, never instructions. Attachments
carry the same framing; images and PDFs, which no tag can wrap, get an
explicit statement of the same stance.

The session-scoped tag has a second job: the request prefix stays
byte-identical across rounds and turns, so Vertex's implicit caching
prices the replayed history at the cached rate — measured 81–95%
cached on an identical 4-round task, vs 0% with a per-call tag. Sound
because nlk/guard refuses content containing the tag name — a leaked
tag can only get its carrier withheld, never escape the wrapper.
