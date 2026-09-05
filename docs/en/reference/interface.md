# The interactive interface

The TUI, the plain REPL, one-shot mode, keys, slash commands, and
completion. What each surface shows and why it behaves the way it does.

## TUI

Bubble Tea in inline mode — native scrollback and copy/paste keep
working. Streaming output with a spinner/status line, an input box with
↑↓ history and multi-line editing, styled tool events, an approval
dialog, and glamour-rendered Markdown responses (wrapped at your
terminal's width, so copied lines are not broken by an artificial cap).

The input box and its status line pin to the window bottom (like Claude
Code), while the conversation scrolls above with native terminal
scrollback intact (ADR-0003; the screen is cleared once at startup). The
status line shows `⚡auto` when auto-approve is on, the model, current
context occupancy against the model's window (auto-detected from model
metadata, or `[model].context_window`), cumulative token consumption,
the live cache hit share (`cache NN%`), and the project directory.

**Mermaid fences render in place** (ADR-0042, ADR-0063): a ```` ```mermaid ````
fence in a reply is drawn as Unicode box art where it stands, at
display time. The transcript keeps the model's source
verbatim, and the model is told nothing about diagrams — no tool, no
format preference, no prohibition; rendering is a view-layer concern
like everything else the Markdown renderer does. The renderer draws
flowchart/graph (any direction, subgraphs; node shapes are drawn as
boxes), sequenceDiagram with ASCII labels, and erDiagram. There is no
width or height gate: art wider than the terminal wraps there, taller
art scrolls — overflow loses no information, and readability is the
operator's call. What IS guarded is wrongness: every label in the
source must appear in the art and every edge must draw exactly one
arrowhead, or the source is shown instead, with a one-line note saying
why. Diagram types the renderer does not know (gantt, classDiagram, …)
pass through as source, note-free. Files the model writes are
untouched.

A positional argument is the first interactive turn (ADR-0064):
`gem-agent "run the tests"` submits it once the banner has printed —
through the exact typed path, so `!` shell escape, slash commands,
`/skill` expansion and `@` mentions all work, and it lands in the
input history — then the session is ordinary interactive gem-agent.
It composes with `--continue`/`--resume`/`--auto` unchanged; combining
it with `-p` is an error, since the two select different session
shapes. In the piped fallback the argument runs before the first
stdin line; piped stdin itself is never prompt text (ADR-0055).

Piped/scripted use falls back to a plain line REPL automatically; the
plain REPL answers the same slash commands, `/mcp reload`,
`/skills reload`, `/auto`, `/clear` and `/compact` included — only
`/settings` is read-only there, since editing needs the panel. One-shot mode
(`-p "<prompt>"`) runs a single turn, answers on stdout, and denies
mutating tools (pipe-friendly; a tool set to `"never"` in the approval
policy never asks, so it still runs — see
[approval](approval.md)).

## Watching a turn run (ADR-0033)

While a turn runs, the status line is live, not a static spinner:

    ⠸ thinking… 1m07s · 34 chunks · last 0s  (Ctrl+C interrupts)

— elapsed time, chunks received from the stream, and seconds since the
last one. Three states that used to look identical are now
distinguishable: a **thinking model** keeps receiving chunks (thought
and metadata chunks count — liveness, not visibility); a **stalled
connection** turns the line into a warning after 90s of silence,
naming Ctrl+C; a **backoff retry** after a transient error (429/5xx)
shows `retry 2/3 (429) — waiting 4s` instead of silence. There is no
automatic timeout: long thinking is legitimate, and the operator
decides.

The warning waits 90 seconds, not 20 (ADR-0056): a long silence in the
middle of a turn is usually the model writing a file, not a broken
connection, and a warning that fires on healthy work is one the
operator learns to ignore. The silence age (`last 40s`) is on the line
from the first second either way.

Ctrl+C cancels the running turn — and the whole process GROUP of a
shell command dies with it, so a skill's python (or anything else the
command spawned) cannot keep the call hanging by holding the output
pipe (ADR-0034). The file walks stop within one syscall and return
what they found, labelled partial; any other tool call that has not
returned one second after the cancel is abandoned by the agent — the
turn ends, the call may finish in the background, and the exit
receipt counts such calls while they run (ADR-0065). A tool that is
waiting on you (`ask_user`) is left alone: you decide when it
returns. If a turn still ignores cancellation, a second Ctrl+C warns
and a third quits gem-agent — the transcript is written per event, so
everything up to the wedged call is already on disk. The plain REPL
and `-p` climb the same ladder; that forced exit returns status 130,
while a plain interrupted `-p` run exits 1 as before.

With `[tui].show_thoughts = true` (the default) the model's thought
summaries also stream into the live area in the dim style, replaced as
the real answer starts — the model narrates its own progress. Thoughts
are display-only: never written to the transcript, never replayed.
`false` restores the quiet spinner; the heartbeat and retry visibility
stay either way.

**The round limit is an intervention, not a guillotine** (ADR-0040).
At `[agent].max_turns` a progress review runs and a dialog asks
whether to continue — the verdict shown as evidence, answered like any
ask dialog (digits, Esc). In auto-approve mode a confident
"progressing" continues by itself with a visible notice; a suspected
loop (three identical calls) raises the same dialog immediately. The
hard cap is 3× `max_turns`, and when a turn does stop, progress is
saved — saying "continue" resumes where it left off.

## Keys

Enter sends, ↑/↓ navigate input history, Ctrl+C interrupts a running
turn — at the prompt it clears the input box, or quits when the box is
already empty — and Ctrl+D quits. All of this is also in
`/help`.

**You can keep typing while a turn runs.** The input box stays visible
and live; Enter queues the message instead of sending it — the agent
loop owns the conversation until it returns — and it goes out as the
next turn once that one finishes cleanly. If the turn errors or you
interrupt it, the queued text comes back to the input box **unsent**,
because a message written against a turn that then failed is rarely
still the message you want (ADR-0007). `!` and `/` commands cannot be
queued — interrupt first.

**Multi-line input**:

| Route | Availability |
|---|---|
| `Ctrl+J` | always — the reliable one |
| trailing `\` then `Enter` | always (the shell convention) |
| `Option`/`Alt` + `Enter` | only if your terminal sends Meta for Option |

Modifier+Enter combinations are a terminal limitation, not an
application choice: unless the terminal is configured to send an escape
prefix, `Option+Enter` and `Shift+Enter` arrive as an ordinary carriage
return, indistinguishable from submit — so they send the message. To
enable `Option+Enter`:

- **Terminal.app** — Settings → Profiles → Keyboard → *Use Option as Meta key*
- **iTerm2** — Settings → Profiles → Keys → Left Option key → *Esc+*

Pasting multi-line text always works regardless: the whole paste lands
in the input box as one message, never one LLM call per line.

## Slash commands

| Command | Does |
|---|---|
| `/help` | commands, file references, shell escape, keys |
| `/tools` | available tools with each one's LIVE approval gate |
| `/mcp` | connected MCP servers with their scope; `/mcp reload` reconnects them (ADR-0039) |
| `/auto` | toggle auto-approve (shift+tab does the same, and works mid-run) |
| `/compact` | summarise the older half of the conversation now |
| `/settings` | every setting with its provenance; edit policy + toggles |
| `/riskbook` | the risk rules the auto-mode reviewer reads; `learn` drafts them from your answers (ADR-0050) |
| `/usage` | the session's token statement (ADR-0019) |
| `/memory` | persisted memories, global + this project |
| `/skills` | installed skills; `/skills reload` re-discovers them (ADR-0039) |
| `/skill <name> [args]` | invoke a skill directly, no extra model round |
| `/version` | this build's version and platform, one line |
| `/clear` | reset the conversation history |
| `/quit` | exit (`/exit` is an alias; Ctrl+D also works) |

Every interactive exit — `/quit`, Ctrl+C, Ctrl+D — ends with a
two-line summary: the session id with the resume command, and the
session's round/token totals. The last thing in the scrollback answers
"how do I get back to this?". A third line names abandoned tool calls
still running, when there are any; and with audit logging enabled the
exit says `sending audit events… (up to 3s)` before its bounded flush
instead of pausing silently (ADR-0065).

`/usage` breaks the session down: main-loop rounds with the cache hit
rate, risk-check and compaction side-calls, and per-tool lines
(summaries, web, the file-search agent) naming the model that spent
the tokens. A web line adds `tool results N` — the page or search
results the provider fed back to the model, billed as input and
usually most of the call.

## Completion

**Tab completes three things**, all with the same behaviour — a unique
match completes in place, multiple matches advance to the common prefix,
and when Tab cannot advance the candidates are listed:

- `@<path>` project file references (see [attachments](attachments.md))
- slash commands
- skill names after `/skill `

## Shell escape

`!<command>` runs a shell command directly in the operator lane of
`shell_exec` (ADR-0073 — the full ADR-0001 profile, same timeout and
output cap) without an approval prompt, since you typed it yourself. It
re-pins nothing: if the command changed a pinned file (`AGENTS.md`,
`.mcp.json`, a project skill), the output ends with a note and the next
interactive start asks (ADR-0074). The command and its output are added to the model's
context, so `!git status` followed by "fix that" just works.

## The approval dialog

Mutating tool calls show a dialog before running. Answer it either by
selection — ←→ or Tab to move, Enter to confirm — or with the
`y` / `n` / `N` / `a` / `p` shortcuts; Esc denies. The selection route
exists because those letters cannot be typed with a Japanese IME
switched on.

- `y` — allow this call
- `n` — deny (fails closed; the model is told to ask you, not retry)
- `N` — deny with a typed reason (ADR-0060): a one-line field opens,
  and the reason rides back to the model inside the denial — guidance
  at the moment of decision instead of a model round of "how should I
  proceed?". Enter on an empty field is a plain deny; Esc backs out to
  the options
- `a` — allow this tool for the rest of the session. Never covers the
  dangerous cases: Block-tier calls and always-policy tools keep asking
  (ADR-0021)
- `p` — allow, and never ask about this tool again: writes the policy
  file and says so. Deliberately separate from `a` — one is a session
  convenience, the other edits a file on disk

The highlight starts on *allow*, except for a call auto-approve
escalated, where it starts on *deny* so a reflexive Enter cannot approve
it. Long call details are budgeted to the terminal height with the
hidden-line count disclosed — you are never asked to approve something
you have not seen. A `write_file` that overwrites an existing file
says what it replaces — `replaces existing file: 42KB → 8KB` — so a
shrinking rewrite is visible at the moment of consent (ADR-0051). A dialog arriving after you pressed Ctrl+C is denied
automatically: the turn is already dead.

## The ask dialog (ADR-0036)

When the model calls `ask_user` mid-turn, a question dialog opens on
the approval dialog's grammar: ←→/Tab move, Enter confirms, **digits
1–9 select and confirm in one press**, Esc declines. Declining is
information, not an error — the model is told you chose not to pick,
and to ask in prose or proceed with stated judgment. The tool is
read-only and never approval-gated (a gate on a question would be a
dialog to permit a dialog), and a dialog arriving after Ctrl+C is
declined automatically, exactly like an approval. Long questions wrap
to the box and a too-tall one discloses its hidden lines — you never
answer what you could not read. In the plain REPL
the question becomes a numbered stderr prompt; in one-shot `-p` mode
the tool refuses informatively — there is nobody to ask, and a
pipeline must not hang.

## `/settings`

`/settings` opens a panel showing every setting **with where its value
came from** — `flag`, `env:VAR`, `config.toml`, `policy.toml`, the
project file (or the project file marked `ignored: untrusted`),
`pattern` for a tool matched by a wildcard policy rule, `session`
(changed in this panel), or `default`. Four
precedence layers with nothing on screen is a design that assumes you
remember them.

↑↓ moves, ←→/Enter changes a value, `s` switches whether a policy change
is saved globally or for this project only, Esc closes. Settings that
cannot change mid-session (the model, the GCP project, the sandbox
switch, theme, language) are shown read-only and say why.

Persisted changes go to `~/.config/gem-agent/policy.toml`, a file
gem-agent owns and rewrites. Your hand-written `config.toml` is never
touched, so its comments survive; entries in `policy.toml` win a
collision, and the panel shows which file decided each one. In a pipe or
the plain REPL, `/settings` prints the same rows read-only. See
ADR-0009.

## Appearance and language

TUI accent colors use the ANSI-16 palette (they follow your terminal
theme); secondary text uses a mid-gray chosen for the detected
background. `[tui].theme = "auto"` detects dark/light at startup; set
`dark`/`light` if detection picks wrong, or `plain` to disable all
styling (errors keep their `✗` marker, so nothing depends on color
alone).

`[tui].language` selects the language of the interactive chrome —
`/help`, hints, prompts, and the approval dialog (ADR-0029). `auto`
follows `LC_ALL`/`LC_MESSAGES`/`LANG` (a `ja` prefix means Japanese,
anything else English); `ja`/`en` force it. Resolved once at startup.
Log-shaped lines (banner labels, `warning:`), `--help`, and
model-facing text stay English by design.
