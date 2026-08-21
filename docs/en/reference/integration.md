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

## MCP servers

Servers are read from two scopes, both in Claude Code `.mcp.json`
format (stdio transport, `${VAR}` expansion) so entries move between
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
tool — see [approval](approval.md)). Timed-out calls kill the server
child (MCP has no cancel) and it respawns lazily on the next call.

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
whenever the primary's environment changes. **Sharing is a symlink you
make**, per skill or wholesale (discovery follows links):

```sh
ln -s ~/.claude/skills/meeting-notes ~/.config/gem-agent/skills/meeting-notes
ln -s ~/.claude/skills ~/.config/gem-agent/skills   # share everything
```

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

Skill content is treated as *instructions*, not wrapped as untrusted
data — it is a file you installed, the same trust tier as `AGENTS.md`.
That exemption is bounded: `load_skill` can only read inside a
discovered skill's directory, symlinks resolved and checked. Skill
`scripts/` run through `shell_exec` stay under the sandbox and the
approval gate like everything else.
