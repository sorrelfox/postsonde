#!/usr/bin/env bash
# wails.sh: build, package or run PostSonde for the host OS through the
# wails3 CLI and the Taskfile (`make dev|build|package|bindings`;
# docs/building.md).
#
#   scripts/wails.sh dev        wails3 dev: Vite dev server + live-rebuilt app
#   scripts/wails.sh build      wails3 build: production binary bin/postsonde
#   scripts/wails.sh package    wails3 package: macOS bin/postsonde.app
#                               (Linux: a .deb in dist/)
#   scripts/wails.sh bindings   regenerate the committed frontend/bindings
#
# build and package stamp main.version with scripts/version.sh (PKG_VERSION
# overrides it). Needs wails3 at the Wails version in go.mod.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"
cd "$root"

usage="usage: $0 <dev|build|package|bindings>"
cmd="${1:?$usage}"

require_wails3

case "$cmd" in
dev)
	exec wails3 task dev
	;;
build | package)
	version="$(scripts/version.sh)"
	echo "==> wails3 $cmd VERSION=$version"
	wails3 "$cmd" VERSION="$version"
	if [ "$(uname -s)" = Darwin ] && [ "$cmd" = package ]; then
		echo "==> bin/postsonde.app"
	elif [ "$cmd" = build ]; then
		echo "==> bin/postsonde: $(bin/postsonde --version)"
	fi
	;;
bindings)
	exec wails3 task bindings
	;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac
