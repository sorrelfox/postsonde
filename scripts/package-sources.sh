#!/usr/bin/env bash
# package-sources.sh: the source tarball, postsonde-<version>.tar.xz. It
# holds the tree, the frontend's production build in frontend/dist and the Go
# modules in vendor/, so that the RPM and the Flatpak build from it without
# a network (packaging/rpm/postsonde.spec, packaging/flatpak/). The release
# workflow's sources job and `make sources` run it.
#
#   scripts/package-sources.sh OUT_DIR
#
# Run it in an export of the tree without .git (git archive HEAD), with
# frontend/dist built (scripts/build-frontend.sh). It adds vendor/ with `go
# mod vendor`, which downloads the modules and checks them against go.sum,
# and writes OUT_DIR/postsonde-<version>.tar.xz with everything below
# postsonde-<version>/, except what npm ci leaves (frontend/node_modules).
#
# Version: scripts/version.sh. Every file time is SOURCE_DATE_EPOCH
# (scripts/source-date-epoch.sh) and the entries are sorted, so the tarball
# is the same for every build of one commit. Needs Go, GNU tar and xz; as
# root on Debian (the golang image, which has no xz) it installs xz-utils.
set -euo pipefail

usage="usage: $0 OUT_DIR"
out="${1:?$usage}"

cd "$(dirname "$0")/.."

if [ -e .git ]; then
	echo "package-sources.sh: run it in an export (git archive HEAD), not in a checkout: it would pack untracked files too" >&2
	exit 1
fi
if [ ! -f frontend/dist/index.html ]; then
	echo "package-sources.sh: frontend/dist/index.html is missing: build the frontend first (scripts/build-frontend.sh)" >&2
	exit 1
fi

version="$(scripts/version.sh)"
sde="$(scripts/source-date-epoch.sh)" || {
	echo "package-sources.sh: needs SOURCE_DATE_EPOCH (the commit time) for the file times" >&2
	exit 1
}
name="postsonde-$version"

if ! command -v xz >/dev/null 2>&1; then
	if [ "$(id -u)" = 0 ] && command -v apt-get >/dev/null 2>&1; then
		echo "==> installing xz-utils"
		export DEBIAN_FRONTEND=noninteractive
		{ apt-get -o Acquire::Retries=5 update -qq && apt-get -o Acquire::Retries=5 install -y -qq --no-install-recommends xz-utils; } >/dev/null
	else
		echo "package-sources.sh: xz not found (Debian/Ubuntu: apt-get install xz-utils)" >&2
		exit 1
	fi
fi

echo "==> go mod vendor ($(go version))"
rm -rf vendor
GOFLAGS=-mod=mod go mod vendor

mkdir -p "$out"
tarball="$(cd "$out" && pwd)/$name.tar.xz"
echo "==> $tarball"
tar --sort=name --mtime="@$sde" --owner=0 --group=0 --numeric-owner \
	--format=gnu --anchored --exclude=./frontend/node_modules \
	--transform="s,^\.,$name," -cf - . | xz -9 -T1 >"$tarball.part"
mv -f "$tarball.part" "$tarball"
echo "==> $(wc -c <"$tarball" | tr -d ' ') bytes, $(tar -tJf "$tarball" | wc -l | tr -d ' ') entries"
