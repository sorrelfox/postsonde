#!/usr/bin/env bash
# build-windows.sh: cross-build the PostSonde Windows executable for one
# CPU architecture, on any OS with Go and wails3
# (docs/packaging-windows.md). Wails on Windows needs no cgo, so this is a
# plain `GOOS=windows go build`; scripts/windows.sh runs it in the golang
# image, and the windows:build CI job on the Linux runner (docs/ci.md).
#
#   scripts/build-windows.sh <amd64|arm64>
#
# Writes bin/postsonde-windows-<arch>.exe. The same steps as
# `wails3 build GOOS=windows ARCH=<arch>` (build/windows/Taskfile.yml),
# minus the frontend build, go mod tidy and the icon sync, plus the version
# stamped into the version resource:
#   1. the Windows resources (icon, manifest, version info) as a .syso next
#      to the main package: `wails3 generate syso`, with build/windows/
#      info.json's versions replaced by this build's;
#   2. `go build -tags production -trimpath -buildvcs=false
#      -ldflags "-w -s -H windowsgui -X main.version=<version>"`;
#   3. checks of the result without Windows tools: the PE machine and GUI
#      subsystem, Go's build info, the stamped version and the manifest.
#
# frontend/dist must already hold the Vite build (scripts/build-frontend.sh),
# or the binary would embed an empty UI. main.version is scripts/version.sh
# (APP_VERSION overrides it). Needs wails3 at the go.mod version
# (scripts/install-wails3.sh).
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"
cd "$root"

usage="usage: $0 <amd64|arm64>"
arch="${1:?$usage}"
case "$arch" in
amd64) want_machine=8664 ;; # PE/COFF IMAGE_FILE_MACHINE_*
arm64) want_machine=aa64 ;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac

if [ ! -f frontend/dist/index.html ]; then
	die "frontend/dist/index.html is missing: build the frontend first (scripts/build-frontend.sh)"
fi
require_wails3

version="${APP_VERSION:-$(scripts/version.sh)}"
numeric="$(windows_version_numeric "$version")"
out="bin/postsonde-windows-$arch.exe"
syso="cmd/postsonde/wails_windows_$arch.syso"

work="$(mktemp -d)"
trap 'rm -rf "$work" "$syso"' EXIT

# build/windows/info.json carries build/config.yml's placeholder version
# (0.0.1). The fixed file version takes four numbers; the strings take the
# full version.
sed -E \
	-e "s/(\"file_version\": *)\"[^\"]*\"/\1\"$numeric\"/" \
	-e "s/(\"ProductVersion\": *)\"[^\"]*\"/\1\"$version\"/" \
	build/windows/info.json >"$work/info.json"
grep -q "\"file_version\": *\"$numeric\"" "$work/info.json" ||
	die "build/windows/info.json has no fixed file_version to stamp"

echo "==> wails3 generate syso ($arch, version $version, file version $numeric)"
wails3 generate syso -arch "$arch" -icon build/windows/icon.ico \
	-manifest build/windows/wails.exe.manifest -info "$work/info.json" -out "$syso"

echo "==> go build -tags production -> $out (version $version, $(go version))"
mkdir -p bin
GOOS=windows GOARCH="$arch" CGO_ENABLED=0 go build -tags production -trimpath -buildvcs=false \
	-ldflags "-w -s -H windowsgui -X main.version=$version" \
	-o "$out" ./cmd/postsonde

size="$(wc -c <"$out" | tr -d ' ')"
echo "==> $out: $size bytes"

# What was built, checked without Windows tools: the PE header (od) and
# Go's build info (go version -m reads any GOOS's binary).
#   offset 0x3c: where the PE header starts; there "PE\0\0", then the
#   machine; the subsystem is 68 bytes into the optional header, which
#   starts 24 bytes after "PE\0\0" (2 = Windows GUI).
le16() { od -An -tx2 -j "$2" -N2 "$1" | tr -d ' '; }
le32() { od -An -tu4 -j "$2" -N4 "$1" | tr -d ' '; }
[ "$(head -c2 "$out")" = MZ ] || die "$out is not a PE file (no MZ header)"
pe="$(le32 "$out" 60)"
[ "$(od -An -c -j "$pe" -N2 "$out" | tr -d ' ')" = PE ] || die "$out: no PE signature at $pe"
machine="$(le16 "$out" $((pe + 4)))"
subsystem="$(le16 "$out" $((pe + 24 + 68)))"
echo "PE machine: 0x$machine, subsystem: $((16#$subsystem))"
[ "$machine" = "$want_machine" ] || die "$out: PE machine 0x$machine, want 0x$want_machine ($arch)"
[ "$subsystem" = 0002 ] || die "$out: PE subsystem $((16#$subsystem)), want 2 (Windows GUI, -H windowsgui)"
buildinfo="$(go version -m "$out")"
for want in "GOOS=windows" "GOARCH=$arch" "CGO_ENABLED=0" "-tags=production"; do
	grep -qF -- "$want" <<<"$buildinfo" || die "$out: Go build info lacks $want"
done
# -trimpath leaves -ldflags out of the build info, so look for the stamped
# version and the manifest (its assembly name, plain text in .rsrc) in the
# binary itself.
grep -qaF "$version" "$out" || die "$out: main.version $version not found"
grep -qaF 'name="io.github.sorrelfox.PostSonde"' "$out" || die "$out: no application manifest (syso not linked?)"
echo "ok: $out (windows/$arch, GUI, manifest and version resource linked)"
