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
    text = re.sub(r"```.*?```", "", io.open(path, encoding="utf-8").read(), flags=re.S)
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

# --- concept coverage: code → the whole-system documents ----------------
# Every check above is a symmetry check (en ↔ ja, ADR ↔ index). None can
# see a concept that exists in the code and in no document. That is how
# the architecture reference missed five internal packages across three
# ADRs while every commit "updated the docs": the rule named no document,
# so the ones nearest the feature were updated and the ones describing
# the whole were not. These checks route by construction:
#   internal/<pkg>        → architecture.md names it
#   agent.Options funcs   → architecture.md names the callback / capability
#   cobra subcommands     → configuration.md's command table has a row
cov_errors=0
arch="docs/en/reference/architecture.md"
for d in internal/*/; do
    pkg="${d%/}"
    # In the tree diagram (fenced) or in prose (backticked): whole word.
    if ! grep -qw "${pkg}" "$arch"; then
        echo "ERROR: ${arch} does not name ${pkg} — add it to the package map" >&2
        cov_errors=$((cov_errors + 1))
    fi
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
