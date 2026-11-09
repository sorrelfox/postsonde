#!/usr/bin/env bash
# clean.sh: remove build outputs and caches (`make clean`, `make distclean`,
# `make clean-docker`; docs/building.md).
#
#   scripts/clean.sh             bin/, dist/, frontend/dist/* (keeping the
#                                committed .gitkeep) and the build caches:
#                                .cache/ (incl. the Linux source export),
#                                .npm/, .task/, the AppImage tool cache and
#                                Wails template by-products
#   scripts/clean.sh distclean   the above plus frontend/node_modules
#   scripts/clean.sh docker      the postsonde-* Docker volumes that
#                                scripts/linux.sh caches downloads in
#
# Only paths that are gitignored; nothing tracked is touched.
set -euo pipefail

cd "$(dirname "$0")/.."

clean() {
	echo "==> removing bin/ dist/ frontend/dist/* .cache/ .npm/ .task/ and build by-products"
	rm -rf bin dist .cache .npm .task build/linux/appimage/build
	rm -f postsonde build/linux/appimage/postsonde build/linux/appimage/postsonde.png \
		build/linux/postsonde.desktop cmd/postsonde/*.syso
	find frontend/dist -mindepth 1 ! -path frontend/dist/.gitkeep -delete
}

case "${1:-clean}" in
clean)
	clean
	;;
distclean)
	clean
	echo "==> removing frontend/node_modules"
	rm -rf frontend/node_modules
	;;
docker)
	if ! command -v docker >/dev/null 2>&1; then
		echo "error: docker isn't on PATH" >&2
		exit 1
	fi
	volumes="$(docker volume ls -q | grep '^postsonde-' || true)"
	if [ -z "$volumes" ]; then
		echo "no postsonde-* Docker volumes"
		exit 0
	fi
	echo "==> removing Docker volumes: $(echo "$volumes" | tr '\n' ' ')"
	# shellcheck disable=SC2086 # one volume name per word
	docker volume rm $volumes
	;;
*)
	echo "usage: $0 [clean|distclean|docker]" >&2
	exit 2
	;;
esac
