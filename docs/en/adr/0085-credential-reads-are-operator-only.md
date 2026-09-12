# ADR-0085: Credential paths are operator-only for the read tools too

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-13) — implemented and unreleased |
| Date | 2026-09-13 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | System risk review 2026-09-13 (gem-agent v0.77.3 / lagent v0.3.3), chapter 10 and finding R02: `read_file .env` inside the project is Safe, and its content flows to the model and into the transcript. The credential list is enforced by the lanes, the write tools and the shell floor — never by the read tools — and "the write tools are protected" must not be read as "the read tools are" |
| Amends | ADR-0073 §3 (one list, three enforcers — now four) |
| Relates to | ADR-0072 §1.4 / §4.5 (OperatorOnly is a floor like Block), ADR-0070 §3 (Block stays the floor for the irreversible), ADR-0052 (every skip is reported), ADR-0037 (the file-search child's gate denies), ADR-0076 (the finite list stays the mechanism) |

## Context

`sandbox.CredentialFilters` / `sandbox.CredentialPath` is the one list
of credential locations (ADR-0073 §3): the home directories and files
that hold the operator's keys and tokens, and the names that are
secrets wherever they sit — `.env` and its variants (the committed
templates `.env.example` / `.sample` / `.template` / `.dist`
re-allowed), `id_rsa` and kin, `credentials.json`,
`*service-account*.json`, `application_default_credentials.json`.
Three parties enforce it: the Seatbelt read and write lanes deny
`file-read*` under it, so a read-lane `cat .env` fails at the kernel;
`risk.Classify` makes `write_file` / `edit_file` to a matching path
Block; a shell command naming one hits the Block floor in every lane.

The read tools — `read_file`, `view_image`, `read_document`,
`file_info`, `summarize_file`, and the walks `search_files`,
`list_tree`, `list_files` — never consult it. They are non-mutating:
`Tool.Mutating` is false, `risk.Classify` answers Safe before it looks
at the path, and `gated()` passes them in every mode. So `read_file
.env` in a project that has one runs without a prompt under the
default gate, under `--auto`, under a `"never"` policy and in `-p`,
and the file's content is in the model's context for the rest of the
session and in the transcript on disk. The review states it plainly
(chapter 10): the direct read checks the roots and not the credential
names; the value read flows to inference and to history; write
protection must not be read as read protection. R02 rates it high.

The asymmetry has a shape the runtime already knows. In the shell,
credentials are readable in the operator lane only — the lane the
operator approves every time, because the lane can read them (ADR-0073
§1). The file tools have no lane; the analogue of "the operator lane"
is the operator's own answer. Two verdicts were considered and set
aside:

- **Block.** Block is the floor for the irreversible (ADR-0070 §3), and
  reading a secret with the operator's yes is legitimate work —
  rotating a key, debugging an environment file, checking what a
  service-account file grants. A Block would send that work to the
  shell's operator lane, which is a worse place to read a file than
  the file tools (ADR-0072 §4: `os.Root`, bounded, no execution).
- **The model tier.** The party that proposed the read cannot judge
  whether its content should reach it: the evaluator-is-the-proposer
  objection of ADR-0020 §4, applied by ADR-0072 §4.5 to the files
  later sessions trust, applies to the files that hold the operator's
  secrets exactly as well.

## Decision

### 1. A read tool on a credential path is Review, operator-only

`risk.Classify` judges the five named read tools before the
non-mutating shortcut: when `path` — or, for `file_info`, any entry of
`paths` — matches the credential rule, the verdict is `Review` with
`OperatorOnly`. The rule is the write tools' own: `sandbox.CredentialPath`
on the whole path (the shell floor's word-splitting `hasCredentialPath`
is for command text — a file named `notes about .ssh keys.md` is one
path, and a name that merely ends in a list entry, `keys.ssh`, is
ordinary; independent review, A2/A10), the `.env.example` / `.sample` /
`.template` / `.dist` re-allow included, names folded like the rest of
the list. No second list exists; the read tools read the one in
`internal/sandbox`, and `risk.JudgesPath` is the one list of the tools
whose path the agent resolves before judging. The reason names the
path that matched, project-relative — `reads credential material
(sub/.env)` — because a `paths` batch of twenty can push the entry
past the approval detail's clip, and a link's spelling is not its
target (A4).

Judged on the real path. `Agent.decide` resolves `path` and `paths`
through `Registry.RealPath` for the read tools as it does for
`write_file` / `edit_file` (final review R2 of ADR-0072): a link named
`notes.txt` that points at `.env` is a `.env` read. A spelling that
does not resolve — outside the roots, a broken link — is judged as
spelled, so the attempt is in front of the operator either way, as it
is for the write tools.

What `OperatorOnly` already means, mode by mode, now holds for these
reads with nothing new in the gate — `gated()` routes
`Mutating || Floor()`, and `decideAuto` returns before the model tier
on `OperatorOnly` (ADR-0072 §4.5, §4.9); the tests pin each row:

| Mode | A credential read |
|---|---|
| default gate | prompts; an earlier `a` for `read_file` does not answer it |
| `--auto` | prompts; the model tier is never consulted |
| `"never"` policy, `--allow read_file` | prompts; the floor is not lifted |
| `-p` | denied, the reason on stderr |
| the file-search child (ADR-0037) | its gate denies; the child reports the refusal, the operator is not asked about a conversation they cannot see |

The `search_files`, `list_tree` and `list_files` walks are not in the
list of judged tools: a walk is one call over many files, and they do
not prompt (below). `@` attachments are unchanged: an `@.env` is typed
by the operator, and that is the operator's yes.

### 2. The walks skip a credential-named entry and say so

`search_files` never reads a credential-named file, and never descends
a credential-named directory; `list_tree` and `list_files` do not list
them. Each reports what it withheld as a count — `[N credential-named
entries skipped — reading one needs the operator's approval]` — the
shape ADR-0052 gave every skip, beside the ignore tally and the caps.

Skipped, not prompted: a prompt per file inside a listing is not a
decision anyone can take, and a walk that stopped to ask would be a
walk the model learns not to run. The names are withheld too, not
only the content: the walks are the model's orientation, and a listing
that names `.env` beside `config.yaml` is the read that asks, offered
on every round; with the count line the model knows that something
was withheld and why, and the operator who wants it read names it.
The rule is the same `sandbox.CredentialPath`, on the entry's real
path: the walk's root is resolved before anything is judged — a link
named `mylink` at `.aws` is `.aws`, and a credential-named root is
never entered, the whole call answering with the count line — and
nothing below the root is a followed link (ADR-0013 §3), so an entry's
real path is the resolved root plus its position (independent review,
A1: the first cut judged the root as spelled, and `search_files
path="mylink"` read `.aws/credentials`). A project `.claude/` (skills)
is not `~/.claude` (tokens), as the profile already distinguishes.

### 3. One list, four enforcers

Nothing is added to any list. The read tools' verdict and the walks'
skip read `sandbox.CredentialPath`; ADR-0073 §3's "three enforcers" —
the profile builder, the write tools' verdict, the shell Block floor —
becomes four, and the amendment there says so. A credential location
added in `internal/sandbox` is denied to the unasked lanes, Block for
the write tools, a Block floor for the shell, operator-only for the
read tools and withheld from the walks, in one edit.

## Consequences

- **One prompt per credential read, in every mode that has an
  operator; a denial where none does.** The price of the evaluator not
  being the proposer, as for `AGENTS.md`. A session that needs to read
  `.env` repeatedly pays a prompt each time — `a` does not stick, by
  design, and `"never"` does not lift it.
- **What does not change.** The committed templates read as before;
  the write side (Block) is untouched; the shell lanes are untouched;
  `@` attachments are untouched; the model tier still never sees a
  credential path on either side.
- **Not covered.** A secret in a file the list does not name — a token
  in `~/.config/<tool>/config.toml`, a password in `notes.md` — is
  readable as before. The list is finite and named, not DLP (ADR-0076
  §1; the review says so in chapter 10). The remedy for that class is
  the review's: no real secrets in a working copy the agent reads.
- **The unattended denial text still names "the read-only file
  tools" as what runs without approval.** True for every read but
  this one; the `[denied: …]` line carries the verdict's reason, so a
  one-shot run's log says why the exception was refused.
- **Tests.** The rule-tier corpus (each read tool on `.env`, an
  `.env.local`, `id_rsa`, `credentials.json`, a home-anchored
  `~/.ssh/id_rsa`, a `file_info` batch with one credential path, the
  case fold; `.env.example`, `.envrc` and ordinary files stay Safe;
  the walks stay Safe on any path); the agent's gate (`read_file .env`
  reaches the gate as must-prompt while `read_file notes.txt` passes
  ungated; a symlink to `.env` is judged by its target; a `"never"`
  policy keeps the prompt; `--auto` prompts with the model tier
  unconsulted; an unattended run denies and the content never enters
  the history); the walks (`.env` and a `.ssh/` directory withheld
  with a count, `.env.example` searched and listed).

