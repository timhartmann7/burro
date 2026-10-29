#!/usr/bin/env bash
# Fails when a tracked file holds a Cyrillic character, printing file and line.
# With arguments it checks those files instead, which the commit-msg hook uses.
set -euo pipefail

# Bytes, not a locale: the UTF-8 encodings of every Unicode block with
# Cyrillic letters, so the check works the same with GNU and BSD grep.
export LC_ALL=C
pattern=$'[\xd0-\xd3][\x80-\xbf]|\xd4[\x80-\xaf]'
pattern+=$'|\xe1\xb2[\x80-\x8f]|\xe1\xb4\xab|\xe1\xb5\xb8'
pattern+=$'|\xe2\xb7[\xa0-\xbf]|\xea\x99[\x80-\xbf]|\xea\x9a[\x80-\x9f]|\xef\xb8[\xae-\xaf]'
pattern+=$'|\xf0\x9e\x80[\xb0-\xbf]|\xf0\x9e\x81[\x80-\xbf]|\xf0\x9e\x82[\x80-\x8f]'

files=("$@")
if [ "$#" -eq 0 ]; then
	while IFS= read -r -d '' f; do
		files+=("$f")
	done < <(git ls-files -z)
fi

# /dev/null keeps grep from reading stdin when there are no files.
status=0
grep -nHIE -- "$pattern" "${files[@]+"${files[@]}"}" /dev/null || status=$?
case $status in
0)
	echo "english: Cyrillic characters found" >&2
	exit 1
	;;
1) exit 0 ;;
*) exit "$status" ;;
esac
