#!/usr/bin/env bash
# Runs unit and integration tests with coverage over ./internal/..., merges both
# runs and fails when the total or any package is below its gate.
set -euo pipefail

total_min=85
package_min=80

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/unit" "$work/integration"

# testutil is test-only code and cmd/burro is wiring, so neither counts.
pkgs=$(go list ./internal/... | grep -v '/internal/testutil$' | paste -sd , -)
go test -count=1 -shuffle=on -cover -coverpkg="$pkgs" ./... -args -test.gocoverdir="$work/unit"
go test -tags integration -count=1 -shuffle=on -cover -coverpkg="$pkgs" ./... -args -test.gocoverdir="$work/integration"

dirs="$work/unit,$work/integration"
go tool covdata textfmt -i="$dirs" -o "$work/cover.out"
total=$(go tool cover -func="$work/cover.out" | awk '$1 == "total:" { sub("%", "", $3); print $3 }')

# Packages with no statements yet print no percentage and are skipped.
go tool covdata percent -i="$dirs" |
	awk '$2 == "coverage:" && $3 ~ /%$/ { sub("%", "", $3); print $1, $3 }' >"$work/packages"
if [ ! -s "$work/packages" ]; then
	echo "coverage: no packages with statements yet"
	exit 0
fi

awk -v total="$total" -v total_min="$total_min" -v package_min="$package_min" '
	{
		printf "coverage: %-50s %5.1f%%\n", $1, $2
		if ($2 + 0 < package_min) { low = low sprintf("coverage: %s is below %d%%\n", $1, package_min) }
	}
	END {
		printf "coverage: %-50s %5.1f%%\n", "total", total
		if (total + 0 < total_min) { low = low sprintf("coverage: total is below %d%%\n", total_min) }
		if (low != "") { printf "%s", low > "/dev/stderr"; exit 1 }
	}
' "$work/packages"
