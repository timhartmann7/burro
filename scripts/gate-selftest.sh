#!/usr/bin/env bash
# Copies the repo into a temp folder, plants one violation per gate and fails
# when a gate still passes. A gate must also fail for the planted reason, so
# a gate that is broken for some other cause doesn't count as caught.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Tracked and new files, the way the next commit would see them.
(
	cd "$root"
	git ls-files -z -co --exclude-standard | while IFS= read -r -d '' f; do
		if [ -e "$f" ]; then printf '%s\0' "$f"; fi
	done | tar --null -T - -cf -
) | (cd "$work" && tar -xf -)

cd "$work"
git init -q
git add -A
git -c user.name=selftest -c user.email=selftest@example.invalid commit -q --no-verify -m baseline

# The pinned binaries stay in the real checkout, so nothing is downloaded twice.
mk() { make --no-print-directory BIN="$root/.bin" "$@"; }
hook() { go tool -modfile=tools/go.mod lefthook run commit-msg "$@"; }

msg=.git/SELFTEST_MSG
printf 'feat: add host add command\n' >"$msg"
if ! out=$(mk lint deadcode english 2>&1 && hook "$msg" 2>&1); then
	printf '%s\n' "$out" | tail -n 30 >&2
	echo "gate-selftest: the gates fail on the unchanged copy" >&2
	exit 1
fi

failures=0
# expect <violation> <marker> <gate command...> runs the gate on the planted
# violation, wants it to fail with marker in its output, then resets the copy.
expect() {
	local name=$1 marker=$2 out
	shift 2
	if out=$("$@" 2>&1); then
		echo "gate-selftest: $name: the gate passed" >&2
		failures=$((failures + 1))
	elif ! printf '%s\n' "$out" | grep -qE -- "$marker"; then
		echo "gate-selftest: $name: the gate failed without \"$marker\":" >&2
		printf '%s\n' "$out" | tail -n 20 >&2
		failures=$((failures + 1))
	else
		echo "gate-selftest: $name: caught"
	fi
	git reset -q --hard
	git clean -qfdx
}

planted=internal/output/planted.go

cat >"$planted" <<'EOF'
package output

// Planted is never called.
func Planted() {}
EOF
expect "unused function" "Planted" mk deadcode

printf 'package output\n\n// %s\n' "$(printf '\320\266')" >"$planted"
git add "$planted"
expect "Cyrillic comment" "$planted:3:" mk english

printf 'package output\n\n// Planted is badly spaced.\nconst  Planted = 1\n' >"$planted"
expect "unformatted file" "gofumpt|goimports" mk lint

cat >"$planted" <<'EOF'
package output

import "golang.org/x/tools/cover"

// Planted pulls in a module outside the allowlist.
var Planted = cover.ParseProfiles
EOF
go mod edit -require="golang.org/x/tools@$(awk '$1 == "golang.org/x/tools" { print $2 }' tools/go.mod)"
# Merged, so the modules the code already uses still type-check.
sort -u -o go.sum go.sum tools/go.sum
expect "module outside the allowlist" "not allowed from list 'module'" mk lint

# agent imports no layer 1 package, so the planted import makes no cycle that
# would fail type checking before depguard runs.
cat >internal/config/planted.go <<'EOF'
// Package config reaches from layer 1 up to layer 2.
package config

import "github.com/timhartmann7/burro/internal/agent"

// Planted breaks the import layers.
var Planted = agent.New
EOF
expect "import across layers" "not allowed from list 'layer-1'" mk lint

cat >"$planted" <<'EOF'
package output

// Planted hides a finding without saying why.
var Planted = 1 //nolint:gochecknoglobals
EOF
# gocritic reports the same directive as whyNoLint; either name is the catch.
expect "nolint without a reason" "nolintlint|whyNoLint" mk lint

# Spelled in two parts so this script holds no such marker itself.
printf 'package output\n\n// %s%s: planted\n' TO DO >"$planted"
expect "TODO comment" "godox" mk lint

cat >"$planted" <<'EOF'
package output

import "fmt"

// Planted prints to the process stdout.
func Planted() { fmt.Println("planted") }
EOF
expect "fmt.Println in internal/" "forbidigo" mk lint

cat >scripts/planted.sh <<'EOF'
#!/usr/bin/env bash
echo $1
EOF
expect "shellcheck warning" "SC2086" mk lint

awk '!done && /uses: actions\/checkout@[0-9a-f]+ # v/ { sub(/@[0-9a-f]+ # /, "@"); done = 1 } { print }' \
	.github/workflows/ci.yml >ci.yml.new
mv ci.yml.new .github/workflows/ci.yml
expect "Action pinned by tag" "unpinned-uses" mk lint

mkdir -p docs/slices
printf '# S9\n\n## Acceptance\n\n- [ ] **S9-01** planted item\n' >docs/slices/s9-selftest.md
expect "acceptance ID without a test" "S9-01" mk acceptance SLICE=s9

printf 'feat: add host add command\n\nCo-Authored-By: Planted <planted@example.invalid>\n' >"$msg"
expect "Co-Authored-By in a commit message" "trailers are not allowed" hook "$msg"

if [ "$failures" -gt 0 ]; then
	echo "gate-selftest: $failures gates missed their planted violation" >&2
	exit 1
fi
echo "gate-selftest: every gate caught its planted violation"
