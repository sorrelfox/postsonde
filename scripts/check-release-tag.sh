#!/usr/bin/env bash
# check-release-tag.sh: fail unless TAG is a release tag vX.Y.Z for the
# release version in build/config.yml (info.version). The release workflow
# runs it for a pushed tag before it builds anything (docs/releasing.md).
#
#   scripts/check-release-tag.sh [TAG]     default: $GITHUB_REF_NAME
set -euo pipefail

cd "$(dirname "$0")/.."

tag="${1:-${GITHUB_REF_NAME:-}}"
[ -n "$tag" ] || {
	echo "usage: $0 TAG" >&2
	exit 2
}

# info.version, not the file's own top-level `version:` (Wails' format).
version="$(awk '
	/^info:/ { inside = 1; next }
	/^[^[:space:]#]/ { inside = 0 }
	inside && $1 == "version:" { gsub(/["'\'']/, "", $2); print $2; exit }
' build/config.yml)"
[ -n "$version" ] || {
	echo "check-release-tag.sh: no info.version in build/config.yml" >&2
	exit 1
}

if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "check-release-tag.sh: $tag is not a release tag (vX.Y.Z)" >&2
	exit 1
fi
if [ "$tag" != "v$version" ]; then
	echo "check-release-tag.sh: the tag is $tag, but build/config.yml says version $version; bump info.version, commit, and tag that commit" >&2
	exit 1
fi
echo "release $version: $tag matches build/config.yml"
