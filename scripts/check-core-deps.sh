#!/usr/bin/env bash
# check-core-deps.sh: fail if anything under internal/core depends on a
# package outside the Go standard library and this module.
#
# Test-only dependencies count too (-test). Run from anywhere:
#   scripts/check-core-deps.sh
set -euo pipefail

cd "$(dirname "$0")/.."

mod="$(go list -m)"

# -e reports unresolvable imports as packages instead of aborting, so a
# missing third-party module is listed as a violation too. With -test,
# recompiled packages print as "path [pkg.test]"; keep the first field.
deps="$(go list -e -deps -test -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/core/... | awk '{print $1}')"

bad="$(printf '%s\n' "$deps" | awk -v m="$mod" 'NF && $0 != m && index($0, m "/") != 1' | sort -u)"

if [ -n "$bad" ]; then
	echo "internal/core must import only the standard library and $mod; found:" >&2
	# shellcheck disable=SC2086 # one package per line
	printf '  %s\n' $bad >&2
	exit 1
fi

echo "ok: internal/core depends only on the standard library and $mod"
