#!/usr/bin/env bash
# Verify that docs/en/ and docs/ja/ are full structural mirrors.
#
# Every docs/en/PATH/X.md must have a paired docs/ja/PATH/X.ja.md,
# and vice versa. This enforces the org's mandatory en/ja mirror rule
# (CONVENTIONS.md §Documentation): English carries no language suffix,
# Japanese carries .ja.md, and neither language may gain a document the
# other lacks. Drift here is invisible in review — a missing mirror looks
# exactly like a document nobody has written yet.
#
# Exit 0 = in sync; exit 1 = drift detected (with diagnostic
# output on stderr listing the unpaired files).
#
# Intended usage:
#   - manual: ./scripts/docs-mirror-check.sh
#   - `make check`
#   - pre-commit hook

set -euo pipefail

# Run from repo root regardless of where the script is invoked.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

if [ ! -d docs/en ] || [ ! -d docs/ja ]; then
    echo "ERROR: docs/en and docs/ja must both exist." >&2
    exit 1
fi

# Build canonical path keys (relative to docs/{en,ja}/, .ja stripped on ja side).
en_keys=$(find docs/en -type f -name '*.md' | \
    sed -e 's|^docs/en/||' -e 's|\.md$||' | sort)

ja_keys=$(find docs/ja -type f -name '*.md' | \
    sed -e 's|^docs/ja/||' -e 's|\.ja\.md$||' -e 's|\.md$||' | sort)

# comm -23: lines only in left (en files with no ja mirror)
# comm -13: lines only in right (ja files with no en mirror)
missing_in_ja=$(comm -23 <(echo "$en_keys") <(echo "$ja_keys") || true)
missing_in_en=$(comm -13 <(echo "$en_keys") <(echo "$ja_keys") || true)

errors=0

if [ -n "$missing_in_ja" ]; then
    echo "ERROR: docs/en files with no paired Japanese mirror in docs/ja/:" >&2
    echo "$missing_in_ja" | while read -r key; do
        [ -z "$key" ] && continue
        echo "  docs/en/${key}.md  →  expected docs/ja/${key}.ja.md" >&2
    done
    errors=$((errors + 1))
fi

if [ -n "$missing_in_en" ]; then
    echo "ERROR: docs/ja files with no paired English mirror in docs/en/:" >&2
    echo "$missing_in_en" | while read -r key; do
        [ -z "$key" ] && continue
        echo "  docs/ja/${key}.ja.md  →  expected docs/en/${key}.md" >&2
    done
    errors=$((errors + 1))
fi

if [ "$errors" -ne 0 ]; then
    echo "" >&2
    echo "docs/en and docs/ja must be full structural mirrors." >&2
    exit 1
fi

count=$(echo "$en_keys" | wc -l | tr -d ' ')
echo "OK: docs/en and docs/ja are in mirror sync (${count} files)."

# --- AGENTS.md keeps its sections ---------------------------------------
# An E2E script once ran in this repository instead of its fixture and
# replaced AGENTS.md with a two-line stub; the commit went through. The
# file that briefs every agent must keep the sections AGENTS.md promises.
agents_errors=0
for heading in "## Build / test" "## Structure" "## Gotchas"; do
    if ! grep -q "^${heading}\$" AGENTS.md; then
        echo "ERROR: AGENTS.md lost its '${heading}' section" >&2
        agents_errors=$((agents_errors + 1))
    fi
done
if [ "$agents_errors" -ne 0 ]; then
    exit 1
fi
echo "OK: AGENTS.md keeps its sections."

# --- ADR index completeness and order ---------------------------------
# Every ADR file must be listed in its language's INDEX, and the listed
# entries must be in ascending order. Both failure modes shipped: an
# entry inserted above the previous number read as "0032 is missing"
# to anyone scanning the ascending list.
adr_errors=0
for lang in en ja; do
    index="docs/${lang}/INDEX.md"
    [ "$lang" = "ja" ] && index="docs/ja/INDEX.ja.md"
    have=$(ls "docs/${lang}/adr" | grep -o '^[0-9]\{4\}' | sort)
    listed=$(grep -o '^- \[`ADR-[0-9]\{4\}' "$index" | grep -o '[0-9]\{4\}')
    for n in $have; do
        if ! echo "$listed" | grep -q "^${n}$"; then
            echo "ERROR: docs/${lang}/adr/${n}-*.md is not listed in ${index}" >&2
            adr_errors=$((adr_errors + 1))
        fi
    done
    if [ "$(echo "$listed" | sort -c 2>&1 | wc -l | tr -d ' ')" != "0" ]; then
        echo "ERROR: ADR entries in ${index} are not in ascending order:" >&2
        echo "$listed" | tr '\n' ' ' >&2; echo "" >&2
        adr_errors=$((adr_errors + 1))
    fi
    dup=$(echo "$listed" | sort | uniq -d)
    if [ -n "$dup" ]; then
        echo "ERROR: duplicate ADR entries in ${index}: $dup" >&2
        adr_errors=$((adr_errors + 1))
    fi
