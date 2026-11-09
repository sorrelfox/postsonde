# Linux packaging

PostSonde ships on Linux as `.deb` packages, an AppImage, a Fedora RPM and
a Flatpak. The `.deb` packages and the AppImage wrap the binaries of the
`linux:gtk4` and `linux:gtk3` CI jobs ([CI › Build](ci.md#build)); the RPM
and the Flatpak are built from the source tarball, with Fedora's own Go
and in the GNOME SDK. This page covers the formats, what they install, how
they are versioned, and how to build and test them locally and in CI.

| Artifact | Wraps | For | Architectures |
|---|---|---|---|
| `postsonde_<version>_<arch>.deb` | gtk4 binary (GTK 4 + WebKitGTK 6.0) | Ubuntu 24.04+, Debian 13+ | `amd64`, `arm64` |
| `postsonde-gtk3_<version>_<arch>.deb` | gtk3 binary (GTK 3 + WebKit2GTK 4.1) | Ubuntu 22.04+, Debian 12+ | `amd64`, `arm64` |
| `PostSonde-<version>-<x86_64\|aarch64>.AppImage` | gtk3 binary plus its libraries | any distribution with glibc 2.35+ (Ubuntu 22.04+, Debian 12+, Fedora 36+, ...) | `x86_64`, `aarch64` |
| `postsonde-<version>-1.fc<44\|45>.<x86_64\|aarch64>.rpm` | gtk4 build, from the source tarball | Fedora 44 (`.fc44`), Fedora 45 (`.fc45`) | `x86_64`, `aarch64` |
| `PostSonde-<version>-x86_64.flatpak` | gtk4 build on the GNOME 51 runtime | any distribution with Flatpak | `x86_64` |

The source tarball `postsonde-<version>.tar.xz` (the tree, the frontend's
production build and the vendored Go modules, from
[`scripts/package-sources.sh`](../scripts/package-sources.sh)) and the
source RPM are released with them. No Arch package for now.

macOS (a universal `.app` in a `.dmg`) is in
[packaging-macos.md](packaging-macos.md), Windows (NSIS installers) in
[packaging-windows.md](packaging-windows.md).

## `.deb` packages

Built with nfpm through `wails3 tool package` (Wails v3.0.0, nfpm
v2.44.1) from [`build/linux/nfpm/`](../build/linux/nfpm/), by
[`scripts/package-linux-deb.sh`](../scripts/package-linux-deb.sh).

### Two package names

The gtk4 and gtk3 builds link different libraries, so they are two packages
rather than one with a version suffix:

- `postsonde`: the default GTK 4 build.
- `postsonde-gtk3`: the legacy GTK 3 build, for releases without WebKitGTK 6.0.

Both install `/usr/bin/postsonde`, so each `Conflicts:` and `Replaces:` the
other. Installing one removes the other, and apt says so. Separate names
keep the Depends honest per stack and let a repository carry both. A
version suffix would make the two builds look like upgrades of each other.

### Dependencies

`Depends` lists exactly the libraries in the binary's `NEEDED` entries
(`readelf -d`), mapped to the packages that ship them (`dpkg -S`), with the
minimum versions `dpkg-shlibdeps` derives:

| `NEEDED` | `postsonde` (Ubuntu 24.04) | `postsonde-gtk3` (Ubuntu 22.04) |
|---|---|---|
| `libc.so.6` | `libc6 (>= 2.34)` | `libc6 (>= 2.34)` |
| `libglib-2.0`, `libgio-2.0`, `libgobject-2.0` | `libglib2.0-0t64 (>= 2.74.0)` | `libglib2.0-0 (>= 2.33.14)` |
| `libgtk-4.so.1` / `libgtk-3.so.0`, `libgdk-3.so.0` | `libgtk-4-1 (>= 4.13.8)` | `libgtk-3-0 (>= 3.21.5)` |
| `libgdk_pixbuf-2.0.so.0` | (not linked) | `libgdk-pixbuf-2.0-0 (>= 2.31.1)` |
| `libwebkitgtk-6.0.so.4` / `libwebkit2gtk-4.1.so.0` | `libwebkitgtk-6.0-4 (>= 2.39.91)` | `libwebkit2gtk-4.1-0 (>= 2.39.91)` |
| `libjavascriptcoregtk-6.0.so.1` / `-4.1.so.0` | `libjavascriptcoregtk-6.0-1` | `libjavascriptcoregtk-4.1-0` |
| `libsoup-3.0.so.0` | `libsoup-3.0-0 (>= 2.4.0)` | `libsoup-3.0-0 (>= 2.4.0)` |
| `libX11.so.6` | `libx11-6` | `libx11-6` |

`libsoup-3.0-0 (>= 2.4.0)` is what `dpkg-shlibdeps` reads from libsoup's
symbols file, not a typo. `postsonde-gtk3` also installs on Ubuntu 24.04,
where `libglib2.0-0t64` provides `libglib2.0-0`.

When a Wails upgrade changes the `#cgo` libraries, regenerate the list
from a binary in a container of the target release. Install the package
first so the libraries are present:

```sh
readelf -d /usr/bin/postsonde | grep NEEDED
for lib in $(readelf -d /usr/bin/postsonde | sed -n 's/.*\[\(.*\)\]/\1/p'); do dpkg -S "*/$lib"; done
mkdir -p /tmp/x/debian && printf 'Source: x\n\nPackage: x\nArchitecture: any\n' > /tmp/x/debian/control
cd /tmp/x && dpkg-shlibdeps -O /usr/bin/postsonde    # needs dpkg-dev
```

### Files

| Path | Contents |
|---|---|
| `/usr/bin/postsonde` | the binary (0755) |
| `/usr/share/applications/io.github.sorrelfox.PostSonde.desktop` | [`build/linux/io.github.sorrelfox.PostSonde.desktop`](../build/linux/io.github.sorrelfox.PostSonde.desktop) |
| `/usr/share/metainfo/io.github.sorrelfox.PostSonde.metainfo.xml` | [AppStream metadata](../build/linux/io.github.sorrelfox.PostSonde.metainfo.xml) for software centres |
| `/usr/share/icons/hicolor/{16x16,24x24,32x32,48x48,64x64,128x128,256x256,512x512}/apps/postsonde.png` | from `assets/icons/linux/hicolor/` |
| `/usr/share/icons/hicolor/scalable/apps/postsonde.svg` | same |
| `/usr/share/doc/<package>/copyright` | machine-readable copyright: MIT from `LICENSE`, plus the fonts |
| `/usr/share/doc/<package>/THIRD_PARTY_NOTICES.md` | the bundled components and their licenses |
| `/usr/share/doc/<package>/fonts/{instrument-sans,jetbrains-mono}/OFL.txt` | the font licenses; the OFL requires them to ship with the fonts, and Vite doesn't copy them into `dist/` |

`<package>` is `postsonde` or `postsonde-gtk3`, as Debian policy asks.
There are no maintainer scripts. dpkg triggers from `desktop-file-utils`
and `hicolor-icon-theme` refresh the menu and icon caches. Installing
runs nothing and makes no network traffic.

`Maintainer:` is `Sorrelfox <348316453+sorrelfox@users.noreply.github.com>`
(the owner's GitHub no-reply address), `Homepage:` is
<https://github.com/sorrelfox/PostSonde>. The `copyright` file names the
same contact (`Upstream-Contact`) and repository (`Source`).

These files replace the Wails template's `build/linux/desktop`,
`nfpm/nfpm.yaml` and `nfpm/scripts/`. `wails3 task
common:update:build-assets` (see `build/config.yml`) writes the template
files back; delete them again, since nothing uses them.

### Desktop entry and window matching

`Exec=postsonde`, `Icon=postsonde`, `Categories=Development;Network;`.
`desktop-file-validate` accepts the file on 22.04, 24.04 and Fedora 44.
It prints a hint that two main categories can list the app twice in some
menus.

One ID ties the window to its menu entry and icon: the app ID
`io.github.sorrelfox.PostSonde`. `internal/app` sets it as
`application.Options.Linux.ApplicationID` (Wails v3.0.0), which replaces Wails' default GTK application ID
`org.wails.<name, lowercased>`. With `Linux.ProgramName` left empty,
Wails also passes it to `g_set_prgname`, so the window reports it in
every session:

| Session | The window reports |
|---|---|
| Wayland | `app_id` `io.github.sorrelfox.PostSonde` |
| X11, gtk4 | `WM_CLASS` `"io.github.sorrelfox.PostSonde", "io.github.sorrelfox.PostSonde"` |
| X11, gtk3 (and so the AppImage) | `WM_CLASS` `"io.github.sorrelfox.PostSonde", "Io.github.sorrelfox.PostSonde"`: GTK 3 capitalises the class's first letter |

The desktop file is named after the ID and says
`StartupWMClass=io.github.sorrelfox.PostSonde`: on Wayland the `app_id`
is the desktop file ID, and under X11 the `WM_CLASS` instance part is
exactly the `StartupWMClass` (GNOME Shell matches that part first; Plasma
compares the class without regard to case). The AppImage, which always
runs under X11 (next section), uses the same file unchanged. With Wails'
defaults, the window reported `org.wails.postsonde` on Wayland and
`postsonde`, the binary name, on X11, and the desktop file could only
match one of them.

Checked on Fedora 44 with KDE Plasma (Wayland session), each run with
`POSTSONDE_HOME` and the `XDG_*_HOME` directories in a scratch folder:
the production gtk4 binary under Wayland (`WAYLAND_DEBUG=1` shows
`xdg_toplevel.set_app_id("io.github.sorrelfox.PostSonde")`) and under
XWayland with `GDK_BACKEND=x11` (`xprop WM_CLASS`), and the gtk3 AppImage
from `make linux` (`xprop WM_CLASS`), with the values above. The
single-instance bus name was
`io.github.sorrelfox.PostSonde.i<16 hex digits>.SingleInstance`
([windows.md](windows.md#single-instance)), and WebKitGTK's folders were
named after the ID as well (see [Portable mode](#portable-mode-app-04)).

The same ID names the AppStream metadata (`<id>`, the `desktop-id`
launchable) and the bundle identifier in `build/config.yml`. The metainfo
also gives the developer (Sorrelfox, `io.github.sorrelfox`), an update
contact and the homepage, bug tracker and source URLs on GitHub.
`appstreamcli validate` passes without warnings once those URLs answer:
until the GitHub repository is public it reports them as
`url-not-reachable`, and `appstreamcli validate --no-net` passes. With
`--pedantic` it also notes the capital letters in the ID (Flathub's
convention for a GitHub-hosted app keeps the repository's spelling) and
that there are no `<releases>` yet.

## AppImage

### Why the gtk3 build

An AppImage has to bundle GTK and WebKitGTK, since nothing in the AppImage
format provides them. The result is only as portable as the oldest system
its libraries were built on, because of glibc. So the AppImage wraps the
**gtk3 binary built on Ubuntu 22.04** and bundles 22.04's GTK 3 and
WebKit2GTK 4.1. That makes it the most portable Linux artifact: glibc 2.35
or newer. The gtk4 stack would need WebKitGTK 6.0, which only exists from
Ubuntu 24.04 (glibc 2.39) on. Where the distribution has the libraries,
the `.deb` is the better choice: smaller, and security updates to WebKit
arrive through apt.

The tradeoffs:

- **Size:** about 80 MB (77 MiB on aarch64, 79 MiB on x86_64), against
  4.5–5 MB for a `.deb`.
- **Bundled WebKit:** it is frozen at the build date's 22.04 version. A
  rebuild picks up newer security updates.
- **X11 only:** the linuxdeploy GTK plugin's AppRun hook sets
  `GDK_BACKEND=x11`, so on Wayland the app runs through XWayland.
- **No GStreamer plugins:** the GStreamer libraries WebKit links are
  bundled, its plugins aren't, so media playback in response previews
  won't work from the AppImage.
- **No printing:** GTK's print backends are left out; the CUPS backend
  would pull in CUPS and its dependencies.
- **Host libraries:** linuxdeploy's excludelist leaves the graphics and font
  stack to the host: `libEGL`, `libGL`, `libgbm`, `libdrm`,
  `libwayland-client`, `libX11`/`libxcb`, `libfontconfig`, `libfreetype`,
  `libharfbuzz` and `libfribidi`, plus a few base libraries every desktop
  has: glibc, `libstdc++`, `libz`, `libgpg-error` and `libcom_err`. WebKit
  also loads `libGLESv2.so.2` with `dlopen`, so it isn't linked and isn't
  bundled, and aborts without it. The GL and EGL libraries must match the
  host's GPU drivers. GNOME, KDE and most desktops install all of these.
  A minimal system may lack `libGLESv2`: on Debian/Ubuntu install
  `libgles2`, on Fedora `libglvnd-gles`. A bare container has none of
  them; there, install them first (`libegl1 libgles2 libgbm1 libgl1
  libwayland-client0 libharfbuzz0b libfribidi0 libfontconfig1
  libfreetype6 libx11-6 libx11-xcb1 libgpg-error0 libcom-err2`): the
  release workflow starts the AppImage on a Debian 13 with exactly these
  ([ci.md, Package tests](ci.md#package-tests)).
- **Working directory:** the app starts in the AppImage's own `usr/`
  directory (see below). The directory it was launched from is in `$OWD`.
- **No update information** is embedded, so nothing checks for updates.

### How it is built

[`scripts/package-linux-appimage.sh <arch>`](../scripts/package-linux-appimage.sh)
runs as root in `ubuntu:22.04` of the **target** architecture. linuxdeploy
finds the libraries to bundle with `ldd`, so it can't cross-package.

1. Installs `libgtk-3-0`, `libwebkit2gtk-4.1-0` and the tools the GTK plugin
   calls (`dpkg-dev`, `pkg-config`, `libglib2.0-bin`, `libgtk-3-bin`,
   `librsvg2-common`), with `--no-install-recommends`.
2. Downloads the pinned tooling and checks each SHA-256 (table below). It
   runs copies of linuxdeploy and appimagetool with the AppImage magic
   bytes (`AI\x02` at ELF offset 8) zeroed. Under emulation through
   binfmt_misc (qemu, or Rosetta in Docker Desktop), which is how `make
   linux` packages the other architecture, the kernel refuses to start an
   ELF with those bytes set ("Exec format error"). The verified downloads
   and the runtime stay untouched.
3. Builds an AppDir in a temporary directory, containing:
   - the binary;
   - the desktop file, the metainfo and the hicolor icons;
   - `usr/share/doc/postsonde/` with `LICENSE`, `THIRD_PARTY_NOTICES.md` and
     both `OFL.txt` files;
   - WebKit's helper directory (`WebKitWebProcess`, `WebKitNetworkProcess`,
     `WebKitGPUProcess`, `injected-bundle/`).
4. Runs linuxdeploy with the GTK plugin (`DEPLOY_GTK_VERSION=3`). It copies
   the libraries, GTK modules, pixbuf loaders and GLib schemas. It also
   copies the Debian `copyright` file of every bundled library into
   `usr/share/doc/`. The plugin is the copy Wails v3.0.0 embeds
   (`internal/commands/linuxdeploy-plugin-gtk.sh`), vendored as
   [`build/linux/appimage/linuxdeploy-plugin-gtk.sh`](../build/linux/appimage/linuxdeploy-plugin-gtk.sh).
   The plugin copies GTK's input-method modules, print backends and the
   pixbuf loaders, but linuxdeploy never processes them. The script drops
   the print backends and runs linuxdeploy a second time with
   `--deploy-deps-only` over the rest. That bundles their libraries (e.g.
   `libtiff`) and sets their RPATH. It also makes `.DirIcon` the 256 px PNG,
   since linuxdeploy picks the SVG and thumbnailers expect a PNG.
5. Relocates WebKit. WebKit2GTK starts its helper processes from a
   directory compiled into the library (`/usr/lib/<triple>/webkit2gtk-4.1`),
   and nothing in the environment overrides that in release builds. The
   script rewrites the path in the bundled `libwebkit2gtk-4.1.so.0` to the
   same-length relative `././/lib/<triple>/webkit2gtk-4.1`. It then adds an
   AppRun hook that changes to `$APPDIR/usr` first. Tauri and AppImageKit's
   AppRun use the same approach. Without it, the bundled WebKit would start
   the host's helpers, or fail on hosts without WebKit. linuxdeploy's
   AppRun then execs `usr/bin/postsonde` directly instead of through its
   `AppRun.wrapped` symlink, so the process is named `postsonde`, as from
   the `.deb`.
6. `appimagetool --runtime-file` packs the AppDir with the static type2
   runtime, which needs no `libfuse2` on the host.

Why not `wails3 generate appimage` (the template's `create:appimage`)? In
beta.22 it downloaded linuxdeploy and AppImageKit's `AppRun` from the
unpinned `continuous` releases, without checksums, and left WebKit
pointing at the host's helper processes. The script keeps its approach
(linuxdeploy plus the GTK plugin, with WebKit's helpers bundled) and fixes
those three things. In v3.0.0 Wails writes its own `AppRun` and relocates
WebKit's helpers (to `usr/lib/<name>`, starting the app in `usr/lib` so
WebKit's bubblewrap sandbox can bind them), but still downloads linuxdeploy
from `continuous` without a checksum.

### Pinned tooling

| Tool | Release | Asset | SHA-256 |
|---|---|---|---|
| [linuxdeploy](https://github.com/linuxdeploy/linuxdeploy) | `1-alpha-20251107-1` | `linuxdeploy-x86_64.AppImage` | `c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d` |
| | | `linuxdeploy-aarch64.AppImage` | `620095110d693282b8ebeb244a95b5e911cf8f65f76c88b4b47d16ae6346fcff` |
| [appimagetool](https://github.com/AppImage/appimagetool) | `1.9.1` | `appimagetool-x86_64.AppImage` | `ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0` |
| | | `appimagetool-aarch64.AppImage` | `f0837e7448a0c1e4e650a93bb3e85802546e60654ef287576f46c71c126a9158` |
| [type2-runtime](https://github.com/AppImage/type2-runtime) | `20251108` | `runtime-x86_64` | `2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d` |
| | | `runtime-aarch64` | `00cbdfcf917cc6c0ff6d3347d59e0ca1f7f45a6df1a428a0d6d8a78664d87444` |
| linuxdeploy-plugin-gtk | as embedded in Wails v3.0.0 (unchanged since beta.22) | vendored, not downloaded | `baa283e1c344d19cf2205336356a45b20583d3f4a2aa82eb74127ff31fec9805` |

The checksums are the digests GitHub publishes for each release asset. To
bump a tool, pick a tagged release (never `continuous`), then update the
version and both checksums in the script. The downloads are build
infrastructure. The AppImage itself never downloads anything.

## RPM

`postsonde-<version>-1.fc<release>.<arch>.rpm`, from
[`packaging/rpm/postsonde.spec`](../packaging/rpm/postsonde.spec): the gtk4
build for each supported Fedora release, 44 and 45, one RPM per release
(the dist tag `.fc44` or `.fc45`). Each is built against its release's
libraries: install the one for your release.

[`scripts/package-linux-rpm.sh`](../scripts/package-linux-rpm.sh) builds it
from the source tarball in a clean container of the Fedora release and
the target architecture, the way a build service builds it: the source
RPM from the tarball and the spec in it, exactly its `BuildRequires`
installed with `dnf
builddep` (Fedora's `golang`, `gtk4-devel`, `webkitgtk6.0-devel`,
`libX11-devel`, `desktop-file-utils`, `appstream`), and the binary RPM
rebuilt from nothing but that source RPM by a normal user, offline: the Go
modules come from the tarball's `vendor/` (`GOFLAGS=-mod=vendor
GOPROXY=off`) and the frontend from its `frontend/dist/`. The build flags
are the other packages' (`-tags production -trimpath -buildvcs=false
-ldflags "-w -s -X main.version=<version>"`). `%check` validates the desktop
file and the metainfo and runs `postsonde --version`.

The Go is Fedora's own `golang` where the release has one at least as new
as `go.mod`'s Go: Fedora 45 (1.27). Fedora 44's `golang` is 1.26, so there
the script installs Go from go.dev at the version and checksum
`scripts/pins.env` pins, the same as the Linux binaries' and the Flatpak's,
and the spec builds with it (the `postsonde_goroot` macro) instead of
requiring `golang`. Go links statically, so either way the RPM depends
only on the system's libraries.

The `Requires` come from rpm's dependency generator, which reads the
libraries the binary links (`libwebkitgtk-6.0.so.4`, `libgtk-4.so.1`,
`libsoup-3.0.so.0`, ...), plus `hicolor-icon-theme` for the icon folders.
The files are the `.deb`'s ([Files](#files)), except that the licenses go
to `/usr/share/licenses/postsonde/` (`LICENSE`, `fonts/*/OFL.txt`) and
`THIRD_PARTY_NOTICES.md` to `/usr/share/doc/postsonde/`, as Fedora does it.
`postsonde` doesn't conflict with anything: Fedora has no gtk3 package of
it.

The source RPM holds the tarball and the spec. One is released,
`postsonde-<version>-1.fc45.src.rpm`: the others differ in the dist tag,
and Fedora 44's in building with the go.dev Go. `rpmbuild --rebuild` makes
the RPM offline on another Fedora 45, and a build service such as COPR can
build it as it is; on Fedora 44, give it the go.dev Go with `--define
'postsonde_goroot <Go's folder>'`.

## Flatpak

`PostSonde-<version>-x86_64.flatpak`: a single-file bundle of
`io.github.sorrelfox.PostSonde`, from
[`packaging/flatpak/io.github.sorrelfox.PostSonde.yml`](../packaging/flatpak/io.github.sorrelfox.PostSonde.yml).
It is the gtk4 build on the GNOME 51 runtime (`org.gnome.Platform`), which
has GTK 4 and WebKitGTK 6.0.

[`scripts/package-flatpak.sh`](../scripts/package-flatpak.sh) builds it
with flatpak-builder in the GNOME SDK, from the source tarball, with Go
from go.dev at the version and checksums
[`scripts/pins.env`](../scripts/pins.env) pins; flatpak-builder downloads
and checks the Go tarball, and the build itself runs offline with the
tarball's vendored modules and frontend build. The SDK and the Platform are
pinned by commit too: the script installs them for the user from Flathub
and puts them at the pinned commits. The manifest is a template: the
script fills in the version, the tarball and the pins.

The sandbox gets:

| Permission | Why |
|---|---|
| `--share=network` | an API client: it sends the requests you make, to the servers you name |
| `--share=ipc`, `--socket=wayland`, `--socket=fallback-x11`, `--device=dri` | the window, on Wayland or X11, with GPU rendering |
| `--filesystem=home` | projects are folders anywhere in the home folder |
| `--talk-name=org.freedesktop.secrets` | the secrets key in the desktop's keyring (the Secret Service); without a keyring PostSonde asks for its master password |

PostSonde's settings and data go to `~/.var/app/io.github.sorrelfox.PostSonde/`.
The desktop file and the icons are exported under the app ID
(`rename-icon`), so the menu entry and the window match as in the `.deb`
([Desktop entry and window matching](#desktop-entry-and-window-matching)).
The bundle names Flathub as its runtime's repository, so `flatpak install`
fetches the GNOME runtime from there when it is missing.

x86_64 only for now: the runtime commits in `scripts/pins.env` are
x86_64's. The manifest isn't Flathub's kind: Flathub builds the frontend
from source too, with npm's packages as declared sources, where this one
takes the frontend build from the tarball.

## Versioning

The packaging scripts take the package version from
[`scripts/version.sh`](../scripts/version.sh), the same helper that stamps
`main.version` into the binaries (`scripts/ci-build-linux.sh`, `make
build`). The first match wins:

1. `PKG_VERSION`, as given: for a manual release build, and in CI, whose
   `setup` job works the version out once and passes it to the other jobs.
2. From git: a tag `vX.Y.Z` on `HEAD` gives `X.Y.Z`, otherwise
   `0.0.0~git<short sha>`, e.g. `0.0.0~gitc3bded3`; `+dirty` is appended
   when tracked files have uncommitted changes.

The RPM and the Flatpak take the version from the source tarball's name,
which `version.sh` gave it. The `~` sorts before anything, in dpkg and in
rpm, so `0.0.0~git...` < `0.1.0` and a release always upgrades a
development build. There is no Debian revision (`-1`); the RPM's release is
`1.fc44` or `1.fc45`. Versions must start with a digit and contain only
`A-Za-z0-9.+~`; `version.sh` fails otherwise. Release builds come from a
tag `vX.Y.Z` and get `X.Y.Z`; a run of the release workflow by hand gets
the `~git` form ([Releasing](releasing.md)).
`postsonde --version` prints the same string as the package version
(`APP_VERSION` overrides the binary's alone). See also
[Building locally, Version](building.md#version).

## Building locally

`make linux` builds the binaries, the `.deb` packages and the AppImages
through Docker, in the CI jobs' images, and collects them with
`SHA256SUMS` in `dist/linux/`; `make linux-packages` repackages the
binaries already there. `make rpm` builds the source tarball and from it
the RPM of Docker's architecture (`ARCH=arm64` for the other one,
emulated), and installs and starts it in a clean Fedora container. `make
flatpak` builds the source tarball and the Flatpak, and installs and
starts it, in a privileged Fedora container with its own Flatpak
installation (the `postsonde-flatpak` volume), so the host's Flatpak
installation isn't touched. See [Building
locally](building.md#linux-builds-through-docker).

`scripts/test-linux-packages.sh <packages>` installs and starts `.deb`,
`.rpm` and AppImage files in clean containers, as the release workflow
does ([ci.md, Package tests](ci.md#package-tests)).

By hand: use a fresh clone and Docker, as in [Reproducing a job
locally](ci.md#reproducing-a-job-locally), and build the binaries first
(`bin/postsonde-linux-<stack>-<arch>`). Then:

```sh
# .deb: any host, any target architecture (nfpm is pure Go)
docker run --rm -v "$PWD":/src -w /src golang:1.27-trixie bash -ec '
  CGO_ENABLED=0 scripts/install-wails3.sh
  export PATH="$(go env GOPATH)/bin:$PATH"
  scripts/package-linux-deb.sh gtk4 arm64
  scripts/package-linux-deb.sh gtk3 arm64'

# AppImage: an ubuntu:22.04 container of the target architecture
docker run --rm --platform linux/arm64 -v "$PWD":/src -w /src ubuntu:22.04 scripts/package-linux-appimage.sh arm64
docker run --rm --platform linux/amd64 -v "$PWD":/src -w /src ubuntu:22.04 scripts/package-linux-appimage.sh amd64   # emulated on arm64
```

The AppImage of the other architecture runs emulated, several times
slower. Docker Desktop has the emulation built in; a Linux host needs
qemu/binfmt (`docker run --privileged --rm tonistiigi/binfmt --install
all`), or the container fails with `exec format error`. The script checks
`dpkg --print-architecture` against its target and fails with a clear
message instead of packaging the wrong architecture.

With `wails3` already on `PATH`, `scripts/package-linux-deb.sh` also runs
directly on macOS or Linux. On a Linux machine with the GTK dev packages,
`wails3 package` (optionally `EXTRA_TAGS=gtk3`) builds a production binary
for the host and packages it as a `.deb`. `wails3 task
linux:create:appimage ARCH=<arch>` runs the AppImage container above.

Everything lands in `dist/` (gitignored). The downloaded AppImage tools
are cached in `build/linux/appimage/build/<arch>/tools/` (gitignored) and
checked again on every run.

## CI

The release workflow builds and tests the Linux packages
([ci.md, Package](ci.md#package)):

```
linux:gtk4 (amd64, arm64) ─┬─ linux:deb ───────────────┬─ linux:test (amd64, arm64)
linux:gtk3 (amd64, arm64) ─┴─ linux:appimage:<arch> ───┤
sources ─┬─ linux:rpm:fc<release>:<arch> ─────────────┼─ linux:packages
         └─ linux:flatpak ─────────────────────────────┘
```

| Job | Runs on | Does |
|---|---|---|
| `linux:deb` | amd64, `golang:1.27-trixie` | `scripts/install-wails3.sh`: the `wails3` CLI at the `go.mod` version (`CGO_ENABLED=0`: packaging needs no GTK), then `scripts/package-linux-deb.sh` for both stacks and both architectures. Go caches as in the build jobs. |
| `linux:appimage:<arch>` | a runner of the architecture, `ubuntu:22.04` | `scripts/package-linux-appimage.sh <arch>` with the gtk3 binary of that architecture |
| `linux:test (<arch>)` | a runner of the architecture, with Docker | `scripts/test-linux-packages.sh` on the `.deb` packages and the AppImage of the architecture |
| `linux:rpm:fc<release>:<arch>` | a runner of the architecture, with Docker | `scripts/package-linux-rpm.sh` in `fedora:44` or `fedora:45`, then `scripts/test-linux-packages.sh` on the RPM, in a clean container of the same release |
| `linux:flatpak` | amd64, on the runner itself | `scripts/package-flatpak.sh`, then `scripts/test-flatpak.sh` |
| `linux:packages` | amd64, `ubuntu:24.04` | `scripts/sha256sums.sh dist`: writes `SHA256SUMS` over all the packages and checks it |

The package jobs keep their files for 1 day. `linux:packages` keeps every
Linux package (the four `.deb`s, both AppImages, both RPMs and the source
RPM, the Flatpak) plus `SHA256SUMS` for **30 days**, in one artifact named
`postsonde-linux-packages-<short sha>`. A tag run releases them
([Releasing](releasing.md)).

Each AppImage job runs natively, `linux:appimage:arm64` on GitHub's hosted
arm64 runner ([ci.md, Runners](ci.md#runners)), so nothing is emulated.
Each uses only the gtk3 binary of its own architecture, but waits for both
`linux:gtk3` jobs: a job can only wait for a whole matrix. The script
still checks `dpkg --print-architecture` against its target and fails with
a clear message instead of packaging the wrong architecture. The jobs
need outbound HTTPS to the Ubuntu and Fedora mirrors, to Flathub, and to
github.com for the AppImage tools.

## Portable mode (APP-04)

Portable mode runs PostSonde from a folder or a USB stick with everything
kept beside it. It applies to every OS, not only Linux.

**Turning it on.** Put an empty file named `postsonde.portable` in the
*app folder*. On the next start PostSonde keeps its settings, data, cache
and logs in `postsonde-data/` in the same folder:

```
<app folder>/
  postsonde.portable          the marker (its content is ignored)
  PostSonde.app | postsonde | PostSonde.exe | PostSonde-….AppImage
  postsonde-data/
    config/  settings.json
    data/    workspaces/…  (history, cookies, tokens, secrets)
    cache/   (webview2/ on Windows)
    logs/    postsonde.log
```

Delete the marker to go back to the normal locations; `postsonde-data/`
is left as it is. `POSTSONDE_HOME` wins over the marker, and the marker
wins over a data directory chosen in the settings
([settings.md](settings.md#data-directory-set-03)). In portable mode
**Change…** is disabled.

**The app folder**, per OS:

| OS and build | The app folder is | Example |
|---|---|---|
| macOS app bundle | the folder holding `PostSonde.app` (not `Contents/MacOS`) | `/Volumes/USB/PostSonde/PostSonde.app` → `/Volumes/USB/PostSonde/postsonde.portable` |
| Windows | the folder holding `PostSonde.exe` | `E:\PostSonde\postsonde.portable` |
| Linux AppImage | the folder holding the `.AppImage` file (`$APPIMAGE`), not its temporary mount | `/media/usb/PostSonde-1.0.0-x86_64.AppImage` → `/media/usb/postsonde.portable` |
| Linux or macOS plain binary | the folder holding the binary (symlinks resolved) | `~/tools/postsonde/postsonde.portable` |

**Read-only locations.** Portable mode needs a folder the user can write:

- A mounted `.dmg` is read-only: copy `PostSonde.app` (and the marker) to
  the USB stick or a folder first.
- On macOS, an app started from where it was downloaded may run from a
  randomised read-only copy (App Translocation); PostSonde then cannot see
  the marker and uses the normal locations. Move the app with Finder once,
  or clear its quarantine attribute, before relying on portable mode.
- The `.deb` packages install `/usr/bin/postsonde`: `/usr/bin` belongs to
  root, so portable mode is not for packages. Use the AppImage or a plain
  binary in a folder you own.
- `C:\Program Files` is not writable without elevation; put a portable
  copy elsewhere.

If the marker is found but `postsonde-data/` cannot be created or written,
PostSonde still starts, keeps nothing on disk (settings in memory, logs on
stderr, no projects can be opened) and says why on the welcome screen. It
never falls back to the home directory.

**What is still written outside the app folder.** Everything PostSonde
itself writes is in `postsonde-data/`. Some files belong to the OS or the
web view and cannot be redirected:

| OS | Outside the app folder |
|---|---|
| Windows | nothing from the web view: the WebView2 profile goes to `postsonde-data/cache/webview2` (it would otherwise be `%APPDATA%\postsonde.exe`). Windows itself may record the app in its jump lists and recent files. |
| macOS | WKWebView's cache and web storage (`~/Library/Caches/<bundle id>`, `~/Library/WebKit/<bundle id>`), the app's preferences and saved window state (`~/Library/Preferences`, `~/Library/Saved Application State`). None hold projects, secrets or history. |
| Linux | WebKitGTK's cache and web storage (`~/.cache/io.github.sorrelfox.PostSonde`, `~/.local/share/io.github.sorrelfox.PostSonde`: WebKitGTK names them after the program name, which is the app ID; older builds used `postsonde`) and GTK's recent-files list for the file dialogs. None hold projects, secrets or history. |

## Installing (for users)

**Debian/Ubuntu.** Download the package for your release and CPU, check it
against `SHA256SUMS`, and install it with apt so the dependencies come
along:

```sh
sudo apt install ./postsonde_<version>_amd64.deb        # Ubuntu 24.04+, Debian 13+
sudo apt install ./postsonde-gtk3_<version>_amd64.deb   # Ubuntu 22.04, Debian 12
```

**Fedora.** The same with dnf:

```sh
sudo dnf install ./postsonde-<version>-1.fc44.x86_64.rpm   # Fedora 44
sudo dnf install ./postsonde-<version>-1.fc45.x86_64.rpm   # Fedora 45
```

**Flatpak.** For your user; the GNOME runtime comes from Flathub when it
isn't installed yet:

```sh
flatpak install --user ./PostSonde-<version>-x86_64.flatpak
flatpak run io.github.sorrelfox.PostSonde
```

**AppImage.**

```sh
chmod +x PostSonde-<version>-x86_64.AppImage
./PostSonde-<version>-x86_64.AppImage
```

The AppImage mounts itself with FUSE: it needs the `fusermount` or
`fusermount3` tool (package `fuse3` or `fuse`) and `/dev/fuse`, but not
`libfuse2`. Where FUSE isn't available (containers, some locked-down
systems), run it with `--appimage-extract-and-run`, which unpacks to a
temporary directory first. Or run `--appimage-extract` once and start
`squashfs-root/AppRun`. The AppImage doesn't add itself to the menu; use an
integration tool such as Gear Lever or AppImageLauncher for that.
