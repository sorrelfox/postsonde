#!/usr/bin/env bash
# package-macos.sh: build PostSonde for macOS as a universal (arm64 + amd64)
# PostSonde.app inside a .dmg (`make macos`; docs/packaging-macos.md).
# Runs on a Mac with the Xcode Command Line Tools; uses only tools that ship
# with macOS (lipo, plutil, codesign, hdiutil, shasum) besides the project's
# own toolchain.
#
#   scripts/package-macos.sh
#
#   1. wails3 task darwin:build:universal: both architectures, merged with
#      lipo into bin/postsonde (frontend build and bindings included)
#   2. PostSonde.app: build/darwin/Info.plist stamped with the version,
#      the icon, LICENSE and THIRD_PARTY_NOTICES.md
#   3. signature as the last change to the bundle (ad hoc, or Developer
#      ID: below), then codesign --verify
#   4. PostSonde-<version>-macos-universal.dmg: the .app plus an
#      /Applications link (drag to install), checked with hdiutil verify;
#      with Developer ID also signed, and notarized when notarytool has
#      credentials
#   5. SHA256SUMS over the .dmg (scripts/sha256sums.sh)
#
# Signing: ad hoc (`codesign --sign -`), which Apple silicon needs to run the
# app at all, unless POSTSONDE_MACOS_SIGN_IDENTITY names a Developer ID
# Application certificate in the keychain. Then the app is signed with it and
# the hardened runtime, and so is the disk image. With notarytool's
# credentials too (POSTSONDE_MACOS_NOTARY_APPLE_ID, _TEAM_ID and _PASSWORD,
# an app-specific password), the disk image is notarized and the ticket
# stapled to it. docs/releasing.md, Signing.
#
# Version: scripts/version.sh (PKG_VERSION overrides it).
#
# Reproducible (docs/reproducible-builds.md): the binary, the .app and
# the .dmg are byte for byte the same for every build of one commit with
# the same toolchain, when signed ad hoc (a Developer ID signature carries
# a timestamp). The .dmg is a bare HFS+ volume (no partition map)
# whose dates and UUIDs cmd/dmgnorm sets from SOURCE_DATE_EPOCH
# (scripts/source-date-epoch.sh, the commit time) and the version.
#
# Writes dist/macos/: PostSonde.app, the .dmg, SHA256SUMS and BUILDINFO
# (the toolchain versions). dist/macos/ holds one build at a time; it is
# cleared first.
set -euo pipefail

# shellcheck source=scripts/lib.sh
. "$(dirname "$0")/lib.sh"
cd "$root"

[ "$(uname -s)" = Darwin ] || die "the macOS package can only be built on macOS (it needs lipo, codesign and hdiutil)"
for tool in lipo plutil codesign hdiutil shasum; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool not found; install the Xcode Command Line Tools: xcode-select --install"
done
require_wails3

identity="${POSTSONDE_MACOS_SIGN_IDENTITY:-}"
# The notary password stays out of the environment of the build.
notary_password="${POSTSONDE_MACOS_NOTARY_PASSWORD:-}"
unset POSTSONDE_MACOS_NOTARY_PASSWORD
notarizing=no
if [ -n "$identity" ] && [ -n "${POSTSONDE_MACOS_NOTARY_APPLE_ID:-}" ] && [ -n "$notary_password" ]; then
	notarizing=yes
	[ -n "${POSTSONDE_MACOS_NOTARY_TEAM_ID:-}" ] || die "POSTSONDE_MACOS_NOTARY_TEAM_ID: the team ID of the Apple ID is missing"
fi

version="$(scripts/version.sh)"
SOURCE_DATE_EPOCH="$(scripts/source-date-epoch.sh)" || die "no SOURCE_DATE_EPOCH: the .dmg needs the commit time (from git, or SOURCE_DATE_EPOCH)"
export SOURCE_DATE_EPOCH
# CFBundleShortVersionString and CFBundleVersion must be up to three
# period-separated integers: 0.1.0 stays, 0.0.0~git7cb8544+dirty becomes
# 0.0.0. The full version goes into CFBundleGetInfoString (Finder's Get
# Info) and main.version (postsonde --version, the About dialog).
bundle_version="$(echo "$version" | sed -E 's/^([0-9]+(\.[0-9]+){0,2}).*/\1/')"
# LSMinimumSystemVersion: the deployment target the binaries are built for.
min_macos="$(awk -F'"' '/MACOSX_DEPLOYMENT_TARGET:/ { print $2; exit }' build/darwin/Taskfile.yml)"
[ -n "$min_macos" ] || die "no MACOSX_DEPLOYMENT_TARGET in build/darwin/Taskfile.yml"

out=dist/macos
app="$out/PostSonde.app"
dmg="$out/PostSonde-$version-macos-universal.dmg"
stage="$out/.stage"

echo "==> universal build, version $version"
wails3 task darwin:build:universal VERSION="$version"
archs="$(lipo -archs bin/postsonde)"
for arch in x86_64 arm64; do
	case " $archs " in
	*" $arch "*) ;;
	*) die "bin/postsonde is not universal (lipo -archs: $archs)" ;;
	esac
done

rm -rf "$out"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

