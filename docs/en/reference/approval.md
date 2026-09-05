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

Two further events, `[[hooks.session_start]]` and
`[[hooks.user_prompt_submit]]` (ADR-0069), are not gates on a call:
they inject context before a turn on the same Claude Code contract,
and a prompt hook may refuse the prompt outright (erased, nothing
recorded). Their output reaches the model as a labelled **data**
attachment beside the typed input — never the system prompt, never
the instruction channel the auto-mode reviewer trusts. See
[configuration](configuration.md).

## What the call is for (ADR-0047)

Every approval-gated tool carries a required `gem_agent_purpose`
argument (namespaced so a server's own argument names cannot collide), and
the model's one sentence appears on the prompt above the arguments:

```
承認が必要です: shell_exec
↪ Slackへの添付準備としてレポートファイルを一時ディレクトリに配置するため
cp report.csv /tmp/share/
```

Without it the prompt answered "what will run" and "why you are being
asked", but never "why the agent wants this" — and for an
innocuous-looking `cp` that is the whole decision. The motivation was
not missing, only unreachable: Gemini 3 writes its preamble as a
thought summary rather than as text (1 text part in 349 tool-calling
turns, measured), and thought text is display-only, cleared the moment
a round ends in a call, and never stored.

The declaration is written by the same party that proposed the call,
so it is **context for you and evidence for nothing**: it is stripped
before the risk evaluator sees the call, cannot move a rule-tier
verdict, and is excluded from the loop guard's signature. A call that
arrives without one still runs — the prompt says *(no purpose
declared)* rather than refusing, since the absence is itself something
worth seeing.

The same line rides the `⚙` event line for calls that never open a
dialog (auto-approved or allowlisted), the `tool.call` audit event as
its own `purpose` attribute, and the session transcript as part of the
call's arguments.

## Per-call approval (MITL)

Mutating tools ask before running (the dialog itself is described in
[interface](interface.md)). `y` allows once, `a` allows the tool for
the session, `p` persists never-ask to the policy file (`p` is a TUI
answer; the plain-stdin gate used when the TUI is off offers
`y`/`n`/`N`/`a`), deny fails closed. `a` never covers the dangerous cases: Block-tier calls (sudo,
recursive deletes, credential paths, …) and tools pinned to `"always"`
by policy keep asking regardless (ADR-0021).

`N` denies **with a typed reason** (ADR-0060): a one-line field opens,
and what you type rides back to the model inside the denial itself —
so "wrong file, put it in notes.md" arrives at the moment of decision
instead of costing a model round of "how should I proceed?". Enter on
an empty field is a plain deny, Esc backs out to the options; `n`
stays the one-keystroke deny. The reason is recorded in the session
transcript (`gate_decision`) but never exported to telemetry.

## Auto-approve mode (ADR-0004)

Off by default. shift+tab (or `/auto`) toggles it; the status line
shows `⚡auto` while on. **shift+tab also works while a turn is
running**, so a long agent loop that started in manual mode can be
switched over without waiting — the change applies from the next tool
call. Each mutating call then goes through:

1. **Rule tier** (no model call): *safe* → runs; *blocked* → always
   asks (`rm -rf`, `sudo`, `osascript … with administrator privileges`,
   `git push`, download-piped-to-shell, disk writes, credential paths;
   for the file tools also anything outside the project and the
   session work directory); *uncertain* → tier 2. For `shell_exec` the
   tier never says *safe* from the command's text: the **lane** the
   model declared does (below).
2. **Model tier**: a separate evaluation round judges the proposed call
   (delivered to it as nonce-wrapped untrusted data, with no tools
   available). It must both approve *and* be confident, or the call
   asks.

**Shell commands are judged by their lane, not their text** (ADR-0073).
`shell_exec` takes an `access` argument the model declares — `read`
(default), `write` or `operator` — and macOS Seatbelt enforces that
lane, whatever the command says:

| lane | the kernel denies | who decides |
|---|---|---|
| `read` | every write except into the session's private scratch directory (`TMPDIR` points there) and the device sinks — the project, the work directory and `/private/tmp` included; the network; Mach and POSIX IPC, Apple Events, launch services (`open`), preference writes (`defaults`), NVRAM, device access, signals to anything but the command's own children; launching the IPC-capable programs (`osascript`, `open`, `launchctl`, `defaults`, `security`, `pbcopy`, `shortcuts`, `automator`, `scutil`, `networksetup`, `systemsetup`, plus `[sandbox].read_lane_deny_exec`) as a second line; reading credential files | nobody — a read-lane command changes nothing but its own scratch and **runs without a prompt in every mode**, like `read_file` — on a machine where the lane's denials were verified at startup (below) |
| `write` | writes to the files later sessions trust (below) — which is why `git init`, `git clone` and `git remote add` (they write `.git/config` and hooks) land in the operator lane, and the refusal says so; reading credential files | the ladder above in auto mode, you in the default mode |
| `operator` | nothing beyond the ADR-0001 profile: the persistent files are writable, credentials readable | **you, always** — the model tier, a session `a` and `--allow` never answer it |

A false declaration gains nothing: `read` can only tighten the cage,
`write` and `operator` only add scrutiny. A command the read lane
refused returns its exit status and one line naming the lane to ask
for; the model re-issues it with `access: "write"`, which is where the
approval happens. The *blocked* patterns above still apply in every
lane — a `sudo` in the read lane asks even though the cage would refuse
it — and they are the only text rule left: a spelling they miss costs a
prompt the kernel catches anyway, never a hole. The three layers have
three jobs: the sandbox bounds what a command can reach, the model tier
judges meaning and consistency inside that bound, and the *blocked*
patterns, the `operator` lane and unconfined execution are the
operator's alone whatever the model thinks.

**The read lane is verified, not assumed.** At startup gem-agent runs
the read-lane profile under `sandbox-exec` against probes that must fail
(a project write, a socket connect, a signal to another process, a
`/private/tmp` write, a denied program) and one that must succeed; only
where every probe behaves does a read-lane command run unasked.
Otherwise every `shell_exec` asks and the banner says why. A sandbox
that is configured on but cannot apply here is a startup error, not a
silent fallback: pass `--no-sandbox` to accept unconfined execution
explicitly. **Unconfined is a mode, not a lane**: under `--no-sandbox`
every `shell_exec` is yours to approve — the model tier never approves
it, and neither a session `a` nor a `never` policy lifts it — and the
audit record carries `lane=unconfined:<declared>`.

**Writes that later sessions trust ask you, not the model** (ADR-0072
§1.4, enforced by the kernel since ADR-0073). A write under `.git/`
through the file tools is *blocked* — a hook or a config value there
runs outside the sandbox on your next git command. A write to
`AGENTS.md`, `AGENT.md`, `CLAUDE.md`, `GEMINI.md`, `.mcp.json`,
`.gem-agent.toml` or anything under `.claude/` through `write_file` or
`edit_file` is *uncertain* and skips tier 2: the edit persists into
what every later session takes instructions or configuration from, so
the party that proposed it cannot be its judge (the memory rule below,
applied to the same class of persistence). From the shell, the write
lane's profile denies those files and `.git/hooks`, `.git/info` and
`.git/config` outright — a redirect, `mv`, `sed -i`, `rm` or
`git config` onto them fails with `Operation not permitted` — and only
the `operator` lane, which you approve, may touch them. The list is one
function (`sandbox.PersistentFiles`) read by the profile and by the
file tools' verdict, so the two cannot disagree.

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

On **every evaluation**, the model tier also sees the request you
typed (ADR-0038; the original 3-round cutoff was measured against
real usage — 70% of evaluations fell outside it — and removed by
ADR-0054) — quoted as evidence inside the same isolation wrap,
clipped; never your attachments, never the conversation. Alignment
with your request supports approval; a call that contradicts it, or
serves directions found in file contents rather than your request,
escalates with the contradiction named (live-measured: a `make
build` your instruction explicitly forbade escalated where the
call-only view approves it). An indirect relation deep in a
multi-step turn is normal and is not by itself a reason to escalate.
The context reaches only calls that reach the model tier: Safe-tier calls
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

## One-shot mode (`-p`) — ADR-0053

One-shot mode has nobody to answer an approval prompt, so by default
every gated call is denied, with the reason on stderr. Two controls
open it up, by increasing risk appetite:

- **A standing `"never"` policy** (above) — right for read-only
  lookups you always want in pipelines.
- **`--allow "name"`** (repeatable, or comma-separated) grants the
  named tools — exact names or `mcp__server__*` prefixes, the
  `[approval.tools]` vocabulary — a `"never"` policy for **this run
  only**. The entries join the global scope at flag precedence
  (flags > machine-owned policy file > hand-written config) and go
  through the normal policy build, so a project's `"always"` tighten
  still wins, the Block floor is not lifted, pre-tool hooks still
  deny, and a bare `"*"` is still an error.
- **`--auto`** arms the ADR-0004 ladder for this run. Approvals work
  exactly as in the TUI (rule tier, then model tier with the
  confidence bar); everything the ladder would *escalate* — Block-tier
  calls, `"always"`-policy tools, model doubts, evaluation errors —
  is denied instead, with the escalation's reason in the
  `[denied: …]` line. Fail-closed: no floor moves.

`[agent].auto_approve` is deliberately **ignored** in one-shot mode:
an unattended run's grant must be visible on the invocation itself —
the command line a script or cron entry shows — not in a standing
config file. Approved calls print `[auto-approved …]` lines to
stderr, so the pipeline's audit trail shows what ran, not only what
was refused.

Piped stdin (`data | gem-agent -p "…"`) is attached to the turn as
**nonce-wrapped data** — the `@`-file lane — never merged into the
prompt (ADR-0055). The `-p` string alone is the instruction the risk
evaluator sees: an injection in whatever the pipe fetched cannot
reach the trusted instruction channel. The read is bounded (256 KiB,
clip disclosed), binary input is skipped with a warning, and a
terminal stdin is never read. A non-terminal stdin is read to EOF —
a slow producer is never cut off — and if it is still open after 2 s
one stderr line announces the wait and names the remedies: close the
pipe, or launch with `< /dev/null` when nothing is meant to be
attached (ADR-0067). Schedulers and tool harnesses that hand a child
an idle inherited pipe are the case this catches.

Note what `--auto` means here: with no human anywhere, the model
evaluator is the sole arbiter of Review-tier calls. For a pipeline
that reads untrusted content and holds an egress-capable tool, prefer
`--allow` with the exact tools over arming the general ladder.

## The risk rulebook (ADR-0050)

The auto-mode risk reviewer can read **operator-authored guidance** —
a rulebook — on every call it judges. Two layers stack:

- **Base rules** — `~/.config/gem-agent/risk-rules.md`. Hand-written;
  gem-agent reads it and never writes it. Your standing risk posture,
  phrased however you like: *"network installs always need eyes"*,
  *"anything touching the customer export gets escalated"*.
- **Project rules** — written by `/riskbook learn` (below) or edited
  by hand, stored per project outside the repository. The reviewer is
  told the project layer is the more specific statement where the two
  conflict.

The rulebook is **guidance, not policy**: it biases the reviewer's
confidence in either direction and never skips a gate. For a question
you have settled deterministically, the per-tool policy above
(`"never"`/`"always"`) remains the vocabulary — one line, mechanical,
no model involved. The rulebook is for what policy cannot express.
Accordingly the floors are untouched: Block-tier calls never consult
the reviewer, pre-tool hooks still run first, memory writes still
always ask, and manual mode is unchanged entirely. Prose urging
blanket approval is itself treated as a reason to escalate — a real
blanket bypass belongs in policy, where it is mechanical and visible.

`/riskbook learn` drafts the project layer from your own decision
record: it aggregates what the reviewer verdicted against what you
actually answered at the gate (typed answers count individually; a
session-allowlist `a` is one vote), has the summary model explain
what corrections that implies, and shows you the **full draft —
byte-for-byte what would be stored**. Nothing takes effect until you
accept it. `/riskbook` shows what is in force (re-read from disk),
`/riskbook reload` picks up hand edits without a restart, and
`/riskbook clear` removes the project layer. While any layer is in
force the startup banner says so.

A rulebook is deliberately **not** read from the repository: a cloned
project's files steer the model that proposes calls, and the reviewer
is the second layer of that defence — its guidance must not arrive
through the first layer's source.

## Learned rules — withdrawn (ADR-0049)

Between v0.46.0 and v0.47.0 a `/learn` command proposed approval rules
from your own recorded gate decisions. It was withdrawn after field
testing: far more ended up permitted than the operator expected, and
per-rule confirmation — even with full disclosure — did not turn out
to be a durable boundary for loosening. ADR-0045, ADR-0048, and
ADR-0049 hold the design and the honest post-mortem.

What remains, and what to check if you used it:

- The transcript records it introduced (`gate_decision`, with the
  answer's source) are still written — they are diagnostic, and any
  future design will need them.
- `[projects."…".commands]` entries in the machine-owned `policy.toml`
  are **no longer applied**; a startup note reports any that exist.
  Delete them or leave them — they do nothing either way.
- Global `[tools]` entries it wrote (`mcp__<server>__*` wildcards) are
  ordinary ADR-0008 policy and **remain in force** — they cannot be
  told apart from hand-written ones. If you accepted server proposals,
  review `~/.config/gem-agent/policy.toml` and delete any wildcard you
  do not want.

## Sandbox (ADR-0001, ADR-0073)

`shell_exec` runs wrapped in macOS sandbox-exec under the profile of
the lane the model declared (see the auto-approve section above for
what each lane denies); `!` commands run in the operator lane because
you typed them. Every lane confines file writes to the project
directory, the session work directory and the scratch dirs (`TMPDIR`,
`/private/tmp`, `/dev/fd` and the device sinks), enforced by Seatbelt
and covered by a real enforcement test that runs the three profiles
against the probes (redirect, `mv`, `sed -i`, `rm`, `git config`
onto `AGENTS.md` and `.git/config`, credential reads, `osascript`, a
child `kill`). The scratch, persistent-file and credential lists are
each one function in `internal/sandbox`, read by the profile and by
the file tools' verdict (ADR-0070 §2, ADR-0073 §3). `--no-sandbox`
disables the wrapper (debugging only) — and with it the read lane, so
every `shell_exec` asks. The sandbox applies in every approval mode.

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
