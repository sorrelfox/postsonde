#!/usr/bin/env bash
# package-windows-installer.sh: wrap a PostSonde Windows executable in the
# NSIS installer (docs/packaging-windows.md).
#
#   scripts/package-windows-installer.sh <amd64|arm64>
#
# Reads bin/postsonde-windows-<arch>.exe (scripts/build-windows.sh) and
# writes dist/PostSonde-<version>-windows-<arch>-setup.exe from
# build/windows/nsis/project.nsi. Runs anywhere makensis 3.x runs (Linux,
# macOS with Homebrew's makensis, Windows with Git Bash). When makensis
# isn't on PATH and the script runs as root on Debian (the golang image),
# it installs Debian's nsis package at the tested version first.
#
# The version is scripts/version.sh (PKG_VERSION overrides it); it has to
# be the version the executable was built with. The installer is the same
# byte for byte for the same executable, sources and makensis version: no
# file times are stored (SetDateSave off) and makensis embeds no build
# time.
#
# Signing: none, unless POSTSONDE_WINDOWS_CERTIFICATE names a code-signing
# certificate (a .pfx file; scripts/sign-windows.sh has the other
# variables). Then the executable is signed before it is packed, and the
# uninstaller and the installer as makensis writes them; a signature's
# timestamp makes the installer differ from build to build.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"
cd "$root"

usage="usage: $0 <amd64|arm64>"
arch="${1:?$usage}"
case "$arch" in
amd64) arg=ARG_WAILS_AMD64_BINARY ;;
arm64) arg=ARG_WAILS_ARM64_BINARY ;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac

# Debian trixie's nsis (the golang image is trixie); pinned so a rebuild
# uses the same makensis and stubs. Bump with the image.
nsis_deb_version=3.11-1

if ! command -v makensis >/dev/null 2>&1; then
	if [ "$(id -u)" = 0 ] && command -v apt-get >/dev/null 2>&1; then
		echo "==> installing nsis=$nsis_deb_version"
		export DEBIAN_FRONTEND=noninteractive
		apt-get update -qq
		apt-get install -y -qq --no-install-recommends "nsis=$nsis_deb_version"
	else
		die "makensis not found: install NSIS 3 (Debian/Ubuntu: apt-get install nsis; macOS: brew install makensis; Windows: https://nsis.sourceforge.io)"
	fi
fi
echo "makensis $(makensis -VERSION)"

version="${PKG_VERSION:-$(scripts/version.sh)}"
numeric="$(windows_version_numeric "$version")"
exe="$root/bin/postsonde-windows-$arch.exe"
[ -f "$exe" ] || die "$exe is missing: build it first (scripts/build-windows.sh $arch)"

# Signing, only with a certificate (scripts/sign-windows.sh): the app before
# it is packed, then makensis signs the uninstaller and the installer.
sign=()
if [ -n "${POSTSONDE_WINDOWS_CERTIFICATE:-}" ]; then
	if ! command -v osslsigncode >/dev/null 2>&1 && [ "$(id -u)" = 0 ] && command -v apt-get >/dev/null 2>&1; then
		echo "==> installing osslsigncode"
		export DEBIAN_FRONTEND=noninteractive
		{ apt-get update -qq && apt-get install -y -qq --no-install-recommends osslsigncode; } >/dev/null
	fi
	scripts/sign-windows.sh "$exe"
	sign=("-DSIGN_COMMAND=$root/scripts/sign-windows.sh")
else
	echo "==> no POSTSONDE_WINDOWS_CERTIFICATE: the app and the installer stay unsigned"
fi

mkdir -p dist
out="$root/dist/PostSonde-$version-windows-$arch-setup.exe"
rm -f "$out"

echo "==> makensis -> $out (version $version, $numeric)"
# -NOCONFIG: no nsisconf.nsh of the machine. -INPUTCHARSET: the .nsi is
# UTF-8 without a BOM. -WX: warnings are errors.
makensis -V3 -NOCONFIG -WX -INPUTCHARSET UTF8 \
	"-D$arg=$exe" \
	"-DINFO_PRODUCTVERSION=$version" \
	"-DVERSION_NUMERIC=$numeric" \
	"-DOUTFILE=$out" \
	${sign[@]+"${sign[@]}"} \
	build/windows/nsis/project.nsi

size="$(wc -c <"$out" | tr -d ' ')"
echo "==> $out: $size bytes"
