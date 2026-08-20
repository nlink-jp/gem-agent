# gem-agent

Interactive CLI agent backed by Vertex AI Gemini 3.x — a continuity tool for
development work when Claude Code is unavailable.

> **Released.** Install with `brew install nlink-jp/tap/gem-agent`
> or from the [releases page](https://github.com/nlink-jp/gem-agent/releases)
> (Developer ID signed, Apple-notarized, macOS arm64) — the releases page
> is the authority on the current version, so this line does not repeat it.
> It is an experimental (lab-series) tool: interfaces may change between
> releases.
> See the [RFP](docs/en/gem-agent-rfp.md) for the full specification.

[日本語版 README](README.ja.md)

## Why

When Claude Code is unavailable (provider-side outage, contractual or network
constraints), development work should not stop. gem-agent is a deliberately
minimal fallback agent on an independent backend (Vertex AI), designed to be
**drop-in**: it reads an existing project's `AGENTS.md` / `CLAUDE.md` /
`.mcp.json` as-is, so switching over requires no per-project reconfiguration.

## Features (implemented)

- **Interactive TUI** (Bubble Tea, inline mode — native scrollback and
  copy/paste keep working): streaming output with a spinner/status line,
  input box with ↑↓ history and multi-line editing (Ctrl+J), styled tool
  events, an approval dialog, and glamour-rendered Markdown responses.
  Piped/scripted use falls back to a plain line REPL automatically
- Gemini agent loop with Gemini 3 thought-signature capture/replay
  (verified live)
- Built-in tools: `list_files` / `list_tree` / `search_files` / `read_file` /
  `summarize_file` / `view_image` / `read_document` / `write_file` / `edit_file` /
  `shell_exec`, all confined to the project directory (symlink escapes
  included). `list_tree` shows the project as a tree; `search_files` is a
  fast dependency-free grep (regex or literal, binaries and `.git`
  skipped, caps reported) — so orientation costs one call, not one round
  per directory
- **`file_info`** (ADR-0016): what a file *is* without reading it into
  context — content-judged type (`file`-command style: Mach-O/ELF/PE,
  archives, scripts; the extension is shown but never trusted), size,
  mode, modified and **created** times (macOS-only by design, so the
  Darwin field is free), and the MD5/SHA1/SHA256 trio that hash-lookup
  tools consume — the IR opening moves in one read-only call. Batch via
  `paths`; symlinks reported, never silently followed
- **Editing without the round-trips** (ADR-0015): `edit_file` keeps its
  exact-unique-string contract — line numbers write to the wrong place
  *silently* when stale; a string anchor fails loudly or works — and
  gains what makes it cheap: an `edits` array applied in order and
  **atomically** (any failure writes nothing and names the failing
  edit), `replace_all` for renames, **diagnosed misses** (a whitespace
  near-match is quoted with the file's real text and line, so the fix is
  a copy-paste, not a re-read), and **evidence on success** — the
  changed region with its line span, so verification needs no read-back.
  The intended loop: windowed read → one batched edit → verify from the
  result
- **Context economy** (ADR-0014): `read_file` takes `start_line`/`end_line`
  to read a window instead of the whole file (annotated, never
  masquerading as the full text — and no line-number prefixes, which
  would poison `edit_file`'s exact-match contract). `summarize_file`
  returns a short summary instead of the bytes, produced by
  `[model].summary` — a lightweight model sharing the main model's
  client — or the main model when unset. File content reaches the
  summariser nonce-wrapped with no tools, exactly like compaction; a
  blocked summary is a reported error, never a silent empty one
- Per-call approval gates (MITL) for mutating tools, with a session-scoped
  allowlist (`y` = once, `a` = always this session; deny fails closed).
  `a` never covers the dangerous cases: Block-tier calls (sudo,
  recursive deletes, credential paths, …) and tools pinned to "always"
  by policy keep asking regardless (ADR-0021)
- **Auto-approve mode** (opt-in, shift+tab or `/auto`): each mutating call
  passes a two-tier review — a rule-based classifier first, then a model
  risk evaluation for anything uncertain. Safe calls run unattended;
  destructive, out-of-project, credential-touching, or uncertain ones
  still ask. See [ADR-0004](docs/en/adr/0004-auto-approve.md)
- `shell_exec` wrapped in macOS sandbox-exec — file writes restricted to the
  project directory + scratch dirs (enforcement covered by a real Seatbelt test)
- Paste-safe input: a multi-line paste becomes one input, never one LLM call per line
- **Session resume**: `--continue` picks up this project's most recent
  session, `--resume <id>` a specific one, `gem-agent sessions` lists
  them. The JSONL transcript under `~/.local/state/gem-agent/sessions/`
  is both the log and the resume source, recorded in full fidelity
  (Gemini reasoning tokens included, which the API requires on replay).
  See [ADR-0005](docs/en/adr/0005-session-resume.md)
- **Context compaction**: when the conversation approaches the model's
  window it is summarised instead of failing the turn — the older half
  becomes one summary, the recent half stays verbatim. Automatic at 80%
  (`[agent].compact_at_pct`), or `/compact` at any time. See
  [ADR-0006](docs/en/adr/0006-context-compaction.md)
- **Document reading** (ADR-0026): PDFs go to the model as-is — it
  reads layout, tables, and scans natively (measured: accepted both as
  an operator attachment and inside a tool response, with clean
  continuation) — and Word/Excel/PowerPoint (.docx/.xlsx/.pptx) are
  extracted to text locally with the standard library: paragraphs,
  tab-separated sheet rows, numbered slides. Two paths per ADR-0012's
  split: `@report.pdf` / `@data.xlsx` when you choose the file
  (absolute and ~ paths allowed — you typed them), the `read_document`
  tool when the model does (project-confined). Legacy .doc/.xls/.ppt
  are out of scope, stated loudly
- **Audio and video input** (ADR-0027): attach recordings and clips
  with `@memo.m4a` / `@clip.mp4` (in-project, absolute, or ~ paths) —
  the model transcribes and understands them natively. With
  `[gcp] bucket` set, media ALWAYS routes through your GCS bucket as a
  `gs://` URI (content-addressed, deduplicated, never deleted by
  gem-agent — set a bucket lifecycle rule): inline bytes would be
  re-sent with every round's history replay. Without a bucket, media
  attaches inline up to 15MB and larger files are refused naming both
  remedies. Verified live: an inline voice memo transcribed exactly,
  and a bucket-routed video answered from both its audio track and its
  frames
- **Agent memory**: short facts persisted across sessions with
  `save_memory` / `delete_memory` — global scope (about you or this
  machine, recalled everywhere) and project scope (this project only).
  Plain markdown under `~/.local/state/gem-agent/memory/`, never inside
  the repository; loaded into the system prompt at session start.
  Writes are approval-gated. See [Memory](#memory) and
  [ADR-0020](docs/en/adr/0020-agent-memory.md)
- Slash commands: `/help` `/tools` `/mcp` `/usage` `/memory` `/compact`
  `/clear` `/quit` — `/usage` is the session's token statement (ADR-0019):
  main-loop rounds with the cache hit rate, risk-check and compaction
  side-calls, and per-tool lines (summarize/web) naming the model that
  spent the tokens
- **Drop-in project compatibility**: the project's agent-instruction files
  are injected into the system prompt — `AGENTS.md`, `AGENT.md`,
  `CLAUDE.md`, `GEMINI.md`, searched up through ancestor directories the
  way other agents do, so workspace-wide rules apply to every repository
  beneath them — and its `.mcp.json` (Claude Code format, stdio servers)
  is connected as-is. Zero per-project setup
- MCP client: tools appear as `mcp__<server>__<tool>`, always approval-gated;
  timed-out calls kill the server child (MCP has no cancel) and it respawns lazily
- Tool output is isolated with nonce XML tags (nlk/guard; session-scoped
  in the main loop, per-call in side-calls — ADR-0018) — content
  returned by tools is framed as data, never instructions
- One-shot mode `-p "<prompt>"`: single turn, answer on stdout, mutating
  tools denied (pipe-friendly)
- Transient Vertex failures (429/5xx) retry with exponential backoff
- **Implicit context caching** (ADR-0018): the isolation tag is
  session-scoped, so the request prefix stays byte-identical across
  rounds and turns and Vertex's implicit caching prices the replayed
  history at the cached rate. Measured on an identical 4-round task:
  0% cached with the old per-call tag, **81–95% cached** with the
  session tag. The footer shows the live share (`cache NN%`). Sound
  because nlk/guard refuses content containing the tag name — a leaked
  tag can only get its carrier withheld, never escape the wrapper

Out of scope by design: RAG or vector memory, data analysis, GUI,
non-macOS platforms.

## Usage

```sh
cd /path/to/your/project
gem-agent                                  # interactive REPL
gem-agent -c                               # continue the last session here
gem-agent sessions                         # list resumable sessions
gem-agent -p "summarize this repository"   # one-shot, pipe-friendly
```

The current directory becomes the project: file tools cannot leave it, and
sandboxed shell commands cannot write outside it. Mutating tool calls show
an approval dialog before running. Answer it either by selection — ←→ or
Tab to move, Enter to confirm — or with the `y` / `n` / `a` shortcuts
(allow once / deny / always this session); Esc denies. The selection route
exists because those letters cannot be typed with a Japanese IME switched
on. The highlight starts on *allow*, except for a call auto-approve
escalated, where it starts on *deny* so a reflexive Enter cannot approve
it. `--no-sandbox` disables the Seatbelt wrapper (debugging only),
`--model` overrides the configured model.

TUI keys (also listed in `/help`): Enter sends, ↑/↓ navigate input
history, Ctrl+C interrupts a running turn (or clears the input), Ctrl+D
quits.

**You can keep typing while a turn runs.** Enter queues the message
instead of sending it — the agent loop owns the conversation until it
returns — and it goes out as the next turn once that one finishes
cleanly. If the turn errors or you interrupt it, the queued text comes
back to the input box **unsent**, because a message written against a
turn that then failed is rarely still the message you want
([ADR-0007](docs/en/adr/0007-input-during-a-turn.md)).

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

### Auto-approve mode

Off by default. shift+tab (or `/auto`) toggles it; the status line shows
`⚡auto` while on. **shift+tab also works while a turn is running**, so a
long agent loop that started in manual mode can be switched over without
waiting for it to finish — the change applies from the next tool call (a
call already waiting at the dialog still needs its answer). Each mutating tool call then goes through:

1. **Rule tier** (no model call): *safe* → runs; *blocked* → always asks
   (`rm -rf`, `sudo`, `git push`, download-piped-to-shell, disk writes,
   credential paths, anything outside the project…); *uncertain* → tier 2.
2. **Model tier**: a separate evaluation round judges the proposed call
   (delivered to it as nonce-wrapped untrusted data, with no tools
   available). It must both approve *and* be confident, or the call asks.

Anything that fails — model error, malformed verdict, unknown tool —
asks. The blocked tier is a hard floor the model cannot override, and the
sandbox applies in every mode.

Both outcomes are explained: auto-approved calls print their reason, and
an escalated call's approval dialog carries a `⚠` line naming the tier
that objected and why — `blocked by rule (always asks): …` for the
deterministic floor, `escalated by risk review: …` for a model judgment.

`@<path>` attaches a project file or directory to your message —
`@src/main.go これ直して` sends the file with the instruction, and
`@docs/` sends a directory listing. **Tab completes** the path (common
prefix first, then a candidate list). References resolve inside the
project only, symlinks included; anything that cannot be attached is
reported rather than silently dropped, and attached content reaches the
model isolated as untrusted data, exactly like tool output.

Tab also completes `/commands` (and skill names after `/skill `) the
same way: unique match in place, common prefix otherwise, candidates
listed when Tab cannot advance.

`!<command>` runs a shell command directly — sandboxed like `shell_exec`
(same timeout and output cap) but without an approval prompt, since you
typed it yourself. The command and its output are added to the model's
context, so `!git status` followed by "fix that" just works.

The input box and its status line pin to the window bottom (like Claude
Code), while the conversation scrolls above with native terminal
scrollback intact (ADR-0003; the screen is cleared once at startup). The
status line shows the model, current context occupancy against the
model's window (auto-detected from model metadata, or
`[model].context_window`), cumulative token consumption, and the project
directory.

### Resuming a session

```sh
gem-agent sessions        # ids, age, model, and the opening question
gem-agent -c              # the most recent session in this directory
gem-agent --resume 20260819-150102
```

A resumed session continues its own transcript — one file is one
conversation however many processes it took — and comes back exactly as
it was, tool results included. Two refusals are deliberate: a session
resumes only in the directory it was recorded in, and only under the
model that produced it (the replayed reasoning tokens are model-bound).
Each message names what to do instead. A session that was compacted
resumes compacted, rather than re-inflating to the size it was shrunk
from.

The transcript therefore holds the full text of every file the agent
read. It lives under
`~/.local/state/gem-agent/sessions/projects/<escaped path>/`, mode
`0600` — one subdirectory per project, the same convention as memory
(ADR-0022), so a listing reads one directory and a cleanup in one
project's directory cannot touch another's. Transcripts recorded by
older versions in the flat `sessions/` directory keep working in place:
they are listed and resumed where they are, never moved. The
`GEMAGENT_STATE_DIR` environment variable relocates the whole state
root (sessions and memory) — its purpose is isolation for tests and
drills.

### Compacting the conversation

At `[agent].compact_at_pct` of the model's window (80% by default), the
older part of the conversation is replaced by a summary of it and the
recent part is kept verbatim; `/compact` does the same on demand. The
notice says how many messages were summarised, because detail from that
half is second-hand afterwards and a model that has forgotten something
must not look like one that never knew it.

If the summarisation call fails — an error, a content filter — the
history is left exactly as it was and the turn continues on a full
context. `auto_compact = false` turns the automatic path off; `/compact`
still works.

### `/settings`

`/settings` opens a panel showing every setting **with where its value
came from** — `flag`, `env:VAR`, `config.toml`, `policy.toml`, the
project file, or `default`. Four precedence layers with nothing on screen
is a design that assumes you remember them.

↑↓ moves, ←→/Enter changes a value, `s` switches whether a policy change
is saved globally or for this project only, Esc closes. Settings that
cannot change mid-session (the model, the GCP project, the sandbox
switch) are shown read-only and say why.

Persisted changes go to `~/.config/gem-agent/policy.toml`, a file
gem-agent owns and rewrites. Your hand-written `config.toml` is never
touched, so its comments survive; entries in `policy.toml` win a
collision, and the panel shows which file decided each one. In a pipe or
the plain REPL, `/settings` prints the same rows read-only. See
[ADR-0009](docs/en/adr/0009-settings-panel.md).

The approval dialog gained a fourth answer, **今後聞かない (`p`)** — allow
this call and never ask about that tool again. It writes the policy and
says so; it is deliberately separate from `a`, which lasts one session.

### Per-tool approval policy

Every MCP tool asks for approval on every call, because gem-agent cannot
know what a server's tool does. You do — so you can say so:

```toml
# ~/.config/gem-agent/config.toml
[approval.tools]
"mcp__tor-exit-lookup__*" = "never"   # a read-only lookup server
"shell_exec"              = "always"  # even in auto-approve mode
```

`"never"` skips the gate in every mode, `"always"` gates in every mode
(auto-approve cannot lift it, and neither can a session `a` answer), and
an unset tool keeps today's behaviour. A trailing `*` matches a whole
MCP server. Resolution is **scope first, then specificity** (ADR-0021):
a project rule beats any global rule for the tools it matches; within
one scope, exact names win over wildcards. A bare `"*"` is rejected —
switching off every gate at once should not be reachable by a
one-character entry.

**`"never"` is not "run anything."** For a tool whose effect varies per
call — `shell_exec` — the rule tier's blocked patterns (`rm -rf`,
`sudo`, `curl … | sh`, credential paths, writes outside the project)
still ask.

A project can carry its own policy in `<project>/.gem-agent.toml` (see
`gem-agent.example.project.toml`), and **direction matters**:

| From a project file | Honoured |
|---|---|
| `"always"` — more approvals | always |
| `"never"` — fewer approvals | only if the project is listed in `[approval].trusted_projects` in *your* config |

A checked-out repository is not necessarily something you wrote, and
cloning one must not be able to switch the gate off. Ignored entries are
named at startup, with the line to add if you do want them. See
[ADR-0008](docs/en/adr/0008-per-tool-approval-policy.md).

One consequence worth knowing: `-p` one-shot mode denies mutating tools
because nothing can answer a prompt there, but a tool set to `"never"`
was never going to ask — so it runs. A read-only MCP lookup with a
`"never"` policy is usable in a pipeline.

## Web access

Two egress tools (ADR-0017), both **approval-gated by default** — the
query or URL itself is a channel where injected instructions could
exfiltrate what the model can read — and both relaxable per tool with the
[approval policy](#per-tool-approval-policy) (`"web_search" = "never"`),
which also makes them usable in `-p` one-shot mode:

- **`web_search(query)`** — Grounding with Google Search on the main
  model: a grounded answer **with its sources** (title, domain, URI), so
  claims can be checked rather than believed. First-party and ToS-clean —
  the reason plain search APIs were not used.
- **`web_fetch(url, focus?)`** — the URL Context tool on the lightweight
  digest model: the page is fetched by the provider's infrastructure and
  read in the digest model's own context; only an **organized
  extraction** (key points with exact names/numbers/dates, caveats)
  enters this conversation. Server-side fetching kills the SSRF class by
  construction — localhost and your LAN are structurally unreachable —
  at the mirror cost that intranet/authenticated pages cannot be fetched
  (failures are reported with their retrieval status)

Web content is untrusted: digests return as ordinarily nonce-wrapped
tool results, and the fetch prompt carries the defensive framing for the
layer that cannot be wrapped.

## Images

Screenshots are first-class input (ADR-0012). Three ways in, plus one
for the model:

| Route | Example |
|---|---|
| Project file | `@docs/mock.png これを再現して` |
| Anywhere (images only) | `@~/Desktop/スクリーンショット.png` |
| Clipboard | Cmd+Ctrl+Shift+4, then `@clipboard ここがおかしい` |
| Model-initiated | the `view_image` tool (project-confined, like `read_file`) |

The out-of-project route exists for images only, and it is safe because
`@` is parsed from what **you** type — never from model output or tool
results. `view_image` is the other direction: MCP servers produce image
files (urlscan's `get_screenshot`, pcap extraction), and the model views
them itself mid-loop; that route stays confined to the project. MIME is
sniffed from bytes (a renamed binary is refused), images are capped at
8MB each and 4 per message, and a too-large image is refused whole — a
truncated PNG is a broken file, not a smaller picture.

Images cannot be nonce-wrapped, so the isolation stance is stated as
framing instead — text visible inside an image is data, never
instructions — which is weaker than tag isolation, and the docs say so
rather than pretending otherwise. Transcripts store the bytes, so a
resumed session keeps the screenshots it was looking at.

## Skills

gem-agent reads **Claude Code's skill format, as-is** — from its own
locations, arranged exactly like MCP: format compatibility is drop-in,
location sharing would be coupling
([ADR-0011](docs/en/adr/0011-skill-scope-separation.md)):

| Scope | Path | |
|---|---|---|
| Global | `~/.config/gem-agent/skills/<name>/SKILL.md` | gem-agent's own |
| Project | `<project>/.claude/skills/<name>/SKILL.md` | shared with Claude Code |

`~/.claude/` is never read — that is Claude Code's live environment, and
inheriting it implicitly would change the fallback's behaviour whenever
the primary's environment changes. **Sharing is a symlink you make**,
per skill or wholesale (discovery follows links):

```sh
ln -s ~/.claude/skills/meeting-notes ~/.config/gem-agent/skills/meeting-notes
ln -s ~/.claude/skills ~/.config/gem-agent/skills   # share everything
```

Frontmatter is read minimally (`name`, `description`, `argument-hint`);
`allowed-tools` is ignored — gem-agent has its own approval model, and
honouring a foreign permission grant would bypass it. The project wins a
name collision, announced like an MCP one.

Skills are progressive disclosure: each contributes one description line
to the system prompt, and the body loads only when used —

- **the model** calls `load_skill(name)` when the task matches a
  description, and `load_skill(name, file)` for the skill's own
  `references/` and `scripts/` files;
- **you** type `/skill <name> [args]` (the body is injected directly, no
  extra model round). `/skills` lists what was found.

Skill content is treated as *instructions*, not wrapped as untrusted
data — it is a file you installed, the same trust tier as `AGENTS.md`.
That exemption is bounded: `load_skill` can only read inside a discovered
skill's directory, symlinks resolved and checked. Skill `scripts/` run
through `shell_exec` stay under the sandbox and the approval gate like
everything else. See [ADR-0010](docs/en/adr/0010-skills.md).

## Memory

The agent persists short facts across sessions (ADR-0020): decisions,
preferences, environment quirks — things worth knowing next time that no
project file states.

| Scope | Recalled in | Stored at |
|---|---|---|
| `global` | every project | `~/.local/state/gem-agent/memory/global/<name>.md` |
| `project` | that project only | `~/.local/state/gem-agent/memory/projects/<escaped path>/<name>.md` |

- One memory = one small markdown file; saving an existing name updates
  it. Everything is loaded into the system prompt at session start
  (global first, then project) under a fixed budget, with any clipping
  reported rather than silent.
- **Writes are approval-gated** (`save_memory` / `delete_memory`): a
  persisted memory reappears in every later session's prompt, so memory
  is a persistence vector for injected instructions — the human reviews
  each write. The [per-tool policy](#per-tool-approval-policy) relaxes
  that per tool if you accept the trade.
- The injected section is framed as background knowledge the agent
  recorded — explicitly below the standing of your own instruction
  files, and possibly stale.
- Nothing is ever written into the repository, and `~/.claude` is never
  read. The files are plain markdown: audit, edit, or delete them by
  hand whenever you like.
- `/memory` lists what is stored right now; a new save takes effect from
  the next session.

See [ADR-0020](docs/en/adr/0020-agent-memory.md).

## Startup safety

Two gates run before anything loads (ADR-0023):

- **Broad roots ask first.** Launched in `/`, your home directory, or an
  ancestor of it, gem-agent explains that file tools and sandboxed
  shell writes would span that entire tree and asks before starting
  (default: no). Non-interactive runs (`-p`, pipes) are refused there
  outright.
- **A new project must be trusted once.** The first launch in a project
  that provides agent-facing files lists what it provides — instruction
  files (injected as *your* instructions), `.mcp.json` (each server
  entry starts a child process), `.claude/skills` — and asks whether to
  trust it (default: no). The answer persists per project in the
  machine-owned `policy.toml` (`trust = "granted" | "declined"`; delete
  the key to be asked again). Declining still starts the session: the
  project's own files are simply not loaded, and the banner says so.
  Ancestor instruction files and all global configuration are outside
  the gate — a clone cannot plant files in directories you own above
  it. Projects listed in `[approval].trusted_projects` are trusted
  without asking. Non-interactive runs in an undecided project run
  bare (nothing of the project's loaded, note on stderr, nothing
  recorded) so read-only `-p` pipelines over fresh clones keep working.

## Project instructions

gem-agent reads the instruction files a repository already carries, in
this order per directory:

| File | Convention |
|---|---|
| `AGENTS.md` | the cross-vendor standard |
| `AGENT.md` | its singular variant |
| `CLAUDE.md` | Claude Code |
| `GEMINI.md` | Gemini CLI |

They are collected from `~/.config/gem-agent/` (your own defaults for
every project), then from ancestor directories outermost-first, then
from the project itself — so workspace-wide rules apply to sibling
repositories and the nearest file is read last, as the most specific.
Files with identical content are injected once. The startup banner lists
what was loaded.

The ancestor walk stops at your home directory: an instruction file is
obeyed as instructions, so gem-agent will not pick one up from a shared
location like `/tmp` that you do not own.

## MCP servers

Servers are read from two scopes, both in Claude Code `.mcp.json` format
(stdio transport, `${VAR}` expansion) so entries move between them
verbatim:

| Scope | Path | Use for |
|---|---|---|
| Global | `~/.config/gem-agent/mcp.json` | servers you want in every project |
| Project | `<project>/.mcp.json` | servers specific to one repository |

Both are optional. They are merged, and on a name collision the project
entry wins. `/mcp` lists the connected servers with their scope.

```json
{
  "mcpServers": {
    "tor-exit": { "command": "tor-exit-lookup", "args": ["mcp"] }
  }
}
```

To add governance and an audit trail, route a server through
[mcp-guardian](https://github.com/nlink-jp/mcp-guardian) — it is itself a
stdio MCP server, so the opt-in is just a `.mcp.json` entry:

```json
{
  "mcpServers": {
    "guarded": { "command": "mcp-guardian", "args": ["--profile", "myserver"] }
  }
}
```

## Requirements

- macOS (Apple Silicon)
- Google Cloud project with Vertex AI enabled
- Application Default Credentials (`gcloud auth application-default login`)
  with `roles/aiplatform.user`

## Build

```sh
make build    # outputs dist/gem-agent
make test
```

## Configuration

Start from the template in this repository:

```sh
mkdir -p ~/.config/gem-agent
cp config.example.toml ~/.config/gem-agent/config.toml
cp mcp.example.json    ~/.config/gem-agent/mcp.json   # optional, MCP servers
```

`~/.config/gem-agent/config.toml`:

```toml
[gcp]
project  = "your-project-id"
location = "global"        # default; Gemini 3 models are global-endpoint-only

[model]
name = "<gemini model id>"
# context_window = 1048576  # optional; exact window for the footer and compaction
# thinking = "high"         # optional; Gemini 3 thinking level: minimal|low|medium|high
#                           # (unset = model default; supported levels are model-dependent;
#                           #  summarize model unaffected — ADR-0025)

[sandbox]
enabled = true             # default

[agent]
max_turns = 50             # default
shell_timeout_sec = 120    # default
auto_approve = false       # default; start sessions in auto-approve mode
auto_compact = true        # default; summarise older history near the window
compact_at_pct = 80        # default; share of the window that triggers it

[tui]
theme = "auto"             # auto | dark | light | plain
language = "auto"          # auto | ja | en
```

TUI accent colors use the ANSI-16 palette (they follow your terminal
theme); secondary text (footer, hints) uses a mid-gray chosen for the
detected background, keeping a real luminance gap on any theme.
`theme = "auto"` detects dark/light at startup; set `dark`/`light` if
detection picks wrong, or `plain` to disable all styling (errors keep
their `✗` marker, so nothing depends on color alone).

`language` selects the language of the interactive chrome — `/help`,
hints, prompts, and the approval dialog. `auto` follows
`LC_ALL`/`LC_MESSAGES`/`LANG` (a `ja` prefix means Japanese, anything
else English); `ja`/`en` force it. Resolved once at startup. Log-shaped
lines (banner labels, `warning:`), `--help`, and model-facing text stay
English by design.

Precedence: flags (`--model`) > `GEMAGENT_*` > `GOOGLE_CLOUD_*` > config file
> defaults. Unknown keys in the file are rejected (strict decode).

### Content filters

Vertex applies content filters to both the request and the response. When
one fires, gem-agent says so explicitly — naming the reason the API
reported — rather than showing an empty answer, and **retries once**,
because the filter is not deterministic: what gets rated is the text that
attempt happened to generate, so the same request usually goes through on
the next try. Measured on ordinary security material (an incident-response
runbook in context): identical requests were blocked on some attempts and
passed on others, at every `[model].safety` setting.

If the retry is blocked too, the error says so; narrowing the request, or
`/clear` to drop large documents from the context, is what helps.

`[model].safety` adjusts the four configurable harm categories — useful
for `SAFETY`-category blocks, but note that `PROHIBITED_CONTENT` comes
from a filter these settings do not cover:

| Value | Effect |
|---|---|
| `default` | the provider's own thresholds |
| `relaxed` | block only high-confidence hits |
| `off` | do not block on those categories |

Loosening it is a deliberate choice, so the default is left alone.

Note: as of 2026-08, the Gemini 3 family (verified with gemini-3.7-flash and
gemini-3-flash-preview) is served only from the global endpoint — regional
locations return 404. Gemini 2.5 models work from regional endpoints such as
`us-central1`; set `location` accordingly if you use one.

## Documentation

[**docs/en/INDEX.md**](docs/en/INDEX.md) is the entry point
([日本語](docs/ja/INDEX.ja.md)) — one catalog rather than a list
maintained in several places. It covers:

- the [RFP](docs/en/gem-agent-rfp.md), which is the canonical spec
- [architecture](docs/en/reference/architecture.md) — current behaviour,
  the two confinement boundaries, failure behaviour in one table
- the [monthly drill](docs/en/reference/drill.md) — a backup that is not
  exercised is not a backup
- [promotion criteria](docs/en/reference/promotion.md) for leaving
  lab-series
- six ADRs, from the sandbox mechanism to context compaction

## License

MIT
