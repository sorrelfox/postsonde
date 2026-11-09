#!/usr/bin/env bash
# source-date-epoch.sh: print the SOURCE_DATE_EPOCH for builds and packages
# (docs/reproducible-builds.md): the commit time of HEAD in seconds since
# the epoch. Every timestamp that ends up in a release (.deb entries,
# AppImage squashfs, file times in the .app and .dmg) is set to it, so two
# builds of one commit can be byte for byte identical.
#
#   scripts/source-date-epoch.sh
#
# First match wins:
#   SOURCE_DATE_EPOCH     as given (scripts/linux.sh passes it into the
#                         containers, which build a git archive export; CI
#                         works it out once with git and passes it to its
#                         package jobs, most of whose images have no git)
#   git                   the committer time of HEAD (git log -1 --format=%ct)
# Otherwise it prints nothing, warns and exits 1; callers then build with
# the current time (not reproducible) rather than failing:
#
#   if sde="$(scripts/source-date-epoch.sh)"; then export SOURCE_DATE_EPOCH="$sde"; fi
set -euo pipefail

cd "$(dirname "$0")/.."

if [ -n "${SOURCE_DATE_EPOCH:-}" ]; then
	sde="$SOURCE_DATE_EPOCH"
elif command -v git >/dev/null 2>&1 && git rev-parse --git-dir >/dev/null 2>&1; then
	sde="$(git log -1 --format=%ct HEAD)"
else
	echo "source-date-epoch.sh: no SOURCE_DATE_EPOCH or git checkout; timestamps will be the build time (not reproducible)" >&2
	exit 1
fi

if ! [[ "$sde" =~ ^[0-9]+$ ]]; then
	echo "source-date-epoch.sh: invalid SOURCE_DATE_EPOCH '$sde' (must be seconds since the epoch)" >&2
	exit 1
fi
echo "$sde"
