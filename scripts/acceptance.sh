#!/usr/bin/env bash
# Fails when an acceptance ID of a slice has no test or gate that references it.
set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: scripts/acceptance.sh <slice>, for example s1" >&2
	exit 4
fi
slice=$1
shopt -s nullglob
specs=(docs/slices/"$slice"-*.md)
if [ "${#specs[@]}" -ne 1 ]; then
	echo "acceptance: want one docs/slices/$slice-*.md, found ${#specs[@]}" >&2
	exit 4
fi
spec=${specs[0]}

id_re='S[0-9]+-[0-9]+'
ids=$(grep -oE "^- \[[ x]\] \*\*$id_re\*\*" "$spec" | grep -oE "$id_re" || true)
release_ids=$(grep -oE "^- \[[ x]\] \*\*$id_re\*\* \[release\]" "$spec" | grep -oE "$id_re" || true)
if [ -z "$ids" ]; then
	echo "acceptance: $spec has no acceptance IDs" >&2
	exit 1
fi

# A test references an ID with "// acceptance: <IDs>" on the line right above
# its func. Gates use a "# acceptance: <IDs>" comment in CI or the Makefile.
# shellcheck disable=SC2016 # $0 belongs to awk, not to the shell
tests=$(find . -path ./.git -prune -o -name '*_test.go' -type f -print0 |
	xargs -0 awk 'FNR == 1 { prev = "" } /^func (Test|Fuzz)/ && prev ~ /^\/\/ acceptance:/ { print prev } { prev = $0 }')
gates=$(cat Makefile .github/workflows/*.yml 2>/dev/null | grep -E '^[[:space:]]*# acceptance:' || true)
smoke=$(grep -E '^[[:space:]]*-?[[:space:]]*name:' .github/workflows/release-smoke.yml 2>/dev/null || true)
referenced=$(printf '%s\n%s\n' "$tests" "$gates" | grep -oE "$id_re" || true)
released=$(printf '%s\n' "$smoke" | grep -oE "$id_re" || true)

missing=0
for id in $ids; do
	if printf '%s\n' "$release_ids" | grep -qx "$id"; then
		found=$released
		where="a release-smoke.yml step name"
	else
		found=$referenced
		where="a test or gate"
	fi
	if ! printf '%s\n' "$found" | grep -qx "$id"; then
		echo "acceptance: $id has no reference in $where" >&2
		missing=$((missing + 1))
	fi
done
total=$(printf '%s\n' "$ids" | wc -l | tr -d ' ')
if [ "$missing" -gt 0 ]; then
	echo "acceptance: $slice: $missing of $total IDs not covered" >&2
	exit 1
fi
echo "acceptance: $slice: all $total IDs covered"
