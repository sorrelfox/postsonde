#!/usr/bin/env bash
# version.sh: print the PostSonde version for builds and packages. The one
# place the version is worked out: the local build scripts (scripts/wails.sh,
# scripts/linux.sh), scripts/ci-build-linux.sh (main.version) and the
# packaging scripts all call it. docs/building.md, Version.
#
#   scripts/version.sh
#
# First match wins:
#   PKG_VERSION           as given (a manual release build; CI works the
#                         version out once with git and passes it to its
#                         build and package jobs, most of whose images have
#                         no git)
#   git, tag vX.Y.Z on HEAD   X.Y.Z
#   git, otherwise            0.0.0~git<short sha>
# With git, "+dirty" is appended when tracked files have uncommitted
# changes. With no git and no PKG_VERSION (e.g. a bare container), the
# version is 0.0.0~unknown, with a warning.
#
# The result is a valid Debian upstream version: it starts with a digit and
# holds only A-Za-z0-9.+~ ("~" sorts before anything, so 0.0.0~git... is
# older than 0.1.0 and a release always upgrades a development build).
set -euo pipefail

cd "$(dirname "$0")/.."

if [ -n "${PKG_VERSION:-}" ]; then
	version="$PKG_VERSION"
elif command -v git >/dev/null 2>&1 && git rev-parse --git-dir >/dev/null 2>&1; then
	if tag="$(git describe --tags --exact-match --match 'v[0-9]*' HEAD 2>/dev/null)"; then
		version="${tag#v}"
	else
		version="0.0.0~git$(git rev-parse --short HEAD)"
	fi
	if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
		version="$version+dirty"
	fi
else
	echo "version.sh: no git checkout and no PKG_VERSION; using 0.0.0~unknown" >&2
	version="0.0.0~unknown"
fi

# Debian upstream version: starts with a digit; no "-" (there is no Debian
# revision) and no "_" (the package file name separator).
if ! [[ "$version" =~ ^[0-9][A-Za-z0-9.+~]*$ ]]; then
	echo "version.sh: invalid version '$version' (must start with a digit and use only A-Za-z0-9.+~)" >&2
	exit 1
fi
echo "$version"
