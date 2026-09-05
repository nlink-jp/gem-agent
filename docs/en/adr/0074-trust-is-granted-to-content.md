# ADR-0074: Trust is granted to content, not to a path — pinned agent-facing files

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-05 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Amends | ADR-0023 (project trust is per directory, once), ADR-0073 §6 (the recorded residue) |
| Triggered by | ADR-0073 §6 residue: a persistent file in a subdirectory can be replaced by renaming its parent directory; the write lane's path rules cannot see it |

## Context — the residue, and what it is an instance of

ADR-0073 protects the files later sessions trust (`AGENTS.md` and kin,
`.mcp.json`, `.gem-agent.toml`, `.claude/`, `.git` and its hooks/config)
with Seatbelt rules keyed on their **paths**. The independent review
showed the limit of that key: `mv sub sub.old && mv prepared sub`
touches only the paths `sub` and `sub.old`, yet afterwards `sub/CLAUDE.md`
is a different file. Probed today (macOS 26):

| write-lane profile | `mv sub sub.old` | `echo > sub/other.txt`, `mkdir sub/deeper` | `mv scratch/prepared brandnew` (new dir with `CLAUDE.md` inside) |
|---|---|---|---|
| as shipped (regex on the persistent names) | **allowed → swap succeeds** | allowed | allowed |
| plus `(deny file-write* (literal <proj>/sub))` for each parent of an existing persistent file | **denied** | allowed | allowed |

So a by-name deny on the *parent directory entry* closes the swap for
persistent files that exist when the profile is built, without touching
ordinary work inside that directory. It cannot close the move-in of a
prepared directory that did not exist before: the destination name is
new and ordinary.

This is one instance of a class, and the class is the root cause:
**trust keyed on a name.** Two more instances already exist:

- **R2 of the final review** (fixed by real-path judgment and
  create-then-rename writes): a link named like an ordinary file carried a
  write into `AGENTS.md`.
- **ADR-0023 trust itself**: the operator trusts a *directory* once;
  from then on whatever `AGENTS.md`, `.mcp.json` or a skill *contains* is
  loaded. A `git pull` that changes `AGENTS.md`, a dependency update that
  rewrites `.mcp.json`, or the swap above all pass through the granted
  trust unasked. The name was trusted; the content is consumed.

Path rules bound *where a write may land*. They cannot bound *what a
trusted reader will find under a name later*, because the reader's name
resolves through ancestors, links and time. A rule that can is one keyed
on the content itself.

## Decision

### 1. Pin the content gem-agent consumes

At the ADR-0023 trust prompt — and whenever trust is (re)granted — record
a SHA-256 **pin** for every agent-facing file gem-agent consumes from the
project root: the instruction files there (`AGENTS.md`, `AGENT.md`,
`CLAUDE.md`, `GEMINI.md`; ancestor files are the operator's own and
outside the gate, as in ADR-0023), `.mcp.json`, `.gem-agent.toml`, and
each project skill, pinned as a directory: the relative path and
content digest of every file under it, links by their target string.
A consumed file is digested the way its loader reads it — through an
`os.Root` at the project directory, so a link inside the project is
pinned by target and content and a link leaving it is absent for both.
Pins live beside the trust record in the machine-owned `policy.toml`
(`[projects."…"]` gains `pinned_at` and a `[projects."…".pins]` table
of `name = "sha256:…"`). `pinned_at` makes an empty set a recorded
answer — a project with no agent-facing files is pinned as "nothing",
once — rather than "never pinned".

The first **interactive** start of an already-trusted project after this
change records the current content as the pins and names what it pinned
(trust on first use: the files the operator had been loading are what
they trust today). A non-interactive start with no pins recorded loads
as before and says that nothing is pinned yet; it records nothing, because
recording trust nobody confirmed is what ADR-0023 §5 refuses to do.

Compare before consuming — at startup, and again at `/clear`, `/mcp
reload` and `/skills reload`:

