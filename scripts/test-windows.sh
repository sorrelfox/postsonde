#!/usr/bin/env bash
# test-windows.sh: install the NSIS installer for the current user without
# questions, check what it installed, start the app, and uninstall it again,
# checking that nothing stays behind. The release workflow runs it on
# windows-latest (x64) and windows-11-arm (ARM64), in Git Bash (docs/ci.md,
# Package tests).
#
#   scripts/test-windows.sh PostSonde-<version>-windows-<arch>-setup.exe
#
# Refuses to run where PostSonde is installed already: the installer would
# replace that installation.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"

setup="${1:?usage: $0 SETUP_EXE}"
version="$(basename "$setup" | sed -n 's/^PostSonde-\(.*\)-windows-[a-z0-9]*-setup\.exe$/\1/p')"
[ -n "$version" ] || die "not an installer's name: $setup"
case "$(uname -s)" in
MINGW* | MSYS*) ;;
*) die "run it on Windows, in Git Bash" ;;
esac
# Windows programs take Windows options: no path conversion of "/S".
native() { MSYS2_ARG_CONV_EXCL='*' "$@"; }

key='HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\PostSonde'
if native reg query "$key" >/dev/null 2>&1; then
	die "PostSonde is installed already; the test would replace it"
fi
dir="$(cygpath -u "$LOCALAPPDATA")/Programs/PostSonde"
shortcut="$(cygpath -u "$APPDATA")/Microsoft/Windows/Start Menu/Programs/PostSonde.lnk"

echo "--- install, for this user, silent:"
native "$setup" /S || die "the installer failed"
for file in PostSonde.exe uninstall.exe licenses/LICENSE.txt licenses/THIRD_PARTY_NOTICES.md; do
	[ -f "$dir/$file" ] || die "the installer left no $file in $dir"
done
[ -f "$shortcut" ] || die "no Start menu entry"
native reg query "$key" /v DisplayVersion | grep DisplayVersion
native reg query "$key" /v DisplayVersion | grep -qF "$version" || die "the uninstall entry doesn't say version $version"

have="$("$dir/PostSonde.exe" --version | tr -d '\r')"
echo "$have"
[ "$have" = "postsonde $version" ] || die "expected \"postsonde $version\""
"$root/scripts/smoke-start.sh" "$dir/PostSonde.exe"

echo "--- uninstall, silent:"
native "$dir/uninstall.exe" /S
# The uninstaller runs from a copy of itself in the temporary folder and
# returns at once.
for _ in $(seq 60); do
	[ -e "$dir/PostSonde.exe" ] || [ -e "$shortcut" ] || break
	sleep 1
done
[ ! -e "$dir/PostSonde.exe" ] && [ ! -e "$dir/uninstall.exe" ] && [ ! -e "$shortcut" ] ||
	die "the uninstaller left the app or its Start menu entry"
for _ in $(seq 30); do
	native reg query "$key" >/dev/null 2>&1 || break
	sleep 1
done
! native reg query "$key" >/dev/null 2>&1 || die "the uninstaller left its registry key"
echo "test-windows: installed, started and uninstalled"
