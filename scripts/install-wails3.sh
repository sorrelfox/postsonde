#!/usr/bin/env bash
# install-wails3.sh: `go install` the wails3 CLI at the Wails module version
# in go.mod, into $(go env GOPATH)/bin. For CI jobs and the Docker
# containers of scripts/linux.sh; on a developer machine `make setup` prints
# the same command instead of running it.
#
#   scripts/install-wails3.sh
#
# Set CGO_ENABLED=0 where only `wails3 tool package` is needed: packaging
# needs no GTK.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"
cd "$root"

cmd="$(wails_install_cmd)"
echo "==> $cmd"
$cmd
add_gobin_to_path
echo "wails3 $(wails3_version) at $(command -v wails3)"
