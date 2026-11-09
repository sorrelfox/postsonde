#!/usr/bin/env bash
# package-linux-deb.sh: package one Linux binary as a .deb. The
# linux:deb CI job runs it for both stacks and both architectures;
# docs/packaging.md explains the package layout and the choices.
#
#   scripts/package-linux-deb.sh <gtk4|gtk3> <amd64|arm64>
#
#   gtk4  bin/postsonde-linux-gtk4-<arch> -> postsonde       (Ubuntu 24.04+, Debian 13+)
#   gtk3  bin/postsonde-linux-gtk3-<arch> -> postsonde-gtk3  (Ubuntu 22.04+, Debian 12+)
#
# The binary must already exist (scripts/ci-build-linux.sh). Needs `wails3`
# (the CLI at the Wails version in go.mod) on PATH: it builds the package
# with nfpm through `wails3 tool package`, from build/linux/nfpm/<name>.yaml.
# nfpm writes packages for any architecture, so this runs on any host.
#
# Version: scripts/version.sh (PKG_VERSION, a vX.Y.Z tag on HEAD without
# the "v", or 0.0.0~git<short sha>).
#
# Writes dist/<name>_<version>_<arch>.deb.
set -euo pipefail

usage="usage: $0 <gtk4|gtk3> <amd64|arm64>"
stack="${1:?$usage}"
arch="${2:?$usage}"

case "$stack" in
gtk4) name=postsonde ;;
gtk3) name=postsonde-gtk3 ;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac
case "$arch" in
amd64 | arm64) ;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac

cd "$(dirname "$0")/.."

binary="bin/postsonde-linux-$stack-$arch"
if [ ! -f "$binary" ]; then
	echo "$binary is missing: build it first (scripts/ci-build-linux.sh $stack $arch)" >&2
	exit 1
fi

version="$(scripts/version.sh)"
# nfpm stamps every file, the control archive and the ar members with
# SOURCE_DATE_EPOCH when it is set: the commit time, so the .deb is
# the same for every build of one commit.
if sde="$(scripts/source-date-epoch.sh)"; then
	export SOURCE_DATE_EPOCH="$sde"
fi

stage="dist/.stage/deb-$name-$arch"
rm -rf "$stage"
mkdir -p "$stage"

# /usr/share/doc/<name>/copyright in the machine-readable format, with the
# MIT text taken from LICENSE (continuation lines indented, blank lines ".").
{
	cat <<'EOF'
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: PostSonde
Upstream-Contact: Sorrelfox <348316453+sorrelfox@users.noreply.github.com>
Source: https://github.com/sorrelfox/PostSonde
Comment: The binary also bundles third-party components (the Go standard
 library, Wails, React, CodeMirror, Lezer and two fonts). They are listed,
 with their licenses, in THIRD_PARTY_NOTICES.md in this directory; the
 font licenses are in fonts/*/OFL.txt.

Files: *
Copyright: 2026 PostSonde contributors
License: MIT

Files: frontend/src/assets/fonts/instrument-sans/*
Copyright: 2022 The Instrument Sans Project Authors (https://github.com/Instrument/instrument-sans)
License: OFL-1.1
 See /usr/share/doc/NAME/fonts/instrument-sans/OFL.txt.

Files: frontend/src/assets/fonts/jetbrains-mono/*
Copyright: 2020 The JetBrains Mono Project Authors (https://github.com/JetBrains/JetBrainsMono)
License: OFL-1.1
 See /usr/share/doc/NAME/fonts/jetbrains-mono/OFL.txt.

License: MIT
EOF
	sed -e 's/^$/./' -e 's/^/ /' LICENSE
} | sed "s|/usr/share/doc/NAME/|/usr/share/doc/$name/|" >"$stage/copyright"

echo "==> $name $version ($arch) from $binary"
export PKG_VERSION="$version" PKG_ARCH="$arch" PKG_BINARY="$binary" PKG_STAGE="$stage"
wails3 tool package -name "$name" -format deb -config "build/linux/nfpm/$name.yaml" -out "$stage"

out="dist/${name}_${version}_${arch}.deb"
mv "$stage/$name.deb" "$out"
rm -rf "$stage"
rmdir dist/.stage 2>/dev/null || true
size="$(wc -c <"$out" | tr -d ' ')"
echo "==> $out ($size bytes)"
if command -v dpkg-deb >/dev/null; then
	dpkg-deb --info "$out"
	dpkg-deb --contents "$out"
fi
