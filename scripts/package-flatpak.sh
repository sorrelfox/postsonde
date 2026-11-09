#!/usr/bin/env bash
# package-flatpak.sh: the Flatpak bundle. Builds
# packaging/flatpak/io.github.sorrelfox.PostSonde.yml in the GNOME SDK from
# the source tarball (scripts/package-sources.sh) into a repository of its
# own, and makes a single-file bundle of it. The release workflow's flatpak
# job runs it on the runner; `make flatpak` runs it in a container
# (scripts/linux.sh flatpak), so a developer's own Flatpak installation is
# never touched. docs/packaging.md, Flatpak.
#
#   scripts/package-flatpak.sh TARBALL OUT_DIR
#
# Writes OUT_DIR/PostSonde-<version>-x86_64.flatpak, the repository and
# build.log. Needs flatpak and flatpak-builder, and runs bubblewrap, so user
# namespaces must work. The GNOME SDK and Platform are put at the commits
# scripts/pins.env pins (x86_64): installed for the user from Flathub when
# missing, moved to the pin when installed at another commit (with
# POSTSONDE_FLATPAK_PIN=1, since other apps may use them; without it that is
# an error). Not --install-deps-from: it would update them past the pins.
# flatpak-builder downloads the Go tarball and checks its sha256; the build
# runs offline.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"

usage="usage: $0 TARBALL OUT_DIR"
tarball="${1:?$usage}"
out="${2:?$usage}"
tarball="$(cd "$(dirname "$tarball")" && pwd)/$(basename "$tarball")"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
version="$(basename "$tarball" | sed -n 's/^postsonde-\(.*\)\.tar\.xz$/\1/p')"
[ -n "$version" ] || die "not a source tarball: $tarball"
[ "$(uname -m)" = x86_64 ] || die "the Flatpak is built for x86_64 only (the runtime pins are x86_64's)"
id=io.github.sorrelfox.PostSonde
gnome="$POSTSONDE_PIN_GNOME_VERSION"

pin() {
	local ref="$1" want="$2" have
	have="$(flatpak info --user --show-commit "$ref" 2>/dev/null || true)"
	[ "$have" = "$want" ] && return 0
	if [ -n "$have" ] && [ "${POSTSONDE_FLATPAK_PIN:-}" != 1 ]; then
		die "$ref is installed at $have, scripts/pins.env pins $want; set POSTSONDE_FLATPAK_PIN=1 to move it there"
	fi
	flatpak remote-add --user --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo
	[ -n "$have" ] || flatpak install --user --noninteractive -y flathub "$ref" >/dev/null
	flatpak update --user --noninteractive -y --commit="$want" "$ref" >/dev/null
	have="$(flatpak info --user --show-commit "$ref")"
	[ "$have" = "$want" ] || die "$ref stayed at $have, not $want"
}
echo "==> org.gnome.Sdk//$gnome and org.gnome.Platform//$gnome at the pinned commits"
pin "org.gnome.Sdk//$gnome" "$POSTSONDE_PIN_GNOME_SDK"
pin "org.gnome.Platform//$gnome" "$POSTSONDE_PIN_GNOME_PLATFORM"

# The manifest with the placeholders filled in, next to the output.
manifest="$out/$id.yml"
sed -e "s|@VERSION@|$version|g" -e "s|@TARBALL@|$tarball|g" \
	-e "s|@GNOME_VERSION@|$gnome|g" -e "s|@GO_VERSION@|$POSTSONDE_PIN_GO_VERSION|g" \
	-e "s|@GO_SHA256_AMD64@|$POSTSONDE_PIN_GO_SHA256_AMD64|g" \
	-e "s|@GO_SHA256_ARM64@|$POSTSONDE_PIN_GO_SHA256_ARM64|g" \
	"$root/packaging/flatpak/$id.yml" >"$manifest"
! grep -n '@[A-Z_0-9]*@' "$manifest" || die "a placeholder of the manifest wasn't filled in"

# --disable-cache: a local tarball isn't part of flatpak-builder's cache key,
# so a rebuilt one would be skipped.
echo "==> flatpak-builder $id $version (log: $out/build.log)"
flatpak-builder --user --force-clean --disable-cache --state-dir="$out/state" \
	--repo="$out/repo" --default-branch=stable "$out/build" "$manifest" >"$out/build.log" 2>&1 || {
	tail -40 "$out/build.log" >&2
	exit 1
}
bundle="$out/PostSonde-$version-x86_64.flatpak"
flatpak build-bundle --runtime-repo=https://dl.flathub.org/repo/flathub.flatpakrepo \
	"$out/repo" "$bundle" "$id" stable
echo "==> $bundle ($(wc -c <"$bundle" | tr -d ' ') bytes)"
