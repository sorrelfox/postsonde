#!/usr/bin/env bash
# windows.sh: build the PostSonde Windows executables and installers from
# any OS through Docker (`make windows`; docs/packaging-windows.md). Each
# step runs a script in a CI image, so a CI job can run the same step:
#
#   frontend    scripts/build-frontend.sh            node:26-trixie-slim
#   executable  scripts/build-windows.sh <arch>      golang:1.27-trixie (cross-build, no cgo)
#   installer   scripts/package-windows-installer.sh golang:1.27-trixie (+ Debian's nsis)
#   checksums   scripts/sha256sums.sh                ubuntu:24.04
#
# The images are pinned by digest in scripts/pins.env (read with lib.sh's
# pinned_image).
#
#   scripts/windows.sh [amd64|arm64|all]     default: all
#
# The containers work on a clean export of HEAD (git archive) in
# .cache/windows/src, never on the working copy, so uncommitted changes are
# not built. Results are collected in dist/windows/: the executables
# (postsonde-windows-<arch>.exe), the installers
# (PostSonde-<version>-windows-<arch>-setup.exe) and SHA256SUMS over the
# installers. Everything runs in containers of Docker's own architecture;
# Go cross-compiles both targets and makensis writes the same x86 installer
# stub for both, so nothing is emulated.
#
# Environment:
#   PKG_VERSION         the version to stamp (default: scripts/version.sh)
#   WINDOWS_GOCACHE_TAG suffix for the Go build cache directory, so a run
#                       can start from an empty cache (a reproducibility check)
#
# Go modules, the Go build cache, npm and apt downloads are cached in the
# same named Docker volumes as scripts/linux.sh (postsonde-*); `make
# clean-docker` removes them.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"
cd "$root"

image_node="$(pinned_image NODE)"
image_golang="$(pinned_image GOLANG)"
image_ubuntu_2404="$(pinned_image UBUNTU_24_04)"

usage="usage: $0 [amd64|arm64|all]"
case "${1:-all}" in
all) archs="amd64 arm64" ;;
amd64 | arm64) archs="$1" ;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac

if ! command -v docker >/dev/null 2>&1; then
	echo "error: the Windows builds need Docker, and docker isn't on PATH." >&2
	echo "Install Docker Desktop (macOS, Windows) or Docker Engine (Linux): https://docs.docker.com/get-docker/" >&2
	exit 1
fi
if ! docker info >/dev/null 2>&1; then
	echo "error: docker is installed but the daemon isn't reachable; start Docker Desktop (or the docker service) and retry." >&2
	exit 1
fi
docker_arch="$(docker version --format '{{.Server.Arch}}')"
case "$docker_arch" in
amd64 | arm64) ;;
*)
	echo "error: unsupported Docker architecture '$docker_arch' (need amd64 or arm64)" >&2
	exit 1
	;;
esac
native="linux/$docker_arch"

# The export is of HEAD, so a "+dirty" version would be wrong.
if [ -n "${PKG_VERSION:-}" ]; then
	version="$PKG_VERSION"
else
	version="$(scripts/version.sh)"
	case "$version" in
	*+dirty)
		version="${version%+dirty}"
		echo "warning: uncommitted changes are not built: the containers build HEAD ($version); commit first to include them" >&2
		;;
	esac
fi
gocache_tag="${WINDOWS_GOCACHE_TAG:+-$WINDOWS_GOCACHE_TAG}"

src=.cache/windows/src
out=dist/windows

# Containers run as root; on a Linux host hand their outputs back to the
# user afterwards. (Docker Desktop maps ownership by itself.)
if [ "$(uname -s)" = Linux ]; then
	HOST_UID="$(id -u)"
	HOST_GID="$(id -g)"
	export HOST_UID HOST_GID
fi

# Runs in every container before the command: keep apt's downloads for the
# cache volume (the Debian and Ubuntu images delete them after each
# install), and chown the outputs back on Linux hosts.
prelude="$(
	cat <<'EOF'
set -e
if [ -f /etc/apt/apt.conf.d/docker-clean ]; then
	rm /etc/apt/apt.conf.d/docker-clean
	echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' >/etc/apt/apt.conf.d/99keep-downloads
fi
if [ -n "${HOST_UID:-}" ]; then
	trap 'for d in /src /out; do if [ -d "$d" ]; then chown -R "$HOST_UID:$HOST_GID" "$d"; fi; done' EXIT
fi
"$@"
EOF
)"

# docker_run <image> <command...>: runs the command in /src (the export)
# with the version and the extra `docker run` arguments in docker_args.
docker_args=()
docker_run() {
	local image="$1"
	shift
	echo "==> [$image] $*"
	docker run --rm --platform "$native" \
		-v "$root/$src:/src" -w /src \
		-e PKG_VERSION="$version" -e APP_VERSION="$version" \
		-e HOST_UID -e HOST_GID \
		${docker_args[@]+"${docker_args[@]}"} \
		"$image" bash -c "$prelude" bash "$@"
}

echo "==> PostSonde $version for windows: archs [$archs] (Docker: $native)"

echo "==> exporting HEAD to $src"
rm -rf "$src"
mkdir -p "$src"
git archive --format=tar HEAD | tar -x -C "$src"

docker_args=(-v postsonde-npm:/cache/npm -e npm_config_cache=/cache/npm -e npm_config_update_notifier=false)
docker_run "$image_node" scripts/build-frontend.sh

image_golang_tag="${image_golang%%@*}"
docker_args=(
	-v postsonde-go-mod:/cache/go-mod -e GOMODCACHE=/cache/go-mod
	-v postsonde-go-build:/cache/go-build -e "GOCACHE=/cache/go-build/windows-on-$docker_arch$gocache_tag"
	-v "postsonde-apt-${image_golang_tag/:/-}-$docker_arch:/var/cache/apt/archives"
	-e GOTOOLCHAIN=local
)
# $archs: one architecture per word. SC2016: expanded in the container.
# shellcheck disable=SC2016,SC2086
docker_run "$image_golang" bash -ec '
	CGO_ENABLED=0 scripts/install-wails3.sh
	export PATH="$(go env GOPATH)/bin:$PATH"
	for arch in "$@"; do
		scripts/build-windows.sh "$arch"
		scripts/package-windows-installer.sh "$arch"
	done' bash $archs

rm -rf "$out"
mkdir -p "$out"
for arch in $archs; do
	cp "$src/bin/postsonde-windows-$arch.exe" "$src/dist/PostSonde-$version-windows-$arch-setup.exe" "$out/"
done
docker_args=(-v "$root/$out:/out")
docker_run "$image_ubuntu_2404" scripts/sha256sums.sh /out

echo "==> $out:"
ls -l "$out"
