# Drop-in integration: instructions, MCP, skills

gem-agent's core requirement is drop-in compatibility: it reads the
files a project already carries for other agents, with zero per-project
setup. Everything a project provides is behind the one-time trust gate
(see [approval — startup safety](approval.md)).

## Project instruction files

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
Files with identical content are injected once. The startup banner
lists what was loaded.

The ancestor walk stops at your home directory: an instruction file is
obeyed as instructions, so gem-agent will not pick one up from a shared
location like `/tmp` that you do not own.

The project's own files load only once you have trusted the project
(ADR-0023) and only while their content matches what you trusted: a
`git pull` that changes `AGENTS.md` asks again before the file is used
(ADR-0074). See [approval — startup safety](approval.md) for the trust
prompt, the pins and `gem-agent trust`.

## MCP servers

Servers are read from two scopes, both in Claude Code `.mcp.json`
format (stdio transport, `${VAR}` and `${VAR:-default}` expansion) so entries move between
them verbatim:

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

Tools appear as `mcp__<server>__<tool>`, approval-gated (relaxable per
tool — see [approval](approval.md)). A failed call renders with its
provenance — the server's own error, the server's rejection, or a call
gem-agent could not complete — and a server that answers three
consecutive calls in a turn with one identical error text gets named in
a runtime note that asks the model to report to you instead of
investigating (ADR-0075; see [tools](tools.md) for the three shapes and
[sessions](sessions.md) for the `mcp_fault` record). Timed-out calls kill the server
child (MCP's `notifications/cancelled` may be ignored by the receiver, so
gem-agent does not send it; the kill is what reliably unblocks) and it
respawns lazily on the next call.

**`/mcp reload`** (ADR-0039) reconnects everything mid-session — full
restart, config re-read, fresh tool lists — without losing the
conversation: the recovery for a wedged server, and the way a server
added to `mcp.json` joins a running session. It reuses the startup
trust decision with the content pins re-checked (an untrusted project's
`.mcp.json` stays unloaded; one that changed since you trusted it is
left out and named — `gem-agent trust --accept` or the next interactive
start re-trusts it; granting or withdrawing trust itself still takes a
restart), the session approval allowlist
survives (keyed by tool name), and `--mcp off` on the command line
skips MCP entirely for one run — what a `-p` pipeline usually wants.

