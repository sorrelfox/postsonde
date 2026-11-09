# Reproducible builds

Anyone can rebuild a PostSonde release from its tag and check that the
files match the published ones byte for byte (PRV-12). This page explains
what makes the builds reproducible, how to check it (`make
verify-reproducible`) and how a user verifies a release on each OS.

## What reproduces

| Artifact | Bit for bit | Checked |
|---|---|---|
| Frontend bundle (`frontend/dist/`, embedded in every binary) | yes | `make verify-reproducible` on macOS |
| macOS universal binary, signed `PostSonde.app` (every file) | yes, with the same Go and Xcode | `make verify-reproducible` on macOS |
| macOS `.dmg` | yes, on the same macOS release (same `hdiutil`) | `make verify-reproducible` on macOS |
| Linux binaries (gtk4, gtk3) | yes, with the same Ubuntu packages; see [limits](#limits) | amd64: `make verify-reproducible ARCH=amd64` on Linux ([results](#results)); arm64: not yet run |
| `.deb` packages | yes (nfpm with `SOURCE_DATE_EPOCH`) | amd64: `make verify-reproducible ARCH=amd64` on Linux; the packaging step alone on macOS |
| AppImages | yes, with the same Ubuntu packages; see [limits](#limits) | amd64: `make verify-reproducible ARCH=amd64` on Linux; arm64: not yet run |
| Windows executables and NSIS installers (amd64, arm64) | yes, with the same nsis package (pinned) | two `make windows` runs on Linux, the second with an empty Go build cache ([packaging-windows.md](packaging-windows.md#reproducibility)); not yet in `make verify-reproducible` |
| Source tarball (`postsonde-<version>.tar.xz`) | yes: sorted entries, `SOURCE_DATE_EPOCH` file times, single-threaded xz | not yet compared |
| Fedora RPM | not checked: rpm records the build host in the package's header | |
| Flatpak bundle | not checked | |

A Developer ID signature (macOS) or an Authenticode one (Windows), which
the release workflow makes only with its signing secrets
([Releasing](releasing.md#signing)), carries a timestamp: those `.dmg` and
installer files then differ from build to build.

## How it works

**Same inputs.**

- *Source:* a commit. `scripts/linux.sh` and `scripts/verify-reproducible.sh`
  build a `git archive` export of `HEAD`, never the working copy, so
  untracked files can't leak in.
- *Version:* `scripts/version.sh` derives it from the tag or commit
  ([building.md, Version](building.md#version)); it is the only build-time
  value in the binary (`-X main.version`).
- *Time:* [`scripts/source-date-epoch.sh`](../scripts/source-date-epoch.sh)
  sets `SOURCE_DATE_EPOCH` to the commit time of `HEAD` (`git log -1
  --format=%ct`). Every timestamp that can end up in a release is set to it.
  In the Linux containers, which have no git, `scripts/linux.sh` passes it
  in; in CI, the `setup` job reads it from git and passes it to the other
  jobs.
- *Dependencies:* Go modules are pinned by `go.sum`, the npm packages by
  `frontend/package-lock.json` (`npm ci`, which fails when the lockfile
  doesn't match `package.json`).
- *Toolchain:* the [toolchain table](architecture.md#toolchain). The
  `wails3` CLI must be exactly the Wails module version in `go.mod` (the
  scripts refuse anything else). The Linux binaries are built with the Go
  tarball whose version and SHA-256 `scripts/pins.env` pins
  (`scripts/ci-build-linux.sh` checks it). The CI images, which `make
  linux` uses too, are pinned by digest in `scripts/pins.env` (lib.sh's
  `pinned_image` reads them), and so is the Flatpak's runtime, by commit.
  The AppImage tools are pinned by version and SHA-256 in
  `scripts/package-linux-appimage.sh`.

**No build-machine data in the outputs.**

- *Go:* `-trimpath` (no file system paths), `-buildvcs=false` (no VCS stamp,
  whose "modified" flag would differ between a checkout and an export),
  `-ldflags "-w -s -X main.version=<version>"` (no DWARF or symbol table,
  which would carry the cgo object paths). The Go build ID is a hash of the
  inputs and the output, so it is the same in both builds. The flags are in
  `build/{darwin,linux,windows}/Taskfile.yml` and `scripts/ci-build-linux.sh`.
- *Frontend:* Vite (rolldown) writes content-hashed file names and no
  timestamps; nothing more was needed.
- *macOS signature:* the ad-hoc signature (`codesign --sign -
  --timestamp=none`) holds only hashes of the bundle's contents: no
  certificate, no secure timestamp. It comes out the same every time.
- *`.dmg`:* `hdiutil create` writes the build time and fresh random UUIDs
  into every image (APFS and GPT both hold UUIDs, and APFS checksums
  cover them). `scripts/package-macos.sh` therefore builds a bare HFS+
  volume (`-fs HFS+ -layout NONE`, which macOS 13+ opens like any other
  disk image), copies the bundle into it without extended attributes
  (`ditto --noextattr`: macOS tags files with a per-machine
  `com.apple.provenance` attribute), and runs
  [`cmd/dmgnorm`](../cmd/dmgnorm/main.go) twice: `dmgnorm hfs` on the raw
  image sets every date in both volume headers and in the catalog's file and
  folder records (including Finder's "date added") to `SOURCE_DATE_EPOCH`
  and the volume UUID to one derived from the version; after `hdiutil
  convert -format UDZO`, `dmgnorm udif` sets the trailer's random
  SegmentID, which no checksum covers, likewise. `hdiutil verify` and
  `codesign --verify` on the mounted image pass afterwards.
- *`.deb`:* nfpm stamps every file, the control archive and the `ar`
  members with `SOURCE_DATE_EPOCH` when it is set, sorts the entries and
  applies a fixed umask (`umask: 0o022` in `build/linux/nfpm/*.yaml`).
  Checked: two builds from exports with different file times gave identical
  `.deb`s.
- *AppImage:* the `mksquashfs` that appimagetool runs takes the
  filesystem and inode times from `SOURCE_DATE_EPOCH`. The bundled
  libraries come from the Ubuntu 22.04 archive; see [limits](#limits).

## Checking it: `make verify-reproducible`

[`scripts/verify-reproducible.sh`](../scripts/verify-reproducible.sh)
builds `HEAD` twice and compares every output file:

```sh
make verify-reproducible                      # frontend, macOS (on a Mac), Linux (with Docker)
scripts/verify-reproducible.sh frontend       # just the frontend bundle
scripts/verify-reproducible.sh macos linux    # pick targets
make verify-reproducible ARCH=amd64           # Linux: only the amd64 binaries and packages
```

- `frontend` and `macos` build in two `git archive` exports at different
  paths (`.cache/repro/a/src`, `.cache/repro/b/src`), each with an empty Go
  build cache, so a path or a stale cache entry that leaks into the output
  shows up. `macos` covers the frontend too.
- `linux` runs `scripts/linux.sh all` twice, each time with a fresh Go build
  cache (`LINUX_GOCACHE_TAG`), and moves `dist/linux/` aside after each run.
  `ARCH=amd64` or `ARCH=arm64` (as for `make linux`) limits it to that
  architecture's two binaries, two `.deb`s and AppImage. Use it on a Linux
  host without qemu/binfmt: the AppImage of the other architecture is
  packaged in an emulated container, and fails without emulation. The
  other architecture's binaries and `.deb`s are cross-built and need none
  ([ci.md, arm64](ci.md#arm64-cross-compiling)).
- Both runs get the same `PKG_VERSION` and `SOURCE_DATE_EPOCH`. npm and Go
  module downloads are shared: both are checked against the lockfile and
  `go.sum`.

Each run writes `.cache/repro/<run>/SHA256SUMS` (one line per file: the
frontend files, every file inside `PostSonde.app`, the `.dmg`, the Linux
binaries and packages). The script prints the headline hashes, then either
`reproducible: all N files are identical` (exit 0) or the files that differ
(exit 1), with a `diffoscope` report for each when diffoscope is installed.
The build logs are in `.cache/repro/<run>/`. It refuses a tree with
uncommitted changes (they wouldn't be built); `KEEP=1` keeps the exports.

A full macOS run takes a few minutes; the Linux run takes as long as two
`make linux`, much more with the emulated AppImage.

## Verifying a release

First check that the files are the published ones: verify `SHA256SUMS`
against its signature and the files against `SHA256SUMS`. Then rebuild
from the signed tag and compare:

```sh
git clone https://github.com/sorrelfox/PostSonde.git && cd PostSonde
git tag -v v0.1.0            # the tag is signed
git checkout v0.1.0
make setup                   # checks the toolchain against the pinned versions
```

### Linux (and any OS with Docker)

The Linux artifacts are built in the pinned images, so the host doesn't
matter: Linux, macOS or Windows (WSL 2 with Docker Desktop) all work.

```sh
make linux                   # or: scripts/linux.sh all
cd dist/linux
sha256sum -c --ignore-missing /path/to/release/SHA256SUMS
```

Every `.deb` and `.AppImage` should report `OK`. The rebuilt binaries are
in `dist/linux/` too; `dpkg-deb -x postsonde_<version>_amd64.deb x` and
`cmp x/usr/bin/postsonde postsonde-linux-gtk4-amd64` shows a released
`.deb` holds exactly the rebuilt binary. On an Apple Silicon Mac the
amd64 binaries are cross-compiled, as on an amd64 runner the arm64 ones;
both paths give the same output.

### macOS

Needs a Mac with the toolchain the release was built with: the release's
`BUILDINFO` (written next to the `.dmg` by `scripts/package-macos.sh`)
lists the Go, `wails3`, Node, npm, macOS, clang and ld versions. Go's version
is part of the binary, so it must match exactly; clang and ld compile and
link the cgo parts.

```sh
make macos VERSION=0.1.0     # the version without the "v"
cd dist/macos
shasum -a 256 -c --ignore-missing /path/to/release/SHA256SUMS
```

The `.dmg` matches when `hdiutil` is the same, i.e. on the same macOS
release as `BUILDINFO`. On another macOS release `hdiutil`'s compression
or HFS+ layout may differ even though the contents are the same; compare
the app inside instead, which depends only on Go and Xcode:

```sh
hdiutil attach -nobrowse -readonly -mountpoint /tmp/ps PostSonde-0.1.0-macos-universal.dmg   # the released one
diff -r /tmp/ps/PostSonde.app dist/macos/PostSonde.app && echo identical
hdiutil detach /tmp/ps
```

The signature is ad-hoc and deterministic, so the signed binary is compared
as it is; nothing has to be stripped first.

### Windows

The Windows executables and installers are built in the pinned images too,
so any OS with Docker rebuilds them; Windows itself isn't needed:

```sh
make windows VERSION=0.1.0   # or: PKG_VERSION=0.1.0 scripts/windows.sh
cd dist/windows
sha256sum -c --ignore-missing /path/to/release/SHA256SUMS
```

Both `-setup.exe` files should report `OK`. `7z e
PostSonde-0.1.0-windows-amd64-setup.exe PostSonde.exe` and `cmp
PostSonde.exe postsonde-windows-amd64.exe` shows a released installer holds
exactly the rebuilt executable. The installer depends on the nsis version
(pinned in `scripts/package-windows-installer.sh`); see
[packaging-windows.md, Reproducibility](packaging-windows.md#reproducibility).
On Windows, run the Docker route under WSL 2, and hash a download with
`Get-FileHash -Algorithm SHA256 <file>` (PowerShell) or `certutil -hashfile
<file> SHA256`.

## Limits

- **Distribution packages aren't pinned.** The images are pinned by
  digest, but `scripts/ci-build-linux.sh` installs gcc and the GTK/WebKit
  headers, and `scripts/package-linux-appimage.sh` the libraries it
  bundles, from the Ubuntu archive at build time. Two builds on the same day
  get the same packages; a rebuild weeks later may get newer ones. Headers
  rarely change the compiled Wails glue, but bundled libraries change the
  AppImage. The build log lists the package versions (`==> build packages`).
  A fix is to build against a dated archive snapshot
  (`snapshot.ubuntu.com/ubuntu/<timestamp>`, `apt-get update --snapshot`
  on 24.04) recorded per release.
- **The macOS toolchain is the developer's.** Go, Xcode (clang, ld) and
  macOS (hdiutil) come from the build machine; `BUILDINFO` records them so a
  verifier can match them.
- **One machine, one day, amd64 only.** The [Linux check](#results) built
  twice on the same machine within the hour, so it shows that nothing
  outside the inputs (the time, the build cache, the AppDir's random
  temporary directory, file order) reaches the outputs, not that another
  machine weeks later fetches the same Ubuntu packages (the first item).
  The arm64 AppImage needs an arm64 host or qemu/binfmt (see
  [`ARCH`](#checking-it-make-verify-reproducible)), which that machine
  didn't have, so `ARCH=arm64` wasn't run there; the arm64 files are built
  by the same scripts with the same flags.

## Results

Runs of `make verify-reproducible` and what they compared. Each line is a
pair of builds of the same commit; "identical" means every file of both
runs had the same SHA-256.

**2026-11-08, Linux amd64.** `KEEP=1 ARCH=amd64
scripts/verify-reproducible.sh linux` at commit `d945a4d5` (version
`0.0.0~gitd945a4d5`, `SOURCE_DATE_EPOCH=1794181497`), Fedora 44 x86_64
with Docker 29.9.3. Toolchain: Go 1.27.3 (go.dev tarball), Wails v3.0.0
(`wails3` v3.0.0 for the `.deb`s), the images pinned at that commit
(`golang:1.27-trixie@sha256:af1bfc26…`, `ubuntu:24.04@sha256:68ee2200…`,
`ubuntu:22.04@sha256:0a988793…`, `node:26-trixie-slim@sha256:36793bcb…`);
gcc 13.3.0 with GTK 4.14.5 and WebKitGTK 2.52.6 (gtk4), gcc 11.4.0 with
GTK 3.24.33 and WebKit2GTK 2.50.4 (gtk3), linuxdeploy 1-alpha-20251107-1,
appimagetool 1.9.1 and runtime 20251108. Each run started from an empty Go
build cache; the AppDirs were in different temporary directories.

All 7 files identical, first time, with no changes needed:

| File | SHA-256 |
|---|---|
| `postsonde-linux-gtk4-amd64` | `0c6b5cc6dbaf80a8284b6e39dec4e82c2d2246fd1e95b2e980c39ff3d62ee50f` |
| `postsonde-linux-gtk3-amd64` | `e8081f3ceb08025da71943fd7fe7c62721f9f54d266126a513eeb4a6452bc68c` |
| `postsonde_0.0.0~gitd945a4d5_amd64.deb` | `d33e13b63d78f70adad1c6efddcf3f3af7c6e93f7de123fe0997193f21ad221c` |
| `postsonde-gtk3_0.0.0~gitd945a4d5_amd64.deb` | `c22f5bc37778249583aa37e3622fd1b13df52dd3d358894151846cc9c3a98d98` |
| `PostSonde-0.0.0~gitd945a4d5-x86_64.AppImage` | `c9b8adfddc33731741b3cf9c16901132e2aea3aac3b33036e74eb66ecb9e2198` |

(plus `SHA256SUMS` and `.version`). The arm64 files weren't built: that
machine had no qemu/binfmt ([limits](#limits)).

**2026-10-26, macOS.** `make verify-reproducible` on a Mac: all 18 files
identical (the frontend, every file of the signed universal
`PostSonde.app`, the `.dmg`); two `.deb` builds from the same binary were
identical.

## CI and releases

- The release is built in CI from the tag ([Releasing](releasing.md)), so
  `SOURCE_DATE_EPOCH` and the version come from the tagged commit.
- Every run of the release workflow also runs `scripts/verify-reproducible.sh
  linux` with `ARCH=amd64` (its `reproducible` job): two builds of the
  Linux amd64 files on a GitHub runner, compared file by file. A
  difference stops the release.
- The release carries `BUILDINFO` (macOS) next to the `.dmg`; the image
  digests and the other pins are in `scripts/pins.env` at the tag.
- Every released file has a build provenance attestation, which ties it to
  the workflow run and the commit it was built from
  ([Releasing](releasing.md#checking-a-release)); a rebuild shows its
  content is what that commit gives.
- Bump the pins deliberately (security updates), as their own commit:
  `docker buildx imagetools inspect <tag>` prints an image's new digest.
