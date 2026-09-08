#!/bin/sh
# verify-release-selftest: exercise the verify-release gate against a
# synthetic dist, and require it to reject every bad zip.
#
# The gate runs by hand, once, at release time, so a hole in it is
# invisible until it lets something through. One did: `|| true`, written
# for the informational spctl probe, sat at the end of an
# `unzip && --version && spctl` chain and forgave all three, so a zip
# that did not unpack still printed "verify-release: OK"
# (review 2026-09-08, F-04). This runs in `make check`.
#
# The fixture binary is a shell script — what is under test is the
# gate's control flow, not the Go build.
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

BIN=gem-agent
V=v0.0.0-selftest
zip=$work/$BIN-$V-darwin-arm64.zip
marker=$zip.notarized
failures=0

# stage_zip BODY — build the release zip around a fixture binary whose
# body is BODY, and mark it notarized.
stage_zip() {
	rm -f "$zip" "$marker"
	stage=$work/stage
	rm -rf "$stage"
	mkdir -p "$stage"
	printf '#!/bin/sh\n%s\n' "$1" >"$stage/$BIN"
	chmod +x "$stage/$BIN"
	(cd "$stage" && zip -q "$zip" "$BIN")
	rm -rf "$stage"
	: >"$marker"
	# Explicit timestamps: the gate's freshness check is `-nt`, which
	# /bin/sh compares at second granularity, and everything here is
	# written inside one second. A real notarization takes far longer.
	touch -t 202001010000 "$zip"
	touch -t 202001010001 "$marker"
}

# expect WANT LABEL — run the gate and compare its exit status.
expect() {
	want=$1
	label=$2
	got=0
	(cd "$repo" && make -s verify-release DIST_DIR="$work" VERSION="$V") >"$work/out" 2>&1 || got=$?
	if [ "$want" = pass ] && [ "$got" -ne 0 ]; then
		echo "verify-release-selftest: FAIL — $label should have passed (exit $got):"
		sed 's/^/    /' "$work/out"
		failures=$((failures + 1))
	elif [ "$want" = fail ] && [ "$got" -eq 0 ]; then
		echo "verify-release-selftest: FAIL — $label was accepted:"
		sed 's/^/    /' "$work/out"
		failures=$((failures + 1))
	fi
}

# A zip with no notarization marker at all.
stage_zip "echo \"$BIN version $V\""
rm -f "$marker"
expect fail "an un-notarized zip"

# A marker older than the zip it vouches for: the zip was rebuilt after.
stage_zip "echo \"$BIN version $V\""
touch -t 200001010000 "$marker"
expect fail "a zip rebuilt after its marker"

# A zip that does not unpack. The marker is re-stamped newer than the
# replaced zip so the freshness check upstream passes and this case
# actually reaches the unpack step.
stage_zip "echo \"$BIN version $V\""
printf 'not a zip' >"$zip"
touch -t 202001010000 "$zip"
touch -t 202001010001 "$marker"
expect fail "a corrupt zip"

# A binary that unpacks but does not run.
stage_zip "exit 3"
expect fail "a binary that exits non-zero"

# A binary that runs but is a build of some other tag.
stage_zip "echo \"$BIN version v0.0.0-other\""
expect fail "a binary from another tag"

# The good one, so a gate that rejects everything is not mistaken for a
# working gate.
stage_zip "echo \"$BIN version $V\""
expect pass "a well-formed notarized zip"

if [ "$failures" -ne 0 ]; then
	echo "verify-release-selftest: $failures case(s) failed"
	exit 1
fi
echo "verify-release-selftest: OK (6 cases)"