- unchanged → load, as today;
- changed or new → at an **interactive startup**: one prompt naming the
  file, the kind of change and its current size (`AGENTS.md changed
  (1234 bytes) since you trusted it. trust the new content? [y/N]`); `y`
  updates that pin, `N` leaves the file out of this session and the pin
  as it was, so the next start asks again. **Non-interactive (`-p`)**,
  and every mid-session re-check (`/clear`, the reloads — nobody is at a
  prompt there): the file is not loaded, one line says so, the session
  runs bare for that file (ADR-0023 §5's rule for "undecided": refuse
  nothing, load nothing unconfirmed);
- removed → nothing to load; the pin stays, so content that comes back
  under the name is "changed", never "new";
- a file gem-agent itself wrote with the **operator's approval** — an
  OperatorOnly `write_file`/`edit_file` the operator answered — re-pins
  **that one name** on success, and only when the file was still what
  its pin records as the write began: the operator saw that write, not
  a drift before it (a `! git pull` followed by an approved one-line
  edit would otherwise pin the whole pulled file). Nothing else in the
  set moves, and a name this session left out is not re-pinned by a
  write into it (the operator never saw the content it replaced). An
  operator-lane or `!` command shows its text, not its effect on those
  files, so it re-pins nothing: the pinned files that now differ are
  named in a note and the next interactive start asks.

`gem-agent trust` shows the trust state, the pins and the files that
differ; `gem-agent trust --accept` records the current content as
trusted — for a scripted `-p` flow after an intended edit. On an
untrusted project `--accept` is an error: there is no trust for the pins
to record.

`.gem-agent.toml` is consumed too: its approval half may loosen the
policy only where the operator's config names the project (ADR-0023 §4)
*and* the file is trusted content under the pins; a changed file may
still tighten, as an untrusted project's may. The trust decision and the
pin check therefore run before anything of the project is read.

### 2. Close the swap for existing nested persistent files

The write-lane profile adds `(deny file-write* (literal <dir>))` for the
parent directory of every persistent file found under the project at
profile build (a walk bounded at 20,000 entries that says when it was
cut), and for each ancestor up to the project root. Rebuilt on `/clear` with the profile.
Ordinary writes inside those directories stay allowed (probed above).

### 3. Make the irreducible residue visible

The move-in of a prepared directory carrying a new nested persistent
file cannot be expressed as a path rule. It targets *other* consumers —
Claude Code's nested `CLAUDE.md`, a nested repository's hooks — never
gem-agent itself (which reads the root and its ancestors only). So it is
made visible rather than pretended closed: at `/clear` and at session
end, gem-agent diffs the set of persistent files under the project
against the set at session start and reports additions and changes —
`this session added sub/CLAUDE.md and changed vendor/x/.git/hooks/pre-commit`
— on stderr and in the transcript (a `persistent_changes` record). The
walk is the one from §2, bounded, and says when it was cut.

### 4. Advisory pins for what the operator's git consumes

`.git/hooks/*` and `.git/config` of the root repository are pinned too,
advisory only: a change since the last session is reported at startup
("`.git/hooks/pre-commit` changed since your last session"), never
blocked — git, not gem-agent, runs them, and the operator may well have
edited them.

## Alternatives considered

- **A "contains" predicate in Seatbelt.** Does not exist; SBPL filters
  match the path of the entry operated on.
- **Deny every directory rename in the write lane.** Not expressible by
  path (a directory and a file look alike), and it would forbid
  `mv src lib` refactors.
- **Immutable flags (`chflags uchg`) on persistent files.** The owner
  clears them; a rename of an ancestor is unaffected; and it fights the
  operator's editor.
- **Copy the trusted files into the state directory and read from
  there.** Equivalent to pins but heavier, and a stale copy confuses an
  operator who just edited `AGENTS.md`. Pins compare and ask; copies
  silently disagree.
- **Verify against the git index (`HEAD:AGENTS.md`).** Not every project
  is a repository, and a malicious commit changes both sides.
- **Do nothing beyond the record.** Leaves ADR-0023's own gap open: a
  `git pull` re-trusts unasked.

## Consequences

- The class "trust keyed on a name" closes for gem-agent's own
  consumption; the remaining path rules become defence in depth rather
  than the guarantee.
- **Cost:** editing `AGENTS.md` outside gem-agent costs one `y` on the next
  launch; a `-p` pipeline after such an edit runs without the changed file
  until `gem-agent trust --accept`. Startup adds a few digests and one
  bounded walk (the trust probe already walks `.claude/skills`).
