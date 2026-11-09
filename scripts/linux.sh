#!/usr/bin/env bash
# linux.sh: build the Linux binaries and packages from any OS through Docker
# (`make linux`, `make linux-gtk4`, `make linux-gtk3`, `make linux-packages`;
# docs/building.md). Each step runs the script the matching CI job runs, in
# that job's image, so local builds and CI can't drift apart:
#
#   frontend   scripts/build-frontend.sh          node:26-trixie-slim
#   binaries   scripts/ci-build-linux.sh          ubuntu:24.04 (gtk4), ubuntu:22.04 (gtk3)
#   .deb       scripts/package-linux-deb.sh       golang:1.27-trixie
#   AppImage   scripts/package-linux-appimage.sh  ubuntu:22.04 of the target architecture
#   checksums  scripts/sha256sums.sh              ubuntu:24.04
#   sources    scripts/package-sources.sh         golang:1.27-trixie
#   .rpm       scripts/package-linux-rpm.sh       fedora:44 or fedora:45 of the target architecture
#   Flatpak    scripts/package-flatpak.sh         fedora:45 with flatpak-builder (privileged)
#
# The images are the CI jobs' own, pinned by digest in scripts/pins.env
# (read with lib.sh's pinned_image).
#
#   scripts/linux.sh all      [amd64|arm64|all]            binaries for both stacks + packages
#   scripts/linux.sh build    [gtk4|gtk3|all] [amd64|arm64|all]
#   scripts/linux.sh packages [amd64|arm64|all]            from the binaries in dist/linux/
#   scripts/linux.sh sources                               the source tarball
#   scripts/linux.sh rpm      [amd64|arm64]                the source tarball, then the RPM,
#                                                          installed and started (default: Docker's arch;
#                                                          the Fedora release: FEDORA, below)
#   scripts/linux.sh flatpak                               the source tarball, then the Flatpak
#                                                          bundle, installed and started (x86_64)
#
# The containers work on a clean export of HEAD (git archive) in
# .cache/linux/src, never on the working copy: npm ci in a container would
# replace the host's node_modules with Linux binaries. Uncommitted changes
# are therefore not built. Results are collected in dist/linux/: the
# binaries, the .deb packages, the AppImages, the source tarball, the RPMs,
# the Flatpak bundle and SHA256SUMS.
#
# The Flatpak is built and tested inside a privileged container
# (flatpak-builder runs bubblewrap), with its own Flatpak installation in the
# postsonde-flatpak volume: the host's Flatpak installation isn't touched.
#
# Builds run in containers of Docker's own architecture; the other target
# is cross-compiled, as on the CI runner. The AppImage of the other
# architecture runs emulated (Docker Desktop has emulation built in; a Linux
# host needs qemu/binfmt).
#
# Environment:
#   PKG_VERSION     the version to stamp (default: scripts/version.sh)
#   LINUX_PLATFORM  container platform for the binary builds, e.g.
#                   linux/amd64 to build exactly as an amd64 runner does
#   SOURCE_DATE_EPOCH  timestamp for the packages (default:
#                   scripts/source-date-epoch.sh, the commit time of HEAD)
#   LINUX_GOCACHE_TAG  suffix for the Go build cache directories, so a run
#                   can start from an empty cache (scripts/verify-reproducible.sh)
#   FEDORA          the Fedora release of `rpm`: 44, 45 or all (default: this
#                   machine's release when it is 44 or 45, otherwise 45, the newest)
#
# Go modules, Go build caches, npm and apt downloads and the AppImage tools
# are cached in named Docker volumes (postsonde-*); `make clean-docker`
# removes them.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"
cd "$root"

image_node="$(pinned_image NODE)"
image_golang="$(pinned_image GOLANG)"
image_ubuntu_2404="$(pinned_image UBUNTU_24_04)"
image_ubuntu_2204="$(pinned_image UBUNTU_22_04)"
image_fedora="$(pinned_image FEDORA_45)"

