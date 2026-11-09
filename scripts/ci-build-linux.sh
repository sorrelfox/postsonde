#!/usr/bin/env bash
# ci-build-linux.sh: build the PostSonde Linux binary for one GTK stack and
# one CPU architecture on a stock Ubuntu image. The linux:gtk4 and
# linux:gtk3 CI jobs run it; docs/ci.md (Build) explains the choices.
#
#   scripts/ci-build-linux.sh <gtk4|gtk3> <amd64|arm64>
#
#   gtk4  GTK4 + WebKitGTK 6.0 (Wails' default), on ubuntu:24.04
#   gtk3  GTK3 + WebKit2GTK 4.1 (`-tags gtk3`), on ubuntu:22.04
#
# Runs as root. Installs Go and the dev packages, then builds
# bin/postsonde-linux-<stack>-<arch>. When <arch> isn't the machine's own
# architecture it cross-compiles with Ubuntu's cross gcc against multiarch
# (:<arch>) dev packages. frontend/dist must already hold the Vite build
# (the frontend:build job), or the binary would embed an empty UI.
#
# main.version is scripts/version.sh (0.0.0~git<short sha> in CI);
# APP_VERSION overrides it.
set -euo pipefail

usage="usage: $0 <gtk4|gtk3> <amd64|arm64>"
stack="${1:?$usage}"
arch="${2:?$usage}"
app_version="${APP_VERSION:-$("$(dirname "$0")/version.sh")}"

# Go toolchain: the official tarball for the machine's architecture, at the
# version scripts/pins.env pins and verified against the checksums it copies
# from https://go.dev/dl/. Bump with go.mod and the golang image there.
# shellcheck source=scripts/pins.env
. "$(dirname "$0")/pins.env"
go_version=$POSTSONDE_PIN_GO_VERSION
declare -A go_sha256=(
	[amd64]=$POSTSONDE_PIN_GO_SHA256_AMD64
	[arm64]=$POSTSONDE_PIN_GO_SHA256_ARM64
)

# Build tags as in build/linux/Taskfile.yml (production build):
# `-tags production[,EXTRA_TAGS]`. The dev packages are the ones behind the
# `#cgo pkg-config` lines of each stack in Wails v3.0.0 (gtk4
# webkitgtk-6.0, or gtk+-3.0 gdk-3.0 webkit2gtk-4.1; plus x11, glib-2.0,
# gobject-2.0, gio-unix-2.0 and libsoup-3.0, which come in as dependencies
# or are listed).
#
# cross_pkgs: host-architecture packages a cross build adds. On 24.04,
# libglib2.0-dev (GLib 2.80, which took in libgirepository) ships
# <triple>-gi-compile-repository wrappers that run the target's tool, and
# so depends on `python3 | qemu-user | qemu-user-static`: plain `python3`
# of the target architecture, or a way to run its binaries. Left to itself
# apt picks python3:<arch>, whose maintainer scripts run the target's
# python3 and fail with "Exec format error" (no emulation on the runner).
# Asking for the host's qemu-user takes the second alternative instead;
# nothing runs under it (no qemu-user-binfmt, which --no-install-recommends
# leaves out), and the host's python3 serves the python3:any dependencies.
# 22.04's GLib 2.72 has no such dependency. docs/ci.md (arm64) has more.
case "$stack" in
gtk4)
	tags=production
	dev_pkgs=(libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev)
	cross_pkgs=(qemu-user)
	want_libs=(libgtk-4.so.1 libwebkitgtk-6.0.so.4)
	;;
gtk3)
	tags=production,gtk3
	dev_pkgs=(libgtk-3-dev libwebkit2gtk-4.1-dev libx11-dev)
	cross_pkgs=()
	want_libs=(libgtk-3.so.0 libwebkit2gtk-4.1.so.0)
	;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac

case "$arch" in
amd64) triple=x86_64-linux-gnu ;;
arm64) triple=aarch64-linux-gnu ;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac

cd "$(dirname "$0")/.."

if [ ! -f frontend/dist/index.html ]; then
	echo "frontend/dist/index.html is missing: build the frontend first (npm run build in frontend/)" >&2
	exit 1
fi

