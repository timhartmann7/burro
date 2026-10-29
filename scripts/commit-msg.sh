#!/usr/bin/env bash
# Checks one commit message file against the git rules in docs/engineering.md.
set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: scripts/commit-msg.sh <message file>" >&2
	exit 4
fi
dir=$(cd "$(dirname "$0")" && pwd)
msg=$(mktemp)
trap 'rm -f "$msg"' EXIT
# git drops comment lines after the hook runs, so they are not part of the message.
grep -v '^#' "$1" >"$msg" || true

problems=()
subject=$(head -n 1 "$msg")
types='feat|fix|test|refactor|perf|docs|ci|build|chore'
if ! printf '%s\n' "$subject" | grep -qE "^($types)(\([a-z0-9][a-z0-9-]*\))?!?: [^ ]"; then
	problems+=("subject needs a Conventional Commits type ($types): $subject")
fi

# Characters, not bytes: UTF-8 continuation bytes are dropped before counting.
chars() { printf '%s' "$1" | LC_ALL=C tr -d '\200-\277' | wc -c | tr -d ' '; }
n=$(chars "$subject")
if [ "$n" -gt 72 ]; then
	problems+=("subject is $n characters, the limit is 72")
fi
line=1
while IFS= read -r body; do
	line=$((line + 1))
	if [ "$line" -gt 2 ] && [ "$(chars "$body")" -gt 72 ]; then
		problems+=("line $line is longer than 72 characters")
	fi
done < <(tail -n +2 "$msg")

if [ -n "$(git interpret-trailers --parse "$msg")" ]; then
	problems+=("trailers are not allowed")
fi
for phrase in 'co-authored-by' 'signed-off-by' 'generated with' 'noreply@anthropic.com'; do
	if grep -qiF -- "$phrase" "$msg"; then
		problems+=("\"$phrase\" is not allowed")
	fi
done
for word in claude anthropic chatgpt copilot; do
	if grep -qiw -- "$word" "$msg"; then
		problems+=("the word \"$word\" is not allowed")
	fi
done
if LC_ALL=C grep -qF $'\xf0\x9f\xa4\x96' "$msg"; then
	problems+=("the robot emoji is not allowed")
fi
if ! "$dir/english.sh" "$msg" >/dev/null 2>&1; then
	problems+=("Cyrillic characters are not allowed")
fi

if [ "${#problems[@]}" -gt 0 ]; then
	for p in "${problems[@]}"; do
		echo "commit-msg: $p" >&2
	done
	exit 1
fi