usage="usage: $0 all [amd64|arm64|all]
       $0 build [gtk4|gtk3|all] [amd64|arm64|all]
       $0 packages [amd64|arm64|all]
       $0 sources
       $0 rpm [amd64|arm64]
       $0 flatpak"
cmd="${1:-}"
case "$cmd" in
all | packages)
	stack_arg=all
	arch_arg="${2:-all}"
	;;
build)
	stack_arg="${2:-all}"
	arch_arg="${3:-all}"
	;;
sources | flatpak)
	stack_arg=all
	arch_arg=all
	;;
rpm)
	stack_arg=all
	# Docker's own architecture unless named: the RPM builds natively.
	arch_arg="${2:-native}"
	# This machine's Fedora release when it is one of the pinned ones,
	# otherwise the newest.
	# shellcheck source=/dev/null
	host_fedora="$(. /etc/os-release 2>/dev/null && [ "${ID:-}" = fedora ] && echo "${VERSION_ID:-}" || true)"
	case "${FEDORA:-}" in
	all) fedora_releases="44 45" ;;
	44 | 45) fedora_releases="$FEDORA" ;;
	"")
		case "$host_fedora" in
		44 | 45) fedora_releases="$host_fedora" ;;
		*) fedora_releases=45 ;;
		esac
		;;
	*)
		echo "FEDORA must be 44, 45 or all, not '$FEDORA'" >&2
		exit 2
		;;
	esac
	;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac
case "$stack_arg" in
all) stacks="gtk4 gtk3" ;;
gtk4 | gtk3) stacks="$stack_arg" ;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac
case "$arch_arg" in
all) archs="amd64 arm64" ;;
amd64 | arm64) archs="$arch_arg" ;;
native) archs= ;; # Docker's, once it is known
*)
	echo "$usage" >&2
	exit 2
	;;
esac

if ! command -v docker >/dev/null 2>&1; then
	echo "error: the Linux builds need Docker, and docker isn't on PATH." >&2
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
build_platform="${LINUX_PLATFORM:-$native}"
archs="${archs:-$docker_arch}"

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
# The containers build an export without git: hand them the commit time.
SOURCE_DATE_EPOCH="$(scripts/source-date-epoch.sh)"
export SOURCE_DATE_EPOCH
gocache_tag="${LINUX_GOCACHE_TAG:+-$LINUX_GOCACHE_TAG}"

src=.cache/linux/src
out=dist/linux

# Containers run as root; on a Linux host hand their outputs back to the
# user afterwards. (Docker Desktop maps ownership by itself.)
if [ "$(uname -s)" = Linux ]; then
	HOST_UID="$(id -u)"
	HOST_GID="$(id -g)"
	export HOST_UID HOST_GID
fi

# Runs in every container before the command: keep apt's downloads for the
# cache volume (the Ubuntu images delete them after each install), and chown
# the outputs back on Linux hosts.
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

# docker_run <platform> <image> <command...>: runs the command in /src (the
# export) with the version and the extra `docker run` arguments in the
# docker_args array.
docker_args=()
docker_run() {
	local platform="$1" image="$2"
	shift 2
	echo "==> [$image $platform] $*"
	docker run --rm --platform "$platform" \
		-v "$root/$src:/src" -w /src \
		-e PKG_VERSION="$version" -e APP_VERSION="$version" -e SOURCE_DATE_EPOCH \
		-e HOST_UID -e HOST_GID \
		${docker_args[@]+"${docker_args[@]}"} \
		"$image" bash -c "$prelude" bash "$@"
}

export_source() {
	echo "==> exporting HEAD to $src"
	rm -rf "$src"
	mkdir -p "$src"
	git archive --format=tar HEAD | tar -x -C "$src"
}

