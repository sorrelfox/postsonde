# Building locally

Build, check and package PostSonde on a developer machine with one command,
without CI. The root [`Makefile`](../Makefile) is a thin wrapper: every
target runs a script in [`scripts/`](../scripts/), so everything also
works [without make](#without-make). CI calls the same
scripts ([docs/ci.md](ci.md)), so a local `make check` or `make linux` runs
exactly what the pipeline runs.

```sh
make setup      # check the toolchain, npm ci
make check      # everything CI checks
make build      # bin/postsonde for this OS
make macos      # universal PostSonde.app and .dmg in dist/macos/ (on a Mac)
make linux      # Linux binaries and packages in dist/linux/, through Docker
make rpm        # Fedora RPM in dist/linux/, built, installed and started in containers
make flatpak    # Flatpak bundle in dist/linux/, built, installed and started in a container
make windows    # Windows executables and installers in dist/windows/, through Docker
make verify-reproducible   # build HEAD twice, compare every output file
```

`make` alone prints the targets. The Makefile works with GNU make (macOS
ships 3.81) and BSD make.

## Prerequisites

The pinned versions are in the toolchain table in
[architecture.md](architecture.md#toolchain); `make setup` checks them and
prints the exact command for anything missing or at the wrong version. It
installs nothing except `frontend/node_modules`.

| | macOS | Linux | Windows |
|---|---|---|---|
| Go | the `go.mod` version (1.27.x) | same | same |
| `wails3` CLI | exactly the Wails module version in `go.mod`: `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0` | same | same |
| Node.js / npm | Node 26 | same | same |
| C toolchain (cgo) | Xcode Command Line Tools (`xcode-select --install`) | `gcc libc6-dev pkg-config libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev` (Debian 13+, Ubuntu 24.04+) | none |
| git | yes (version stamp, gofmt check) | yes | yes |
| Docker | Docker Desktop, only for the Linux and Windows targets | Docker Engine, only for the Linux and Windows targets; qemu/binfmt for the other architecture's AppImage | Docker Desktop (WSL 2), only for the Linux and Windows targets |

The scripts find a `wails3` in `$(go env GOPATH)/bin` even when that
directory isn't on `PATH`. The scripts are bash and run on macOS's bash
3.2.

## Targets

| Target | Script | Does |
|---|---|---|
| `help` (default) | | lists the targets |
| `setup` | `scripts/setup.sh` | checks Go, `wails3`, Node/npm, git, the C toolchain (and notes Docker), then `npm ci` in `frontend/` |
| `dev` | `scripts/wails.sh dev` | `wails3 task dev`: Vite dev server plus the live-rebuilt app |
| `build` | `scripts/wails.sh build` | `wails3 build VERSION=<version>`: `bin/postsonde` for the host OS (`npm ci`, bindings, frontend build, `go build -tags production`) |
| `package` | `scripts/wails.sh package` | `wails3 package VERSION=<version>`: macOS `bin/postsonde.app` (host architecture, ad-hoc signed); on Linux a production build and its `.deb` in `dist/` |
| `macos` | `scripts/package-macos.sh` | macOS only: universal (arm64 + amd64) `PostSonde.app`, ad-hoc signed, in `PostSonde-<version>-macos-universal.dmg`, plus `SHA256SUMS`, in `dist/macos/` ([packaging-macos.md](packaging-macos.md)) |
| `check` | `scripts/check-go.sh`, then `scripts/check-frontend.sh` | everything CI's two check jobs run |
| `check-go` | `scripts/check-go.sh` | gofmt (tracked files), `scripts/check-core-deps.sh`, `go vet ./...`, `go test -race ./...`, `wails3 task bindings:check` |
| `check-frontend` | `scripts/check-frontend.sh` | `scripts/check-css-tokens.sh`, `npm ci`, `npm run typecheck`, `npm test`, `npm run build`, `npm run check:dist` (no remote URLs, CSP; [privacy](privacy.md)) |
| `bindings` | `scripts/wails.sh bindings` | regenerates the committed `frontend/bindings/` |
| `linux` | `scripts/linux.sh all` | both stacks' binaries, then `linux-packages` ([below](#linux-builds-through-docker)) |
| `linux-gtk4`, `linux-gtk3` | `scripts/linux.sh build <stack>` | that stack's binaries |
| `linux-packages` | `scripts/linux.sh packages` | `.deb`s, AppImages and `SHA256SUMS` from the binaries in `dist/linux/` |
| `sources` | `scripts/linux.sh sources` | the source tarball `postsonde-<version>.tar.xz`: the tree, the frontend build and the vendored Go modules ([packaging.md](packaging.md)) |
| `rpm` | `scripts/linux.sh rpm` | the source tarball, then the Fedora RPM and source RPM of Docker's architecture (`ARCH=` for the other one) and of one Fedora release (`FEDORA=`, below), installed and started in a clean container of that release ([packaging.md, RPM](packaging.md#rpm)) |
| `flatpak` | `scripts/linux.sh flatpak` | the source tarball, then the Flatpak bundle (x86_64), installed and started, all in a privileged container with a Flatpak installation of its own ([packaging.md, Flatpak](packaging.md#flatpak)) |
| `windows` | `scripts/windows.sh` | from any OS with Docker: the Windows executables (`scripts/build-windows.sh`), the NSIS installers `PostSonde-<version>-windows-<arch>-setup.exe` (`scripts/package-windows-installer.sh`) and `SHA256SUMS` in `dist/windows/` ([packaging-windows.md](packaging-windows.md)) |
| `verify-reproducible` | `scripts/verify-reproducible.sh` | builds `HEAD` twice from clean exports (frontend, macOS on a Mac, Linux with Docker) and reports any output file that differs ([reproducible-builds.md](reproducible-builds.md)) |
| `clean` | `scripts/clean.sh` | removes `bin/`, `dist/`, `frontend/dist/*` (keeping `.gitkeep`), `.cache/` (incl. the Linux and Windows source exports), `.npm/`, `.task/`, the AppImage tool cache and leftover Windows `.syso` resources |
| `distclean` | `scripts/clean.sh distclean` | `clean` plus `frontend/node_modules` |
| `clean-docker` | `scripts/clean.sh docker` | removes the `postsonde-*` Docker cache volumes |

Variables:

- `ARCH=amd64|arm64` narrows the Linux and Windows targets to one
  architecture (default: both). `make linux ARCH=arm64` builds gtk4 and
  gtk3 for arm64 and packages them.
- `FEDORA=44|45|all` picks the Fedora release `make rpm` builds for. The
  default is the release this machine runs when it is 44 or 45, otherwise
  45, the newest; `all` builds both.
- `VERSION=<version>` overrides the version stamp (same as `PKG_VERSION`
  in the environment).

The clean targets remove only gitignored paths: `node_modules` goes only
with `distclean` (`setup` and `check-frontend` reinstall it with `npm ci`),
the Docker volumes only with `clean-docker`. Every target leaves
`git status` clean.

## Version

[`scripts/version.sh`](../scripts/version.sh) is the one place the version
is worked out. `make build`/`package` stamp it into `main.version` (through
`wails3 build VERSION=...`), `scripts/ci-build-linux.sh` stamps it into the
Linux binaries and the packaging scripts name the `.deb`s and AppImages
after it, locally and in CI. First match wins:

| Source | Version |
|---|---|
| `PKG_VERSION` (or `make ... VERSION=`) | as given |
| git: tag `vX.Y.Z` on `HEAD` | `X.Y.Z` |
| git: otherwise | `0.0.0~git<short sha>`, e.g. `0.0.0~git7cb8544` |

With git, `+dirty` is appended when tracked files have uncommitted changes
(`0.0.0~git7cb8544+dirty`). CI works the version out once, from git, and
passes it to its jobs as `PKG_VERSION` ([ci.md, Jobs](ci.md#jobs)). The
result is always a valid Debian upstream version; `~` sorts before
anything, so a release (`0.1.0`) upgrades any development build.
`postsonde --version` prints it. Details:
[packaging.md, Versioning](packaging.md#versioning).

The timestamps in the packages come from
[`scripts/source-date-epoch.sh`](../scripts/source-date-epoch.sh): the
commit time of `HEAD`, as `SOURCE_DATE_EPOCH` (an existing
`SOURCE_DATE_EPOCH` wins). With the version, it makes every build of one
commit byte for byte the same: [reproducible-builds.md](reproducible-builds.md).

## Linux builds through Docker

`make linux` builds the Linux binaries and packages from any OS with
Docker, running the same scripts in the same images as the CI jobs
([ci.md, Build](ci.md#build) and [Package](ci.md#package)):

| Step | Script | Image | CI job |
|---|---|---|---|
| frontend, once | `scripts/build-frontend.sh` | `node:26-trixie-slim` | `frontend:build` |
| binaries | `scripts/ci-build-linux.sh <stack> <arch>` | `ubuntu:24.04` (gtk4), `ubuntu:22.04` (gtk3) | `linux:gtk4`, `linux:gtk3` |
| `.deb` | `scripts/install-wails3.sh`, `scripts/package-linux-deb.sh` | `golang:1.27-trixie` | `linux:deb` |
| AppImage | `scripts/package-linux-appimage.sh <arch>` | `ubuntu:22.04` of the target architecture | `linux:appimage:<arch>` |
| checksums | `scripts/sha256sums.sh` | `ubuntu:24.04` | `linux:packages` |
| source tarball (`make sources`, `rpm`, `flatpak`) | `scripts/package-sources.sh` | `golang:1.27-trixie` | `sources` |
| RPM (`make rpm`) | `scripts/package-linux-rpm.sh`, `scripts/test-linux-packages.sh` | `fedora:44` or `fedora:45` of the target architecture | `linux:rpm:fc<release>:<arch>` |
| Flatpak (`make flatpak`) | `scripts/package-flatpak.sh`, `scripts/test-flatpak.sh` | `fedora:45`, privileged | `linux:flatpak` |

The images are pinned by digest in
[`scripts/pins.env`](../scripts/pins.env), and `scripts/linux.sh` and the
CI workflows both read them from there, so local and CI builds run on
exactly the same images ([reproducible-builds.md](reproducible-builds.md)).
The Flatpak needs a privileged container: flatpak-builder runs bubblewrap,
which needs namespaces a plain container doesn't allow.

**Source: a clean export of `HEAD`.** The containers never see the working
copy. `scripts/linux.sh` exports `HEAD` with `git archive` into
`.cache/linux/src` (gitignored) and mounts that. Bind-mounting the working
copy would let `npm ci` in the Linux container replace your macOS
`node_modules` with Linux binaries; a named volume for `node_modules`
would avoid that but still build whatever untracked files lie around. The
export builds exactly what CI would build from that commit. The flip side:
**uncommitted changes aren't built** (the script warns, and drops `+dirty`
from the version); commit first, or build the host OS with `make build`.

**Architectures.** Every build runs in a container of Docker's own
architecture, and the other target is cross-compiled, as on the CI runner
([ci.md, arm64: cross-compiling](ci.md#arm64-cross-compiling)). On an Apple
Silicon Mac arm64 is native and amd64 is cross-compiled; on an amd64
machine it's the other way round. `LINUX_PLATFORM=linux/amd64 make
linux-gtk4 ARCH=arm64` runs the build container as amd64 (emulated on
arm64) to reproduce exactly what the amd64 CI runner does. The AppImage
can't be cross-built, so the other architecture's AppImage runs emulated
(Docker Desktop has this built in; a Linux host needs qemu/binfmt, see
[packaging.md, Building locally](packaging.md#building-locally)) and takes
several times longer.

**Outputs** land in `dist/linux/`:

| File | From |
|---|---|
| `postsonde-linux-{gtk4,gtk3}-{amd64,arm64}` | the binaries |
| `postsonde_<version>_{amd64,arm64}.deb` | gtk4 `.deb` |
| `postsonde-gtk3_<version>_{amd64,arm64}.deb` | gtk3 `.deb` |
| `PostSonde-<version>-{x86_64,aarch64}.AppImage` | AppImage of the gtk3 build |
| `postsonde-<version>.tar.xz` | the source tarball (`make sources`, `rpm`, `flatpak`) |
| `postsonde-<version>-1.fc{44,45}.{x86_64,aarch64}.rpm`, `.src.rpm` | the RPM and the source RPM (`make rpm`) |
| `PostSonde-<version>-x86_64.flatpak` | the Flatpak bundle (`make flatpak`) |
| `SHA256SUMS` | over the packages and the tarball, as in CI |
| `.version` | the version the binaries were built at |

`dist/linux/` holds one version at a time: building another version clears
it first, and `make linux-packages` refuses binaries of a different
version, so packages never wrap a stale binary.

**Caches** are named Docker volumes, so repeated runs skip most downloads:
`postsonde-go-mod` (Go modules), `postsonde-go-build` (Go build cache, one
directory per stack/architecture), `postsonde-npm`,
`postsonde-apt-<image>-<arch>` (apt's `.deb` downloads),
`postsonde-appimage-tools`, `postsonde-flatpak` (the Flatpak container's
own installation, with the GNOME SDK) and `postsonde-dnf-fedora-45-<arch>`
(dnf's downloads for it). The Go tarball, the apt indexes and the RPM
build's packages are fetched on every run, as in CI. `make clean-docker`
removes the volumes.

On a Linux host the containers hand their outputs back to your user
(`chown`), since they run as root. Without Docker the script stops with an
install hint.

## Without make

Call the scripts directly from the repo root; the targets above map one to
one:

```sh
scripts/setup.sh
scripts/check-go.sh && scripts/check-frontend.sh
scripts/wails.sh build              # or: wails3 build VERSION="$(scripts/version.sh)"
scripts/package-macos.sh            # on a Mac
scripts/linux.sh all arm64          # scripts/linux.sh build gtk3 amd64, scripts/linux.sh packages
scripts/windows.sh arm64            # Windows executable and installer, through Docker
```

**On Windows** itself the scripts need a bash (Git Bash); otherwise use
`wails3 build` (or `wails3 task <task>`) directly. `wails3 package` makes a
quick per-user installer with `makensis` on `PATH`; release installers come
from `make windows` ([packaging-windows.md](packaging-windows.md)).