done
if [ "$adr_errors" -ne 0 ]; then
    exit 1
fi
echo "OK: ADR index complete and ordered in both languages."

# --- INDEX links resolve ----------------------------------------------
# The check above matches ADR files to entries by number, and a number
# survives a rename: the ADR-0080 rewrite (2026-09-09) renamed both
# files and left both indexes pointing at a path that no longer
# existed, with every line above still green. A number is not a link.
link_errors=0
for lang in en ja; do
    index="docs/${lang}/INDEX.md"
    [ "$lang" = "ja" ] && index="docs/ja/INDEX.ja.md"
    dir=$(dirname "$index")
    targets=$(grep -o ']([^)]*)' "$index" \
        | sed 's/^](//; s/)$//; s/#.*$//' \
        | grep -v '^[a-z][a-z0-9+.-]*:' \
        | grep -v '^$' | sort -u)
    for t in $targets; do
        if [ ! -e "${dir}/${t}" ]; then
            echo "ERROR: ${index} links to ${t}, which does not exist" >&2
            link_errors=$((link_errors + 1))
        fi
    done
done
if [ "$link_errors" -ne 0 ]; then
    exit 1
fi
echo "OK: every INDEX link resolves."

# --- identifier parity between each en/ja pair -------------------------
# Pairing alone proved too weak. A capability documented in one language
# only passes every check above: README.md lost the terminal-diagram
# sentence for six releases while README.ja.md carried it, because the
# pair existed and nobody compares content.
#
# Full prose comparison is impossible across a translation, but the
# things that actually go stale — tool names, config keys, CLI flags,
# slash commands — are identifiers a translation must NOT change. This
# compares exactly those, in backticks, outside fenced blocks, and
# ignores anything a translator legitimately rewrites (placeholders like
# `<escaped-path>`, filenames, prose in either language). Measured over
# the whole doc set at introduction: 55 pairs, 0 differences, and it
# found three real one-sided identifiers on its first run.
#
# The root READMEs are included: they are a mirror pair too, and the
# structural check above only walks docs/.
python3 - <<'PY' || exit 1
import glob, io, os, re, sys

IDENT = re.compile(
    r"^(?:--[a-z][a-z0-9-]*"          # --flag
    r"|/[a-z]+"                        # /command
    r"|[a-z][a-z0-9]*(?:_[a-z0-9]+)+"  # snake_case identifier
    r"|\[[a-z_]+\]\.[a-z_]+"          # [section].key
    r"|[a-z_]+\.[a-z_]+)$"             # section.key
)


def idents(path):
    # A fence opens and closes at the start of a line. Matching ``` anywhere
    # paired an escaped fence quoted in prose (```` ```mermaid ````) with the
    # next real one and silently blanked every identifier between them —
    # forty lines of interface.md, which is the opposite of what this check
    # is for.
    # An opener is exactly three backticks at the start of a line, with an
    # optional language; a closer is three backticks alone on a line. The
    # first fix anchored to line starts, which still let a line-initial
    # ```` (an escaped fence, as in ADR-0063) open a span and blank
    # everything to the next real fence.
    text = re.sub(r"(?m)^```(?!`)[^\n]*\n[\s\S]*?^```[ \t]*$", "",
                  io.open(path, encoding="utf-8").read())
    found = set()
    for m in re.finditer(r"`([^`\n]+)`", text):
        tok = m.group(1).strip()
        if tok.endswith((".md", ".json", ".toml")):
            continue
        if IDENT.match(tok):
            found.add(tok)
    return found


pairs = []
for en in sorted(glob.glob("docs/en/**/*.md", recursive=True)):
    ja = "docs/ja/" + en[len("docs/en/"):-3] + ".ja.md"
    if os.path.exists(ja):
        pairs.append((en, ja))
for en, ja in (("README.md", "README.ja.md"),):
    if os.path.exists(en) and os.path.exists(ja):
        pairs.append((en, ja))

bad = 0
for en, ja in pairs:
    a, b = idents(en), idents(ja)
    if a != b:
        bad += 1
        print(f"ERROR: identifiers differ between {en} and {ja}", file=sys.stderr)
        if a - b:
            print(f"  only in {en}: {' '.join(sorted(a - b))}", file=sys.stderr)
        if b - a:
            print(f"  only in {ja}: {' '.join(sorted(b - a))}", file=sys.stderr)