# dist/linux/ holds one version at a time: a different version starts over,
# so binaries and packages of two commits never mix.
prepare_out() {
	if [ -f "$out/.version" ] && [ "$(cat "$out/.version")" != "$version" ]; then
		echo "==> $out holds $(cat "$out/.version"), building $version: clearing it"
		rm -rf "$out"
	fi
	mkdir -p "$out"
	echo "$version" >"$out/.version"
}

build_frontend() {
	docker_args=(-v postsonde-npm:/cache/npm -e npm_config_cache=/cache/npm -e npm_config_update_notifier=false)
	docker_run "$native" "$image_node" scripts/build-frontend.sh
}

build_binaries() {
	local stack arch image image_tag platform_arch
	platform_arch="${build_platform#linux/}"
	for stack in $stacks; do
		case "$stack" in
		gtk4) image="$image_ubuntu_2404" ;;
		gtk3) image="$image_ubuntu_2204" ;;
		esac
		image_tag="${image%%@*}"
		for arch in $archs; do
			docker_args=(
				-v postsonde-go-mod:/cache/go-mod -e GOMODCACHE=/cache/go-mod
				-v postsonde-go-build:/cache/go-build -e "GOCACHE=/cache/go-build/$stack-$arch-on-$platform_arch$gocache_tag"
				-v "postsonde-apt-${image_tag/:/-}-$platform_arch:/var/cache/apt/archives"
			)
			docker_run "$build_platform" "$image" scripts/ci-build-linux.sh "$stack" "$arch"
			cp "$src/bin/postsonde-linux-$stack-$arch" "$out/"
		done
	done
}

