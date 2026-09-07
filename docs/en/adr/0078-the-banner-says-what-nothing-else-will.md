# ADR-0078: The banner says only what nothing else will say

| Field | Value |
|-------|-------|
| Status | **Proposed** |
| Date | 2026-09-08 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The pre-release label read-through for v0.72.0: the banner was measured at 28 wrapped rows before the operator has typed anything, and 11 of them are one comma-joined list |
| Amends | ADR-0002 (the inline TUI's startup output), ADR-0039 §banner, ADR-0050 §banner line — each contributed a line to a wall nobody decided to build |
| Relates to | ADR-0077 §4 (the same argument, aimed at a report that was never added), ADR-0029 §3 (banner labels stay English — unchanged) |

## Context

Measured on a real start in the operator's own workspace, 24 configured
MCP servers, at 80 columns:

| line | characters | rows |
|---|---|---|
| project trust warning | 106 | 2 |
| `gem-agent <version> — <model> @ <project>/<location>` | 82 | 2 |
| `project: <abs path>` | 120 | 2 |
| `sandbox: enabled (shell lanes: …)` | 73 | 1 |
| `session log: <abs path>` | 301 | 4 |
| `mcp: <every server, comma-joined>` | 809 | 11 |
| `skills: …` | 88 | 2 |
| `approval policy: …, … N rules total (/tools shows each tool's effective gate)` | 150 | 2 |
| `risk rulebook: base + project (/riskbook shows it)` | 40 | 1 |
| `/help for commands, Ctrl+D to quit` | 34 | 1 |
| **total** | | **28** |

Nobody decided to print 28 rows. The banner is assembled by teeing every
startup write into it (`cmd/root.go`), and each ADR that added a feature
added its line: this is how a wall accumulates without an author.

Two of those lines end by naming the command that supersedes them —
`(/tools shows each tool's effective gate)`, `(/riskbook shows it)`. A
line whose own text says "run this other thing to actually see it" has
conceded that it is not the fact the operator needed.

And the principle is already recorded here. ADR-0077 §4 refused to add a
drift report to this banner: **a report is not a control.** A line
printed before anyone has a question is a status; a status printed on
every start is read on none of them; printing it buys the runtime the
feeling of having disclosed something and buys the operator nothing.
That ADR dropped a line that did not exist yet. This one is the same
argument aimed at the twenty-eight that do.

## Decision

**A line earns a place at startup only if nothing else will say it.**

That is the whole rule. It has three consequences, and they are what the
rest of this section works out: what the persistent chrome already
shows does not go in the banner; what a `/` command shows on request
does not go in the banner; and what is *abnormal* always does, because
the operator has no reason to go looking for it.

### 1. What stays

- **`gem-agent <version> — <model>`.** Which build, and what this
  session is spending. **Without `@ <project>/<location>`**: a GCP
  project id in the first row of every screenshot and pasted bug report
  is an environment-specific value in a shared artifact, which this
  workspace's conventions keep out of them, and `/settings` shows it on
  request.
- **`instructions: …`.** Files discovered on disk that change how the
  agent behaves, which the operator did not type and no command lists.
  This is the clearest case in the banner and the one line nobody
  questioned.
- **`resumed: session <id> (<n> messages restored)`.** A fact about this
  session with no other surface.
- **Everything abnormal**, unchanged: a disabled or unverified sandbox,
  `read_lane_prompts`, an untrusted project, an MCP server that would
  not start, a policy entry ignored, a stale `[mcp] exclude` name. These
  are changes, not status, and they carry their own next command.
- **`/help for commands, Ctrl+D to quit`.** One row, and it is where
  everything dropped below can be found.

### 2. What becomes one row

The MCP servers, the skills and the memories become counts, with the
commands that expand them:

```
mcp: 24 servers, 251 tools · skills: 5 · memory: 3 (/mcp /skills /memory)
```

Fifteen rows to one. The count is kept rather than dropped because
"did my toolset come up as expected" is a question the operator has
*before* typing — a server that fails warns, but a server that is
missing from the configuration warns nobody. The enumeration is what
goes: `/mcp` already prints it one server per line, better.

### 3. What goes

- **`project: <abs path>`** — the TUI footer shows the project directory
  continuously, beside the model and the context meter.
- **`sandbox: enabled (shell lanes: read runs unasked, write and
  operator ask)`** in the normal case — a three-lane policy summary,
  identical on every start, is a manual excerpt. The three abnormal
  variants stay (§1).
- **`session log: <abs path>`** — no command consumes the path;
  `--continue` and `--resume <id>` are what resume takes, `gem-agent
  sessions` lists them, and the exit summary already prints the id with
  its resume command.
- **`approval policy: …`** and **`risk rulebook: …`** — both name their
  own replacement.

### 4. The work-directory note is gated on size

`cmd/root.go` gates it on the number of leftover directories, so two
empty ones produce `note: 2 earlier session work dir(s), 0B — …` on
startup. Nothing has accumulated; nothing needs doing. It is gated on
bytes now, and the trailing "nothing is deleted automatically" goes:
`gem-agent workdirs` says that in its own help, and a design assurance
is not a next command.

### 5. What is not done, and why

- **No mode-dependent banner.** The plain REPL has no footer, so
  dropping `project:` costs it there. Branching the banner on the
  presence of chrome is a rule the next reader has to reconstruct from
  two code paths; `/settings` shows the project in both modes, and the
  operator is standing in the directory they launched from.
- **No configurable banner** (`[tui].banner = "full" | "minimal"`). It
  would preserve the wall for anyone who set it and add a setting to
  every future line's design. If a line is worth printing it is worth
  printing for everyone; if it is not, an option is a way of not
  deciding.
- **Nothing is localized by this ADR.** ADR-0029 §3 keeps banner labels
  English as grep-stable output, and that is untouched.
- **The exit summary is not touched.** It already passes this rule: a
  session id and the command that resumes it.

## Alternatives considered

- **Cap the MCP list like `skillBannerLine` does** (8 names, then
  `… +N more`) — rejected as the smaller half of the answer: it fixes
  eleven rows to three and leaves the question of why an enumeration is
  in a banner at all unanswered.
- **Print the banner only on the first run in a project** — rejected:
  the abnormal lines are exactly the ones that must not be seasonal.
- **Keep everything and rely on the operator scrolling** — this is the
  status quo, and the read-through that measured it could not see the
  banner at all until `tools/labels` was fixed. A wall nobody can read
  end to end is how four consecutive releases shipped explanatory
  banners.

## Consequences

- `cmd/root.go` builds the banner from the rule rather than from
  whatever wrote to stderr: the counts line replaces `mcpSummary`'s
  join, `skillBannerLine` and `memory.BannerLine`; the policy and
  rulebook lines go; the sandbox line prints only its abnormal variants.
- The identity line loses `@ <project>/<location>`.
- `workdirs` note gated on bytes.
- Tests: the normal start prints the identity, instructions, counts and
  hint and nothing else; each abnormal condition still prints its line;
  a resumed session prints its line; the counts line names the commands.
- Docs: the interface reference's startup section, README and README.ja
  where the banner is shown, CHANGELOG, and both INDEX files.
- `make labels` now shows the banner, so the result is read end to end
  before it ships — which is the check that did not exist when these
  lines accumulated.
