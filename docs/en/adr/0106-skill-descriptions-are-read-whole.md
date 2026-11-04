# ADR-0106: a skill's description is read as Claude Code reads it — up to 1,024 characters, multi-line YAML included

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-04) |
| Date | 2026-11-04 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Issue #3 (2026-11-04): skill descriptions are cut at 400 runes, so skills written to the 1,024-character limit lose their triggers. A second defect was found in the same code while checking the issue |
| Amends | ADR-0010 (the description line it left without a length) |
| Relates to | nlink-jp/.github ADR-027 (tool guidance layers; skills bundled with tools are written to the same limit) |

## Context

ADR-0010 reads a skill directory "as-is" in Claude Code's format. A skill costs one description line in the system prompt until it is loaded, and the description is "the load-bearing half of progressive disclosure": it alone decides whether the skill is loaded. ADR-0010 gave the line no length.

The code (checked against `4848c71`) does two things that read less of the format than Claude Code does.

1. **The description is cut at 400 runes** (`internal/skills/skills.go`, `DefaultLimits().MaxDescription`), with nothing said about it. Claude Code and the organization's skill validator accept 1,024 characters. Measured over the nine skills in skills-series, six are longer than 400:

   | Skill | Characters | Where 400 falls |
   |---|---|---|
   | mcp-tactics | 1,020 | right after "Splunk logs;": BigQuery, data analysis, browser, audio, transcription, second opinion and every Japanese trigger are lost |
   | news-digest | 644 | — |
   | compliance-review | 594 | inside the Japanese triggers |
   | weather-forecast | 580 | 週末の天気, 雨が降るか, 傘が要るか, 気温 are lost |
   | incident-review | 578 | before the Japanese triggers |
   | incident-research | 509 | before the Japanese triggers |

2. **A multi-line description is read as its first line.** The frontmatter parser is minimal: one `key: value` per line, indented lines skipped. YAML's block scalars (`description: >` or `|` followed by indented lines) and plain scalars continued on indented lines are valid in Claude Code's format. Measured: `description: >` followed by two indented lines is read as the description `>`, one character. Every trigger of such a skill is lost, and the skill is still listed. No skill the organization ships uses this form today.

## Decision

### 1. The description is shown up to 1,024 characters

`MaxDescription` becomes 1,024, the limit Claude Code and the organization's validator hold skills to. A longer description is still cut, and the cut is reported to the operator among the discovery notes: `skill "<name>": description is <n> characters; only the first 1024 are listed`. A skill whose triggers are hidden is then visible, and that was the gap.

The idle cost grows by at most about 624 characters for each skill longer than 400. `MaxSkills` (100) already bounds the number of lines. For the nine skills of skills-series the listing grows by about 1,700 characters.

### 2. Multi-line YAML values are read

`parseFrontmatter` reads a value that continues on the following more-indented lines:

- **Block scalars.** `>` folds the lines into one, joining them with spaces. A blank line becomes a line break. `|` keeps the line breaks. The chomping indicators (`-`, `+`) and an explicit indentation digit are accepted. The value is trimmed afterwards, as every description already is.
- **Plain scalars continued on indented lines** are folded with spaces, as YAML does.

The common indentation is removed. The block ends at the first line that is not indented and not blank. This is still not a YAML parser: quoted values spanning lines, anchors and flow collections stay unread. They are not used in skill frontmatter, and a full parser is a dependency the organization does not take for this (supply-chain rule).

## Consequences

- Skills written to the platform's limit trigger in gem-agent on everything they declare.
- The system prompt grows with long descriptions, within the bound above.
- A description longer than 1,024 characters is cut with a note, not silently.
- lagent has the same 400-rune limit and the same parser. It decides for itself.

## Alternatives considered

- **Keep 400 and add a note.** The note would fire for six of the nine skills the organization ships, and their triggers would stay unseen. 400 has no recorded reason. ADR-0010 names Claude Code's format as the contract, and this is the one place gem-agent read less of it.
- **No limit.** A description is operator-installed text, but an unbounded line in every prompt is the cost ADR-0010 set out to bound. The platform's own limit is the bound skill authors already write to.
- **A YAML library.** It would read the forms named above and many more, at the cost of a third-party dependency for one field.

## References

- Issue #3; `internal/skills/skills.go` (`DefaultLimits`, `readSkill`, `parseFrontmatter`)
- nlink-jp/.github `templates/validate-skill.sh` (the 1,024-character check)
