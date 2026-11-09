#!/usr/bin/env bash
# setup.sh: check the local toolchain against the pinned versions, then
# install the frontend dependencies (`make setup`; docs/building.md).
#
#   scripts/setup.sh
#
# Checks Go (the go.mod version), the wails3 CLI (exactly the Wails module
# version in go.mod), Node.js (the major in lib.sh) and npm, git, and the
# platform's C toolchain. It installs nothing but frontend/node_modules
# (`npm ci`): for anything missing it prints the command that fixes it and
# fails. Docker is optional (only `make linux` needs it).
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"
cd "$root"

problems=0
ok() { printf '  ok       %s\n' "$*"; }
missing() {
	# $1: what is wrong, $2...: fix lines
	printf '  MISSING  %s\n' "$1"
	shift
	for fix in "$@"; do
		printf '           fix: %s\n' "$fix"
	done
	problems=$((problems + 1))
}

echo "==> toolchain"

# Go: the same minor as go.mod's `go` directive, at least its patch.
want_go="$(go_mod_version)"
if command -v go >/dev/null 2>&1; then
	have_go="$(go env GOVERSION)"
	have_go="${have_go#go}"
	IFS=. read -r want_maj want_min want_patch <<<"$want_go"
	IFS=. read -r have_maj have_min have_patch <<<"$have_go"
	have_patch="${have_patch%%[!0-9]*}"
	if [ "$have_maj.$have_min" = "$want_maj.$want_min" ] && [ "${have_patch:-0}" -ge "${want_patch:-0}" ]; then
		ok "go $have_go (go.mod: $want_go)"
	else
		missing "go $have_go, go.mod needs $want_maj.$want_min.x (>= $want_go)" \
			"install Go $want_maj.$want_min from https://go.dev/dl/"
	fi
else
	missing "go (go.mod needs $want_go)" "install Go $want_go or newer $(cut -d. -f1,2 <<<"$want_go").x from https://go.dev/dl/"
fi

# wails3: exactly the Wails module version.
want_wails="$(wails_mod_version)"
add_gobin_to_path
if command -v wails3 >/dev/null 2>&1; then
	have_wails="$(wails3_version)"
	if [ "$have_wails" = "$want_wails" ]; then
		ok "wails3 $have_wails ($(command -v wails3))"
	else
		missing "wails3 $have_wails, go.mod needs $want_wails" "$(wails_install_cmd)"
	fi
else
	missing "wails3 (go.mod needs $want_wails)" "$(wails_install_cmd)" \
		"then put \$(go env GOPATH)/bin on your PATH"
fi

# Node.js and npm.
if command -v node >/dev/null 2>&1; then
	have_node="$(node --version)"
	have_major="${have_node#v}"
	have_major="${have_major%%.*}"
	if [ "$have_major" = "$node_major" ]; then
		ok "node $have_node"
	else
		missing "node $have_node, the toolchain needs Node.js $node_major" \
			"install Node.js $node_major from https://nodejs.org/ (or: nvm install $node_major)"
	fi
else
	missing "node (Node.js $node_major)" "install Node.js $node_major from https://nodejs.org/ (or: nvm install $node_major)"
fi
if command -v npm >/dev/null 2>&1; then
	ok "npm $(npm --version)"
else
	missing "npm (ships with Node.js)" "install Node.js $node_major from https://nodejs.org/"
fi

if command -v git >/dev/null 2>&1; then
	ok "$(git --version)"
else
	missing "git (the version stamp and the gofmt check use it)" "install git"
fi

# The C toolchain cgo needs for the Wails shell.
case "$(uname -s)" in
Darwin)
	if xcode-select -p >/dev/null 2>&1; then
		ok "Xcode Command Line Tools ($(xcode-select -p))"
	else
		missing "Xcode Command Line Tools (cgo)" "xcode-select --install"
	fi
	;;
Linux)
	if command -v pkg-config >/dev/null 2>&1 && pkg-config --exists gtk4 webkitgtk-6.0 x11; then
		ok "GTK4 + WebKitGTK 6.0 dev packages"
	else
		missing "gcc, pkg-config and the GTK4 + WebKitGTK 6.0 dev packages (cgo)" \
			"Debian 13+/Ubuntu 24.04+: sudo apt install gcc libc6-dev pkg-config libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev"
	fi
	;;
*)
	echo "  note     $(uname -s): see docs/building.md (Windows: wails3 build / wails3 task)"
	;;
esac

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
	ok "docker $(docker version --format '{{.Server.Version}}') (for make linux)"
else
	echo "  note     docker not found or not running: needed only for make linux (https://docs.docker.com/get-docker/)"
fi

if [ "$problems" -gt 0 ]; then
	echo "setup: $problems problem(s) above; fix them and run make setup again" >&2
	exit 1
fi

echo "==> frontend dependencies (npm ci in frontend/)"
cd frontend
npm ci --no-audit --no-fund
echo "setup: done"
