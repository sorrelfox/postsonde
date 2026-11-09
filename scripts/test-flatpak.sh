#!/usr/bin/env bash
# test-flatpak.sh: install the Flatpak bundle for the user and start it with
# no network, as people run it. The release workflow's flatpak job runs it
# after scripts/package-flatpak.sh, on the runner; `make flatpak` in its
# container. docs/packaging.md, Flatpak.
#
#   scripts/test-flatpak.sh PostSonde-<version>-x86_64.flatpak
#
# The GNOME Platform must be installed already (package-flatpak.sh does
# that): the bundle is installed with --no-deps, so it can't update the
# runtime past its pin. Then `postsonde --version` must print the bundle's
# version, and scripts/smoke-start.sh must see the first window ready, with
# the sandbox's network unshared and only the test's own POSTSONDE_HOME
# shared. An install that was there before stays; one made here is removed
# again, with its data.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"

bundle="${1:?usage: $0 BUNDLE}"
id=io.github.sorrelfox.PostSonde
version="$(basename "$bundle" | sed -n 's/^PostSonde-\(.*\)-x86_64\.flatpak$/\1/p')"
[ -n "$version" ] || die "not a bundle's name: $bundle"

if flatpak ps --columns=application 2>/dev/null | grep -qx "$id"; then
	die "$id is running; close it first"
fi
had="$(flatpak info --user "$id" >/dev/null 2>&1 && echo yes || echo no)"
flatpak install --user --noninteractive -y --reinstall --no-deps "$bundle" >/dev/null
cleanup() {
	flatpak kill "$id" >/dev/null 2>&1 || true
	if [ "$had" = no ]; then
		flatpak uninstall --user --noninteractive -y --delete-data "$id" >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT
flatpak info --user "$id" | grep -E '^ *(Ref|Commit|Runtime|Installed):' || true

have="$(flatpak run --unshare=network "$id" --version)"
echo "$have"
[ "$have" = "postsonde $version" ] || die "expected \"postsonde $version\""

# The sandbox sees POSTSONDE_HOME (smoke-start.sh's temporary folder) at the
# same path, and nothing else of the host's files beyond the manifest's.
# WebKitGTK's own sandbox is off, as in the container tests (for tests
# only): inside a Flatpak it starts its web process through the Flatpak
# portal, which needs a desktop's user session that neither a CI runner nor
# a container has.
# shellcheck disable=SC2016 # expanded by the inner bash, with smoke-start.sh's POSTSONDE_HOME
"$root/scripts/smoke-start.sh" bash -c 'exec flatpak run --unshare=network \
	--filesystem="$POSTSONDE_HOME" --env=POSTSONDE_HOME="$POSTSONDE_HOME" \
	--env=WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1 "$0"' "$id"