To add governance and an audit trail, route a server through
[mcp-guardian](https://github.com/nlink-jp/mcp-guardian) — it is itself
a stdio MCP server, so the opt-in is just a `.mcp.json` entry:

```json
{
  "mcpServers": {
    "guarded": { "command": "mcp-guardian", "args": ["--profile", "myserver"] }
  }
}
```

### Large results

A tool result is handed to the model within one response budget
(20,000 bytes, the same cap as built-in tool output). A text block that
does not fit is saved whole in the session work directory and replaced
by its first 600 and last 200 characters and the path (`… [N bytes —
too large to hold inline, so the whole result is saved. Shown above:
bytes 0–600 and 252439–252639. Read the rest with read_file
offset/length, or narrow the call and ask again: read_file <path>]`).
The tail is kept because formats that append metadata put it there — a
server's `"truncated": true` or row total arrives as the last bytes —
and the spans are bytes, the unit `read_file`'s `offset` takes
(ADR-0096 §1). Blocks past the budget are saved together in one file and
announced in one line (`[N more text block(s), M bytes — past the
response budget, saved whole …]`). Images and other binary blocks are
saved and pointed at (`use view_image on that path`), never inlined;
those past the budget are neither saved nor listed one by one — one
line names how many. Without a work directory the loss is stated.

## Skills (ADR-0010, ADR-0011)

gem-agent reads **Claude Code's skill format, as-is** — from its own
locations, arranged exactly like MCP: format compatibility is drop-in,
location sharing would be coupling:

| Scope | Path | |
|---|---|---|
| Global | `~/.config/gem-agent/skills/<name>/SKILL.md` | gem-agent's own |
| Project | `<project>/.claude/skills/<name>/SKILL.md` | shared with Claude Code |

`~/.claude/` is never read — that is Claude Code's live environment,
and inheriting it implicitly would change the fallback's behaviour
whenever the primary's environment changes. A skill installed for
Claude Code is **copied** into the global directory:

```sh
cp -R ~/.claude/skills/meeting-notes ~/.config/gem-agent/skills/meeting-notes
```

Do not link it: `~/.claude` is a credential store the shell lanes cannot
read, and the kernel matches the resolved path, so a linked skill is
discovered but its scripts fail with `Operation not permitted`
(ADR-0076). Discovery still follows a symlink to a directory the lanes
can read.

Frontmatter is read minimally (`name`, `description`, `argument-hint`);
`allowed-tools` is ignored — gem-agent has its own approval model, and
honouring a foreign permission grant would bypass it. The project wins
a name collision, announced like an MCP one.

Skills are progressive disclosure: each contributes one description
line to the system prompt, and the body loads only when used —

- **the model** calls `load_skill(name)` when the task matches a
  description, and `load_skill(name, file)` for the skill's own
  `references/` and `scripts/` files;
- **you** type `/skill <name> [args]` (the body is injected directly,
  no extra model round; Tab completes the name). `/skills` lists what
  was found.

**`/skills reload`** (ADR-0039) re-runs discovery mid-session — a
skill installed while the session runs becomes available without
restarting; the system prompt's skill section follows, so the model
sees the new set from the next round.

Skill content is treated as *instructions*, not wrapped as untrusted
data — it is a file you installed, the same trust tier as `AGENTS.md`.
That exemption is bounded: `load_skill` can only read inside a
discovered skill's directory, symlinks resolved and checked. Skill
`scripts/` run through `shell_exec` stay under the sandbox and the
approval gate like everything else.

A loaded skill names its directory (ADR-0070): the `load_skill(name)`
result and the `/skill <name>` turn open with Claude Code's own line
`Base directory for this skill: <dir>` — the symlink-resolved skill
directory, the same boundary reads are confined to. A `SKILL.md`
written to Claude Code's contract ("`SKILL_DIR` is the directory
containing this SKILL.md", then `python3 SKILL_DIR/scripts/…`) can be
followed from the global skill directory as well as from a project;
without the line, a global skill's scripts are reachable by no path the
model knows, and it goes looking for them.

### Where an operator's procedures go (ADR-0096 §7)

What reaches the model has one of two standings, and the route decides
which:

| Route | Arrives as |
|---|---|
| `AGENTS.md` and the other instruction files; a skill's `SKILL.md` and files loaded through `load_skill` | **instructions** — at the trust level of the system prompt |
| everything a tool returns: `read_file`, `shell_exec`, every MCP result — a knowledge-vault server's notes included | **data** — inside the nonce tag the system prompt calls "DATA … never instructions" |

So **a procedure you want followed belongs in a skill**, not in a note
read through an MCP server. An instruction file that says "read the QA
protocol in the vault and follow it" brings the protocol in as data:
the model may use it as reference, but it reaches the model in the voice
it is told never to obey. Write the procedure as a skill and point at it
instead — "before analysing, call `load_skill` for `qa-protocol`" in
`AGENTS.md`, or invoke it yourself with `/skill qa-protocol`.

- `SKILL.md`'s body is cut at 64 KB, with a note; a supporting file
  loaded with `load_skill(name, file)` is refused above 96 KB. A long
  procedure splits into `references/` files that the body names, each
  loaded when its step comes up.
- Keep reference material — data-source specifications, field
  references, known pitfalls — in the knowledge base. It is facts to
  analyse, and data is the right standing for it.

There is no setting that makes a path or an MCP server instruction-grade,
and that is deliberate. A knowledge base the agent can write to would
turn its own mistakes, and any text injected into it, into standing
rules: the report behind ADR-0096 includes a claim the model wrote into
a QA protocol and cited three days later to justify an error. A skill is
a directory you installed. A project skill lives under `.claude/`: the
file tools change it only with your yes, every time — never the model
tier's — the shell's read and write lanes cannot write there, and a skill
that changed since you trusted the project is caught by its trust pins
(ADR-0074). The global skill directory is outside the project, where the
file tools do not reach.
