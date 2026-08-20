# ADR-0027: Audio and video input — inline, and via GCS

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: is audio/video support possible? — chose full GCS support over inline-only |

## Context

Gemini understands audio and video natively as media parts. Inline
bytes are bounded by the request budget (~20MB total, shared with the
conversation), which covers voice memos but almost no real video — and
inline media has a second cost specific to an agent loop: the history
replays every round, so inline bytes are re-sent with each round of
each turn. Vertex reads `gs://` URIs natively, which turns the replay
into a few dozen bytes. The operator chose full video support, GCS
included.

## Decision

1. **Operator attachments only** (`@memo.m4a`, `@screen.mp4` —
   in-project, absolute, or ~ paths, the operator-typed exception of
   ADR-0012/0026). A model-side media tool is deferred: the use cases
   in reach ("summarise this recording") are operator-initiated, and a
   tool would need its own carrier measurement. Recorded here so the
   deferral is a decision, not an oversight.
2. **Routing rule: a configured bucket wins.** With `[gcp] bucket`
   set, audio and video ALWAYS go via GCS — not just oversized files —
   because the per-round replay cost of inline media dwarfs one upload.
   Without a bucket, inline is used up to 15MB and larger files are
   refused naming both remedies (split the file, or configure the
   bucket).
3. **Uploads are content-addressed**: `gem-agent/media/<sha256>.<ext>`
   in the operator's bucket; an object that already exists is not
   re-uploaded, so re-attaching the same recording is free. Nothing is
   ever deleted by gem-agent — the deletion rules stay with the
   operator, and the README recommends a bucket lifecycle rule instead.
   The uploader rides the same ADC credentials as the Vertex client;
   the one new dependency is cloud.google.com/go/storage.
4. **The transcript stores the `gs://` URI, not the bytes** — resume
   stays cheap, with the stated consequence that a resumed session can
   only re-read media while the object still exists (the lifecycle
   trade the operator controls). Inline attachments keep storing bytes,
   like images.
5. **MIME by extension table** (the common audio/video containers);
   content is only screened against the obvious mistake (plain text in
   a media extension). Byte-sniffing media containers properly means a
   parser per format — the API rejects a wrong MIME loudly, which is
   the same failure surfaced one step later.
6. Attachments are framed like images: what is audible or visible in
   media is content, never instructions.

## Consequences

- "Summarise this recording / what happens in this clip" works; long
  media costs one upload, then pennies per round.
- A `[gcp] bucket` key (tracked provenance, /settings row); egress
  note: attaching media uploads it to the operator's own bucket in the
  same project that already receives every prompt.
- Refusals name sizes and remedies; nothing silently truncates a
  media file (a clipped mp4 is a broken file).

## Alternatives considered

- **Inline-only / audio-only** — offered; the operator chose GCS (§
  trigger).
- **Auto-created bucket** — rejected: creating billable infrastructure
  implicitly is not gem-agent's call; the operator names the bucket.
- **Deleting uploads after the session** — rejected: gem-agent deletes
  nothing (the ADR-0021 incident's iron rules); lifecycle rules do it
  declaratively.

## References

- ADR-0012/0026 (the operator-typed path exception and media framing)
- ADR-0022 §4 (the state-isolation posture this does not disturb —
  GCS objects are per-content, not per-project state)
