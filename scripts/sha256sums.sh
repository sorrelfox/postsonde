#!/usr/bin/env bash
# sha256sums.sh: write SHA256SUMS over the packages in a directory and
# verify it. The linux:packages CI job runs it on dist/, scripts/linux.sh
# on dist/linux/, scripts/package-macos.sh on dist/macos/,
# scripts/windows.sh on dist/windows/.
#
#   scripts/sha256sums.sh <dir>
#
# Covers every *.deb, *.rpm (binary and source), *.AppImage, *.flatpak,
# *.dmg, *-setup.exe (the Windows installers, not the bare executables) and
# *.tar.xz (the source tarball) in <dir>; fails if there are none.
# Uses GNU coreutils' sha256sum where there is one (the Ubuntu containers),
# otherwise `shasum -a 256` (macOS); both write the same format.
set -euo pipefail

dir="${1:?usage: $0 <dir>}"
cd "$dir"

if command -v sha256sum >/dev/null 2>&1; then
	sha256=(sha256sum)
else
	sha256=(shasum -a 256)
fi

shopt -s nullglob
files=(*.deb *.rpm *.AppImage *.flatpak *.dmg *-setup.exe *.tar.xz)
if [ "${#files[@]}" -eq 0 ]; then
	echo "sha256sums: no packages (.deb, .rpm, .AppImage, .flatpak, .dmg, -setup.exe, .tar.xz) in $dir" >&2
	exit 1
fi
LC_ALL=C "${sha256[@]}" -- "${files[@]}" >SHA256SUMS
cat SHA256SUMS
"${sha256[@]}" -c SHA256SUMS
