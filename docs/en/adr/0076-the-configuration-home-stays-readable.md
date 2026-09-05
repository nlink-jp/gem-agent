# ADR-0076: The operator's configuration home stays readable to the lanes; skills are copied, not linked

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-06; §3 shipped in v0.71.0, §1 by operator decision the same day) |
| Date | 2026-09-06 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Session 4d6bb685 (2026-09-05): while investigating a remote server's error (ADR-0075), the read lane ran `cat ~/.config/mcp-bridge/config.json` and `grep … ~/.config/gem-agent/mcp.json` unasked. A first draft of this ADR denied `~/.config` to the lanes; its review (2026-09-06) is what decided the other way |
| Amends | ADR-0011 §3 (the symlink into `~/.claude/skills` is withdrawn) |
| Relates to | ADR-0073 §1/§3 (the credential list and its three enforcers), ADR-0075 (the trigger that stops the investigation) |

## Context

### What happened

The two files the read lane read hold credentials by design.
`~/.config/mcp-bridge/config.json` exists to hold pre-registered OAuth
client credentials; `~/.config/gem-agent/mcp.json` holds each server's
command and `env`, where API keys conventionally go. Neither is on the
credential list (ADR-0073 §3), which names 12 directories and 9 files.
Measured on the operator's machine by file name only, `~/.config` holds
52 tool directories, and 13 of their `config.toml` / `config.json` files
carry a key named exactly `token`, `api_key` or `secret` (16 with a
looser match).

### The first draft, and what its review found

The first draft applied the ADR-0073 rule — close the class, not the
instance — by denying the whole configuration home (`~/.config`,
`$XDG_CONFIG_HOME`) for `file-read*` in the read and write lanes, with a
finite re-allow: the skills directory, git's configuration, and an
operator key for build tools. A code and Seatbelt review of that draft
(an independent verifier, a control run for every probe) found, in one
pass:

- the skills re-allow had to be written on the resolved real path,
  because Seatbelt matches the path after symlink resolution — an allow
  or a deny written on a link's path does nothing (measured);
- the name-based denies (`.env`, `id_rsa`, `credentials.json`) had to be
  emitted again after every subpath re-allow, or a `.env` inside the
  re-allowed skills tree became readable (measured);
- git's directory could not be re-allowed whole: `~/.config/git/credentials`
  is git's documented plain-text token file;
- the runtime reads `$XDG_CONFIG_HOME` nowhere, so the cage would have
  carried a notion of "configuration home" the rest of the program lacks;
- two startup probes, one of which wrote into `~/.config`;
- the operator key would have been load-bearing for every read-only tool
  that keeps its settings there, growing with the machine's tools.

One deny produced four re-allow rules, two rules about the rules' order,
one operator-maintained list and two probes. That is the shape ADR-0073
was written to end: a design that keeps asking for the next entry — here
the complement of the credential list instead of the list.

### Why the kernel cannot draw this line

ADR-0073 could hand the read lane to the kernel because a read-only
command has no legitimate use for a network socket, a Mach port or a
preference write: denying the capability family cost nothing, and the
family is finite and documented. The configuration home is different in
kind. Secrets and ordinary settings share it, and the tools the read
lane exists for read their settings there — git its `config` and
`ignore`, `fd` its ignore file, `tshark` its preferences. Whether a
`file-read*` of `~/.config/x` is the model going after a token or git
starting up is not in the syscall; it is in the command. A kernel rule on
the directory therefore has to enumerate the legitimate readers, and that
set is unbounded where ADR-0073's denied families were empty.

### The symlink

The review also measured the mechanism ADR-0011 §3 recommends for sharing
skills with Claude Code: `ln -s ~/.claude/skills ~/.config/gem-agent/skills`.
`~/.claude` has been on the credential list since ADR-0073 (it holds
Claude Code's tokens beside its skills), and Seatbelt matches the resolved
path: a linked skill is discovered and its body loads (the runtime reads
it outside the cage), but its scripts fail in the read and write lanes
with `Operation not permitted`. This has been so since v0.69.0.

## Decision

### 1. The configuration home is not denied

The credential list stays what ADR-0073 §3 made it: one list of
well-known credential stores and secret file names, read by the profile,
the file tools and the risk floor. `~/.config` is not added, nor are the
two files of the incident. A plain-text token stored under a tool's
configuration directory is readable in the read and write lanes, as the
approval reference already states; that boundary now carries this
example.

### 2. The behaviour is addressed at its trigger

What went wrong in the session was a model investigating the runtime
when a server misbehaved. ADR-0075 supplies the trigger that was
missing — the runtime names the server's repeated identical answer and
asks the model to report. The files it read on the way are the symptom.

### 3. Skills are copied, not linked (ADR-0011 §3 withdrawn)

A skill installed for Claude Code is copied into the global directory:

```sh
cp -R ~/.claude/skills/<name> ~/.config/gem-agent/skills/<name>
```

The `/skills` empty state prints this command in place of the two
`ln -s` lines; the integration reference says the same. Discovery still
follows symlinks — a link to a directory the lanes can read works as
before — but the runtime no longer recommends one into `~/.claude`.
ADR-0011's rejected alternative "copy-on-install tooling" stays
rejected: one copy command is not tooling, and a copy that diverges from
Claude Code's is the operator's choice, as ADR-0011 wanted.

## Not adopted, recorded for the next reader

- **Denying `~/.config` in the kernel** — the first draft, above.
- **Denying it in the read lane only** — halves the collateral (build
  tools run in the write lane) but keeps three of the re-allow rules and
  the symlink resolution, and every read-only tool that reads its
  settings would fall through to the write lane and its prompt, eroding
  what the unasked read lane is for.
- **A Block floor for commands that name a path under `~/.config`** —
  cheap, no collateral (tools do not name the path on the command line),
  and it would have caught both commands of the session, which named
  `~/.config/…` literally. Not adopted with this ADR: a text rule is a
  floor, not a cage (ADR-0073 §2), and the trigger fix (ADR-0075) is
  measured first. **Decision point:** add it now, or only if the
  investigation pattern recurs after ADR-0075 ships.
- **The runtime's own wiring files as literal entries** (`mcp.json`,
  mcp-bridge's `config.json`) — a set closed by construction, unlike the
  fleet's. Not adopted with this ADR, for the same reason as the floor.
  **Decision point:** same.

## Consequences

- `cmd/skills.go`: the empty-state text prints the copy command; the
  `internal/skills` and `internal/trustpin` comments no longer describe
  the symlink as the sharing mechanism. Discovery is unchanged.
- Docs: the integration reference (skills section), the approval
  reference (the credential-list paragraph names the configuration-home
  example), ADR-0011 *Amended by*, AGENTS.md gotcha, CHANGELOG.
- An operator who linked `~/.claude/skills` replaces the link with a
  copy; until then the linked skills' scripts keep failing in the read
  and write lanes, with the lane note that already accompanies the
  refusal.
