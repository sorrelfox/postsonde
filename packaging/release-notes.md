## Downloads

| For | File | Install |
|---|---|---|
| Ubuntu 24.04+, Debian 13+ (x64, ARM64) | `postsonde_<version>_amd64.deb`, `_arm64.deb` | `sudo apt install ./postsonde_<version>_amd64.deb` |
| Ubuntu 22.04, Debian 12 (x64, ARM64) | `postsonde-gtk3_<version>_amd64.deb`, `_arm64.deb` | `sudo apt install ./postsonde-gtk3_<version>_amd64.deb` |
| Fedora 44 (x64, ARM64) | `postsonde-<version>-1.fc44.x86_64.rpm`, `.aarch64.rpm` | `sudo dnf install ./postsonde-<version>-1.fc44.x86_64.rpm` |
| Fedora 45 (x64, ARM64) | `postsonde-<version>-1.fc45.x86_64.rpm`, `.aarch64.rpm` | `sudo dnf install ./postsonde-<version>-1.fc45.x86_64.rpm` |
| Linux with Flatpak (x64) | `PostSonde-<version>-x86_64.flatpak` | `flatpak install --user ./PostSonde-<version>-x86_64.flatpak` (the GNOME runtime comes from Flathub) |
| Linux with glibc 2.35+ (x64, ARM64) | `PostSonde-<version>-x86_64.AppImage`, `-aarch64.AppImage` | `chmod +x PostSonde-<version>-x86_64.AppImage`, then run it |
| macOS 13+ (Apple silicon and Intel) | `PostSonde-<version>-macos-universal.dmg` | open it and drag PostSonde to Applications |
| Windows 10 and 11 (x64, ARM64) | `PostSonde-<version>-windows-amd64-setup.exe`, `-arm64-setup.exe` | run it; no administrator rights needed |
| Sources | `postsonde-<version>.tar.xz` (with the frontend build and the Go modules), `postsonde-<version>-1.fc45.src.rpm` | |

## Check the download

`SHA256SUMS` lists every file's checksum: `sha256sum -c --ignore-missing SHA256SUMS`
(macOS: `shasum -a 256 -c --ignore-missing SHA256SUMS`). Each file also has a
build provenance attestation, which shows it was built by this repository's
release workflow: `gh attestation verify <file> --repo sorrelfox/postsonde`.

## First start on macOS and Windows

A macOS app that isn't notarized, or a Windows installer that isn't signed,
gets a warning the first time it is opened: on macOS, open **System Settings ›
Privacy & Security** and click **Open Anyway**; on Windows, click **More info ›
Run anyway**. The steps for each system:
[macOS](https://github.com/sorrelfox/postsonde/blob/HEAD/docs/packaging-macos.md#installing-for-users),
[Windows](https://github.com/sorrelfox/postsonde/blob/HEAD/docs/packaging-windows.md#installing-for-users),
[Linux](https://github.com/sorrelfox/postsonde/blob/HEAD/docs/packaging.md#installing-for-users).