build_packages() {
	local arch stack bin
	for arch in $archs; do
		for stack in gtk4 gtk3; do
			bin="postsonde-linux-$stack-$arch"
			if [ ! -f "$src/bin/$bin" ]; then
				echo "error: $out/$bin is missing; build it first: make linux-$stack ARCH=$arch" >&2
				exit 1
			fi
		done
	done
	rm -rf "$src/dist"

	# .deb: nfpm writes any architecture on any host, so one container.
	docker_args=(
		-v postsonde-go-mod:/cache/go-mod -e GOMODCACHE=/cache/go-mod
		-v postsonde-go-build:/cache/go-build -e "GOCACHE=/cache/go-build/deb$gocache_tag"
		-e GOTOOLCHAIN=local -e CGO_ENABLED=0
	)
	# $archs: one architecture per word. SC2016: expanded in the container.
	# shellcheck disable=SC2016,SC2086
	docker_run "$native" "$image_golang" bash -ec '
		scripts/install-wails3.sh
		export PATH="$(go env GOPATH)/bin:$PATH"
		for stack in gtk4 gtk3; do
			for arch in "$@"; do
				scripts/package-linux-deb.sh "$stack" "$arch"
			done
		done' bash $archs

	# AppImage: linuxdeploy can't cross-package, so an ubuntu:22.04 container
	# of each target architecture (emulated when it isn't Docker's own).
	for arch in $archs; do
		docker_args=(
			-v "postsonde-apt-ubuntu-22.04-$arch:/var/cache/apt/archives"
			-v postsonde-appimage-tools:/src/build/linux/appimage/build
		)
		docker_run "linux/$arch" "$image_ubuntu_2204" scripts/package-linux-appimage.sh "$arch"
	done

	cp "$src"/dist/*.deb "$src"/dist/*.AppImage "$out/"
	docker_args=(-v "$root/$out:/out")
	docker_run "$native" "$image_ubuntu_2404" scripts/sha256sums.sh /out
}

# A packages-only run takes the binaries from dist/linux/, which must be of
# this version.
packages_from_out() {
	local arch stack
	if [ ! -f "$out/.version" ] || [ "$(cat "$out/.version")" != "$version" ]; then
		echo "error: $out has no binaries of $version; build them first: make linux" >&2
		exit 1
	fi
	mkdir -p "$src/bin"
	for arch in $archs; do
		for stack in gtk4 gtk3; do
			if [ -f "$out/postsonde-linux-$stack-$arch" ]; then
				cp "$out/postsonde-linux-$stack-$arch" "$src/bin/"
			fi
		done
	done
}

# The source tarball (the export, the frontend build and vendor/), written
# to dist/linux/. The frontend is built in the export first.
tarball="$out/postsonde-$version.tar.xz"
build_sources() {
	build_frontend
	docker_args=(
		-v postsonde-go-mod:/cache/go-mod -e GOMODCACHE=/cache/go-mod
		-e GOTOOLCHAIN=local -v "$root/$out:/out"
	)
	docker_run "$native" "$image_golang" scripts/package-sources.sh /out
}

# The RPM of each Fedora release in $fedora_releases and each architecture
# in $archs, from the source tarball, in a clean Fedora container of that
# release and architecture; then installed and started.
build_rpm() {
	local arch rpm_arch
	[ -s "$tarball" ] || build_sources
	for arch in $archs; do
		case "$arch" in
		amd64) rpm_arch=x86_64 ;;
		arm64) rpm_arch=aarch64 ;;
		esac
		for release in $fedora_releases; do
			docker_args=(-v "$root/$out:/out")
			docker_run "linux/$arch" "$(pinned_image "FEDORA_$release")" \
				scripts/package-linux-rpm.sh "/out/$(basename "$tarball")" "/out/rpm-$release-$arch"
			mv "$out/rpm-$release-$arch"/postsonde-*.rpm "$out/"
			rm -rf "$out/rpm-$release-$arch"
			scripts/test-linux-packages.sh "$out/postsonde-$version-1.fc$release.$rpm_arch.rpm"
		done
	done
}

# The Flatpak bundle, built and tested in a privileged Fedora container
# (bubblewrap needs namespaces) with a Flatpak installation of its own in
# the postsonde-flatpak volume, which keeps the GNOME SDK between runs.
build_flatpak() {
	[ "$docker_arch" = amd64 ] || die "the Flatpak is built for x86_64 only (Docker here is $docker_arch)"
	[ -s "$tarball" ] || build_sources
	docker_args=(
		--privileged -v postsonde-flatpak:/root/.local/share/flatpak
		-v "postsonde-dnf-fedora-45-$docker_arch:/var/cache/libdnf5"
		-v "$root/$out:/out" -e POSTSONDE_FLATPAK_PIN=1
	)
	# shellcheck disable=SC2016 # expanded in the container
	docker_run "$native" "$image_fedora" bash -ec '
		dnf -y -q --setopt=keepcache=True install flatpak flatpak-builder git-core \
			xorg-x11-server-Xvfb xorg-x11-xauth dbus-daemon >/dev/null
		scripts/package-flatpak.sh "$1" /tmp/flatpak
		scripts/test-flatpak.sh /tmp/flatpak/PostSonde-*-x86_64.flatpak
		cp /tmp/flatpak/PostSonde-*-x86_64.flatpak /out/' bash "/out/$(basename "$tarball")"
}

echo "==> PostSonde $version (SOURCE_DATE_EPOCH $SOURCE_DATE_EPOCH) for linux: stacks [$stacks], archs [$archs]; builds on $build_platform (Docker: $native)"
case "$cmd" in
all)
	prepare_out
	export_source
	build_frontend
	build_binaries
	build_packages
	;;
build)
	prepare_out
	export_source
	build_frontend
	build_binaries
	;;
packages)
	export_source
	packages_from_out
	build_packages
	;;
sources)
	prepare_out
	export_source
	build_sources
	;;
rpm)
	prepare_out
	export_source
	build_rpm
	;;
flatpak)
	prepare_out
	export_source
	build_flatpak
	;;
esac
case "$cmd" in
sources | rpm | flatpak)
	docker_args=(-v "$root/$out:/out")
	docker_run "$native" "$image_ubuntu_2404" scripts/sha256sums.sh /out
	;;
esac

echo "==> $out:"
ls -l "$out"
