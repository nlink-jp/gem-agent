# Built-in tools

Every tool the model can call, what it returns, and the design decision
behind it. All file tools are confined to the project directory,
symlink escapes included. `/tools` lists them with each one's live
approval gate.

## Orientation: `list_files`, `list_tree`, `search_files`

`list_tree` shows the project as a tree; `search_files` is a fast
dependency-free grep (regex or literal, binaries and `.git` skipped,
caps reported) — so orientation costs one call, not one round per
directory.

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
from the result. `write_file` is for new files and full rewrites.

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
options, the operator picks one (arrows/Tab, digits 1–9 in one press,
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
approval/sandbox state, project trust, connected MCP servers (as of
startup) and skills. Read-only, no approval. GCP identifiers and
hostname deliberately excluded.

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
  lightweight digest model: the page is fetched by the provider's
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
