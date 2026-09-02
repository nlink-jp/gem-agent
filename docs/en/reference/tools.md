# Built-in tools

Every tool the model can call, what it returns, and the design decision
behind it. All file tools are confined to the project directory,
symlink escapes included. `/tools` lists them with each one's live
approval gate.

Every approval-gated tool — the built-in mutating ones below and every
MCP tool — takes one extra argument gem-agent adds to its schema:
`gem_agent_purpose`, the model's one-sentence statement of why the
call is needed, shown to the operator on the approval prompt (ADR-0047). It is
removed again before the tool runs, so no MCP server receives an
argument its own schema never declared, and it is evidence for nothing
— see [approval](approval.md).

The name is namespaced so that a server's own argument names cannot
collide with it. If one ever does, gem-agent adds nothing to that tool, passes the argument through
untouched, and shows it among the arguments on the prompt like any
other — the approval prompt never drops an argument to make room for
an annotation.

## Orientation: `list_files`, `list_tree`, `search_files`

`list_tree` shows the project as a tree; `search_files` is a fast
dependency-free grep (regex or literal, binaries and `.git` skipped,
caps reported) — so orientation costs one call, not one round per
directory.

Both walks are ignore-aware (ADR-0052): well-known dependency and
build directories (`node_modules`, `vendor`, `dist`, `target`, …) and
`.gitignore`'d entries — full gitignore semantics, implemented
in-repo and cross-checked against `git check-ignore` — are skipped
during enumeration. Measured on a real 19k-file Tauri project, that
is 99.3% of what the old walk scanned, and it was the noise: matches
inside dependency READMEs, a tree cap eaten 86% by `node_modules`.
Ignoring filters discovery only. Ignored directories still appear,
marked `[ignored]` (in `list_files` too); every skip is reported with
the escape hatch (`include_ignored=true`); a walk explicitly rooted
inside an ignored area shows everything and says so; and explicitly
named paths (`read_file` and friends) never consult the filter.

The result shapes answer "where", not "first 200 lines" (ADR-0052):
`search_files` shows at most 5 match lines per file and counts the
rest, takes `include` (a gitignore-syntax file pattern such as
`*.go` or `src/**`) and `mode="files"` for per-file counts only;
`list_tree` elides big directories at a reported per-directory cap
instead of letting one directory starve the rest, and takes
`dirs_only=true` for a file-count-annotated directory skeleton.

Both walks stop on Ctrl+C (ADR-0065): they consult the turn's context
before every directory and file read (and every 1024 lines inside a
file), so an interrupt on a slow filesystem costs one syscall, not
the remaining project. What was found stays a result, labelled —
`[interrupted after N files scanned — results above are partial]`,
`[interrupted — the tree above is partial]` — and is kept in the
transcript for a resume.

## Reading: `read_file`, `summarize_file` (ADR-0014)

`read_file` takes `start_line`/`end_line` to read a window instead of
the whole file — annotated, never masquerading as the full text, and
with no line-number prefixes, which would poison `edit_file`'s
exact-match contract. Everything the model reads is replayed on every
later round, so windows are the default working style.

`summarize_file` returns a short summary instead of the bytes, produced
by `[model].summary` — a lightweight model sharing the main model's
client — or the main model when unset. File content reaches the
summariser nonce-wrapped with no tools, exactly like compaction; a
blocked summary is a reported error, never a silent empty one.

## Delegated search: `agentic_file_search` (ADR-0037, routing ADR-0062)

ADR-0014's principle generalised from one file to one question: a
child agent loop (on the main model) explores the project in its own
isolated context and returns only a compact report — the exploration,
dead ends included, never enters the conversation. `search_files`
finds strings you already know; this answers "where/how is X done" —
and since ADR-0062 the system prompt routes exploration here *first*
(self-navigation is the known-target path), after measurement showed
the tool had never fired spontaneously while the prompt prescribed
the manual loop.
The child gets a positive allowlist of read-only tools (orientation,
windowed reads, summaries — never shell, edits, web, MCP, or itself:
recursion is structurally impossible), 10 rounds, and a deny-all
approval gate as fail-closed insurance. The report contract names its
negative space — what was *not* found is stated explicitly — and
evidence comes as `path:line-range` with verbatim quotes, flagged
lossy: the report is to be trusted for answers, and re-read only for
the lines the caller will edit or quote (ADR-0062). Child tool calls render
live as `↳ tool` lines, and every child audit event carries
`agent="agentic_file_search"` in telemetry; token spend shows in
`/usage` as its own category.

## Editing: `edit_file`, `write_file` (ADR-0015)

`edit_file` keeps its exact-unique-string contract — line numbers write
to the wrong place *silently* when stale; a string anchor fails loudly
or works — and has what makes it cheap: an `edits` array applied in
order and **atomically** (any failure writes nothing and names the
failing edit), `replace_all` for renames, **diagnosed misses** (a
whitespace near-match is quoted with the file's real text and line, so
the fix is a copy-paste, not a re-read), and **evidence on success** —
the changed region with its line span, so verification needs no
read-back. The intended loop: windowed read → one batched edit → verify
from the result.

