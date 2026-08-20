# ADR-0029: A UI language mode with a complete two-sided catalog

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: help and hint messages mix English and Japanese arbitrarily — unify them behind a Japanese-mode / English-mode concept |

## Context

The interactive chrome grew string by string across nineteen releases,
and each string shipped in whatever language the moment suggested:
`/help` lists commands in English but explains the keys in Japanese,
the approval dialog answers in Japanese while its verdicts print in
English, the trust prompt is English and the queue-refusal warning is
Japanese. No single string is wrong; the set is incoherent, and there
is no mechanism that would ever make it coherent — a new feature adds
its strings in one language and the drift compounds.

## Decision

### 1. `[tui].language = "auto" | "ja" | "en"`, default `auto`

`auto` follows the POSIX message-catalog convention: the first
non-empty of `LC_ALL`, `LC_MESSAGES`, `LANG` decides, a `ja` prefix
selects Japanese, anything else (including `C`/`POSIX`) selects
English. The language is resolved once at startup; `/settings` shows
the row read-only with a restart note. A mid-session switch would
bisect the scrollback and re-render nothing that is already printed —
a consistency feature that itself introduced inconsistency.

### 2. One catalog struct, two complete literals

`internal/uitext` defines a `Messages` struct — one field per
operator-facing string — and exactly two package-level catalogs, `en`
and `ja`. A reflection test walks every field of both and fails on any
empty string. That test is the actual fix: the original problem was
not a bad translation but the absence of anything forcing the two
languages to cover the same surface. A new chrome string now either
lands in both catalogs or fails `make check`.

### 3. Scope: the interactive chrome; four surfaces stay English

Cataloged: `/help`, the approval dialog (labels, hint, verdicts,
hidden-line warning), input chrome (placeholder, queue notices,
interrupt/error markers), the settings panel hint, `/auto`, `/clear`
and `/compact` feedback, and the ADR-0023 startup-safety prompts.

Deliberately not cataloged, in either mode:

- **banner labels and `warning:` lines** — log-shaped output that the
  operator greps and pastes into issues; stable English tokens are the
  feature;
- **cobra `--help` / CLI flags** — CLI convention, and cobra's own
  scaffolding text is English anyway;
- **model-facing text** — the system prompt and tool descriptions are
  written for the model, not the operator; the model's response
  language follows the conversation, not this setting;
- **Go error chains** — wrapped causes come from libraries in English;
  a translated prefix on an English chain is the mixing this ADR
  removes.

## Consequences

- The operator-facing chrome is monolingual in either mode, and stays
  that way mechanically (the completeness test).
- Every string moved into the catalog is one more indirection; the
  code reads `m.msgs.QueueRefused` where it used to read the text.
  Accepted: the literal-in-place style is exactly what produced the
  drift.
- `auto` means a `ja_JP` terminal changes appearance on upgrade. The
  old mixed display was not a state worth preserving; `language =
  "en"` restores full English.
