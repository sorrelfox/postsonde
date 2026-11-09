#!/usr/bin/env bash
# test-linux-packages.sh: install each package in a clean system and start
# it there with no network, as a user would get it (docs/ci.md, Package
# tests). The release workflow runs it on every .deb, .rpm and AppImage;
# `make rpm` on the RPM it built.
#
#   scripts/test-linux-packages.sh PACKAGE...
#
#   postsonde_<v>_<arch>.deb        Ubuntu 24.04 and Debian 13 (apt install)
#   postsonde-gtk3_<v>_<arch>.deb   Ubuntu 22.04 and Debian 12 (apt install)
#   postsonde-<v>-1.fc<N>.<arch>.rpm   Fedora N, 44 or 45 (dnf install)
#   PostSonde-<v>-<arch>.AppImage   Debian 13 with only the graphics and font
#                                   libraries the AppImage leaves to the
#                                   host (docs/packaging.md, AppImage)
#
# For each system: a container of the pinned image (scripts/pins.env) and the
# package's architecture installs the package, with its dependencies from the
# distribution, plus Xvfb and D-Bus to start it on; it is committed and run
# again with no network at all (--network none), where `postsonde --version`
# must print the package's version and scripts/smoke-start.sh must see the
# first window ready. WebKitGTK 6.0's sandbox can't create its namespaces in
# a container, so the start runs without it (WEBKIT_DISABLE_SANDBOX_..., for
# tests only; docs/ci.md). Another architecture than Docker's runs emulated
# (qemu/binfmt). The containers and images are removed afterwards.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"

[ "$#" -gt 0 ] || {
	echo "usage: $0 PACKAGE..." >&2
	exit 2
}
command -v docker >/dev/null 2>&1 || die "the package tests need Docker"

name="postsonde-test-$$"
cleanup() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	docker rmi -f "localhost/$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# test_in IMAGE PLATFORM PACKAGE INSTALL VERSION COMMAND...: installs with
# the INSTALL script (the package is /pkg/<file>), then checks COMMAND
# offline.
test_in() {
	local image="$1" platform="$2" pkg="$3" install="$4" want="$5"
	shift 5
	echo "=== $(basename "$pkg") on ${image%%@*} ($platform)"
	cleanup
	docker run --name "$name" --platform "$platform" \
		-v "$(cd "$(dirname "$pkg")" && pwd)/$(basename "$pkg"):/pkg/$(basename "$pkg"):ro" \
		-e PKG="/pkg/$(basename "$pkg")" "$image" bash -euc "$install"
	docker commit "$name" "localhost/$name" >/dev/null
	docker rm "$name" >/dev/null
	docker run --rm --platform "$platform" --network none \
		-v "$root/scripts/smoke-start.sh:/smoke-start.sh:ro" \
		-e WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1 -e APPIMAGE_EXTRACT_AND_RUN=1 \
		-e WANT="postsonde $want" "localhost/$name" bash -euc '
			have="$("$@" --version)"
			echo "$have"
			[ "$have" = "$WANT" ] || { echo "expected \"$WANT\"" >&2; exit 1; }
			/smoke-start.sh "$@"
		' bash "$@"
	docker rmi -f "localhost/$name" >/dev/null
}

# The install scripts run in the container, which expands them (SC2016).
# shellcheck disable=SC2016
apt_install='export DEBIAN_FRONTEND=noninteractive
	{ apt-get update && apt-get install -y "$PKG" xvfb xauth dbus; } >/tmp/apt.log 2>&1 || { tail -30 /tmp/apt.log; exit 1; }
	dpkg-query -W -f="installed \${Package} \${Version}\n" "$(dpkg-deb -f "$PKG" Package)"'
# shellcheck disable=SC2016
dnf_install='{ dnf -y install "$PKG" xorg-x11-server-Xvfb xorg-x11-xauth dbus-daemon; } >/tmp/dnf.log 2>&1 || { tail -30 /tmp/dnf.log; exit 1; }
	rpm -q postsonde | sed "s/^/installed /"'
# The libraries docs/packaging.md lists for the AppImage's host, and
# nothing else of the desktop.
# shellcheck disable=SC2016
appimage_install='export DEBIAN_FRONTEND=noninteractive
	{ apt-get update && apt-get install -y --no-install-recommends libegl1 libgles2 libgbm1 libgl1 \
		libwayland-client0 libharfbuzz0b libfribidi0 libfontconfig1 libfreetype6 libx11-6 libx11-xcb1 \
		libgpg-error0 libcom-err2 xvfb xauth dbus; } >/tmp/apt.log 2>&1 || { tail -30 /tmp/apt.log; exit 1; }
	cp "$PKG" /PostSonde.AppImage && chmod +x /PostSonde.AppImage'

# The versions in the file names have no "_" or "-" (scripts/version.sh), so
# the shortest suffix after one is the architecture and the extension.
for pkg in "$@"; do
	[ -f "$pkg" ] || die "$pkg is missing"
	file="$(basename "$pkg")"
	case "$file" in
	*_amd64.deb | *.x86_64.rpm | *-x86_64.AppImage) platform=linux/amd64 ;;
	*_arm64.deb | *.aarch64.rpm | *-aarch64.AppImage) platform=linux/arm64 ;;
	*) die "$file: not a package this script knows (.deb, .rpm, .AppImage)" ;;
	esac
	case "$file" in
	postsonde_*.deb)
		version="${file#postsonde_}" version="${version%_*.deb}"
		for image in "$(pinned_image UBUNTU_24_04)" "$(pinned_image DEBIAN_13)"; do
			test_in "$image" "$platform" "$pkg" "$apt_install" "$version" postsonde
		done
		;;
	postsonde-gtk3_*.deb)
		version="${file#postsonde-gtk3_}" version="${version%_*.deb}"
		for image in "$(pinned_image UBUNTU_22_04)" "$(pinned_image DEBIAN_12)"; do
			test_in "$image" "$platform" "$pkg" "$apt_install" "$version" postsonde
		done
		;;
	*.src.rpm)
		echo "=== $file: a source RPM, nothing to install"
		;;
	postsonde-*.rpm)
		version="${file#postsonde-}" version="${version%-*.rpm}"
		# The Fedora release from the dist tag, .fc<N>.
		fedora="${file##*.fc}" fedora="${fedora%%.*}"
		test_in "$(pinned_image "FEDORA_$fedora")" "$platform" "$pkg" "$dnf_install" "$version" postsonde
		;;
	PostSonde-*.AppImage)
		version="${file#PostSonde-}" version="${version%-*.AppImage}"
		test_in "$(pinned_image DEBIAN_13)" "$platform" "$pkg" "$appimage_install" "$version" /PostSonde.AppImage
		;;
	*) die "$file: not a package this script knows" ;;
	esac
done
echo "test-linux-packages: every package installed and started"
