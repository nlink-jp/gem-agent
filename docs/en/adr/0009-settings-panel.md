# ADR-0009: A settings panel, and a machine-owned policy file

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "it is probably time for a `/` command that manages settings visually" |

## Context

Configuration is resolved through four layers — flags, `GEMAGENT_*`,
`GOOGLE_CLOUD_*`, the file, built-in defaults — and **none of it is
visible from inside a session**. The banner shows a handful of values with
no indication of where they came from. "Why is it using that model?" is
currently answered by reading a config file and reciting the precedence
rules from memory.

Editing has a separate problem. The setting that actually changes with
use is the approval policy (ADR-0008): the moment to decide "I never want
to be asked about this tool" is the moment the dialog appears, and the
only way to act on it today is to quit, edit TOML, and start again.

There is a constraint on writing, too. The TOML library in use does not
preserve comments, and the shipped template is 71 lines of them —
explaining what each key does and why the defaults are what they are.
A settings UI that wrote back to `config.toml` would delete the
documentation the operator relies on, silently, the first time they
changed a value.

## Decision

**`/settings` opens a panel that shows everything and edits a little.**

1. **Everything is shown with its provenance.** Each row carries the
   effective value and where it came from: `flag`, `env`, `config.toml`,
   `policy.toml`, the project file, or `default`. This is the half of the
   feature that pays for itself immediately, and it is read-only for
   settings that cannot change mid-session (the GCP project, the model —
   changing those means a new backend client).
2. **What can be edited is what can take effect now:** the approval
   policy per tool, auto-approve, auto-compaction, and the theme. Theme
   switching is safe because only `auto` queries the terminal, and that
   detection already happened before Bubble Tea took the keyboard
   (ADR-0002); moving between `dark`/`light`/`plain` rebuilds styles
   without asking the terminal anything.
3. **Persisted changes go to a machine-owned file**,
   `~/.config/gem-agent/policy.toml`, whose first line says gem-agent
   rewrites it. `config.toml` stays hand-written and is never touched, so
   its comments survive. The two are merged, and `policy.toml` wins a
   collision — a change made through the UI must not silently do nothing
   because the hand-written file happens to mention the same tool. The
   panel shows which file each entry came from, so a shadowed entry is
   visible rather than mysterious.
4. **The UI can scope a policy to this project without writing into the
   project.** `policy.toml` carries both `[tools]` and
   `[projects."<path>".tools]`, and the panel has a key to switch which
   one it writes. Writing `<project>/.gem-agent.toml` from a settings
   panel was rejected: adding a file to somebody's repository is a
   surprising side effect of pressing a key, and a loosening entry there
   would be inert anyway unless the project were trusted (ADR-0008 §4).
   The hand-written project file is still read, and shown, read-only.
5. **The approval dialog gains a fourth answer: "never ask again".** It
   writes `never` for that tool into `policy.toml` at global scope and
   says so on a line of its own. This is the flow the feature exists for.
   It is deliberately not the default selection, and it is deliberately
   distinct from the existing `a` (allow for this session): one is a
   session convenience, the other edits a file on disk.
6. **Non-TTY prints the table read-only.** `/settings` in the plain REPL
   and in a pipe shows the same rows without the editor, for the same
   reason the plain REPL exists at all.

## Consequences

- The precedence chain stops being folklore. Four layers with no display
  is a design that assumes the operator remembers; showing provenance per
  row removes the assumption.
- Two files now describe approval policy at global scope. That is a real
  cost — ADR-0005 argued against exactly this shape for the transcript —
  and it is accepted here because the two have different authors: one is
  hand-written and commented, the other is generated. The merge is
  explicit, the winner is stated, and the panel shows which file each
  entry came from.
- `never ask again` puts a persistent weakening of the primary defense
  one keypress away. It is the operator's own decision, made about a tool
  they are looking at, and it is announced when it happens and visible in
  `/settings` afterwards — but it should never become the default
  selection, and the rule-tier Block floor still applies to what it
  allows (ADR-0008 §2).
- The panel is a new TUI phase. Keys inside it are its own; the
  approval dialog and a running turn keep theirs (ADR-0007).

## Alternatives considered

- **View-only** — rejected: it leaves the moment that matters (the dialog
  is on screen, the operator knows the answer) still costing a restart.
- **Edit every setting** — rejected for now: the model and GCP project
  need a new backend client, and `[sandbox].enabled` is not something to
  toggle mid-session from a menu. They are shown, not edited.
- **Write back to `config.toml`** — rejected: it destroys the comments,
  which are the only documentation of the file at the moment someone is
  editing it. Re-emitting them from a template would mean the generator
  owning a file the operator hand-writes.
- **Write `<project>/.gem-agent.toml`** — rejected: see decision 4.
- **A separate `gem-agent config set` subcommand** — a reasonable
  addition later, but it does not solve the case that prompted this: the
  decision happens mid-session, with the dialog on screen.

## References

- ADR-0002 (inline TUI, phases; theme detection happens before start)
- ADR-0007 (keys during a turn; the panel is another phase with its own)
- ADR-0008 (the policy this panel edits, and the Block floor it keeps)
