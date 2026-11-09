# macOS packaging

PostSonde ships on macOS as one universal (Apple Silicon + Intel)
`PostSonde.app` inside a disk image, ad-hoc signed; the release workflow
signs it with a Developer ID and notarizes it instead when it has the
signing secrets ([Releasing, Signing](releasing.md#signing)). This page
covers what is in the bundle and the disk image, how it is built and
checked, and what users see on first launch. Linux packages: [packaging.md](packaging.md).

| Artifact | Contains | For |
|---|---|---|
| `PostSonde-<version>-macos-universal.dmg` | `PostSonde.app` and a link to `/Applications` | macOS 13 Ventura or later, `arm64` and `x86_64` |

## Building

On a Mac with the Xcode Command Line Tools and the toolchain from
[Building locally](building.md#prerequisites):

```sh
make macos                  # or: scripts/package-macos.sh
make macos VERSION=0.1.0    # a release build (PKG_VERSION=0.1.0 without make)
```

No Docker, no Apple account and nothing beyond what ships with macOS and
the Command Line Tools (`lipo`, `plutil`, `codesign`, `ditto`, `hdiutil`,
`shasum`). It can't run on Linux. Outputs land in `dist/macos/`
(gitignored), which holds one build at a time:

| File | |
|---|---|
| `PostSonde.app` | the signed bundle, for trying it without the disk image |
| `PostSonde-<version>-macos-universal.dmg` | the release file |
| `SHA256SUMS` | over the `.dmg`, written and checked by `scripts/sha256sums.sh` |
| `BUILDINFO` | the toolchain versions, for [rebuilding the release](reproducible-builds.md#macos) |

`make package` still builds a host-architecture `bin/postsonde.app` through
`wails3 package`, for a quick local bundle; `make macos` is the one that
makes release files.

### Steps

[`scripts/package-macos.sh`](../scripts/package-macos.sh) runs, in order:

1. **Universal binary.** `wails3 task darwin:build:universal
   VERSION=<version>` builds the frontend, then `darwin/amd64` and
   `darwin/arm64` production binaries (`-tags production -trimpath`,
   deployment target macOS 13.0) and merges them with `lipo` into
   `bin/postsonde`. The script fails unless `lipo -archs` lists both.
2. **Bundle.** `PostSonde.app/Contents/` gets `MacOS/postsonde`,
   `Resources/icons.icns` (from `assets/icons/postsonde.icns`, synced into
   `build/darwin/`), `Resources/LICENSE`, `Resources/THIRD_PARTY_NOTICES.md`
   and `Info.plist`.
3. **Info.plist** is [`build/darwin/Info.plist`](../build/darwin/Info.plist)
   with these keys set by `plutil -replace` (the file itself is regenerated
   by `wails3 task common:update:build-assets`, so it isn't edited by hand),
   then checked with `plutil -lint`:

   | Key | Value |
   |---|---|
   | `CFBundleIdentifier` | `io.github.sorrelfox.PostSonde` (from `build/config.yml`) |
   | `CFBundleExecutable` | `postsonde` |
   | `CFBundleIconFile` | `icons` |
   | `LSMinimumSystemVersion` | `13.0.0`, taken from `MACOSX_DEPLOYMENT_TARGET` in `build/darwin/Taskfile.yml` (macOS 13: Go 1.27's own objects target 13.0) |
   | `CFBundleShortVersionString`, `CFBundleVersion` | the numeric part of the version: `0.1.0` stays `0.1.0`, `0.0.0~git7cb8544+dirty` becomes `0.0.0` (Apple allows only up to three integers) |
   | `CFBundleGetInfoString` | `PostSonde <full version>`, shown by Finder's Get Info |
   | `CFBundleDisplayName` | `PostSonde` |
   | `NSHighResolutionCapable` | `true` (a boolean; the generated file has a string) |
   | `LSApplicationCategoryType` | `public.app-category.developer-tools` |

   The full version is also in the binary (`main.version`):
   `PostSonde.app/Contents/MacOS/postsonde --version` prints it.
4. **Ad-hoc signature,** the last change to the bundle:
   `codesign --force --deep --sign - --timestamp=none`. `lipo` breaks the
   signature the Go linker puts on arm64 binaries, and Apple Silicon won't
   run unsigned arm64 code, so this step is required. No hardened runtime,
   no entitlements (they only matter for notarization). The script then
   requires `codesign --verify --deep --strict` to pass and `codesign -dv`
   to report `Signature=adhoc`. With `POSTSONDE_MACOS_SIGN_IDENTITY` set (a
   Developer ID Application certificate in the keychain), the app is signed
   with it and the hardened runtime instead, and the disk image is signed
   too; with notarytool's credentials as well, the disk image is notarized
   and its ticket stapled ([Releasing, Signing](releasing.md#signing)).
5. **Disk image.** A staging folder holds a `ditto` copy of the bundle
   (byte for byte, so the signature stays valid, but without extended
   attributes) and an `Applications` symlink to `/Applications`. `hdiutil
   create -volname PostSonde -srcfolder <stage> -fs HFS+ -layout NONE`
   makes a bare HFS+ volume, `cmd/dmgnorm` sets its dates to the commit
   time and its UUIDs to ones derived from the version, `hdiutil convert
   -format UDZO` compresses it, and `hdiutil verify` checks it. That makes
   the `.dmg` reproducible: [reproducible-builds.md](reproducible-builds.md).
   The disk image itself is not signed. Finder shows the default icon
   layout; there is no custom background.
6. **Build info.** `BUILDINFO` lists the version, `SOURCE_DATE_EPOCH` and
   the toolchain (Go, `wails3`, Node, npm, macOS, clang, ld) a rebuild
   needs to match.
7. **Checksums.** `scripts/sha256sums.sh dist/macos` writes `SHA256SUMS`
   (`sha256sum`, or `shasum -a 256` on macOS releases without it; both
   write the same format) and
   checks it. The release-wide `SHA256SUMS` and its signature are made at
   release time over all platforms' files.

### Checking a build by hand

```sh
codesign --verify --deep --strict --verbose=2 dist/macos/PostSonde.app
codesign -dv dist/macos/PostSonde.app 2>&1 | grep Signature   # Signature=adhoc
lipo -info dist/macos/PostSonde.app/Contents/MacOS/postsonde   # x86_64 arm64
plutil -lint dist/macos/PostSonde.app/Contents/Info.plist
hdiutil verify dist/macos/PostSonde-*-macos-universal.dmg
hdiutil attach -nobrowse -readonly dist/macos/PostSonde-*-macos-universal.dmg
ls -l /Volumes/PostSonde                                       # PostSonde.app, Applications -> /Applications
hdiutil detach /Volumes/PostSonde
```

`spctl --assess` **rejects** the app: that is expected for an app that
isn't notarized, not a build failure.

### In CI

The release workflow builds the disk image in `macos:dmg`, on GitHub's
Intel image `macos-15-intel`, with `make macos` and the Go and Node.js
versions `scripts/pins.env` pins; Xcode is the image's, and `BUILDINFO`
records it. [`scripts/test-macos.sh`](../scripts/test-macos.sh) then checks
the image as above (`spctl` only for a Developer ID signature), runs
`postsonde --version` from it with no network, and starts the app from the
image until its first window is ready, on Intel. The arm64 slice is not
started in CI (Apple silicon runners are often unavailable); start it on an
Apple silicon Mac before a release. The
`.dmg` and `BUILDINFO` are kept for 30 days and, on a release tag, released
([ci.md, Package](ci.md#package)).

## Installing (for users)

1. Check the download against `SHA256SUMS`:
   `shasum -a 256 -c --ignore-missing SHA256SUMS`.
2. Open the `.dmg` and drag **PostSonde** onto **Applications**. Eject the
   disk image.
3. First launch: PostSonde is not notarized by Apple (that needs a paid
   developer account), so macOS blocks it the first time:
   1. Open PostSonde from Applications. When macOS says it can't verify the
      app, click **Done**.
   2. Open **System Settings › Privacy & Security**, scroll to
      **Security** and click **Open Anyway** next to PostSonde. The button
      stays there for about an hour after step 1.
   3. Confirm with your password. After this, PostSonde opens normally.

   If macOS says the app "is damaged", or you prefer the Terminal:
   `xattr -dr com.apple.quarantine /Applications/PostSonde.app`, then open
   it again.

   A release that is notarized opens without these steps.

Updating means downloading the new `.dmg` and replacing the app; the first
launch steps come back with every new version until PostSonde is
notarized. Copying the app out of the read-only disk image also matters
for [portable mode](packaging.md#portable-mode-app-04).
