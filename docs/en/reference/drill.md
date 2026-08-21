# Monthly drill

A backup that is never exercised is not a backup. gem-agent exists for
the day Claude Code is unavailable, which is the worst possible day to
discover that credentials expired, the configured model was retired, or
the binary no longer launches. This drill is how that day stays boring.

**Cadence:** once a month. **Time box:** 20 minutes. **Verdict:** every
step below either passes or produces an issue — there is no "mostly
worked".

Run it from a real project, not a scratch directory. Half of what rots
is project-shaped: instruction files, `.mcp.json` entries, the paths the
sandbox allows.

## Why these steps

Each step exists because something behind it decays on its own, without
anyone touching gem-agent:

| Decays | Caught by |
|---|---|
| Application Default Credentials expire | step 2 |
| A configured model is retired (Gemini 2.5 went 2026-10) | step 2 |
| Vertex endpoint/region rules change | step 2 |
| The binary is quarantined or its signature stops validating after an OS update | step 1 |
| An MCP server was renamed, moved, or lost its API key | step 3 |
| A project's instruction files moved or changed shape | step 1 |
| Transcript format drift makes older sessions unresumable | step 6 |
| The architecture overview drifts from the code — it has no per-release update trigger | step 7 |
| The operator forgets the keys and the approval flow | all of it |

## Procedure

Record the session id from the banner before you start (`session log:` on
the fourth line). The transcript **is** the drill's evidence: it holds
every prompt, tool call, and answer, so nothing needs transcribing by
hand.

### 1. Launch (2 min)

```sh
cd <a real project>
gem-agent --version
gem-agent
```

Read the banner and confirm all of it:

- version matches what you expect (`brew outdated nlink-jp/tap/gem-agent`
  should be silent)
- `project:` is the directory you meant
- `sandbox: enabled`
- `session log:` names a writable path
- `mcp:` lists the servers you expect, with their scopes
- `instructions:` lists the project's `AGENTS.md` / `CLAUDE.md` etc.

**Fails if** the binary will not launch, the banner reports a missing
config key, or `instructions:`/`mcp:` is empty where it should not be.

Then check the status line for `⚡auto`. If auto-approve is on (a config
default), **turn it off with shift+tab** and confirm the indicator
disappears: the drill is where the approval gate gets exercised, and in
auto mode most of step 4 would run unattended. Toggling also drills the
toggle.

### 2. Read-only question (3 min)

Ask something that **cannot** be answered from the instruction files, so
the tool path is actually exercised. Counts and specific lines work well:

> how many .go files are in the repository root, and what is the first
> function declared in <the main file>?

Do **not** ask what the project does or what its build commands are: a
project with a decent `AGENTS.md` already states those, so a correct
answer arrives with no tool calls and proves nothing. (Measured on the
first drill — the question was the runbook's, and it was the wrong
question.)

**Fails if** the model errors (auth, model not found, region), or answers
without calling `read_file`/`list_files` — with a question like the one
above, no tool calls means the answer was invented.

A 404 here almost always means the configured model is gone: check
`[model].name`, and remember the Gemini 3 family is served only from the
`global` endpoint.

### 3. MCP tool call (3 min)

Ask for something only an MCP server can answer — with the org's lookup
servers, for instance:

> is 1.1.1.1 a Tor exit node?

Approve the call when the dialog appears.

**Fails if** the server does not start, the call times out, or the tool
is missing from `/tools`. Skip this step only if you genuinely have no
MCP servers configured; note that you skipped it.

### 4. Write with approval, and the sandbox (5 min)

**4a — the gate.** Have it make a real, small change — a line in a
scratch file, a comment:

> add a one-line comment at the top of <some file> saying it was checked in a drill

Answer the approval dialog by **selection** (←→ / Tab, then Enter), not
by typing `y`. The selection route is the one that survives a Japanese
IME being on, so it is the one worth keeping in muscle memory. Then
`!git diff` to confirm the change is what you asked for, and revert it.

**4b — containment, tested directly.** Do *not* test this by asking the
model to write outside the project. Measured on the first drill: the
model reads the project confinement out of the system prompt and often
declines to try, which looks like a pass while proving nothing about the
sandbox. On another run it did try, and the approval gate asked first —
so the outcome varies with the model's mood, which is not what a drill
should rest on.

Test the containment layer with no model discretion in the way:

```
!echo drill > ../outside-drill.txt
```

Expect `Operation not permitted` and a non-zero exit status, and confirm
nothing was created:

```
!ls -la ../outside-drill.txt
```

**Fails if** that write succeeds — the Seatbelt profile is not in force,
whatever the banner said. This is the one step where a failure means stop
using gem-agent until it is fixed, rather than filing an issue and
carrying on.

Also fails if the approval dialog in 4a cannot be answered without typing
letters.

### 5. One-shot in a pipe (1 min)

```sh
gem-agent -p "list the top-level directories of this project" | head
```

**Fails if** the answer does not reach stdout cleanly, or if UI text
pollutes the pipe (banner and tool events belong on stderr).

### 6. Resume (3 min)

Quit (`Ctrl+D`), then:

```sh
gem-agent sessions
gem-agent -c
```

Ask something that can only be answered from the restored history:

> from memory only, without reading anything: what change did we make earlier?

