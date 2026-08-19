# ADR-0014: Context economy — summarize_file and partial reads

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a file-summary tool on a lightweight model, separate from the main reasoning model; and partial file reads — read_file currently reads everything |

## Context

Both requests attack the same cost from different ends. `read_file` is
all-or-nothing: to answer one question about a 2000-line file, the model
pulls all 2000 lines into a history that is then **replayed on every
subsequent round**. ADR-0013 made *finding* things cheap; the expensive
step left is *reading* them.

Two shapes of waste:
- The model needs lines 560–575 and pays for the whole file — worse,
  keeps paying for it every round afterwards.
- The model needs the gist of a document, not its text — and the gist
  could be produced by a model that costs a fraction of the one doing
  the reasoning.

## Decision

### Partial reads: `read_file` gains `start_line` / `end_line`

1. Optional, 1-based, inclusive; absent means today's behaviour. The
   slice is annotated — `[showing lines A–B of N]` — as a trailing note
   in the established truncation style, so a partial view can never
   masquerade as the whole file. A `start_line` beyond the end is an
   error naming the file's real length.
2. Content stays raw — **no line-number prefixes**. Numbered output
   reads nicely but poisons the edit loop: a model that copies what it
   read into `edit_file`'s old_string would embed the numbers and every
   edit would miss. The pairing works the other way: `search_files`
   reports `path:line`, and the model asks for a window around it.

### `summarize_file(path, focus?)` — a summary instead of the bytes

3. A read-only tool that sends the file to a summariser and returns a
   short summary; `focus` narrows what to look for. The main loop's
   history then carries the summary, not the file — and unlike a full
   read, that saving repeats on every round the conversation still has
   left.
4. **The summariser model is configuration**: `[model].summary`, unset
   meaning the main model. The operator's request — a *lightweight*
   model — is the point of the knob; the tool is still worth having
   without it (a summary in history beats a file in history even at
   main-model prices), which is why unset does not disable the tool.
   The second model shares the main model's Vertex client (same
   project, location, credentials) — model choice is per-call, so this
   is a name, not a second connection. Model names stay config-driven,
   never compiled in (org rule).
5. **The summariser is the compaction pattern applied to one file**
   (ADR-0006): file content is untrusted, so it arrives nonce-wrapped
   with the defensive framing first, the call offers no tools, and a
   blocked or empty response is a reported error, never a silent empty
   summary. The summary itself returns as an ordinary tool result and
   gets the ordinary tool-result wrap — derived-from-untrusted stays
   untrusted, no exemption (contrast ADR-0010, where the content is
   operator-authored).
6. Reuse over invention: the tool reads the file through `read_file`'s
   own path — same confinement, same image refusal, same size cap and
   truncation note (a summary of a truncated read says so).
7. Summariser tokens are not yet in the footer counters: the footer's
   context gauge tracks the main conversation, and mixing in a side
   call's prompt tokens would misstate occupancy. Recorded in the
   session log instead; a separate cumulative counter is a refinement
   for later if the spend turns out to matter.

## Consequences

- The read-everything tax becomes opt-in: windows for precision,
  summaries for gist, whole files only when the whole file is the
  point. The system prompt steers accordingly.
- `[model].summary` finally gives the "cheaper second model" slot that
  ADR-0004 waved at for risk evaluation and did not build. If a light
  model proves itself here, pointing the risk tier at it is a natural
  follow-up — separate decision, not taken now.
- A summary is lossy and the model may act on it as if it were not.
  The result text names itself as a summary and names the model that
  wrote it; for anything load-bearing the guidance stays "read the
  actual lines".
- One more knob, one more tool (nine built-ins). Both idle at zero
  cost: the knob defaults to the main model, the tool to one prompt
  line.

## Alternatives considered

- **Line numbers in read_file output** — rejected: poisons edit_file's
  exact-match contract (see §2).
- **A separate `read_lines` tool** — rejected: same operation, second
  name; models pick tools by name and would have to learn when each
  applies. Parameters on the existing verb are the smaller surface.
- **Registering summarize_file only when `[model].summary` is set** —
  rejected: the context saving does not depend on the model being
  cheaper, and a tool that appears and disappears with configuration is
  harder to build habits on.
- **Byte offsets instead of line ranges** — rejected: everything nearby
  speaks lines (`search_files` output, compiler errors, stack traces).
- **Embedding-backed retrieval** — out of scope by the operator's own
  ceiling in ADR-0013 ("not RAG").

## References

- ADR-0006 (compaction; the summariser pattern reused per-file)
- ADR-0010 (the unwrap exemption this tool deliberately does not get)
- ADR-0013 (navigation; this closes the read half of the same cost)