`write_file` is for new files — and for *deliberate* whole-file
replacement, which is where large documents used to die (ADR-0051): a
model revising a big file it holds only partially or post-compaction
regenerates the whole thing from memory, and everything it does not
reproduce verbatim is silently destroyed, reported as success.
Overwriting an existing file of 2KB or more with content under 70% of
its current size is therefore **refused** unless the call declares
`allow_shrink: true`; the refusal names both sizes and both remedies
(targeted `edit_file`, or re-read then declare). The declaration is an
argument, so it is visible on the approval dialog and recorded in the
transcript — a shrinking rewrite is possible, just never silent. The
dialog also annotates any overwrite with what it replaces
(`replaces existing file: 42KB → 8KB`).

## `file_info` (ADR-0016)

What a file *is* without reading it into context — content-judged type
(`file`-command style: Mach-O/ELF/PE, archives, scripts; the extension
is shown but never trusted), size, mode, modified and **created** times
(macOS-only by design, so the Darwin field is free), and the
MD5/SHA1/SHA256 trio that hash-lookup tools consume — the IR opening
moves in one read-only call. Batch via `paths`; symlinks reported,
never silently followed.

## `view_image`, `read_document`

`view_image` lets the model look at an image file in the project
mid-loop (MCP servers produce them: urlscan screenshots, pcap
extraction) — project-confined like `read_file`, MIME sniffed from
bytes. `read_document` reads PDFs natively (layout, tables, scans) and
extracts Word/Excel/PowerPoint text locally with the standard library.
Operator-side attachment routes for both live in
[attachments](attachments.md).

## `shell_exec`

Runs a shell command wrapped in macOS sandbox-exec — file writes
restricted to the project directory + scratch dirs (enforcement covered
by a real Seatbelt test), with a timeout and an output cap, exit status
surfaced. Approval-gated; the `!command` input route runs the same way
without the prompt because you typed it (see
[interface](interface.md)).

## `datetime` (ADR-0032)

A clock and a deterministic calendar — LLMs guess confidently at
exactly this arithmetic. Five ops: `now` (local/UTC/unix/weekday/ISO
week), `info` (weekday, day of year, ISO week, days in month, leap
year), `add` (signed calendar shifts — Go's month-end normalization is
disclosed in the output when it fires), `diff` (calendar breakdown +
total days/hours/minutes), `convert` (IANA timezone conversion).
Business-day counts are refused by design: weekday arithmetic without a
holiday calendar is wrong exactly where it would be used. The
session-start date also rides the system prompt (cache-stable),
pointing the model here for the live moment.

## `ask_user` (ADR-0036)

A structured mid-turn choice: the model presents a question and 2–8
options, the operator picks one (arrows/Tab, the option's digit in one press,
Enter; Esc declines), and the result names the choice — no
end-the-turn round-trip. Read-only and never approval-gated (a gate
on a question would be a dialog to permit a dialog). Esc returns a
distinct "declined" result — information, not an error. One-shot `-p`
mode refuses informatively (nobody to ask); the plain REPL prompts
with numbers on stderr. No free-text option by design: ending the
turn and asking IS the free-text channel.

## `agent_info` (ADR-0030)

The model's view of its own runtime — version, platform, the model it
runs as, thinking level, context occupancy, cumulative token usage
(the `/usage` numbers — one accounting source), limits,
approval/sandbox state, project trust, and connected MCP servers (as
last connected — startup or the most recent `/mcp reload`) and skills.
Read-only, no approval. GCP identifiers and hostname deliberately
excluded.

There is no diagram tool (ADR-0063): a ```` ```mermaid ```` fence in a
reply is rendered in place by the TUI when the terminal can draw it
faithfully, and shown as source otherwise. The model is told nothing
about diagrams — rendering is a view-layer concern, like everything
else the Markdown renderer does to a reply.

## Web access: `web_search`, `web_fetch` (ADR-0017)

Two egress tools, both **approval-gated by default** — the query or URL
itself is a channel where injected instructions could exfiltrate what
the model can read — and both relaxable per tool with the
[approval policy](approval.md) (`"web_search" = "never"`), which also
makes them usable in `-p` one-shot mode.

- **`web_search(query)`** — Grounding with Google Search on the main
  model: a grounded answer **with its sources** (title, domain, URI),
  so claims can be checked rather than believed. First-party and
  ToS-clean — the reason plain search APIs were not used.
- **`web_fetch(url, focus?)`** — the URL Context tool on the
  `[model].summary` model, or the main model when that is unset: the page is fetched by the provider's
  infrastructure and read in the digest model's own context; only an
  **organized extraction** (key points with exact
  names/numbers/dates, caveats) enters this conversation. Server-side
  fetching kills the SSRF class by construction — localhost and your
  LAN are structurally unreachable — at the mirror cost that
  intranet/authenticated pages cannot be fetched (failures are
  reported with their retrieval status).

Web content is untrusted: digests return as ordinarily nonce-wrapped
tool results, and the fetch prompt carries the defensive framing for
the layer that cannot be wrapped.

## The rest

`load_skill` (skills — see [integration](integration.md)),
`save_memory` / `delete_memory` (agent memory — see
[sessions](sessions.md)), and MCP tools as `mcp__<server>__<tool>`
(see [integration](integration.md)).
