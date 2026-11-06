# ADR-0108: a path is judged where it leads

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-06) — implemented; §1 redesigned after the independent design review |
| Date | 2026-11-06 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Found while verifying ADR-0107: with `--add-dir ~/Dropbox/<folder>`, a `write_file` to `~/Dropbox/<folder>/x.txt` was refused as "absolute path outside the project, session work and added directories", while the same file through `~/Library/CloudStorage/Dropbox/<folder>/x.txt` was written |
| Amends | the file tools' path confinement (`docs/en/reference/architecture.md`, "Go-level path confinement"): a path argument is normalised before the call is judged; the confinement itself and ADR-0072 §4's `os.Root` opens are kept |
| Relates to | ADR-0099 (added roots are stored as real paths), ADR-0101 (a directory, not its spelling), ADR-0107 (synced folders, reached through links such as `~/Dropbox`), the final review's R2 (judge the real path, write by replacement) |

## Context

The roots — the project, the session work directory and the added
directories — are stored as real paths. The file tools confine a path in
`Registry.resolvePath`: the typed path, made absolute and cleaned, must
lie **lexically** inside a root; only then is it resolved, and the real
path must lie inside a root too. The decision point resolves a path
through the same function (`Registry.RealPath`) before the rule tier
judges it, so the tier sees what the file is (R2). `@` mentions in the
input box follow the same two-step check in `mention.resolve`.

A path typed through a link that lies **outside** every root therefore
never reaches the second step. macOS and the cloud providers put such
links in the home directory: `~/Dropbox` → `~/Library/CloudStorage/Dropbox`,
`~/OneDrive` and `~/Google Drive` likewise; an operator may keep
`~/work` → a checkout elsewhere. ADR-0107 made those folders write
roots, and their link is the name the operator and the model know.

Measured on this machine (2026-11-06), with a registry holding a project,
a write root and a read root, and links in a directory outside all of
them:

| Path typed | `RealPath` | Rule tier | `write_file` | `read_file` |
|---|---|---|---|---|
| link → write root, new file | refused | Block, "outside the project and added directories" | refused, "path escapes the project directory" | refused |
| link → write root, existing file | refused | Block | refused | refused |
| link → read root | refused | Block | refused | refused |
| link → the project | refused | Block | refused | refused |
| link → outside every root | refused | Block | refused | refused |

So the gap is not only the tier's: an operator who approves the write at
the Block prompt gets "path escapes the project directory" from the tool
itself, and even a read is refused. The shell lanes are not affected —
the kernel resolves the link, and ADR-0107's measurement wrote through
`~/Dropbox` in the write lane.

The rule tier does no I/O by design (`pathAliases` in `internal/risk`):
it judges strings it is given. It already receives real paths from the
decision point; it receives the typed one only because `RealPath` refused.

## Decision

### 1. The call is rewritten once, where it comes in

When a tool call from the model comes in — before it is shown, judged,
run or audited — each path argument of a built-in file tool is looked at
once: `path` of `read_file`, `write_file`, `edit_file`, `view_image`,
`show_image`, `read_document`, `file_info`, `summarize_file`,
`list_files`, `list_tree` and `search_files`, `write_file`'s `from`, and
every entry of `file_info`'s `paths`. No other tool is touched —
`shell_exec`, `request_directory`, MCP tools, and `datetime`, whose
`from` is a time zone. An argument that is

- an absolute path,
- lexically outside every root,
- not credential material by its spelling (`sandbox.CredentialPath`),
- not under `/Volumes`, `/Network`, `/net` or `/home`, and reached
  through no link that leads into one of them

has its **parent directory** resolved. When that real parent joined with
the typed last element lies inside a root, the argument is replaced by
it. Otherwise the argument is left as typed, and the file tools refuse it
later exactly as today, with today's message.

A root named by its own link — `list_files ~/Dropbox` when the folder
behind `~/Dropbox` is the root — becomes that root: the whole path,
last element included, resolves to a directory that is a root, and
naming a directory writes through nothing.

The rewritten call is the call from then on: the approval prompt shows
it, the decision point judges it, a pre-tool hook receives it, the tool
runs it, and the audit and telemetry record it. The model's own message
in the history keeps what the model sent, so the transcript pairs the
typed call with the result of the real one.

Each condition has a reason:

- **Once, at intake** — the decision point and the tool both resolve the
  same string today, so a link swapped between the verdict and the run
  would be written through a verdict made for another file. That window
  exists for a link inside a root already; extending resolution to links
  outside the roots would have widened it to the shared temporary
  directories a write-lane command may write (design review of this
  ADR). After the rewrite, the link outside the roots is never consulted
  again.
- **The parent only** — the last element stays as typed, so a link there
  is what any link inside a root is: judged on its real name and written
  by replacement, never through (R2).
