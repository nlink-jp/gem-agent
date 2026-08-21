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
