#!/usr/bin/env bash
# check-go.sh: the Go checks, exactly as the go:lint-test CI job runs them
# (it calls this script; docs/ci.md). Locally: `make check-go`.
#
#   scripts/check-go.sh
#
# gofmt over the tracked Go files, the stdlib-only core guard, go vet, the
# race tests and the committed-bindings check. Installs nothing: the CI job
# installs the GTK dev packages (Linux cgo) and the wails3 CLI
# (scripts/install-wails3.sh) first; on a developer machine `make setup`
# checks them.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"
cd "$root"

# -race and the Wails bindings need cgo. Go's default flags plus silencing
# Wails' GTK deprecation warnings, which otherwise bury the test output.
export CGO_ENABLED=1
export CGO_CFLAGS="${CGO_CFLAGS:--O2 -g -Wno-deprecated-declarations}"

go version
require_wails3

# Only tracked files, so node_modules and .cache are never scanned.
echo "==> gofmt"
unformatted="$(git ls-files -z '*.go' | xargs -0 gofmt -l)"
if [ -n "$unformatted" ]; then
	echo "gofmt: these files need formatting (run gofmt -w):" >&2
	# shellcheck disable=SC2086 # one file per line
	printf '  %s\n' $unformatted >&2
	exit 1
fi

echo "==> scripts/check-core-deps.sh"
scripts/check-core-deps.sh

echo "==> go vet ./..."
go vet ./...

echo "==> go test -race ./..."
go test -race ./...

# The committed TypeScript bindings must match the Go services.
echo "==> wails3 task bindings:check"
wails3 task bindings:check

echo "check-go: ok"
