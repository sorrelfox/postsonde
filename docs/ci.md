# CI

CI runs on GitHub Actions, in two workflows:

- [`.github/workflows/ci.yml`](../.github/workflows/ci.yml), **the checks**,
  on every push and pull request: the same checks as [Running the
  checks](architecture.md#running-the-checks) on Linux, and `go vet` and
  the Go tests on macOS and Windows;
- [`.github/workflows/release.yml`](../.github/workflows/release.yml),
  **the release builds**, on a release tag or by hand: every package,
  installed and started in a clean system, and on a tag a draft GitHub
  Release ([Releasing](releasing.md)).

The jobs' steps live in [`scripts/`](../scripts/), not in the YAML: the
workflows only pick the image, install what the image lacks, set caches
and call the script. The local `make` targets call the same scripts
([Building locally](building.md)), so a local `make check` or `make linux`
can't drift from CI:

| Job | Workflow | Script | Locally |
|---|---|---|---|
| `go:lint-test` | ci | `scripts/check-go.sh` | `make check-go` |
| `frontend:typecheck-test` | ci | `scripts/check-frontend.sh` | `make check-frontend` |
| `go:test (<os>)` | ci | `go vet ./...`, `go test ./...` | the same commands on a Mac or a Windows PC |
| `frontend:build` | release | `scripts/build-frontend.sh` | first step of `make linux` |
| `sources` | release | `scripts/package-sources.sh` | `make sources` |
| `linux:gtk4`, `linux:gtk3` | release | `scripts/ci-build-linux.sh` | `make linux-gtk4`, `make linux-gtk3` |
| `windows:build` | release | `scripts/install-wails3.sh`, `scripts/build-windows.sh` | `make windows` |
| `linux:deb` | release | `scripts/install-wails3.sh`, `scripts/package-linux-deb.sh` | `make linux-packages` |
| `linux:appimage:<arch>` | release | `scripts/package-linux-appimage.sh` | `make linux-packages` |
| `linux:test (<arch>)` | release | `scripts/test-linux-packages.sh` | `scripts/test-linux-packages.sh <packages>` |
| `linux:rpm:fc<release>:<arch>` | release | `scripts/package-linux-rpm.sh`, `scripts/test-linux-packages.sh` | `make rpm FEDORA=<release>` |
| `linux:flatpak` | release | `scripts/package-flatpak.sh`, `scripts/test-flatpak.sh` | `make flatpak` |
| `linux:packages` | release | `scripts/sha256sums.sh` | `make linux-packages` |
| `macos:dmg`, `macos:test (arm64)` | release | `scripts/package-macos.sh`, `scripts/test-macos.sh` | `make macos` |
| `windows:installer` | release | `scripts/package-windows-installer.sh`, `scripts/sha256sums.sh` | `make windows` |
| `windows:test (<arch>)` | release | `scripts/test-windows.sh` | `scripts/test-windows.sh <installer>` on a Windows PC, in Git Bash |
| `size:budgets` | release | `scripts/check-size.sh` | `scripts/check-size.sh <files>`, e.g. `dist/linux/postsonde* dist/linux/*.AppImage` after `make linux` |
| `reproducible` | release | `scripts/verify-reproducible.sh linux` | `make verify-reproducible ARCH=amd64` |

The Linux jobs run in containers of images pinned by digest in
[`scripts/pins.env`](../scripts/pins.env), which also pins Go, the macOS
build's Node.js and the Flatpak runtime ([Pins](#pins)).
`scripts/linux.sh` and `scripts/windows.sh` read the same file, so `make
linux` and `make windows` run on the same images.

## When it runs

**ci.yml** runs for every push to any branch and for every pull request.
One change starts one run: a pull request from a branch of this repository
is already checked by that branch's push run, so its own run skips every
job; a pull request from a fork has no push run here and is checked by its
own. A newer push to the same branch or pull request cancels the run that
is still going for the older one (`concurrency`).

**release.yml** runs for a pushed tag `v*` and by hand (Actions › Release ›
Run workflow, `workflow_dispatch`, on the branch you pick). A tag must be
`vX.Y.Z` for the release version in `build/config.yml`, or the run stops
at once (`scripts/check-release-tag.sh`). A run by hand builds and tests
every package without releasing: the packages are the run's
[artifacts](#artifacts). Only a tag run ends with the draft release
([Releasing](releasing.md)).

A pull request run checks the merge of its branch into the base branch,
which is what GitHub checks out for it.

**Minutes.** The repository is public, so GitHub's standard hosted runners
cost nothing, macOS, Windows and the arm64 ones included. The checks
therefore run on all three systems for every change. The packages are left
to release.yml all the same: a full release run builds about twenty jobs
and takes most of an hour, which no single push needs.

Both workflows have `permissions: contents: read`. Only the release job of
a tag run may write: it creates the draft release and its build provenance
attestations. Nothing is ever pushed to a registry or a package
repository.

## Jobs

### ci.yml

`setup` reads the pins the other jobs need and gates the run (one run per
change, above). Then three jobs run side by side, so a failure points at
one side straight away:

- `go:lint-test` and `frontend:typecheck-test` on Linux, in the pinned
  images;
- `go:test (macos-15-intel)` and `go:test (windows-latest)`, a matrix with
  `fail-fast: false`, so one system failing doesn't cancel the other, and a
  60-minute timeout. Each sets up the pinned Go (`actions/setup-go`, which
  caches the modules and the build cache by `go.sum`) and runs `go vet
  ./...` and `go test ./...`, with the race detector on macOS (Windows has
  no cgo, which the race detector needs). Without it, the Windows job is
  also the one that checks the time bounds of the speed tests, which the
  race detector lifts ([test conventions](architecture.md#test-conventions)).
  macOS and Windows have code of
  their own: the keychain backends, the system proxy, the Wails shell. The
  Intel macOS image: arm64 macOS runners are often short of capacity.

### release.yml

`setup` checks a tag against `build/config.yml`, validates the desktop file
and the metainfo (`desktop-file-validate`, `appstreamcli validate`), reads
the pins, and works out the version (`scripts/version.sh`) and
`SOURCE_DATE_EPOCH` (`scripts/source-date-epoch.sh`) from git, once for the
whole run. Every build and package job gets both as `PKG_VERSION` and
`SOURCE_DATE_EPOCH`, which the two scripts take as given. The jobs couldn't
work them out themselves: the ubuntu, node and fedora images have no git,
so their checkout is a download without `.git`, and in the golang image
git refuses the checkout, which belongs to the runner's user.

```
setup ─┬─ frontend:build ─┬─ linux:gtk4, linux:gtk3 ─┬─ linux:deb ──────────────┬─ linux:test (<arch>)
       │                  │                          └─ linux:appimage:<arch> ─┘
       │                  ├─ sources ─┬─ linux:rpm:fc<release>:<arch>
       │                  │           └─ linux:flatpak
       │                  └─ windows:build ─ windows:installer ─ windows:test (<arch>)
       ├─ macos:dmg ─ macos:test (arm64)
       └─ reproducible
```

`linux:packages` collects the `.deb`, AppImage, RPM and Flatpak files, and
`size:budgets` checks the binaries, `linux:packages` and the installers.
The draft release, on a tag only, waits for all of it: a package that
fails to build, install or start, a size over its budget or a file that
builds differently the second time stops it.

The checks of ci.yml don't run again here: the commit was checked when it
was pushed.

### `go:lint-test`

Image: `golang:1.27-trixie` (Debian 13). Before the script, the job
installs the GTK dev packages (below) and the `wails3` CLI at the Wails
version in `go.mod` (`scripts/install-wails3.sh`); the script is
[`scripts/check-go.sh`](../scripts/check-go.sh):

| Step | Fails when |
|------|------------|
| `gofmt -l` over `git ls-files '*.go'` | any tracked Go file isn't gofmt-formatted; the files are listed |
| `scripts/check-core-deps.sh` | `internal/core` imports anything outside the standard library (see [below](#the-stdlib-only-guard)) |
| `go vet ./...` | vet finds a problem |
| `go test -race ./...` | a test fails or the race detector fires |
| `wails3 task bindings:check` | the committed `frontend/bindings/` differ from what `wails3 generate bindings` produces for the current Go services; fails first if `wails3` isn't at the Wails version in `go.mod` |

The Wails packages (`internal/app`, `cmd/postsonde`) use cgo on Linux, so
`go vet` and `go test` need a C compiler and the GTK headers. The Wails
v3.0.0 sources ask for `#cgo linux pkg-config: gtk4 webkitgtk-6.0`
(`pkg/application/linux_cgo.go`) and `#cgo pkg-config: x11` (global
shortcuts). The job installs, with `--no-install-recommends`:

```
gcc libc6-dev pkg-config libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev
```

Debian 13 "trixie" is the first Debian release with WebKitGTK 6.0, which is
why the image is pinned to `-trixie` rather than the default Debian variant.
The install takes about a minute per run; if that becomes a bottleneck,
bake the packages into a custom image.

Variables:

- `GOTOOLCHAIN=local`: use the image's Go and never download another
  toolchain. When `go.mod` moves to a newer Go minor, bump the image tag
  in the same commit (see the toolchain table in `architecture.md`).
- `CGO_ENABLED=1`: required by `-race` and the GTK bindings.
- `CGO_CFLAGS=-O2 -g -Wno-deprecated-declarations`: Go's defaults, minus
  the GTK deprecation warnings the Wails C code prints, which otherwise
  bury the test output.

The checkout belongs to the runner's user and the container runs as root,
so git would refuse to work in it; the job marks it as a `safe.directory`
before the gofmt step and the bindings check run git.

Cache: `GOMODCACHE` and `GOCACHE` live in the workspace (`.cache/go-mod`,
`.cache/go-build`, gitignored), keyed on `go.sum` ([Caches](#caches)).
`./...` skips directories starting with `.`, and the gofmt step only looks
at tracked files, so the cache is never checked.

### `frontend:typecheck-test`

Image: `node:26-trixie-slim` (Node 26, matching the toolchain table).
[`scripts/check-frontend.sh`](../scripts/check-frontend.sh) runs:

```sh
scripts/check-css-tokens.sh   # no colours outside tokens.css
cd frontend
npm ci
npm run typecheck   # tsc --noEmit
npm test            # vitest run
npm run build       # the production build must succeed too
npm run check:dist  # no remote URLs in dist/, CSP <meta> present
```

Vitest starts one worker per CPU core. When several checkouts run their
tests at the same time (parallel worktrees), set `VITEST_MAX_WORKERS=3`
or similar, otherwise the jsdom tests hit their 5 s timeout under load:
`VITEST_MAX_WORKERS=3 scripts/check-frontend.sh`.

`npm run check:dist` ([`frontend/scripts/check-dist.mjs`](../frontend/scripts/check-dist.mjs))
fails the job when the built frontend contains a remote URL (`http://`,
`https://`, `ws://`, `wss://`, `ftp://`, protocol-relative `//host`) that
isn't on its short, documented allowlist, or when `dist/index.html` lacks the
Content-Security-Policy `<meta>` from `frontend/csp.json`. It lists each hit
as `file:line:column`. What it checks, the allowlist and the CSP itself:
[`docs/privacy.md`](privacy.md). Its own tests run in `npm test`.

npm's download cache is kept in `.npm/` (gitignored), keyed on
`frontend/package-lock.json`; the job points npm at it with
`npm_config_cache` and `npm_config_prefer_offline`. The generated Wails
bindings (`frontend/bindings/`) are committed; the Go job checks they are
current.

## Build

The release workflow builds the Linux binaries for both GTK stacks that
Wails v3 supports, on amd64 and arm64, and the Windows binaries for amd64
and arm64, from one frontend build:

```
frontend:build ──┬─ linux:gtk4 (amd64)      ubuntu:24.04
                 ├─ linux:gtk4 (arm64)      ubuntu:24.04
                 ├─ linux:gtk3 (amd64)      ubuntu:22.04
                 ├─ linux:gtk3 (arm64)      ubuntu:22.04
                 ├─ windows:build (amd64)   golang:1.27-trixie
                 └─ windows:build (arm64)   golang:1.27-trixie
```

The macOS app is built by `macos:dmg` ([Package](#package)), with its own
frontend build.

### `frontend:build`

Image: `node:26-trixie-slim`. Runs
[`scripts/build-frontend.sh`](../scripts/build-frontend.sh) (`npm ci && npm
run build && npm run check:dist` in `frontend/`, the same npm cache as
`frontend:typecheck-test`) and passes `frontend/dist/` on as the
`frontend-dist` artifact (kept 1 day) to the Linux and Windows builds and
to `sources`. The Go binary embeds it with `//go:embed all:dist`
(`frontend/assets.go`), so it must exist before `go build`; without it the
app starts with no page. The no-remote-resources check runs here too, so a
`dist/` with a remote URL never becomes the artifact the binaries embed.

### `sources`

The source tarball `postsonde-<version>.tar.xz`
([`scripts/package-sources.sh`](../scripts/package-sources.sh), in the
golang image): a `git archive` export of the commit, with the
`frontend-dist` build in `frontend/dist/` and the Go modules in `vendor/`
(`go mod vendor`, checked against `go.sum`). The RPM and the Flatpak build
from it without a network, and it is released next to the packages. Every
file time is `SOURCE_DATE_EPOCH` and the entries are sorted, so the tarball
is the same for every build of one commit.

### `linux:gtk4` and `linux:gtk3`

Both run
[`scripts/ci-build-linux.sh <stack> <arch>`](../scripts/ci-build-linux.sh)
on the amd64 runner, with a matrix over `arch: [amd64, arm64]`.

| Job | Image | Stack | Tags | Dev packages |
|-----|-------|-------|------|--------------|
| `linux:gtk4` | `ubuntu:24.04` | GTK4 + WebKitGTK 6.0 (default) | `production` | `libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev` |
| `linux:gtk3` | `ubuntu:22.04` | GTK3 + WebKit2GTK 4.1 (legacy) | `production,gtk3` | `libgtk-3-dev libwebkit2gtk-4.1-dev libx11-dev` |

Each job also installs `gcc` (or the cross gcc), `libc6-dev`, `pkg-config`,
`curl`, `ca-certificates` and `file`, all with `--no-install-recommends`.

Why these packages: in Wails v3.0.0, the files built by default
(`//go:build linux && cgo && !gtk3`, e.g. `pkg/application/linux_cgo.go`)
ask for `#cgo linux pkg-config: gtk4 webkitgtk-6.0`, and their `gtk3`
twins (`linux_cgo_gtk3.go`, `//go:build ... && gtk3`) for `gtk+-3.0
webkit2gtk-4.1 gdk-3.0`. The asset server adds `libsoup-3.0`,
`gio-unix-2.0`, `glib-2.0` and `gobject-2.0`, which the WebKit dev
packages pull in, and the global shortcuts `x11` on both stacks. Ubuntu
24.04 is the first LTS with WebKitGTK 6.0; 22.04 is the oldest supported
LTS with WebKit2GTK 4.1, so the gtk3 binary runs on older distributions
too.

**Go**: the script downloads the official `go<version>.linux-<host>.tar.gz`
from go.dev/dl at the version `scripts/pins.env` pins
(`POSTSONDE_PIN_GO_VERSION`) and checks its SHA-256 (both host
architectures' sums are there) before unpacking it to `/usr/local/go`.
`GOTOOLCHAIN=local`. A job runs in one image and can't copy Go out of
another, so the tarball is the simplest option on a stock Ubuntu image.

**Build command**: the same flags as `wails3 build` for a production build
(`build/linux/Taskfile.yml`, `DEV` unset):

```sh
GOOS=linux GOARCH=<arch> CGO_ENABLED=1 CC=<gcc> \
go build -tags production[,gtk3] -trimpath -buildvcs=false \
  -ldflags "-w -s -X main.version=<version>" \
  -o bin/postsonde-linux-<stack>-<arch> ./cmd/postsonde
```

`<version>` is `scripts/version.sh`: the `PKG_VERSION` from `setup`, `X.Y.Z`
on a release tag and `0.0.0~git<short sha>` otherwise
([Version](building.md#version)), or `APP_VERSION` if set.

`production` selects Wails' release code paths (no devtools, production
logger); `gtk3` is the Taskfile's `EXTRA_TAGS=gtk3`. CI calls `go build`
directly rather than `wails3 build`: the Taskfile would also rerun
`npm ci`, the bindings and `go mod tidy`, which the other jobs already
cover. `CGO_CFLAGS` drops the GTK deprecation warnings, as in
`go:lint-test`.

After the build the script prints `file`, the size and the `NEEDED`
entries (`readelf -d`), and **fails** if the binary isn't linked against
the stack's GTK and WebKit libraries (`libgtk-4.so.1` and
`libwebkitgtk-6.0.so.4`, or `libgtk-3.so.0` and `libwebkit2gtk-4.1.so.0`).

**Cache**: `GOMODCACHE` and `GOCACHE` as in `go:lint-test`, one cache per
stack and architecture (`linux-gtk4-amd64-<hash of go.sum>`, ...). The apt
install (a few minutes; WebKit pulls in a lot) isn't cached.

### arm64: cross-compiling

The Linux build jobs run on amd64 runners, and the arm64 binaries are
**cross-compiled** there, not emulated or built on an arm64 runner. The
script compares the target with `dpkg --print-architecture` and, when they
differ:

1. `dpkg --add-architecture <arch>`.
2. Pins the image's own apt sources to the host architecture
   (`Architectures:` in 24.04's deb822 `ubuntu.sources`, `[arch=...]` in
   22.04's `sources.list`) and adds the target's mirror for the target
   architecture: arm64 lives on `ports.ubuntu.com/ubuntu-ports`, amd64 on
   `archive.ubuntu.com` / `security.ubuntu.com` (release, `-updates` and
   `-security`; `main universe`).
3. Installs `gcc-aarch64-linux-gnu` (or `gcc-x86-64-linux-gnu`),
   `libc6-dev:<arch>` and the dev packages as `:<arch>`; for gtk4 also
   the host's `qemu-user` (see below).
4. Builds with `CC=<triple>-gcc` and
   `PKG_CONFIG_LIBDIR=/usr/lib/<triple>/pkgconfig:/usr/share/pkgconfig`, so
   pkg-config resolves only the target's `.pc` files.

The same path works in both directions, so an arm64 machine builds amd64
the same way, and a machine of either architecture builds all four
binaries. Emulation (an arm64 container under qemu) was the other option;
it needs qemu/binfmt on the runner and is many times slower. A native
arm64 runner isn't needed: the cross build gives the same binary and keeps
all four Linux builds on one kind of runner. The packages that must run on
their own architecture are built and tested on arm64 runners instead
([Runners](#runners)).

No target-architecture binary may run during the install either: a
`:<arch>` package whose maintainer scripts execute the target's programs
fails with "Exec format error" and aborts dpkg. On 24.04 (gtk4) one would
come in through GLib. GLib 2.80 took in libgirepository, and
`libglib2.0-dev` ships `<triple>-gi-compile-repository` and friends,
wrappers that run the target's own tool natively or through qemu; so it
depends on `python3 | qemu-user | qemu-user-static`. That plain `python3`
means the target's (`python3` is `Multi-Arch: allowed`; only
`python3:any` accepts the host's), and apt picks the first alternative:
`python3:arm64`, then `python3.12-minimal:arm64`, whose `postinst`
byte-compiles with the arm64 interpreter and fails. The script therefore
also asks for the host's `qemu-user` (`Multi-Arch: foreign`, so it serves
the dependency of an `:<arch>` package), which apt takes for the second
alternative; the host's `python3` then serves the `python3:any`
dependencies of `libglib2.0-dev-bin`. qemu-user only sits there: we don't
generate GObject introspection data, and without `qemu-user-binfmt` (a
recommendation, left out by `--no-install-recommends`) nothing is routed
through it, so the build still runs no emulation. 22.04's GLib 2.72 has no
such dependency, which is why gtk3 never needed it. The same holds the
other way round: an arm64 machine building amd64 gets its own `qemu-user`.

The log still shows a few "Exec format error" lines from the `postinst`
of `libglib2.0-0` (`libglib2.0-0t64` on 24.04), `libgdk-pixbuf-2.0-0` and
`libgtk-3-0` or `libgtk-4-1` (`glib-compile-schemas`, `gio-querymodules`,
`gdk-pixbuf-query-loaders`, `gtk-query-immodules-3.0`). Those scripts
expect to be configured for a foreign architecture and carry on; the
caches they would refresh are runtime files the build doesn't use.

Multiarch needs the `:arm64` and `:amd64` versions of every shared package
to match exactly. Right after an Ubuntu update, `ports.ubuntu.com` can
briefly lag `archive.ubuntu.com` and apt then refuses the install; re-run
the failed jobs.

### The Linux binaries

Each Linux job keeps `bin/postsonde-linux-<stack>-<arch>` for **30 days**
([Artifacts](#artifacts)). `postsonde --version` prints the version, the
same string the packages carry. The binaries are dynamically linked: they
need the GTK and WebKit runtime libraries of their stack on the target
machine.

Starting a gtk4 binary inside a plain Docker container aborts in
`g_application_run` with `bwrap: Creating new namespace failed`: WebKitGTK
6.0 always runs its web process in a bubblewrap sandbox, which needs user
namespaces the container doesn't allow. That's the container, not the
build: with `WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1` (for tests only)
the same binary starts and keeps running, which is how the [package
tests](#package-tests) start it. The gtk3 binaries (WebKit2GTK 4.1) start
in a plain container as they are.

### `windows:build`

The Windows binaries for amd64 and arm64, a matrix over `arch: [amd64,
arm64]`, **cross-compiled on the Linux runner**. Wails v3 on Windows needs
no cgo: the WebView2 bindings (`go-webview2`, including its loader) are
pure Go, so `GOOS=windows CGO_ENABLED=0 go build` works from any OS and
needs neither a Windows runner nor a C cross toolchain. The job uses the
Go image (`golang:1.27-trixie`) and installs nothing with apt.

After `CGO_ENABLED=0 scripts/install-wails3.sh` (the `wails3` CLI at the
go.mod version, for `wails3 generate syso`) it runs
[`scripts/build-windows.sh <arch>`](../scripts/build-windows.sh), the
script `make windows` runs too: the icon, manifest and version resource as
a `.syso`, then `go build -tags production -trimpath -buildvcs=false
-ldflags "-w -s -H windowsgui -X main.version=<version>"`, the flags of
`wails3 build` for a production Windows build
([packaging-windows.md, The executable](packaging-windows.md#the-executable)).
The script then checks the result without Windows tools and **fails** if
the PE machine isn't the target's (`0x8664` amd64, `0xaa64` arm64), the
subsystem isn't 2 (Windows GUI), Go's build info (`go version -m`) lacks
`GOOS=windows`, the architecture, `CGO_ENABLED=0` or `-tags=production`,
or the version string or the manifest isn't in the binary.

The binaries need the **WebView2 runtime** on the target machine; it ships
with Windows 11 and current Windows 10, and the installer checks for it.
Two builds of one commit at one version are byte for byte the same.

Cache: `GOMODCACHE` and `GOCACHE` per architecture
(`windows-build-amd64-<hash of go.sum>`, `windows-build-arm64-...`), as in
the Linux jobs. Each job keeps `bin/postsonde-windows-<arch>.exe` for
**30 days**.

## Package

From the build jobs' binaries and the source tarball:

| Job | Runner | Image | Makes |
|---|---|---|---|
| `linux:deb` | amd64 | `golang:1.27-trixie` | the `postsonde` (gtk4) and `postsonde-gtk3` `.deb` packages for amd64 and arm64: nfpm writes packages for any architecture on any host |
| `linux:appimage:<arch>` | amd64; arm64 | `ubuntu:22.04` | the AppImage of the gtk3 binary of that architecture: linuxdeploy can't cross-package |
| `linux:rpm:fc<release>:<arch>` | amd64; arm64 | `fedora:44`, `fedora:45` | the Fedora RPM of each release, from the source tarball, the way a build service builds it (`scripts/package-linux-rpm.sh`): with Fedora's own Go on 45, with the pinned go.dev Go on 44, whose `golang` is older than `go.mod`'s Go. `linux:rpm:fc45:amd64` also keeps the source RPM, the one released: the others differ in the dist tag, and Fedora 44's in building with the go.dev Go |
| `linux:flatpak` | amd64, on the runner itself | GNOME 51 SDK | the Flatpak bundle (x86_64), from the source tarball (`scripts/package-flatpak.sh`); flatpak-builder needs user namespaces a container doesn't give |
| `linux:packages` | amd64 | `ubuntu:24.04` | all Linux packages in one artifact, with `SHA256SUMS` |
| `macos:dmg` | `macos-15-intel` | | the universal `PostSonde.app` in `PostSonde-<version>-macos-universal.dmg` (`make macos`), with Go and Node.js at their pins |
| `windows:installer` | amd64 | `golang:1.27-trixie` | the NSIS installers `PostSonde-<version>-windows-<arch>-setup.exe` for both architectures, and `SHA256SUMS` over them (`scripts/package-windows-installer.sh`, which installs Debian's `nsis` at a pinned version; it needs the Debian mirror) |

[Linux packaging](packaging.md) covers the formats, dependencies, file
layout, versioning and how to build the packages locally;
[macOS packaging](packaging-macos.md) the disk image;
[Windows packaging](packaging-windows.md) the installer. Signing, which
`macos:dmg` and `windows:installer` do only with the repository's signing
secrets: [Releasing, Signing](releasing.md#signing).

### Package tests

Every package is installed and started as a user gets it, in a clean
system:

| Job | Where | Checks |
|---|---|---|
| `linux:test (amd64)`, `(arm64)` | a runner of the architecture, in containers of the pinned images | the gtk4 `.deb` on Ubuntu 24.04 and Debian 13, the gtk3 `.deb` on Ubuntu 22.04 and Debian 12 (`apt install`), the AppImage on a Debian 13 with only the libraries the AppImage leaves to the host |
| `linux:rpm:fc<release>:<arch>` | the same | the RPM on its Fedora release (`dnf install`) |
| `linux:flatpak` | the runner itself | the bundle installed for the user, run with its network unshared |
| `macos:dmg`, `macos:test (arm64)` | `macos-15-intel`, `macos-15` | the disk image's layout, signature and universal binary; the app started from the image, on Intel and on Apple silicon (there with Rosetta, both slices) |
| `windows:test (amd64)`, `(arm64)` | `windows-latest`, `windows-11-arm` | a silent install for the current user, its files, Start menu entry and uninstall entry; the app started; a silent uninstall that leaves nothing behind |

Each test checks that `postsonde --version` prints the package's version,
and then [`scripts/smoke-start.sh`](../scripts/smoke-start.sh) starts the
app with a new, empty `POSTSONDE_HOME` and waits for its log's `first
window ready` record: the window has loaded the UI. On Linux that runs
under Xvfb on a D-Bus session of its own. Offline: the Linux containers
run with `--network none` once the package is installed, the Flatpak with
`--unshare=network`, and on macOS `--version` runs in a sandbox without
network. The Windows runners have no switch for that; the app opens no
connection at start anyway ([privacy](privacy.md)).

In the Linux containers the start runs with
`WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1`, for tests only: WebKitGTK
6.0's bubblewrap sandbox can't create its namespaces in a container
([The Linux binaries](#the-linux-binaries)).

The Windows runners are Windows Server, not Windows 11, so they don't
replace the check on a real Windows 11 PC ([packaging-windows.md, On
Windows 11](packaging-windows.md#on-windows-11-owner-check-not-done-yet)).

### Size budgets

`size:budgets` (APP-05) needs `linux:gtk4`, `linux:gtk3`,
`windows:build`, `linux:packages` and `windows:installer`, and runs, on
`ubuntu:24.04`:

```sh
scripts/check-size.sh bin/postsonde-* dist/*.deb dist/*.AppImage dist/*-setup.exe \
  dist/*.x86_64.rpm dist/*.aarch64.rpm dist/*.flatpak
```

[`scripts/check-size.sh`](../scripts/check-size.sh) looks each file up in
[`scripts/size-budgets.txt`](../scripts/size-budgets.txt) (a glob on the
file name and a budget in MiB) and prints its size, budget and the share
used. It **fails** when a file is bigger than its budget, has no budget,
or doesn't exist (a glob that matched nothing), so a new artifact needs a
budget before it can ship. The `.dmg` has no budget yet and isn't checked.
The budgets, how they were picked and when to raise them:
[footprint.md](footprint.md).

### `reproducible`

[`scripts/verify-reproducible.sh linux`](../scripts/verify-reproducible.sh)
with `ARCH=amd64`, as `make verify-reproducible ARCH=amd64`: two full
`scripts/linux.sh` builds from clean exports, each with an empty Go build
cache, compared file by file (the binaries, `.deb`s and AppImage of amd64).
Any difference fails the release. The runner is a second machine at a
later date, next to the developer's own checks
([reproducible-builds.md](reproducible-builds.md)).

## Artifacts

The jobs hand their results on as artifacts of the run: the binaries and
packages are kept for **30 days**, what only later jobs need for 1 day.
They are on the run's summary page.

| Job | Artifact | Holds | Kept |
|---|---|---|---|
| `frontend:build` | `frontend-dist` | `frontend/dist/` | 1 day |
| `sources` | `sources` | `postsonde-<version>.tar.xz` | 30 days |
| `linux:gtk4`, `linux:gtk3` | `postsonde-linux-<stack>-<arch>-<short sha>` | `postsonde-linux-<stack>-<arch>` | 30 days |
| `windows:build` | `postsonde-windows-<arch>-<short sha>` | `postsonde-windows-<arch>.exe` | 30 days |
| `linux:deb` | `linux-deb` | the four `.deb` packages | 1 day |
| `linux:appimage:<arch>` | `linux-appimage-<arch>` | the AppImage | 1 day |
| `linux:rpm:fc<release>:<arch>` | `linux-rpm-fc<release>-<arch>` | the RPM (and, Fedora 45 amd64, the source RPM) | 1 day |
| `linux:flatpak` | `linux-flatpak` | the bundle | 1 day |
| `linux:packages` | `postsonde-linux-packages-<short sha>` | every Linux package and `SHA256SUMS` | 30 days |
| `macos:dmg` | `postsonde-macos-<short sha>` | the `.dmg` and `BUILDINFO` | 30 days |
| `windows:installer` | `postsonde-windows-installers-<short sha>` | both installers and `SHA256SUMS` | 30 days |

`<short sha>` is that of the commit the run checked out. An artifact
downloads as a zip, which keeps no file modes: `chmod +x` a Linux binary
or AppImage after unpacking it.

## Caches

The Go and npm caches of the Linux jobs live in gitignored directories of
the workspace, which `actions/cache` restores before the job's script and
saves after it:

| Cache | Directories | Key | Jobs |
|---|---|---|---|
| Go modules and build cache | `.cache/go-mod` (`GOMODCACHE`), `.cache/go-build` (`GOCACHE`) | `go-<hash of go.sum>` | `go:lint-test` |
| | | `<job>-<hash of go.sum>`: `linux-gtk4-amd64-...`, `linux-gtk3-arm64-...`, `windows-build-amd64-...`, `linux-deb-...` | each Linux and Windows build, `linux:deb` |
| Go modules | `.cache/go-mod` | `sources-<hash of go.sum>` | `sources` |
| npm's download cache | `.npm` (`npm_config_cache`) | `npm-<hash of frontend/package-lock.json>` | `frontend:typecheck-test`, `frontend:build` |

A step writes the variables to `$GITHUB_ENV`, with the workspace path as
the container sees it. A cache is saved when its job succeeds and its key
is new, and is not updated after that: a change to `go.sum` or the
lockfile starts a new one. Caches saved on the default branch serve every
branch, tag and pull request; a pull request's own caches serve only that
pull request. The `go:test` jobs and `macos:dmg` use `actions/setup-go`'s
own cache.

The apt and dnf installs, the Go tarball of the Linux builds, the AppImage
tools and the Flatpak SDK aren't cached.

## Pins

| Image | Jobs |
|---|---|
| `golang:1.27-trixie` | `go:lint-test`, `sources`, `windows:build`, `linux:deb`, `windows:installer` |
| `node:26-trixie-slim` | `frontend:typecheck-test`, `frontend:build` |
| `ubuntu:24.04` | `linux:gtk4`, `linux:packages`, `size:budgets`, the gtk4 `.deb` test |
| `ubuntu:22.04` | `linux:gtk3`, `linux:appimage:*`, the gtk3 `.deb` test |
| `debian:13`, `debian:12` | the `.deb` and AppImage tests |
| `fedora:44`, `fedora:45` | `linux:rpm:*` and their tests; `fedora:45` also runs `make flatpak` |

[`scripts/pins.env`](../scripts/pins.env) is the one place they are
pinned, each as its tag plus the digest of its multi-arch index
(`golang:1.27-trixie@sha256:...`), and so are the other inputs a rebuild
must match: Go's version and the checksums of its tarballs, the Node.js
version of the macOS build (the node image's), and the GNOME SDK and
Platform of the Flatpak by commit. `setup` reads the file with
`pinned_image` from `scripts/lib.sh` and hands the refs to the jobs
(`container: image:`); the scripts source it. So a rebuild of a commit
runs on the same images and tools, in CI and with `make linux`
([reproducible builds](reproducible-builds.md)). Because each digest is the
multi-arch index, an arm64 runner gets the arm64 image from the same ref.

The tags are the readable part; the commands under [Reproducing a job
locally](#reproducing-a-job-locally) use them, `make linux` the digests.
They name a Debian, Ubuntu or Fedora release, which keeps the package names
stable until we choose to move to the next release.

Bump a pin on purpose, in its own commit: `docker buildx imagetools inspect
<tag>` prints an image's current digest (`Digest:`), `flatpak remote-info
flathub org.gnome.Sdk//<version>` a runtime's commit. `pinned_image`
refuses an image without a digest. The golang tag follows the Go minor in
`go.mod`, the node tag the Node major in `scripts/lib.sh`.

## Runners

GitHub-hosted runners only. The Linux jobs run in a container of their
image (`container:`), as root (`apt-get` and `dnf` need it), or, where
they start containers themselves (the package tests, the RPM build), on the
runner with Docker. `linux:flatpak` runs on the runner itself, after
allowing unprivileged user namespaces, which bubblewrap needs and Ubuntu's
AppArmor restricts.

amd64 (`ubuntu-24.04`) is the default; arm64 runners are used where a job
must run on its own architecture:

- **Cross-builds stay on amd64.** The arm64 Linux binaries are
  cross-compiled ([arm64: cross-compiling](#arm64-cross-compiling)), the
  Windows binaries are pure Go, nfpm writes a `.deb` for any architecture
  and makensis builds both installers on any host. None of them needs an
  arm64 machine.
- **`ubuntu-24.04-arm`** for what must run on arm64 itself:
  `linux:appimage:arm64` (linuxdeploy finds the libraries to bundle with
  `ldd`, so the AppImage can only be packaged on its own architecture),
  `linux:rpm:fc<release>:arm64` (built in an arm64 Fedora container, as Fedora builds
  its own packages) and `linux:test (arm64)`. On an amd64 runner all three
  would need qemu emulation, which works (`make linux` does it on an amd64
  machine) but is many times slower.
- **`windows-11-arm`** starts the ARM64 installer's app natively.
- **macOS**: `macos:dmg` builds on the Intel image `macos-15-intel`, since
  the arm64 macOS runners are often short of capacity and lipo joins both
  slices anyway; `macos:test (arm64)` starts the app on `macos-15`, Apple
  silicon, where Rosetta runs the Intel slice too.

The jobs need outbound HTTP(S) to Docker Hub and the Fedora registry, the
Debian, Ubuntu and Fedora mirrors (`ports.ubuntu.com` too, for the arm64
cross builds), go.dev, the Go module proxy, the npm registry, Flathub and
github.com (the AppImage tools).

## The stdlib-only guard

`internal/core` may import only the standard library and this module's
own packages (see [Dependency rule](architecture.md#dependency-rule)).
`scripts/check-core-deps.sh` runs

```sh
go list -e -deps -test -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/core/...
```

and fails if any printed package is outside the module. It covers:

- **indirect** imports: `-deps` walks the whole import graph;
- **test** imports: `-test` includes `_test.go` files and their deps;
- **missing modules**: `-e` lists imports that don't resolve instead of
  aborting, so they're reported as violations too.

It runs before `go vet` so a violation fails fast with a clear message.
`internal/core/deps_test.go` runs the same check inside `go test`, so it
fails locally too. Adding a Wails import to a file in `internal/core`
fails the job like this. The list is the whole non-stdlib import graph
pulled in (34 packages for one Wails import), trimmed here:

```
$ scripts/check-core-deps.sh
internal/core must import only the standard library and github.com/sorrelfox/postsonde; found:
  github.com/adrg/xdg
  github.com/adrg/xdg/internal/pathutil
  ...
  github.com/wailsapp/wails/v3/pkg/application
  ...
  golang.org/x/sys/unix
```

## Reproducing a job locally

Usually `make check` (natively), and `make linux`, `make rpm`, `make
flatpak` and `make windows` (in Docker, the build and package jobs in their
images, with the package tests) are all you need; see [Building
locally](building.md). To run a check job in its own image:

Both Linux check jobs run in plain Docker. Use a **fresh clone** rather
than your working copy: `npm ci` inside the container would replace your
macOS `node_modules` with Linux binaries, and `git ls-files` doesn't work
on a bind-mounted git worktree. As in CI, git in the container runs as root
on files that belong to you, so the Go job marks the clone as a
`safe.directory` first.

```sh
git clone -q --branch my-branch . /tmp/ps-ci && cd /tmp/ps-ci

docker run --rm -v "$PWD":/src -w /src \
  -e GOTOOLCHAIN=local -e CGO_CFLAGS='-O2 -g -Wno-deprecated-declarations' \
  golang:1.27-trixie bash -ec '
    git config --global --add safe.directory /src
    apt-get update -qq
    apt-get install -y -qq --no-install-recommends gcc libc6-dev pkg-config \
      libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev
    scripts/install-wails3.sh
    scripts/check-go.sh'

docker run --rm -v "$PWD":/src -w /src node:26-trixie-slim scripts/check-frontend.sh
```

The build jobs by hand, in the same clone: build the frontend once (the
`frontend:build` artifact), then run the build script in each job's image.
It installs everything itself, so the stock image is enough. Without git
in the image, pass the version in, as `setup` does in CI:

```sh
docker run --rm -v "$PWD":/src -w /src node:26-trixie-slim scripts/build-frontend.sh

v="$(scripts/version.sh)"
docker run --rm -e PKG_VERSION="$v" -v "$PWD":/src -w /src ubuntu:24.04 scripts/ci-build-linux.sh gtk4 arm64
docker run --rm -e PKG_VERSION="$v" -v "$PWD":/src -w /src ubuntu:22.04 scripts/ci-build-linux.sh gtk3 arm64
docker run --rm -e PKG_VERSION="$v" -v "$PWD":/src -w /src ubuntu:24.04 scripts/ci-build-linux.sh gtk4 amd64
docker run --rm -e PKG_VERSION="$v" -v "$PWD":/src -w /src ubuntu:22.04 scripts/ci-build-linux.sh gtk3 amd64
docker run --rm -e PKG_VERSION="$v" -v "$PWD":/src -w /src golang:1.27-trixie bash -ec '
  CGO_ENABLED=0 scripts/install-wails3.sh
  export PATH="$(go env GOPATH)/bin:$PATH"
  scripts/build-windows.sh amd64
  scripts/build-windows.sh arm64'
```

`make windows` runs the same Windows steps, plus the installers, in the
pinned images ([packaging-windows.md](packaging-windows.md#locally-make-windows));
with Go and `wails3` on the host, `scripts/build-windows.sh <arch>` also
runs without Docker. The package tests run on any machine with Docker:
`scripts/test-linux-packages.sh dist/linux/*.deb dist/linux/*.AppImage`
after `make linux` (another architecture than Docker's runs emulated).

On an Apple Silicon Mac the containers are arm64, so the arm64 lines are
native builds and the amd64 lines exercise the cross-compile path (from
arm64 to amd64, the mirror image of what the amd64 runner does for arm64).
Add `--platform linux/amd64` to run a container as amd64 under emulation
instead (slow); `LINUX_PLATFORM=linux/amd64 make linux-gtk4 ARCH=arm64`
does that for the exact case of the amd64 runner building arm64. The
binaries land in `bin/`.

The workflows are the source of truth; if they change, update these
commands too. Without Docker, the [checks in
`architecture.md`](architecture.md#running-the-checks) run the same steps
natively (on macOS the cgo side builds against WebKit instead of GTK).