echo "==> $app"
cp bin/postsonde "$app/Contents/MacOS/postsonde"
cp build/darwin/icons.icns "$app/Contents/Resources/icons.icns"
cp LICENSE THIRD_PARTY_NOTICES.md "$app/Contents/Resources/"
plist="$app/Contents/Info.plist"
cp build/darwin/Info.plist "$plist"
# Stamped here rather than edited in build/darwin/Info.plist, which
# `wails3 task common:update:build-assets` regenerates from build/config.yml.
plutil -replace CFBundleShortVersionString -string "$bundle_version" "$plist"
plutil -replace CFBundleVersion -string "$bundle_version" "$plist"
plutil -replace CFBundleGetInfoString -string "PostSonde $version" "$plist"
plutil -replace LSMinimumSystemVersion -string "$min_macos.0" "$plist"
plutil -replace CFBundleDisplayName -string PostSonde "$plist"
plutil -replace CFBundleInfoDictionaryVersion -string 6.0 "$plist"
plutil -replace NSHighResolutionCapable -bool YES "$plist"
plutil -replace LSApplicationCategoryType -string public.app-category.developer-tools "$plist"
plutil -lint "$plist"
[ "$(plutil -extract CFBundleExecutable raw "$plist")" = postsonde ] || die "CFBundleExecutable in $plist is not postsonde"

# Last change to the bundle: anything after it breaks the signature.
# Ad hoc: no hardened runtime, no entitlements (they matter only for
# notarization). Developer ID: the hardened runtime, which notarization
# requires; the app needs no entitlements for it.
if [ -z "$identity" ]; then
	echo "==> ad-hoc signing (no POSTSONDE_MACOS_SIGN_IDENTITY)"
	codesign --force --deep --sign - --timestamp=none "$app"
	codesign --verify --deep --strict --verbose=2 "$app"
	case "$(codesign -dv "$app" 2>&1)" in
	*Signature=adhoc*) ;;
	*) die "$app is not ad-hoc signed" ;;
	esac
else
	echo "==> signing with $identity"
	codesign --force --deep --options runtime --timestamp --sign "$identity" "$app"
	codesign --verify --deep --strict --verbose=2 "$app"
fi

echo "==> $dmg"
mkdir -p "$stage"
# ditto keeps the bundle byte for byte (and so its signature); without
# extended attributes, which hold per-machine data (com.apple.provenance)
# and aren't part of the signature.
ditto --norsrc --noextattr --noqtn --noacl "$app" "$stage/PostSonde.app"
ln -s /Applications "$stage/Applications"
# Bare HFS+ rather than hdiutil's default APFS inside a GPT: APFS and GPT
# carry random UUIDs, and APFS checksums over them. Build it read-write,
# convert it to a raw image, set its dates and volume UUID (dmgnorm hfs),
# compress it to UDZO and set the trailer's random SegmentID (dmgnorm
# udif). Both UUIDs are derived from the version.
raw="$out/.raw"
hdiutil create -quiet -volname PostSonde -srcfolder "$stage" -fs HFS+ -layout NONE -format UDRW -o "$raw.dmg"
hdiutil convert -quiet "$raw.dmg" -format UDTO -o "$raw"
go run ./cmd/dmgnorm hfs -epoch "$SOURCE_DATE_EPOCH" -seed "PostSonde $version" "$raw.cdr"
hdiutil convert -quiet "$raw.cdr" -format UDZO -ov -o "$dmg"
go run ./cmd/dmgnorm udif -seed "PostSonde $version" "$dmg"
rm -rf "$stage" "$raw.dmg" "$raw.cdr"
if [ -n "$identity" ]; then
	codesign --sign "$identity" --timestamp "$dmg"
	if [ "$notarizing" = yes ]; then
		echo "==> notarizing $(basename "$dmg")"
		json_field() { python3 -c 'import json, sys; print(json.loads(sys.argv[1]).get(sys.argv[2], ""))' "$1" "$2"; }
		notary=(--apple-id "$POSTSONDE_MACOS_NOTARY_APPLE_ID" --team-id "$POSTSONDE_MACOS_NOTARY_TEAM_ID" --password "$notary_password")
		result="$(xcrun notarytool submit "$dmg" "${notary[@]}" --wait --output-format json)"
		status="$(json_field "$result" status)"
		echo "notarization: $status"
		if [ "$status" != Accepted ]; then
			xcrun notarytool log "$(json_field "$result" id)" "${notary[@]}" || true
			die "notarization of $(basename "$dmg") was not accepted"
		fi
		xcrun stapler staple "$dmg"
	else
		echo "==> not notarized: no POSTSONDE_MACOS_NOTARY_APPLE_ID and _PASSWORD"
	fi
fi
hdiutil verify -quiet "$dmg"

# The toolchain this build used: a rebuild must use the same Go (its
# version is in the binary), clang/ld and hdiutil to match bit for bit
# (docs/reproducible-builds.md).
{
	echo "PostSonde $version"
	echo "SOURCE_DATE_EPOCH $SOURCE_DATE_EPOCH"
	echo "go $(go env GOVERSION)"
	echo "wails3 $(wails3_version)"
	echo "node $(node --version)"
	echo "npm $(npm --version)"
	echo "macOS $(sw_vers -productVersion) ($(sw_vers -buildVersion)) $(uname -m)"
	echo "clang $(clang --version | head -1)"
	echo "ld $(ld -v 2>&1 | head -1)"
	echo "signature ${identity:-ad hoc}$([ "$notarizing" = yes ] && echo ", notarized")"
} >"$out/BUILDINFO"

scripts/sha256sums.sh "$out"

echo "==> $out:"
echo "    $(lipo -info "$app/Contents/MacOS/postsonde")"
echo "    PostSonde.app $version (CFBundleShortVersionString $bundle_version), signed ${identity:-ad hoc}"
echo "    $(basename "$dmg") ($(wc -c <"$dmg" | tr -d ' ') bytes)"