if bad:
    print("", file=sys.stderr)
    print(
        "A tool name, config key, flag or slash command is documented in one "
        "language only. Document it in both, or (if it is prose rather than an "
        "identifier) drop the backticks.",
        file=sys.stderr,
    )
    sys.exit(1)

print(f"OK: identifiers agree across all {len(pairs)} en/ja pairs.")
PY

# --- an en/ja pair's mermaid diagrams have the same shape ---------------
# Every check above strips fenced blocks, so a mermaid diagram is the one
# thing in the doc set nothing compares: an edge added on the English
# side and not the Japanese would pass the whole file. Labels are
# translated and cannot be compared, but the graph must not be — the
# node ids, the edges and the header are the diagram, and a translation
# that changes them has changed the architecture in one language only.
python3 - <<'PY' || exit 1
import glob, io, os, re, sys


def blocks(path):
    text = io.open(path, encoding="utf-8").read()
    return re.findall(r"(?m)^```mermaid[ \t]*\n([\s\S]*?)^```[ \t]*$", text)


def skeleton(src):
    # Quoted node labels and |edge labels| are the translated parts.
    s = re.sub(r'"[^"]*"', '""', src)
    s = re.sub(r"\|[^|]*\|", "||", s)
    return [re.sub(r"\s+", " ", l).strip() for l in s.split("\n") if l.strip()]


bad = 0
for en in sorted(glob.glob("docs/en/**/*.md", recursive=True)):
    ja = "docs/ja/" + en[len("docs/en/"):-3] + ".ja.md"
    if not os.path.exists(ja):
        continue
    a, b = blocks(en), blocks(ja)
    if len(a) != len(b):
        bad += 1
        print(f"ERROR: {en} has {len(a)} mermaid diagram(s), {ja} has {len(b)}",
              file=sys.stderr)
        continue
    for i, (x, y) in enumerate(zip(a, b), 1):
        sx, sy = skeleton(x), skeleton(y)
        if sx != sy:
            bad += 1
            print(f"ERROR: mermaid diagram {i} differs in shape between {en} and {ja}:",
                  file=sys.stderr)
            for line in sorted(set(sx) - set(sy)):
                print(f"  only in en: {line}", file=sys.stderr)
            for line in sorted(set(sy) - set(sx)):
                print(f"  only in ja: {line}", file=sys.stderr)
            if set(sx) == set(sy):
                print("  (same statements, different order)", file=sys.stderr)

if bad:
    print("", file=sys.stderr)
    print("Translate the labels, not the graph: the node ids, edges and header "
          "must match.", file=sys.stderr)
    sys.exit(1)

print("OK: mermaid diagrams have the same shape in both languages.")
PY

# --- a diagram does not close its right edge over CJK -------------------
# A right edge is a column-counting promise, and no amount of padding can
# keep it in a document that mixes CJK with a proportional fallback font.
# Three readings of the same box were tried and all three failed:
#
#   1. pad it as written        — the rows measured 53, 54 and 57 columns
#   2. pad it CJK=2, box=1      — correct under the model internal/tui
#      pins go-runewidth to (AGENTS.md §Gotchas), broken under a CJK
#      locale's, where box drawing and arrows are Ambiguous and go wide
#   3. redraw in ASCII, CJK=2   — still broken in the viewer that
#      reported it: its monospace stack has no CJK, so kana and kanji
#      come from a fallback whose advance is not exactly twice the Latin
#      one. There is no integer padding that fixes a non-integer ratio.
#
# So the rule is not about padding. A framed row — one that both opens
# and closes with an edge — may not contain CJK at all. Left edges,
# indentation and tree stems are ASCII and align under any font; it is
# only the closing edge that has to be earned, and over CJK it cannot
# be. Diagrams with no right edge (the package trees, the ADR-0050
# pipeline, the turn diagram now) have nothing to break.
#
# An all-ASCII box is still allowed, and still has to line up: the width
# and Ambiguous checks below are what it must pass.
python3 - <<'PY' || exit 1
import glob, io, re, sys, unicodedata

# Both alphabets are recognised: an edge drawn the old way has to be
# caught in order to be reported, not skipped for not being ASCII.
EDGE = set("|+┌│└├┐┘┤")


def width(s):
    return sum(2 if unicodedata.east_asian_width(c) in ("W", "F") else 1 for c in s)


def framed(line):
    # A lone connector ("|" on its own) frames nothing; a row that opens
    # with an edge and ends in prose is the caption under the box.
    s = line.strip()
    return len(s) > 1 and s[0] in EDGE and s[-1] in EDGE


