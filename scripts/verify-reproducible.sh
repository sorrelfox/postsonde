#!/usr/bin/env bash
# verify-reproducible.sh: build HEAD twice from clean exports and compare
# the results file by file (`make verify-reproducible`;
# docs/reproducible-builds.md).
#
#   scripts/verify-reproducible.sh [frontend|macos|linux|all]...
#
#   frontend  scripts/build-frontend.sh: frontend/dist
#   macos     scripts/package-macos.sh: frontend/dist, PostSonde.app (every
#             file in it), the .dmg (macOS only)
#   linux     scripts/linux.sh all: the four binaries, .debs and AppImages
#             (needs Docker); ARCH=amd64 or ARCH=arm64 builds only that
#             architecture's two binaries and packages
#   all       frontend, plus macos on a Mac, plus linux when Docker is up
# Default: all.
#
# frontend and macos build each run in its own `git archive` export of HEAD
# (.cache/repro/a/src and .cache/repro/b/src: different paths on purpose,
# so a build that embeds its directory shows up) with its own empty Go build
# cache. linux runs scripts/linux.sh twice, which exports HEAD itself, with
# a separate Go build cache per run (LINUX_GOCACHE_TAG); the outputs are
# moved out of dist/linux/ after each run. npm and Go module downloads are
# shared: both are checked against the lockfile and go.sum.
#
# Both runs get the same PKG_VERSION (scripts/version.sh) and
# SOURCE_DATE_EPOCH (scripts/source-date-epoch.sh). Each run writes
# .cache/repro/<run>/SHA256SUMS over everything it produced; the script
# prints the files whose hashes differ (and runs diffoscope on them if it
# is installed) and exits 1 if any do.
#
# Uncommitted changes are not built (the exports are of HEAD); the script
# refuses to run with a dirty tree unless ALLOW_DIRTY=1.
#
# Environment:
#   ARCH          amd64 or arm64: the linux target builds only that
#                 architecture (as `make linux ARCH=...`); default both
#   KEEP=1        keep .cache/repro/<run>/src after a successful run
#   REPRO_DIR     work directory (default .cache/repro)
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"
cd "$root"

targets=("$@")
[ "${#targets[@]}" -gt 0 ] || targets=(all)
want_frontend=0 want_macos=0 want_linux=0
for t in "${targets[@]}"; do
	case "$t" in
	frontend) want_frontend=1 ;;
	macos) want_macos=1 ;;
	linux) want_linux=1 ;;
	all)
		want_frontend=1
		[ "$(uname -s)" = Darwin ] && want_macos=1
		if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
			want_linux=1
		else
			echo "note: Docker isn't available; skipping the Linux builds" >&2
		fi
		;;
	*)
		echo "usage: $0 [frontend|macos|linux|all]..." >&2
		exit 2
		;;
	esac
done
linux_arch="${ARCH:-all}"
case "$linux_arch" in
all | amd64 | arm64) ;;
*) die "ARCH must be amd64 or arm64 (or unset for both), not '$linux_arch'" ;;
esac
# The macOS build includes the frontend build: one pair of builds covers both.
[ "$want_macos" = 1 ] && want_frontend=0

if [ -n "$(git status --porcelain --untracked-files=no)" ] && [ "${ALLOW_DIRTY:-}" != 1 ]; then
	die "tracked files have uncommitted changes, which wouldn't be built (HEAD is); commit them or set ALLOW_DIRTY=1"
fi

