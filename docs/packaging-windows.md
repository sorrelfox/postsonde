# Windows packaging

PostSonde ships on Windows as an NSIS installer per CPU architecture,
unsigned for now. This page covers what the installer does, how it is
built and checked, and what is left to check on Windows. Linux packages:
[packaging.md](packaging.md); macOS: [packaging-macos.md](packaging-macos.md).

| Artifact | Contains | For |
|---|---|---|
| `PostSonde-<version>-windows-amd64-setup.exe` | `PostSonde.exe` (amd64), the uninstaller, the license texts | Windows 10 and 11 on x64 |
| `PostSonde-<version>-windows-arm64-setup.exe` | `PostSonde.exe` (arm64), the uninstaller, the license texts | Windows 10 and 11 on ARM64 |

The installer itself is a 32-bit x86 program (NSIS has no ARM64 stub);
Windows on ARM runs it emulated, and it installs the native arm64 app.
Each installer refuses the other architecture ("This product can't be
installed on the current Windows architecture"), and both refuse Windows
older than 10.

## What the installer does

| | |
|---|---|
| **Scope** | Per user: no administrator rights, no UAC prompt. Installs into `%LOCALAPPDATA%\Programs\PostSonde` (the folder can be changed on the second page). Another Windows user on the same PC installs their own copy. |
| **Files** | `PostSonde.exe`, `uninstall.exe`, and `licenses\` with `LICENSE.txt`, `THIRD_PARTY_NOTICES.md` and `fonts\{instrument-sans,jetbrains-mono}\OFL.txt` (the licenses of the bundled components require them, see [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)). |
| **Start menu** | `PostSonde.lnk` in the user's Start-menu programs folder, working directory the install folder. No desktop shortcut. |
| **Apps › Installed apps** | An entry under `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\PostSonde` with the name, version, publisher (`Sorrelfox`), icon, install folder, size, the (quiet) uninstall commands and two links, the project page (`URLInfoAbout`) and the issue tracker (`HelpLink`) on GitHub; no Modify or Repair buttons. |
| **Finish page** | A **Start PostSonde** box (ticked), which starts the app as the current user. |
| **Upgrades** | Running a newer installer installs into the folder of the previous install (`InstallLocation`) and replaces the files. Settings and data are not touched. |
| **While PostSonde runs** | Installing over it or uninstalling it stops with "PostSonde is running. Close all PostSonde windows, then click Retry." (a running executable can't be replaced). A silent run fails instead. |
| **Network** | None. No telemetry, no update check, no download (below). |
| **Not done** | No file associations, no URL protocol, no services, no scheduled tasks, no autostart, no firewall rules, no `PATH` change. |

### WebView2 runtime: checked, never downloaded

PostSonde draws its window with the Microsoft Edge WebView2 Runtime
(Evergreen). Windows 11 ships it, and on Windows 10 it comes with Microsoft
Edge updates, so it is almost always there.

The Wails template's installer runs Microsoft's WebView2 bootstrapper,
which downloads and installs the runtime without asking, and its Taskfile
fetches that bootstrapper at build time. PostSonde's installer does
neither ([PRIVACY.md](../PRIVACY.md): nothing is downloaded behind the
user's back). It only checks: the runtime's `pv` value under
`SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`,
per machine (`HKLM`, both registry views; on 64-bit Windows that is
`WOW6432Node`) or per user (`HKCU`). Empty or `0.0.0.0` counts as missing,
as Microsoft's [distribution
guide](https://learn.microsoft.com/microsoft-edge/webview2/concepts/distribution)
says.

If it is missing, a page after the welcome page says so:

> **Microsoft Edge WebView2 Runtime not found.** PostSonde needs it to show
> its window. … This installer doesn't download anything. Get the Evergreen
> Runtime from Microsoft (the Bootstrapper, or the Standalone Installer for
> x64 or ARM64): <https://developer.microsoft.com/microsoft-edge/webview2/>
> … Click Next to install PostSonde now; it will start once the runtime is
> installed. Or click Cancel, install the runtime, and run this installer
> again.

The link opens the page in the browser only when clicked. When the runtime
is there the page is skipped, and the install log shows its version
(`Microsoft Edge WebView2 Runtime: 1xx.0.….`). A silent install (`/S`)
with no runtime installs anyway and logs where to get it.

### Uninstalling

From **Settings › Apps › Installed apps › PostSonde › Uninstall**, from
`uninstall.exe`, or silently with `uninstall.exe /S`
(`QuietUninstallString`). It removes:

- `PostSonde.exe`, `uninstall.exe` and the `licenses\` files, then the
  install folder if it is empty. Only files the installer wrote are
  deleted: a folder that holds anything else (for example a
  [portable copy's](packaging.md#portable-mode-app-04) `postsonde-data\`)
  is left with that in it;
- the Start-menu shortcut;
- the Installed apps entry.

The uninstaller's second page has one box, **Delete my settings and data**,
off by default and never set in a silent uninstall. It deletes this Windows
user's `%APPDATA%\PostSonde` (settings), `%LOCALAPPDATA%\PostSonde`
(history, cookies, tokens, the DPAPI key files of saved secrets, cache,
logs) and `%APPDATA%\PostSonde.exe` (the WebView2 profile, which holds the
UI's local storage). Project folders are never touched: they are folders
the user chose, not part of the app's directories
([workspace.md](workspace.md)). A data directory moved in the settings
(SET-03), a `POSTSONDE_HOME` directory and portable copies are not touched
either.

## Building

### Locally: `make windows`

From any OS with Docker (no Windows needed):

```sh
make windows                  # both architectures; or: scripts/windows.sh
make windows ARCH=arm64       # one architecture
make windows VERSION=0.1.0    # a release build (PKG_VERSION=0.1.0 without make)
```

[`scripts/windows.sh`](../scripts/windows.sh) exports `HEAD` with `git
archive` into `.cache/windows/src` (uncommitted changes are not built) and
runs each step in a CI image pinned by digest in `scripts/pins.env`:

| Step | Script | Image |
|---|---|---|
| frontend | `scripts/build-frontend.sh` | `node:26-trixie-slim` |
| executable, per architecture | `scripts/install-wails3.sh`, then [`scripts/build-windows.sh <arch>`](../scripts/build-windows.sh) | `golang:1.27-trixie` |
| installer, per architecture | [`scripts/package-windows-installer.sh <arch>`](../scripts/package-windows-installer.sh) | `golang:1.27-trixie` plus Debian's `nsis` 3.11-1 |
| checksums | `scripts/sha256sums.sh` | `ubuntu:24.04` |

Both architectures build in a container of Docker's own architecture: Go
cross-compiles (Wails needs no cgo on Windows) and makensis writes the same
x86 stub for both, so nothing is emulated. Outputs land in `dist/windows/`
(gitignored), which holds one build at a time:

| File | |
|---|---|
| `postsonde-windows-<arch>.exe` | the bare executable, for trying it without installing |
| `PostSonde-<version>-windows-<arch>-setup.exe` | the release file |
| `SHA256SUMS` | over the installers, written and checked by `scripts/sha256sums.sh` |

Caches: the same `postsonde-*` Docker volumes as the Linux builds (`make
clean-docker`). With the images, npm packages and Go modules cached, a run
takes under a minute (34 s, 44 s with an empty Go build cache, on the
machine the checks below ran on). The installers are about 9 MB (amd64)
and 8 MB (arm64); the executables they hold are 31 MB and 28 MB.

### The executable

`scripts/build-windows.sh <arch>` does what `wails3 build GOOS=windows
ARCH=<arch>` ([`build/windows/Taskfile.yml`](../build/windows/Taskfile.yml))
does, minus the frontend build, `go mod tidy` and the icon sync, and writes
`bin/postsonde-windows-<arch>.exe`:

1. `wails3 generate syso` writes the Windows resources next to the main
   package (`cmd/postsonde/wails_windows_<arch>.syso`, deleted afterwards):
   the icon (`build/windows/icon.ico`, a copy of
   `assets/icons/postsonde.ico`), `build/windows/wails.exe.manifest` (per-
   monitor DPI awareness, common controls 6, `asInvoker`) and the version
   resource from `build/windows/info.json`, with its placeholder version
   replaced: the file version is the version's leading numbers padded to
   four (`0.1.0` → `0.1.0.0`, `0.0.0~git1234abc` → `0.0.0.0`), the product
   version string the full version.
2. `GOOS=windows GOARCH=<arch> CGO_ENABLED=0 go build -tags production
   -trimpath -buildvcs=false -ldflags "-w -s -H windowsgui -X
   main.version=<version>" ./cmd/postsonde`.

It needs `frontend/dist` built first and `wails3` at the `go.mod` version.

### The installer

`scripts/package-windows-installer.sh <arch>` runs makensis on
[`build/windows/nsis/project.nsi`](../build/windows/nsis/project.nsi) with
the executable from `bin/` and writes
`dist/PostSonde-<version>-windows-<arch>-setup.exe`. It works wherever
makensis 3 runs (`brew install makensis` on macOS, Git Bash on Windows);
without makensis it installs Debian's `nsis` at the pinned version when it
runs as root on Debian, as in the golang image.

Where the installer's values come from:

| Value | From |
|---|---|
| Company (the publisher, `Sorrelfox`), product name, copyright | `build/config.yml` `info`, through `build/windows/nsis/wails_tools.nsh`, which `wails3 task common:update:build-assets` regenerates from it |
| Project and support links in Installed apps | `HOMEPAGE_URL` and `HELP_URL` in `project.nsi` |
| Version (Installed apps, version resource strings, branding line) | [`scripts/version.sh`](../scripts/version.sh), as for every package ([packaging.md, Versioning](packaging.md#versioning)) |
| Version resource numbers | the version's leading numbers padded to four |
| Installer and uninstaller icon | `build/windows/icon.ico` |

`project.nsi` is PostSonde's own (`common:update:build-assets` doesn't
overwrite it). It still includes `wails_tools.nsh` for those names and for
the Wails macros that check the Windows version and architecture and pick
the executable, but not for `wails.webview2runtime` (the bootstrapper) or
the template's install and uninstall steps.

`wails3 package` on Windows (the Taskfile's `windows:create:nsis:installer`)
still works for a quick installer of a `bin/postsonde.exe` build, with
`makensis` on `PATH`. It no longer fetches the WebView2 bootstrapper, and
its version is `build/config.yml`'s placeholder; release installers come
from the script.

### In CI

Three jobs of the release workflow, `.github/workflows/release.yml`: the
build and the installers on the Linux runner in the Go image, the test on
Windows ([ci.md, `windows:build`](ci.md#windowsbuild), [Package](ci.md#package)
and [Package tests](ci.md#package-tests)):

| Job | Runs after | Runs | Keeps (30 days) |
|---|---|---|---|
| `windows:build (amd64)`, `(arm64)` | `frontend:build` | `CGO_ENABLED=0 scripts/install-wails3.sh`, then `scripts/build-windows.sh <arch>` | `bin/postsonde-windows-<arch>.exe` |
| `windows:installer` | both `windows:build` jobs | `scripts/package-windows-installer.sh <arch>` for both, then `scripts/sha256sums.sh dist` | `dist/PostSonde-<version>-windows-<arch>-setup.exe`, `dist/SHA256SUMS` |
| `windows:test (amd64)`, `(arm64)` | `windows:installer` | [`scripts/test-windows.sh`](../scripts/test-windows.sh) on `windows-latest` and `windows-11-arm`: a silent install for the current user, its files, Start menu entry and uninstall entry; `PostSonde.exe --version`; the app started until its first window is ready; a silent uninstall that leaves nothing behind | |

The jobs take the version from `scripts/version.sh`, which returns the
`PKG_VERSION` that CI works out once per run (`X.Y.Z` on a release tag,
`0.0.0~git<short sha>` otherwise), so the installer wraps the executable
of the same version. `windows:installer` needs outbound HTTPS to the
Debian mirror for nsis.

Signing: with the repository's code-signing secret
([Releasing, Signing](releasing.md#signing)), `windows:installer` signs
`PostSonde.exe` before it is packed, and makensis signs the uninstaller
and the installer through `!uninstfinalize` and `!finalize` in
`project.nsi`, with `scripts/sign-windows.sh` (osslsigncode). Without the
secret, all three stay unsigned.

## Reproducibility

Two builds of one commit give byte-identical installers and executables.
Checked in November 2026 with two `make windows` runs of commit `d945a4d5`
on Linux (amd64 Docker, golang image with Go 1.27.3, nsis 3.11-1), the
second with an empty Go build cache (`WINDOWS_GOCACHE_TAG`); all four files
matched:

| File | SHA-256 (both runs) |
|---|---|
| `PostSonde-0.0.0~gitd945a4d5-windows-amd64-setup.exe` | `358dce8664165c9585dd6037a3dd8fde437669e91edca2e670261ef6ec6dc8ab` |
| `PostSonde-0.0.0~gitd945a4d5-windows-arm64-setup.exe` | `44d71467c8922d6e98f38ebb42bc550149564a13aafe8ef03e2ed0b8b84ed1bd` |
| `postsonde-windows-amd64.exe` | `c85195f03bed78a0c2efbd069970a78ed28c1f413fb8baa80c937e42461ba5b5` |
| `postsonde-windows-arm64.exe` | `f15bcc06cab1d0075f6e496a0b8d56edf0449b7803157625dc827fb27426aa88` |

makensis alone was also run twice on the same input from directories with
different paths and file times: identical output. Like the [Linux
check](reproducible-builds.md#limits), this was one machine on one day;
`make verify-reproducible` doesn't cover Windows yet.

What makes the installer reproducible:

- `SetDateSave off`: the installer stores no file times (installed files
  get the time of the install).
- makensis embeds no build time; the PE header's timestamp is the one of
  the stub in Debian's nsis package, the same in every build.
- `-NOCONFIG`: no machine-wide `nsisconf.nsh` takes part.
- nsis is pinned (`3.11-1`, Debian trixie, in the digest-pinned golang
  image). A different makensis version gives a different installer.
- Solid LZMA compression is deterministic.

The executable is reproducible like the other Go builds
([reproducible-builds.md](reproducible-builds.md)): `-trimpath`,
`-buildvcs=false`, `-w -s`, the version as the only build-time input, and
`wails3 generate syso` writes no timestamp.

## Checking a build

On Linux, in any container with `p7zip-full` and `libimage-exiftool-perl`:

```sh
7z l dist/windows/PostSonde-*-windows-amd64-setup.exe     # PostSonde.exe, licenses/…, $PLUGINSDIR/…
exiftool dist/windows/PostSonde-*-windows-amd64-setup.exe  # Company Name, Product Version, …
exiftool dist/windows/postsonde-windows-arm64.exe          # Machine Type: ARM64, File Version Number
```

What was checked on Linux (commit `ba330e3b`, when the installer was added):

- `7z l`: each installer holds `PostSonde.exe`, the four license files and
  the `nsDialogs`/`System` plugins and wizard bitmap; `7z e` of
  `PostSonde.exe` is byte-identical to `postsonde-windows-<arch>.exe`.
- `exiftool`: the installers are x86 GUI executables with Company Name and
  Product Name `PostSonde`, File Description `PostSonde Installer`, Product
  Version and File Version `0.0.0~git` plus that commit, version number
  `0.0.0.0`;
  the executables are AMD64 and ARM64 GUI executables with the same
  product version, PE timestamp 0, and the seven icon sizes and the
  manifest from `build/windows/` (`7z l` lists `.rsrc`).
- Under Wine 10.0 (Debian trixie, amd64, Windows 10 mode, no network, no
  WebView2) in a container: `/S` installs exit 0 with the files above in
  `%LOCALAPPDATA%\Programs\PostSonde`, `PostSonde.lnk` in the Start menu,
  and the uninstall key with all values; a second `/S` install over it
  works; `uninstall.exe /S` removes the folder, shortcut and key and keeps
  `%APPDATA%\PostSonde`; with a `postsonde.portable` copy's
  `postsonde-data\` in the install folder, the folder stays with only that
  in it. The arm64 installer exits 65 (wrong architecture). Driving the GUI
  with xdotool: the welcome page, the WebView2 page (shown, since Wine has
  no runtime) with its link, the folder, progress and finish pages, and the
  uninstaller's confirm and components pages render; uninstalling with
  **Delete my settings and data** ticked removes `%APPDATA%\PostSonde`,
  `%LOCALAPPDATA%\PostSonde` and `%APPDATA%\PostSonde.exe` and leaves other
  folders. Wine can't run the app itself (no WebView2), so none of this
  shows PostSonde starting.

Rechecked on Linux after the move to Sorrelfox, amd64 from `make windows
ARCH=amd64`: the version resources of `postsonde-windows-amd64.exe` and of
the installer (`wrestool --raw -x -t16 <file> | strings -el`) give Company
Name `Sorrelfox`, with the product name and copyright unchanged; the
executable's manifest (`-t24`) has `assemblyIdentity` name
`io.github.sorrelfox.PostSonde`; and the installer's script strings hold
the uninstall entry's `Publisher` `Sorrelfox`, `URLInfoAbout`
`https://github.com/sorrelfox/PostSonde` and `HelpLink`
`https://github.com/sorrelfox/PostSonde/issues`.

### On Windows 11 (owner check, not done yet)

The installer has not been run on Windows yet. On a Windows 11 PC or VM
(x64; ARM64 too if one is at hand), as a standard user:

1. Check the hash: `Get-FileHash .\PostSonde-<version>-windows-amd64-setup.exe
   -Algorithm SHA256` against `SHA256SUMS`.
2. Run the installer. SmartScreen warns ("Windows protected your PC"):
   **More info › Run anyway**. There must be **no UAC prompt**.
3. The WebView2 page must not appear (Windows 11 has the runtime); the
   details list `Microsoft Edge WebView2 Runtime: <version>`. Keep the
   default folder, finish with **Start PostSonde** ticked: PostSonde starts
   and shows its window.
4. Check: the Start menu has **PostSonde** with the icon and it starts the
   app; **Settings › Apps › Installed apps** lists PostSonde with the
   version and icon, publisher Sorrelfox; `PostSonde.exe` › Properties ›
   Details shows the version; nothing on the desktop.
5. With PostSonde open, run the installer again: it must ask to close
   PostSonde, and Retry must work after closing it.
6. Uninstall from Installed apps, leaving **Delete my settings and data**
   off: the folder `%LOCALAPPDATA%\Programs\PostSonde`, the Start-menu entry
   and the Installed apps entry are gone; `%APPDATA%\PostSonde` is still
   there. Install again: the settings are back.
7. Uninstall with the box ticked: `%APPDATA%\PostSonde`,
   `%LOCALAPPDATA%\PostSonde` and `%APPDATA%\PostSonde.exe` are gone too.
8. Silent: `.\PostSonde-…-setup.exe /S`, then
   `& "$env:LOCALAPPDATA\Programs\PostSonde\uninstall.exe" /S`.
9. Optional, the missing-runtime path: on a Windows 10 VM without the
   runtime (or after uninstalling the runtime there), the WebView2 page
   appears, its link opens Microsoft's page, Next installs.
10. Optional, network: with an outbound firewall log (see
    [privacy.md, Manual checks](privacy.md#manual-checks)), the installer
    and uninstaller make no connection.

## Installing (for users)

1. Download the installer for your PC: `windows-amd64` for most PCs,
   `windows-arm64` for Windows on ARM (Snapdragon and similar; **Settings ›
   System › About › System type** says which).
2. Check it against `SHA256SUMS`: in PowerShell, `Get-FileHash
   .\PostSonde-<version>-windows-amd64-setup.exe -Algorithm SHA256`, or
   `certutil -hashfile <file> SHA256`.
3. Run it. The installer is not code-signed yet, so Windows may say
   "Windows protected your PC": click **More info › Run anyway**. If Smart
   App Control blocks it, check the file against `SHA256SUMS` first, then
   decide whether to allow it.
4. Click through: no administrator rights are needed. PostSonde is in the
   Start menu afterwards.

To update, run the installer of the new version; it replaces the old one in
place. To remove PostSonde, use **Settings › Apps › Installed apps**.

For a copy without installing (a USB stick, a locked-down PC), use the bare
`PostSonde.exe` of a build with a `postsonde.portable` file next to it
([portable mode](packaging.md#portable-mode-app-04)).