bad = 0
files = sorted(glob.glob("docs/**/*.md", recursive=True))
files += [f for f in ("README.md", "README.ja.md", "AGENTS.md")]
for path in files:
    lines = io.open(path, encoding="utf-8").read().split("\n")
    inside, start, block = False, 0, []
    for n, line in enumerate(lines, 1):
        if re.match(r"^```", line):
            if inside:
                rows = [(m, x) for m, x in block if framed(x)]
                if len(rows) >= 2:
                    wide = [(m, x) for m, x in rows
                            if any(unicodedata.east_asian_width(c) in ("W", "F")
                                   for c in x)]
                    if wide:
                        bad += 1
                        print(f"ERROR: {path}:{start} — a row closes its right edge "
                              f"over CJK, which no padding can align:", file=sys.stderr)
                        for m, x in wide:
                            print(f"  {m}: {x}", file=sys.stderr)
                    widths = {width(x) for _, x in rows}
                    indents = {len(x) - len(x.lstrip()) for _, x in rows}
                    if len(widths) > 1 or len(indents) > 1:
                        bad += 1
                        print(f"ERROR: {path}:{start} — the box rows are not one width:",
                              file=sys.stderr)
                        for m, x in rows:
                            print(f"  {m}: {width(x):>3} columns  {x}", file=sys.stderr)
                    # The whole block, not only the framed rows: a
                    # connector or an arrow above the box shifts with
                    # the same ambiguity.
                    amb = sorted({c for _, x in block for c in x
                                  if unicodedata.east_asian_width(c) == "A"})
                    if amb:
                        bad += 1
                        print(f"ERROR: {path}:{start} — a framed diagram must be ASCII; "
                              f"these are ambiguous-width: {' '.join(amb)}", file=sys.stderr)
                inside, block = False, []
            else:
                inside, start = True, n
            continue
        if inside:
            block.append((n, line))

if bad:
    print("", file=sys.stderr)
    print("Leave the right edge open (a tree or a flow, as the other diagrams do), "
          "or keep the box ASCII-only and padded to one width.", file=sys.stderr)
    sys.exit(1)

print("OK: no diagram closes a right edge it cannot hold.")
PY

# --- concept coverage: code → the whole-system documents ----------------
# Every check above is a symmetry check (en ↔ ja, ADR ↔ index). None can
# see a concept that exists in the code and in no document. That is how
# the architecture reference missed five internal packages across three
# ADRs while every commit "updated the docs": the rule named no document,
# so the ones nearest the feature were updated and the ones describing
# the whole were not. These checks route by construction:
#   internal/<pkg>        → architecture.md AND AGENTS.md name it
#   agent.Options funcs   → architecture.md names the callback / capability
#   cobra subcommands     → configuration.md's command table has a row
#
# Both package maps are checked, not just the architecture reference:
# AGENTS.md §Structure is the map an agent reads before touching the
# tree, its own routing table requires it, and checking one of the two
# is how `internal/banner` reached three releases in the architecture
# map and nowhere in AGENTS.md.
cov_errors=0
arch="docs/en/reference/architecture.md"
for d in internal/*/; do
    pkg="${d%/}"
    # In the tree diagram (fenced) or in prose (backticked): whole word.
    for doc in "$arch" AGENTS.md; do
        if ! grep -qw "${pkg}" "$doc"; then
            echo "ERROR: ${doc} does not name ${pkg} — add it to the package map" >&2
            cov_errors=$((cov_errors + 1))
        fi
    done
done
for cb in $(awk '/^type Options struct/,/^}/' internal/agent/agent.go | grep -E '^	[A-Z][A-Za-z]* +func' | awk '{print $1}'); do
    if ! grep -q "\`${cb}\`" "$arch"; then
        echo "ERROR: ${arch} does not name agent.Options.${cb} — the UI/runtime contract is documented there" >&2
        cov_errors=$((cov_errors + 1))
    fi
done
conf="docs/en/reference/configuration.md"
for use in $(grep -h '^	Use: *"' cmd/*.go | sed 's/.*Use: *"\([a-z-]*\).*/\1/' | grep -v '^gem-agent$' | sort -u); do
    # Nested commands (`workdirs clean`) appear on their parent's row.
    if ! grep -q "| \`${use}" "$conf" && ! grep -q "\`[a-z-]* ${use}" "$conf"; then
        echo "ERROR: ${conf} has no row for subcommand \`${use}\`" >&2
        cov_errors=$((cov_errors + 1))
    fi
done
if [ "$cov_errors" -ne 0 ]; then
    echo "" >&2
    echo "A concept in the code is missing from the document that describes the whole (AGENTS.md §Docs routing)." >&2
    exit 1
fi
echo "OK: every internal package, agent callback and subcommand is documented."
