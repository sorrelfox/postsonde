#!/usr/bin/env bash
# test-macos.sh: check the disk image as a user gets it, and start the app
# from it. The release workflow's macos job runs it after
# scripts/package-macos.sh (docs/ci.md, Package tests).
#
#   scripts/test-macos.sh PostSonde-<version>-macos-universal.dmg
#
# - hdiutil verify, then the image attached read-only;
# - its layout: PostSonde.app and the Applications link; Info.plist passes
#   plutil -lint; the binary is universal (x86_64 and arm64);
# - codesign --verify --deep --strict. An ad-hoc signature (no Developer ID
#   secrets) is what Gatekeeper rejects, so spctl is only asked about a
#   Developer ID signature, and the stapled ticket is checked when there is
#   one;
# - `postsonde --version` from the image with no network (sandbox-exec), on
#   this Mac's architecture and, on Apple silicon with Rosetta, on Intel's;
# - scripts/smoke-start.sh on the app in the image: the first window ready.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"

dmg="${1:?usage: $0 DMG}"
version="$(basename "$dmg" | sed -n 's/^PostSonde-\(.*\)-macos-universal\.dmg$/\1/p')"
[ -n "$version" ] || die "not a disk image's name: $dmg"

work="$(mktemp -d)"
mnt="$work/mnt"
cleanup() {
	if [ -d "$mnt/PostSonde.app" ]; then
		hdiutil detach -quiet "$mnt" || hdiutil detach -quiet -force "$mnt" || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT

hdiutil verify -quiet "$dmg"
mkdir "$mnt"
hdiutil attach -quiet -nobrowse -readonly -mountpoint "$mnt" "$dmg"
app="$mnt/PostSonde.app"
bin="$app/Contents/MacOS/postsonde"
[ -L "$mnt/Applications" ] || die "no Applications link in the image"
for file in MacOS/postsonde Resources/icons.icns Resources/LICENSE Resources/THIRD_PARTY_NOTICES.md Info.plist; do
	[ -f "$app/Contents/$file" ] || die "the app has no Contents/$file"
done
plutil -lint "$app/Contents/Info.plist"
archs="$(lipo -archs "$bin")"
# One word at a time: in a single pattern like *" x86_64 "*" arm64 "* the
# two words would need a space each, but lipo separates them with one.
for arch in x86_64 arm64; do
	case " $archs " in
	*" $arch "*) ;;
	*) die "the binary is not universal ($archs)" ;;
	esac
done
echo "universal: $archs"

codesign --verify --deep --strict --verbose=2 "$app"
signature="$(codesign --display --verbose=2 "$app" 2>&1)"
grep -E '^(Identifier|Format|Authority|Signature|TeamIdentifier)=' <<<"$signature" || true
if grep -q '^Authority=Developer ID Application' <<<"$signature"; then
	spctl --assess --type execute --verbose "$app"
	if xcrun stapler validate "$dmg"; then
		spctl --assess --type open --context context:primary-signature --verbose "$dmg"
	fi
fi

echo "--- --version from the image, no network:"
offline='(version 1) (allow default) (deny network*)'
run_archs="$(uname -m)"
if [ "$run_archs" = arm64 ] && arch -x86_64 /usr/bin/true 2>/dev/null; then
	run_archs="arm64 x86_64"
fi
for arch in $run_archs; do
	have="$(sandbox-exec -p "$offline" arch "-$arch" "$bin" --version)"
	echo "$arch: $have"
	[ "$have" = "postsonde $version" ] || die "expected \"postsonde $version\""
done

"$root/scripts/smoke-start.sh" "$bin"
