# lib.sh: helpers shared by the local build scripts (setup.sh, wails.sh,
# check-go.sh). Source it; it doesn't run anything by itself.
#
# The pinned versions come from go.mod (Go, the Wails module and so the
# wails3 CLI) and scripts/pins.env (the build images, Go for the release
# builds, the Flatpak runtime), which this file sources. The Node major is
# the one in the toolchain table in docs/architecture.md and the node:<major>
# image in scripts/pins.env; bump them together.
# shellcheck shell=bash

# shellcheck disable=SC2034 # used by the scripts that source this file
node_major=26

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# shellcheck source=scripts/pins.env
. "$root/scripts/pins.env"

die() {
	echo "error: $*" >&2
	exit 1
}

# The `go` directive of go.mod, e.g. 1.27.0.
go_mod_version() {
	awk '$1 == "go" { print $2; exit }' "$root/go.mod"
}

# The Wails v3 module version required by go.mod, e.g. v3.0.0.
# Read from go.mod rather than `go list -m`, so it works without Go.
wails_mod_version() {
	awk '$1 == "require" && $2 == "github.com/wailsapp/wails/v3" { print $3; exit }
		$1 == "github.com/wailsapp/wails/v3" { print $2; exit }' "$root/go.mod"
}

# A build image pinned by digest in scripts/pins.env, by the name after
# POSTSONDE_PIN_, e.g. `pinned_image UBUNTU_24_04` -> ubuntu:24.04@sha256:...
pinned_image() {
	local var="POSTSONDE_PIN_$1" ref
	ref="${!var:-}"
	[ -n "$ref" ] || die "no $var in scripts/pins.env"
	[[ "$ref" =~ @sha256:[0-9a-f]{64}$ ]] || die "$var in scripts/pins.env is not pinned by digest: $ref"
	echo "$ref"
}

# The command that installs the wails3 CLI at the go.mod version.
wails_install_cmd() {
	echo "go install github.com/wailsapp/wails/v3/cmd/wails3@$(wails_mod_version)"
}

# Puts `go install`'s bin directory on PATH, so a wails3 installed there is
# found even when the shell profile doesn't add it.
add_gobin_to_path() {
	local gobin
	if command -v go >/dev/null 2>&1; then
		gobin="$(go env GOBIN)"
		[ -n "$gobin" ] || gobin="$(go env GOPATH)/bin"
		PATH="$gobin:$PATH"
		export PATH
	fi
}

# The installed wails3's version, e.g. v3.0.0. `wails3 version`
# prints it on stderr.
wails3_version() {
	wails3 version 2>&1
}

# Fails unless wails3 is on PATH (or in go install's bin directory) at
# exactly the Wails module version in go.mod.
require_wails3() {
	local want have
	want="$(wails_mod_version)"
	add_gobin_to_path
	if ! command -v wails3 >/dev/null 2>&1; then
		die "wails3 not found; install it with: $(wails_install_cmd)"
	fi
	have="$(wails3_version)"
	if [ "$have" != "$want" ]; then
		die "wails3 is $have, go.mod needs $want; install it with: $(wails_install_cmd)"
	fi
}

# A version as the four numbers of a Windows version resource: the
# leading dotted numbers of a scripts/version.sh version, padded with zeros.
# 0.1.0 -> 0.1.0.0, 0.0.0~git1234abc -> 0.0.0.0, 1.2.3+dirty -> 1.2.3.0.
windows_version_numeric() {
	local nums
	nums="$(printf '%s\n' "$1" | sed -E -n 's/^([0-9]+(\.[0-9]+){0,3}).*/\1/p')"
	[ -n "$nums" ] || die "version '$1' doesn't start with a number"
	while [ "$(printf '%s' "$nums" | tr -cd . | wc -c | tr -d ' ')" -lt 3 ]; do
		nums="$nums.0"
	done
	echo "$nums"
}