host="$(dpkg --print-architecture)"
# shellcheck source=/dev/null
codename="$(. /etc/os-release && echo "$VERSION_CODENAME")"
export DEBIAN_FRONTEND=noninteractive

if [ "$arch" = "$host" ]; then
	echo "==> native $stack build for $arch on $codename"
	pkgs=(gcc libc6-dev pkg-config "${dev_pkgs[@]}")
	cc=gcc
else
	echo "==> cross $stack build for $arch on $codename ($host)"
	# amd64 packages live on archive/security.ubuntu.com, every other
	# architecture on ports.ubuntu.com. The image only lists its own
	# mirror, so pin those entries to the host architecture and add the
	# target's mirror for the target architecture.
	case "$arch" in
	amd64)
		mirror=http://archive.ubuntu.com/ubuntu
		security=http://security.ubuntu.com/ubuntu
		;;
	*)
		mirror=http://ports.ubuntu.com/ubuntu-ports
		security=$mirror
		;;
	esac
	dpkg --add-architecture "$arch"
	if [ -f /etc/apt/sources.list.d/ubuntu.sources ]; then # 24.04: deb822
		sed -i "/^Types:/a Architectures: $host" /etc/apt/sources.list.d/ubuntu.sources
	fi
	if [ -f /etc/apt/sources.list ]; then # 22.04: one-line format
		sed -i -E "s/^deb (https?:)/deb [arch=$host] \1/" /etc/apt/sources.list
	fi
	cat >"/etc/apt/sources.list.d/cross-$arch.list" <<EOF
deb [arch=$arch] $mirror $codename main universe
deb [arch=$arch] $mirror $codename-updates main universe
deb [arch=$arch] $security $codename-security main universe
EOF
	pkgs=("gcc-${triple//_/-}" "libc6-dev:$arch" pkg-config ${cross_pkgs[@]+"${cross_pkgs[@]}"} "${dev_pkgs[@]/%/:$arch}")
	cc="$triple-gcc"
	# Resolve the target's .pc files only, never the host's.
	export PKG_CONFIG_LIBDIR="/usr/lib/$triple/pkgconfig:/usr/share/pkgconfig"
	unset PKG_CONFIG_PATH
fi

apt-get update -qq
apt-get install -y -qq --no-install-recommends ca-certificates curl file "${pkgs[@]}"
# The C toolchain and headers come from the distribution's current updates,
# not a pinned set: log their versions, so a rebuild that differs can be
# traced to them (docs/reproducible-builds.md).
echo "==> build packages"
dpkg-query -W "${pkgs[@]}" || true

echo "==> installing Go $go_version ($host)"
sum="${go_sha256[$host]:-}"
if [ -z "$sum" ]; then
	echo "no Go checksum for host architecture $host" >&2
	exit 1
fi
curl -fsSL -o /tmp/go.tar.gz "https://go.dev/dl/go$go_version.linux-$host.tar.gz"
echo "$sum  /tmp/go.tar.gz" | sha256sum -c -
rm -rf /usr/local/go
tar -C /usr/local -xzf /tmp/go.tar.gz
rm /tmp/go.tar.gz
export PATH="/usr/local/go/bin:$PATH"
go version

out="bin/postsonde-linux-$stack-$arch"
echo "==> go build -tags $tags -> $out (version $app_version)"
export GOTOOLCHAIN=local GOOS=linux GOARCH="$arch" CGO_ENABLED=1 CC="$cc"
# Go's default flags, minus the GTK deprecation warnings from the Wails C code.
export CGO_CFLAGS="-O2 -g -Wno-deprecated-declarations"
go build -tags "$tags" -trimpath -buildvcs=false \
	-ldflags "-w -s -X main.version=$app_version" \
	-o "$out" ./cmd/postsonde

echo "==> $out"
file "$out"
size="$(stat -c %s "$out")"
echo "size: $size bytes ($(numfmt --to=iec "$size"))"
needed="$("$triple-readelf" -d "$out" | grep NEEDED)"
echo "$needed"
for lib in "${want_libs[@]}"; do
	if ! grep -qF "[$lib]" <<<"$needed"; then
		echo "$out is not linked against $lib" >&2
		exit 1
	fi
done
echo "ok: $out links ${want_libs[*]}"