- **Behaviour change** (breaking-change process): default on, with
  `[approval].pin_trusted_files = false` as the opt-out. The CHANGELOG
  entry carries `Breaking:`.
- The cross-consumer residue (§3) is stated as such in the ADR and the
  approval reference, not claimed closed.

## Decisions taken (2026-09-05)

The operator took the recommended option on every point: pins are on by
default with `[approval].pin_trusted_files = false` as the opt-out; a
`-p` run skips a changed pinned file rather than refusing to start; the
pinned set is the instruction files at the project root, `.mcp.json`,
`.gem-agent.toml` and the project skills, with `.git/hooks` and
`.git/config` advisory through the persistent-file snapshot; the
persistent-file walk stops at 20,000 entries and says so.

## Review (2026-09-05)

Two independent reviews of the implementation returned 31 findings; the
facts were taken and the remedies decided here (CONVENTIONS: reviewers
observe, contributors decide). Fixed in this ADR's final form: a consumed
file that is a link was absent to the pin and present to the loader (now
digested through the same root, link target included); an empty pin set
read as "never pinned" (`pinned_at`); a re-pin rewrote the whole set,
unseen files and operator-lane commands included (scoped to the one
approved write; commands report instead); the skill grant was keyed by
frontmatter name while the pin used the directory entry (both use the
entry); `.gem-agent.toml` was pinned but read before the pin check (trust
and pins now precede every project read); a non-interactive first start
pinned silently (loads as before, records nothing); `/clear` re-checked
the instruction files but carried the old skill list (re-discovered);
`--accept` on an untrusted project exited 0 (error). Declined: pinning
ancestor instruction files (they are the operator's own directories,
outside ADR-0023's gate); a line-count diff in the prompt (the operator
has the file; the prompt names the change and its size); asking at
`/clear` and the reloads (the TUI owns the terminal there — the file is
left out and named, and the next start asks). An E2E script of this work
ran in the repository instead of its fixture and replaced `AGENTS.md`; it
was restored, and `make check` now refuses an `AGENTS.md` without its
sections.

A third pass verified the fixes (all confirmed) and found seven more,
all taken: resetting a project tool policy to default deleted the entry
and its pins for a project trusted through the config (the entry now
survives while it carries pins); an approved edit re-pinned a file that
had drifted before it (the agent now announces the write before it runs,
and the pin is compared then); pins were rewritten from a snapshot taken
outside the policy file's lock (edited under it); the settings panel
rebound its policy-file pointer and left the session's copy stale;
instruction files and skills were read after the MCP servers started
(read first now); `/clear` printed the pin note twice; an excluded
project skill took the operator's global skill of the same name with it
(the global one comes back). Left as recorded: the gap between digesting
a file and reading it is now the few calls between them, with nothing
of the project running in between — closing it fully means the loaders
returning the digest of the bytes they read, a change kept for when a
reader outside this order appears.

## Survey of other agents (2026-09-05)

No mainstream coding agent keys trust on content today: Claude Code,
Codex CLI, Gemini CLI, Copilot CLI and Cursor persist a per-directory
trust that survives a `git pull`; Claude Code's `ConfigChange` hook can
react to a mid-session settings change but is operator-written policy,
not a default re-ask. Instruction prose (`AGENTS.md` and kin) is gated by
Gemini CLI alone, and several 2025–26 incidents reached execution through
exactly those files. Self-modification of settings directories is gated
by Claude Code, Codex and Cursor (rules directories excepted). This ADR's
pins are therefore a departure from the field, taken knowingly; the
re-trust prompt's frequency is the cost to watch.

## References

- ADR-0023 — project trust is decided once per directory; §4 the
  config-level grant, §5 the non-interactive rule this ADR follows
- ADR-0072 §4 — check-then-use on a lexical path; the `os.Root` reads the
  pins now mirror
- ADR-0073 §6 — the recorded residue this ADR closes for gem-agent's own
  consumption and makes visible for other consumers
- `internal/trustpin`, `cmd/pins.go`, `cmd/trustcmd.go` — the
  implementation; `internal/archtest` pins that every loader of project
  content takes the grant
