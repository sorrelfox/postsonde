#!/usr/bin/env bash
# sio-interop.sh: the opt-in interop tests of the Socket.IO client and
# server against the official Node implementations: PostSonde's client
# against socket.io 2.x, 3.x and 4.x servers, and the official
# socket.io-client 4.x against PostSonde's server. See
# docs/socketio.md#interop-with-the-official-implementations.
#
#   scripts/sio-interop.sh <install dir> [go test flags...]
#
# Installs the exact versions locked in scripts/sio-interop/v{2,3,4}
# (npm ci, from the npm registry, install scripts off) into
# <install dir>/v2, v3 and v4, outside the repository, then runs the
# interop tests with POSTSONDE_SIO_INTEROP=<install dir>. Needs node and
# npm on the PATH (POSTSONDE_NODE overrides the node binary) and Go. The
# Node side listens on 127.0.0.1 only. A default `go test ./...` skips
# these tests and never needs Node.
#
# Example: scripts/sio-interop.sh "$HOME/.cache/postsonde-sio-interop" -v
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"

[ $# -ge 1 ] || die "usage: scripts/sio-interop.sh <install dir> [go test flags...]"
dir="$1"
shift

command -v npm >/dev/null || die "npm is not on the PATH"
node="${POSTSONDE_NODE:-node}"
command -v "$node" >/dev/null || die "$node is not on the PATH"
major="$("$node" -p 'process.versions.node.split(".")[0]')"
[ "$major" = "$node_major" ] || echo "warning: node $major, the project uses $node_major" >&2

mkdir -p "$dir"
dir="$(cd "$dir" && pwd)"
case "$dir/" in
"$root"/*) die "the install dir must be outside the repository: $dir" ;;
esac

for v in v2 v3 v4; do
	echo "==> npm ci in $dir/$v"
	mkdir -p "$dir/$v"
	cp "$root/scripts/sio-interop/$v/package.json" "$root/scripts/sio-interop/$v/package-lock.json" "$dir/$v/"
	(cd "$dir/$v" && npm ci --ignore-scripts --no-audit --no-fund --loglevel=error)
done

echo "==> go test (interop)"
cd "$root"
POSTSONDE_SIO_INTEROP="$dir" POSTSONDE_NODE="$node" go test -count=1 -run 'Interop' "$@" \
	./internal/core/realtime/sioclient/ ./internal/core/servers/sioserver/