## Independent review (2026-09-13, before v0.78.0)

A reader who did not write the change reviewed the release diff
(CONVENTIONS §Verify with an independent pass). Findings and what was
done with each:

| # | Finding | Outcome |
|---|---|---|
| A1 (Medium) | The walks judged the spelled path: `search_files path="mylink"` with `mylink → .aws` read `.aws/credentials` | Adopted — the root is resolved and a credential-named root is never entered (§2) |
| A2 (Low) | `CredentialPath` matched a list entry as a suffix (`keys.ssh`, `my.netrc`) | Adopted — whole path segments (§1) |
| A10 (Nit) | The file tools judged a path through the shell floor's word splitter | Adopted — the whole path (§1) |
| A4 (Low) | The prompt did not name the matched path in a `paths` batch | Adopted — the reason names it (§1) |
| A8 (Nit) | Two hand-kept copies of the read-tool list | Adopted — `risk.JudgesPath` |
| A9 (Nit) | `list_tree` said "(empty directory)" beside the skip note | Adopted |
| C | The scrub test could not show the export exemption's effect | Adopted — `keepEnvName` is tested with a secret-looking name listed and unlisted |
| B | The child row and the `paths` batch were untested | Adopted — tests added |
| A3 (Low) | A project rooted under `~/.claude` or a `/x.ssh/` path makes every read a prompt | Not adopted: the profile already denies those reads to the unasked lanes, and a project inside a credential store is the operator's placement; the count line names the rule |
| A5 (Low) | A spelling that does not resolve prompts for a read that then fails at the open | Not adopted: the write tools show the same attempt as Block; the prompt is the operator seeing it, as §1 says |
| A6 (Low) | The read-only ceiling (ADR-0080) does not refuse a credential read | Not adopted: the ceiling bounds what a session changes, and a read changes nothing — the prompt is the control; recorded here so the ADR-0080 analogy is not read as a ceiling rule |
| A7 (Low) | A link retargeted between `withRealPaths` and the open is read as judged | Not adopted now: the same class as the write tools' check-then-open (ADR-0072 §4 refuses an escape at the open, not a retarget inside the roots); a design pass, not a release patch |
