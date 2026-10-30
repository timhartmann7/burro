#!/usr/bin/env bash
# Checks author, committer and message of every commit in a range, for CI.
set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: scripts/commits.sh <range>" >&2
	exit 4
fi
dir=$(cd "$(dirname "$0")" && pwd)
want='Tim Hartmann <timhartmann7@proton.me>'
msg=$(mktemp)
trap 'rm -f "$msg"' EXIT

# A merge commit from GitHub's merge button carries GitHub as committer and
# no change of its own; the commits it merges are in the range and checked.
commits=$(git rev-list --no-merges "$1")
status=0
for sha in $commits; do
	short=$(git rev-parse --short "$sha")
	for role in author committer; do
		if [ "$role" = author ]; then
			who=$(git log -1 --format='%an <%ae>' "$sha")
		else
			who=$(git log -1 --format='%cn <%ce>' "$sha")
		fi
		if [ "$who" != "$want" ]; then
			echo "commits: $short: $role is $who, want $want" >&2
			status=1
		fi
	done
	git log -1 --format=%B "$sha" >"$msg"
	if ! out=$("$dir/commit-msg.sh" "$msg" 2>&1); then
		printf '%s\n' "$out" | sed "s/^/commits: $short: /" >&2
		status=1
	fi
done
exit "$status"
