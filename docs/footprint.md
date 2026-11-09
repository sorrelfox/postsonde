# Footprint: start time, size and idle memory

APP-05 asks for a small footprint: **under 1 s cold start**, a small
binary, low idle memory, and no bundled browser engine (the UI uses the OS
web view: WebKitGTK on Linux, WKWebView on macOS, WebView2 on Windows).
This page has the numbers, how they were taken and the budgets that keep
them there.

| What | Where |
|---|---|
| Size budgets | [`scripts/size-budgets.txt`](../scripts/size-budgets.txt), checked by [`scripts/check-size.sh`](../scripts/check-size.sh) in the `size:budgets` CI job ([ci.md](ci.md#size-budgets)) |
| Start time and idle memory on Linux | [`scripts/measure-footprint.sh`](../scripts/measure-footprint.sh) |
| The app's own start time | the `first window ready` log record (`internal/app/startup.go`) |

## Results

### Linux

Measured on 2026-11-08 at commit `94bed909` plus the startup log record
(Wails v3.0.0), with the CI binaries (built in `ubuntu:24.04` and
`ubuntu:22.04` by `scripts/ci-build-linux.sh`), on Fedora 44, KDE Plasma (Wayland), AMD
Ryzen 9 9950X3D2 (16 cores), 60 GB RAM, NVMe SSD, WebKitGTK 2.54.2,
GTK 4.22.5. `scripts/measure-footprint.sh -n 7`, idle 10 s on the welcome
screen; median of 7 runs (range in brackets):

| Binary | Start | App's own count | Idle PSS, all processes | Idle RSS, all processes | Idle RSS, main process | Processes |
|---|---|---|---|---|---|---|
| `postsonde-linux-gtk4-amd64` (GTK4, WebKitGTK 6.0) | **277 ms** (246–283) | 228 ms | **189 MiB** | 487 MiB | 195 MiB | 8 |
| `postsonde-linux-gtk3-amd64` (GTK3, WebKit2GTK 4.1) | **280 ms** (253–292) | 224 ms | **228 MiB** | 456 MiB | 179 MiB | 3 |

- **Start** is well under the 1 s target on this (fast) machine. Most of
  it is GTK and WebKit starting their processes; the app's own count, from
  its Go package init, is about 50 ms less than the time from the exec
  (dynamic loading of the GTK and WebKit libraries comes first).
- **Memory** is mostly WebKit: the main process (Go, GTK, the UI process
  side of WebKit) is about 190 MiB resident, the web content process and
  the network process add the rest. RSS counts every shared library once
  per process, so the all-processes RSS overstates what the app costs;
  PSS splits shared pages between the processes that map them and is the
  better number. The gtk4 build has 8 processes because WebKitGTK 6.0
  always sandboxes its web and network processes with bubblewrap (`bwrap`,
  plus `xdg-dbus-proxy`); the small sandbox processes cost little PSS.
- The arm64 builds were not measured (no arm64 Linux machine with a
  desktop at hand).

### macOS and Windows

Not measured yet; to be done by the owner on real machines, with the
[manual method](#on-macos-and-windows) below, and added here.

| OS | Binary | Start | Idle memory |
|---|---|---|---|
| macOS | `PostSonde.app` (universal) | not measured | not measured |
| Windows | `postsonde-windows-amd64.exe` | not measured | not measured |
| Windows | `postsonde-windows-arm64.exe` | not measured | not measured |

### Sizes

Measured on 2026-11-08 (commit `94bed909`, Go 1.27, the CI images; the
packages at version `0.0.0~git...`). The budget is what `size:budgets`
enforces.

| File | Size | Budget |
|---|---|---|
| `postsonde-linux-gtk4-amd64` | 29.8 MiB | 33 MiB |
| `postsonde-linux-gtk4-arm64` | 27.3 MiB | 30 MiB |
| `postsonde-linux-gtk3-amd64` | 29.8 MiB | 33 MiB |
| `postsonde-linux-gtk3-arm64` | 27.2 MiB | 30 MiB |
| `postsonde-windows-amd64.exe` | 30.5 MiB | 34 MiB |
| `postsonde-windows-arm64.exe` | 27.6 MiB | 31 MiB |
| `postsonde_<version>_amd64.deb` | 11.9 MiB | 14 MiB |
| `postsonde_<version>_arm64.deb` | 10.8 MiB | 12 MiB |
| `postsonde-gtk3_<version>_amd64.deb` | 11.9 MiB | 14 MiB |
| `postsonde-gtk3_<version>_arm64.deb` | 10.8 MiB | 12 MiB |
| `PostSonde-<version>-x86_64.AppImage` | 84.8 MiB | 94 MiB |
| `PostSonde-<version>-aarch64.AppImage` | not built locally | 94 MiB |
| `postsonde-<version>-1.fc44.x86_64.rpm` | 9.3 MiB | 11 MiB |
| `postsonde-<version>-1.fc45.x86_64.rpm` | 9.3 MiB | 11 MiB |
| `postsonde-<version>-1.fc<44\|45>.aarch64.rpm` | not built locally | 11 MiB |
| `PostSonde-<version>-x86_64.flatpak` | 8.7 MiB | 10 MiB |
| `PostSonde-<version>-windows-amd64-setup.exe` | 8.9 MiB | 10 MiB |
| `PostSonde-<version>-windows-arm64-setup.exe` | 7.6 MiB | 9 MiB |
| macOS `.dmg` | not measured | none yet: not checked |

The binaries are about 30 MiB: Go, the embedded frontend (CodeMirror and
the UI, a few MiB) and Wails; no browser engine. The `.deb`s are
compressed and depend on the distribution's GTK and WebKit; the AppImage
bundles GTK 3 and WebKit2GTK, hence its size. The RPM and the Flatpak were
measured on 2026-11-09, from `make rpm` (Fedora 44's and 45's libraries)
and `make flatpak` (the GNOME 51 SDK); like the `.deb`s they leave GTK and
WebKit to the system or the runtime. The aarch64 RPMs' budget is the
x86_64 one.

The gtk4 arm64 binary and `.deb` were measured later the same day, at
`5bd257a7` plus the fix to their cross build: 28,586,512 and
11,338,096 bytes. Before that fix they couldn't be cross-built on an amd64
machine ([ci.md, arm64](ci.md#arm64-cross-compiling)), and their budgets
had been set from gtk3; the measured sizes leave them as they were. The
aarch64 AppImage, still "not built locally", needs arm64 emulation
(qemu/binfmt) on an amd64 machine, which the machine above didn't have;
its budget is the x86_64 one and should be checked against the first CI
run.

## Budgets

| | Budget | Enforced |
|---|---|---|
| Size of every binary and package | the table above | **CI**, `size:budgets`: fails when a file is over its budget, has none, or is missing |
| Start (exec to first window ready) | **1 s** (APP-05), on a mid-range machine | by hand, `scripts/measure-footprint.sh`; CI has no display |
| Idle memory, PSS of all processes | **300 MiB** on Linux | by hand, as above |

How the size budgets were picked: the measured size **plus about 10%,
rounded up to a whole MiB**. That leaves room for normal growth over a few
features, but a jump (a new dependency, an accidentally embedded file, a
debug build) fails the pipeline. A target that couldn't be built locally
got the budget of its sibling.

**Raising a budget** is a decision, not a fix for a red pipeline: find out
what grew first (`go tool nm -size -sort size`, or compare the
`frontend/dist` sizes), and if the growth is worth it, raise the budget in
its own commit that says why, and update the table above. The same goes
for adding a budget for a new artifact (the macOS `.dmg`, once a release
run of `macos:dmg` has measured it).

The start and memory budgets are not checked in CI: the jobs have no
display, and timings on shared runners vary too much to fail a pipeline
on. Re-measure before a release and after changes to startup (services,
the first page, Wails upgrades).

## Method

### Size

`scripts/check-size.sh <file>...` prints each file's size, its budget and
the share used:

```sh
scripts/check-size.sh bin/postsonde-* dist/*.deb dist/*.AppImage dist/*-setup.exe   # as in CI
scripts/check-size.sh dist/linux/postsonde* dist/linux/*.AppImage                   # after make linux
scripts/check-size.sh dist/windows/*.exe                                            # after make windows
```

Sizes are bytes on disk (`wc -c`); the budgets are in MiB.

### Start time and idle memory on Linux

```sh
scripts/measure-footprint.sh -n 7 bin/postsonde-linux-gtk4-amd64
```

It runs in the current graphical session (Wayland or X11) and, for each
run:

1. creates an empty `POSTSONDE_HOME`, so the run starts like a first
   start, with no settings or project, and doesn't touch your own
   PostSonde (other directories, so another single-instance lock);
2. drops the binary from the page cache (`dd iflag=nocache count=0`);
   the GTK and WebKit libraries stay cached, since evicting them needs
   root, so this is a start of an app that isn't in memory on a system
   that has run other GTK apps, not a start right after boot;
3. starts the binary and polls its log every 10 ms until the `first
   window ready` record appears: **start** is the time from the exec to
   that record. The record is written when the first window's page has
   loaded the Wails runtime (Wails' `WindowRuntimeReady` event): the
   page's script has run and the UI renders next. Its `startupMs` field,
   the app's own count from its Go package init, is shown as well;
4. waits 10 s (`-i`) on the welcome screen, then sums `VmRSS` (from
   `/proc/<pid>/status`) and `Pss` (`/proc/<pid>/smaps_rollup`) over the
   process and all its descendants (the WebKit processes, and with GTK4
   the bubblewrap sandbox);
5. sends SIGTERM and waits for the app to quit.

It prints every run and the medians. Close other heavy programs first;
the numbers above were taken on an otherwise idle desktop.

### On macOS and Windows

The script is Linux-only (it reads `/proc`). By hand:

- **Start**: start PostSonde with `POSTSONDE_HOME` set to an empty folder
  and read `startupMs` from the `first window ready` record in
  `$POSTSONDE_HOME/logs/postsonde.log`. That count starts at the Go
  package init, so it misses the time the OS takes to load the
  executable; take the median of several starts, after one start to warm
  the caches. On macOS, start the binary inside the bundle
  (`PostSonde.app/Contents/MacOS/postsonde`) from a terminal with the
  variable set; on Windows, `set POSTSONDE_HOME=...` in `cmd` first.
- **Idle memory** after 10 s on the welcome screen:
  - macOS: Activity Monitor, Memory column, for PostSonde plus its WebKit
    processes (`com.apple.WebKit.WebContent` and `.Networking`; recent
    macOS versions name them after the app, "... Web Content"), or
    `footprint -p <pid>` for each. The WebKit processes are XPC services,
    not children of the app.
  - Windows: Task Manager, Details tab, column "Memory (private working
    set)", for `postsonde-windows-<arch>.exe` plus its
    `msedgewebview2.exe` processes (the WebView2 browser, GPU, renderer
    and utility processes).

Add the results to the tables above, with the date, commit, OS version and
machine.