PKG_VERSION="$(PKG_VERSION="${PKG_VERSION:-}" scripts/version.sh)"
PKG_VERSION="${PKG_VERSION%+dirty}"
SOURCE_DATE_EPOCH="$(scripts/source-date-epoch.sh)"
export PKG_VERSION SOURCE_DATE_EPOCH
work="${REPRO_DIR:-$root/.cache/repro}"
case "$work" in /*) ;; *) work="$root/$work" ;; esac

if command -v sha256sum >/dev/null 2>&1; then
	sha256=(sha256sum)
else
	sha256=(shasum -a 256)
fi

# manifest <dir> <label> <path>...: one "sha256  label/path" line per
# regular file under each path (relative to dir), "symlink  label/path ->
# target" per symlink, sorted.
manifest() {
	local dir="$1" label="$2"
	shift 2
	(
		cd "$dir"
		find "$@" \( -type f -o -type l \) ! -name .DS_Store ! -name .gitkeep -print | sed 's|^\./||' | LC_ALL=C sort | while IFS= read -r f; do
			if [ -L "$f" ]; then
				printf 'symlink  %s/%s -> %s\n' "$label" "$f" "$(readlink "$f")"
			else
				printf '%s  %s/%s\n' "$("${sha256[@]}" <"$f" | cut -d' ' -f1)" "$label" "$f"
			fi
		done
	)
}

export_head() {
	local dest="$1"
	rm -rf "$dest"
	mkdir -p "$dest"
	git archive --format=tar HEAD | tar -x -C "$dest"
}

# build_export <run>: frontend or macOS build in .cache/repro/<run>/src.
build_export() {
	local run="$1" src="$work/$1/src"
	echo "==> [$run] exporting HEAD to $src"
	export_head "$src"
	rm -rf "$work/$run/gocache"
	mkdir -p "$work/$run/gocache"
	if [ "$want_macos" = 1 ]; then
		echo "==> [$run] scripts/package-macos.sh"
		(cd "$src" && GOCACHE="$work/$run/gocache" scripts/package-macos.sh) >"$work/$run/build.log" 2>&1 ||
			die "[$run] macOS build failed; see $work/$run/build.log"
		{
			manifest "$src/frontend" frontend dist
			manifest "$src/dist/macos" macos .
		} >>"$work/$run/SHA256SUMS"
	elif [ "$want_frontend" = 1 ]; then
		echo "==> [$run] scripts/build-frontend.sh"
		(cd "$src" && scripts/build-frontend.sh) >"$work/$run/build.log" 2>&1 ||
			die "[$run] frontend build failed; see $work/$run/build.log"
		manifest "$src/frontend" frontend dist >>"$work/$run/SHA256SUMS"
	fi
}

# build_linux <run>: scripts/linux.sh all, outputs moved to <run>/linux.
build_linux() {
	local run="$1"
	echo "==> [$run] scripts/linux.sh all $linux_arch"
	LINUX_GOCACHE_TAG="repro-$run" scripts/linux.sh all "$linux_arch" >"$work/$run/linux.log" 2>&1 ||
		die "[$run] Linux build failed; see $work/$run/linux.log"
	rm -rf "$work/$run/linux"
	mv dist/linux "$work/$run/linux"
	manifest "$work/$run/linux" linux . >>"$work/$run/SHA256SUMS"
}

echo "==> verifying PostSonde $PKG_VERSION ($(git rev-parse --short HEAD)), SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH"
echo "    targets: frontend=$((want_frontend | want_macos)) macos=$want_macos linux=$want_linux (arch $linux_arch); work dir $work"
for run in a b; do
	rm -rf "${work:?}/$run"
	mkdir -p "$work/$run"
	: >"$work/$run/SHA256SUMS"
	if [ "$want_frontend" = 1 ] || [ "$want_macos" = 1 ]; then
		build_export "$run"
	fi
	if [ "$want_linux" = 1 ]; then
		build_linux "$run"
	fi
done

# Headline artifacts: what a release ships.
echo "==> SHA256SUMS of run a ($work/a/SHA256SUMS):"
grep -E '  (macos/[^/]+\.dmg|macos/PostSonde\.app/Contents/MacOS/postsonde|linux/[^/]+|frontend/dist/index\.html)$' "$work/a/SHA256SUMS" || true

total="$(wc -l <"$work/a/SHA256SUMS" | tr -d ' ')"
if cmp -s "$work/a/SHA256SUMS" "$work/b/SHA256SUMS"; then
	echo "==> reproducible: all $total files are identical in both builds"
	if [ "${KEEP:-}" != 1 ]; then
		rm -rf "$work/a/src" "$work/b/src" "$work/a/gocache" "$work/b/gocache"
	fi
	exit 0
fi

echo "==> NOT reproducible: these files differ between the builds:" >&2
# Entries in only one run's manifest, by path (a file only one run made
# shows up too).
differing="$(LC_ALL=C comm -3 <(LC_ALL=C sort "$work/a/SHA256SUMS") <(LC_ALL=C sort "$work/b/SHA256SUMS") |
	sed -E 's/^[[:space:]]+//; s/^[^ ]+  //' | LC_ALL=C sort -u)"
echo "$differing" | sed 's/^/    /' >&2
echo "    ($(echo "$differing" | wc -l | tr -d ' ') of $total files; manifests: $work/a/SHA256SUMS, $work/b/SHA256SUMS)" >&2

# path of a manifest entry in a run's tree.
entry_path() {
	local run="$1" entry="$2"
	case "$entry" in
	frontend/*) echo "$work/$run/src/frontend/${entry#frontend/}" ;;
	macos/*) echo "$work/$run/src/dist/macos/${entry#macos/}" ;;
	linux/*) echo "$work/$run/linux/${entry#linux/}" ;;
	esac
}
if command -v diffoscope >/dev/null 2>&1; then
	while IFS= read -r entry; do
		case "$entry" in symlink* | *" -> "*) continue ;; esac
		echo "==> diffoscope $entry" >&2
		diffoscope --text - "$(entry_path a "$entry")" "$(entry_path b "$entry")" >&2 || true
	done <<<"$differing"
else
	echo "    diffoscope isn't installed; compare the files under $work/a and $work/b by hand (cmp -l, or diffoscope)" >&2
fi
exit 1