**Fails if** the session is missing from the listing, the resume errors,
or the answer shows the history did not come back. A 400 on the first
message after resume means thought signatures did not replay — report it
with the session id, because it makes resume unusable rather than
degraded.

### 7. The architecture doc against the code (2 min)

The feature references are protected by the release habit — one README
line, the matching reference doc, the INDEX — but
[architecture.md](architecture.md) is cross-cutting and has no
per-release trigger, so it rots silently as the code moves (measured:
it went ten releases stale before an audit caught it, still claiming
"five built-ins" when there were eleven). While the session is open,
have gem-agent audit its own description:

> read docs/en/reference/architecture.md and compare it against the
> code: the package list, the tool inventory, the failure-behaviour
> table. Report only what the document gets wrong or no longer
> mentions.

**Fails if** the report names a real discrepancy — fix it on the spot
or file it like any other finding, both languages in the same commit.
A clean report costs two minutes; ten stale releases cost an afternoon.

### 8. One real task, gem-agent only (rest of the box)

The point of the drill is not the checklist. Pick a piece of work you
would otherwise do in Claude Code — small enough to finish, real enough
to matter (a focused fix, a test, a doc correction) — and **finish it
with gem-agent alone**: no switching back mid-task.

Then answer two questions honestly and write the answers down:

1. What did you have to work around?
2. Would you have shipped this?

Those two answers are the drill's actual output. The checklist proves
gem-agent runs; this proves it is usable, which is the thing that
silently degrades as Claude Code moves ahead of it.

## Recording the outcome

- **Pass:** note the date and the session id wherever you track
  operations. No further ceremony.
- **Fail:** open an issue on `nlink-jp/gem-agent` with the session id and
  the step number. The transcript under
  `~/.local/state/gem-agent/sessions/<id>.jsonl` holds the exact
  exchange, so the issue does not need a reconstruction from memory.
- **Skipped a step:** say which and why. A drill with an unrecorded gap
  reads as a pass, which is how a backup rots while looking maintained.

Two consecutive failed drills, or a failure that has stayed open for a
month, mean gem-agent cannot currently be relied on as the fallback —
which is worth knowing deliberately rather than discovering it under
pressure.

## First drill — 2026-08-19

Run against `util-series/json-filter` and this repository, on v0.2.1.
**Verdict: pass.** Recorded because a runbook is worth only as much as
its first honest run, and this one rewrote three of its own steps.

What the run changed in the runbook above:

- **Step 2's question was wrong.** "What does this project do, and what
  are its build and test commands?" got a correct answer with **no tool
  calls** — a project with a decent `AGENTS.md` already states that, and
  the instruction files are injected into the system prompt. The step
  intended to prove the tool path works and proved nothing. Replaced
  with a question no instruction file can answer (file counts, the first
  function in a specific file), which produced `list_files` + `read_file`
  and a correct answer.
- **Auto-approve was on** (a config default), so most of step 4 would
  have run unattended. Step 1 now checks the indicator and turns it off.
- **Step 4's containment check depended on the model's cooperation.**
  Asked to write outside the project, the model read the confinement out
  of the system prompt and *declined to try* — which looks like a pass
  while testing nothing. On an earlier run of the same request it did
  try, and the gate asked first. Two different outcomes for one step is
  not a check. Replaced with `!echo drill > ../outside-drill.txt`, which
  has no model discretion in it: Seatbelt answered `Operation not
  permitted`, exit status 1, and nothing was created.

What passed as written: banner (project, sandbox, session log, ten MCP
servers with scopes, instruction files), the MCP round trip through
`tor-exit-lookup` approved by selection, `!git status`, the piped
one-shot with a clean stdout, `gem-agent sessions`, and `--continue`
answering from restored history.

**Step 7** (one real task, gem-agent only) was a read-only review: *which
function enforces path confinement in `internal/tools/tools.go`, which
tools route through it, and does anything skip it?* — plus a follow-up on
symlinks. It named `resolvePath` with its `within` / `resolveExisting`
helpers, listed the four path-taking tools correctly, correctly
identified `shell_exec` as the one with no path (delegating to
`cmd.Dir` + the sandbox), quoted the symlink check **verbatim**, and
explained the not-yet-existing-file case correctly. Every claim checked
against the source. This is the step that says the tool is usable, not
merely running.

Two findings that were gem-agent's, not the runbook's — **both fixed the
same day** (the drill's purpose is to produce these, so leaving them open
would have wasted the run):

- **Typing while a turn was running was discarded with no feedback.** The
  TUI accepted only Ctrl+C and shift+tab during a run. Now the box stays
  live and Enter queues the message
  ([ADR-0007](../adr/0007-input-during-a-turn.md)).
- A session whose only input was a `!` command previewed in
  `gem-agent sessions` as `I ran this shell command myself:` — accurate
  (it is a user-role message) but it read like a bug. It now shows the
  command, and a typed message always wins the preview.

## Why in this order

Launch and auth first: a failure there invalidates every later step, so
finding it in minute two saves the other eighteen. Read-only before
destructive, so the first write happens against a model you have already
seen answer correctly. Resume after the interactive steps, because it
needs a session to resume; the docs audit rides the resumed session; and
the real task comes last because it gets whatever time remains.
