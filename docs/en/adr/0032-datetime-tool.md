# ADR-0032: A deterministic date/time tool

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-21 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a built-in tool for the current date/time, and one for date arithmetic — LLMs are said to be bad at calendar math |

## Context

The model has no clock: nothing in the system prompt or the tool set
tells it today's date, so "what day is it" is a guess anchored to its
training cutoff, and every relative expression the operator uses
("since last Tuesday", "by the end of next month") silently resolves
against the wrong now. Calendar arithmetic is worse than absent — it
is confidently wrong: month lengths, leap years, ISO weeks, and
timezone offsets are exactly the kind of carry-heavy symbol
manipulation language models fumble while sounding sure. The nearest
workaround, `shell_exec date`, spends an approval (or an auto-approve
risk evaluation) on a computation with zero risk.

## Decision

### 1. One read-only tool, `datetime`, with five operations

`now` (the default), `info`, `add`, `diff`, `convert` — selected by an
`op` argument. One tool, not two: "current time" is just the
arithmetic tool evaluated at zero, the registry stays small, and the
model learns one name. `Mutating: false` — no approval, like
`agent_info` (ADR-0030): it discloses a clock.

- **now**: local time (RFC3339), timezone name and offset, UTC, unix
  seconds, weekday, ISO week.
- **info** (`date`): weekday, day of year, ISO week, days in that
  month, leap year.
- **add** (`base`, signed `years/months/days/hours/minutes`): the
  shifted moment plus its weekday. Go's `AddDate` normalization is
  kept and stated in the output when it fires (Jan 31 + 1 month =
  Mar 3): silently normalized dates are how "one month later" bugs
  ship.
- **diff** (`a`, `b`): signed calendar breakdown (years/months/days),
  total days/hours/minutes, and both endpoints' weekdays. No
  business-day count: weekday arithmetic without a holiday calendar
  is wrong in exactly the cases it would be used for (Japanese
  national holidays), and a wrong number with an authoritative shape
  is worse than none.
- **convert** (`time`, `to`, optional `from`): IANA-zone conversion;
  an explicit offset in the input wins over `from`.

Dates parse as RFC3339, `2006-01-02`, or a naive
`2006-01-02[T ]15:04[:05]` interpreted in the operation's timezone
argument (default: local). Errors name the accepted forms.

### 2. The session-start date rides the system prompt

One line — date, weekday, timezone — so passive references resolve
without a tool round. It is deliberately the SESSION-START date, which
is cache-stable (ADR-0018: a per-request timestamp would bust the
prefix cache every turn) and honest about drift: the line itself says
to call `datetime` for the current moment.

### 3. Implementation shape

`internal/tools/datetime.go`, registered with the other built-ins;
pure computation over an injectable clock (`timeNow` package variable)
so tests pin exact values. Output is compact key: value text, English
(model-facing, ADR-0029 §3). Timezone database is the host's
`/usr/share/zoneinfo` — always present on the macOS this project
binds to.

## Consequences

- "今日は何曜日?", "この 2 つの日付の間は何日?", timezone conversions
  and deadline math become one deterministic call instead of confident
  guesses or an approval-gated `date`.
- The clock the model sees is the host's: a wrong system clock
  propagates. Accepted — every alternative anchors on the same clock.
- Month-end normalization semantics are surfaced, not hidden; business
  days are refused, not approximated.