- **Credential spelling kept** — the floor that judges a path by its
  spelling (`classifyPath`, `ReadCopySource`'s check of both spellings)
  keeps seeing it. A credential name reached through a link into a root
  is still denied by the kernel in every lane but the operator's.
- **No mounts** — resolving a path the model names under `/Volumes`,
  `/Network` or the automounter's `/net` and `/home` would touch a
  share, or wait on a stale one, before anyone approved anything. A
  link that leads into one (`~/nas` → `/Volumes/nas`) stops the
  resolution too. The resolution walks the path one element at a time,
  each joined to a prefix that is already real, and judges that real
  prefix against the trees before reading into it — so a chain of
  links, a relative link taken from the real directory it sits in, the
  data volume's spelling (`/System/Volumes/Data/home`) and a case
  variant (`/VOLUMES`) all stop there (implementation review; then the
  pre-release review, which found the first cut judged only the typed
  spelling and one link's own target).
- **A failure says nothing new** — a parent that does not resolve, or
  resolves outside every root, leaves the argument as typed, so the
  refusal is today's and does not tell whether a link exists there or
  where it leads (ADR-0021).

### 2. `@` mentions follow the same rule

An absolute `@` reference in the input box is rewritten by the same
function before `mention.resolve` confines it. A reference starting with
`~`, or a relative one, is unchanged.

### 3. The confinement and the rule tier are unchanged

`Registry.resolvePath` keeps its order — lexical containment, then the
real path — because what reaches it after §1 is already the real parent.
The rule tier does no I/O and gains none. ADR-0072 §4's `os.Root` opens
are unchanged.

### 4. Tests

- The rewrite function: each row of the measured table (accepted with
  the real path for the project, a write root and a read root; left as
  typed for a link leading out); a last element that is a link stays a
  link; a credential spelling, a path under `/Volumes`, a relative path
  and a lexically inside path are left as typed.
- End to end through the agent loop with a fake model: a `write_file`
  through an outside link to a write root is approved as an edit inside
  the added directory and lands; one to the project's `AGENTS.md` through
  a link is the operator's alone; the approval detail and the call
  shown carry the real path, and the verdict is taken on the string the
  tool runs; the refusal for a link leading out does not
  name its target.
- Swapping the outside link after the rewrite does not change where the
  write lands.
- An absolute `@` reference through an outside link attaches the file.

## Consequences

- `write_file`, `edit_file`, `read_file` and the other file tools reach a
  root through a link the operator or the model knows it by, and what is
  approved, judged, run and recorded is one path, the real one.
- No new reach: the rewrite only ever produces a path inside a root,
  which the file tools then confine as any typed real path.
- **Accepted residual: metadata reads before approval.** Resolving the
  parent reads the metadata of a path the model chose, outside the roots
  — the link and its parents — before approval, where nothing was read
  before. Mounts are excluded (§1). A protected folder (`~/Documents`,
  for one) may make macOS ask whether the terminal may access it; that is
  the operating system's question, about a path the model named.
- **Unchanged residual:** a directory *inside* a root can still be
  swapped for a link by a write-lane command between the verdict and the
  open; ADR-0072 §4's `os.Root` keeps the open inside the root, as it
  does today.
- **Accepted residual:** the resolution runs synchronously where the
  call comes in, before approval and without the turn's cancellation; a
  directory that blocks outside the mount trees would hold the turn
  there.
- **Not covered:** a path typed with `~` is not expanded, by the file
  tools or here; the full path is what reaches the root.
- **Not in scope:** document and media attachments opened by `@` through
  `openConfined` are confined to the project only, not the added roots —
  an existing gap this ADR does not touch.

## Alternatives considered

- **Resolve in `resolvePath`, at each use.** The first draft. Rejected
  by the design review: the verdict and the run would each resolve the
  link, a swap in between would write through a verdict made for another
  file, and resolving the last element would write through a link where
  R2 replaces it.
- **Resolve in the rule tier.** Rejected: the tier does no I/O by design.
- **Record each root's links as aliases at startup.** Rejected: a link
  can be made after startup, and a list of spellings is what ADR-0101
  replaced with one comparison on the directory.
- **Leave it, and tell the model the real paths.** Rejected: the system
  prompt already names the real paths, the operator types the link, and
  the result was a refusal that says the opposite of what is true.

## References

- `internal/agent/agent.go` (the tool-call loop, where §1 runs),
  `internal/agent/decision.go` (`intoRoots`, `pathArgTools`, `withRealPaths`),
  `internal/sandbox/intoroot.go` (`IntoRoot`, `linksIntoMount`), `internal/tools/tools.go`
  (`resolvePath`, `RealPath`, `resolveExisting`), `internal/tools/copysource.go`,
  `internal/tools/fileinfo.go`, `internal/mention/mention.go` (`resolve`)
- ADR-0099, ADR-0101, ADR-0107
