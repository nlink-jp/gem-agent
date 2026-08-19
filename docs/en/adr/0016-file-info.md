# ADR-0016: file_info — type detection, metadata, hashes

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a file-type judgement (the `file` command's job) and file metadata — size, dates, hashes — as a function |

## Context

The model can read, search, and view files, but it cannot answer "what
*is* this file" without reading it — and for a binary it cannot even do
that (`read_file` refuses them, correctly). The questions this leaves
unanswerable are exactly the ones the operator's IR work starts with:
what kind of executable is this, when did it change, and **what is its
hash** — the org's own malware-lookup MCP consumes MD5/SHA1/SHA256, and
today there is no in-tool way to produce them short of an approval-gated
`shell_exec shasum`.

## Decision

One read-only built-in, `file_info(path)` — also `paths: [...]` for a
batch, the edit_file dual-form precedent — confined to the project like
every model-triggered tool. Per file it reports:

1. **Type, judged from content** — the `file`-command job: MIME sniff
   plus magic refinements for what IR actually meets (Mach-O including
   fat and 64-bit variants, ELF, PE, zip/gzip, PDF, SQLite, shebang
   scripts), and a text/binary verdict with a line count for text. The
   extension is reported but never trusted — the whole point of a type
   judgement is catching the mismatch.
2. **Metadata** — size, mode (executable bit called out), modified
   time, and **birth time**: gem-agent is macOS-only by design, so the
   Darwin-specific field costs nothing that was promised (ADR-0001's
   argument, again).
3. **Hashes — MD5, SHA1, SHA256** in one streaming pass, precisely the
   trio malware-lookup takes, so "hash this and look it up" is one loop
   with no shell round. Oversized files (>512MB) skip hashing with a
   note rather than stalling the turn.
4. **Symlinks are reported, not silently followed**: the link's target
   is shown, and content inspection happens only when the resolved
   target stays in the project — the same containment story as
   everything else.
5. Directories get entry counts and shallow size, no hashes.

## Consequences

- The IR opening moves — identify, date, hash, look up — run inside one
  agent loop, hash trio feeding the MCP tool directly.
- Ten built-ins. The description carries the guidance; the system
  prompt does not grow.
- Type detection is a finite magic table, not libmagic: it names what
  it knows and says `data` honestly otherwise. Extending the table is a
  test-first edit, not a dependency.

## Alternatives considered

- **Just use `shell_exec` (`file`, `shasum`, `stat`)** — rejected as
  the *answer* (it stays available): every call is approval-gated
  mutating-tool ceremony for a read-only question, output formats vary,
  and three commands replace one call.
- **libmagic / a Go magic library** — rejected: a dependency for a
  fallback tool, buying thousands of formats the loop never asks about.
- **Hashing opt-in via a parameter** — rejected: the hash is the
  feature's centre for this operator; one streaming pass alongside the
  type sniff is near-free at project file sizes.

## References

- ADR-0013/0014/0015 (the tool-economy series this extends)
- malware-lookup MCP (the consumer of the hash trio)
