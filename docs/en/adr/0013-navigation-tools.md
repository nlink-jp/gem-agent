# ADR-0013: Project navigation tools — list_tree and search_files

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a tree-structured project listing would help, and a fast in-project search — "not RAG, fast grep" |

## Context

The model's only discovery tool is `list_files`, one directory at a
time. Orienting in an unfamiliar repository therefore costs a round per
directory, and *finding* something costs reading files wholesale — the
most expensive possible grep, paid in context window. Claude Code's
equivalents (Glob, Grep) are the tools its sessions leanmost on for
navigation; the fallback has neither.

The operator's framing sets the ceiling explicitly: not RAG, no index,
no embeddings — fast grep.

## Decision

Two read-only built-ins, both confined to the project like every file
tool, both dependency-free (pure Go — a backup tool must not acquire a
ripgrep prerequisite on the day it is needed).

1. **`list_tree(path?, depth?)`** — a recursive listing of the subtree,
   indented, directories marked with `/`. VCS internals (`.git`, `.hg`,
   `.svn`) are skipped — stated in the tool description, since they are
   plumbing, not project content. Everything else is shown up to
   explicit caps (entries and depth), and hitting a cap prints what was
   left out and how to see it (pass a subdirectory) — no silent
   truncation, per the org lesson.
2. **`search_files(pattern, path?, literal?)`** — regex search (Go
   syntax) across the subtree, results as `path:line: text`. `literal`
   escapes the pattern for models (and operators) who mean the string,
   not the syntax. Binary files are skipped by content sniff, oversized
   files and VCS internals likewise; the match cap is reported when
   hit. Sequential scan — at project scale, pure-Go grep is milliseconds
   to tens of milliseconds, and an index would be a cache with an
   invalidation problem the RFP's scope does not want.
3. **Symlinks are not followed** by either tool — a walk that follows
   links can leave the project through a link the per-path checks never
   see. `read_file` remains the way to read a specific in-project
   symlink, with its resolved-path containment check.

## Consequences

- Orientation drops from a round per directory to one call; search
  stops being "read everything". Both matter doubly here because every
  round replays the whole history (images included) — fewer, smaller
  rounds is the cheapest optimisation this tool has.
- Two more tools in the prompt (eight built-ins now). The working-style
  guidance steers: navigate with `list_tree`/`search_files`, then
  `read_file` the specific files.
- No index means no staleness and no build step; the cost is rescanning
  per call, which is the right trade at project scale by measurement.

## Alternatives considered

- **Shelling out to ripgrep when present** — rejected: a fallback tool
  with an optional dependency has two behaviours, and the drill would
  only ever exercise one of them.
- **An embedding/RAG index** — rejected by the operator in the request
  itself.
- **Extending `list_files` with a recursive flag** — rejected: the flat
  and tree outputs want different caps and formats, and models pick
  tools by name; a second name is clearer than a mode switch.
- **Honouring `.gitignore`** — rejected for now: correct gitignore
  semantics are a project in themselves, and silently hiding files the
  operator can see invites "why can't it find X". The caps plus
  subdirectory narrowing cover the node_modules problem in practice.

## References

- ADR-0001 (project confinement; both tools ride `resolvePath`)
- Org lesson: no silent caps — report what was dropped
