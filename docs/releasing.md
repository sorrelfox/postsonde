# Releasing

A release is a GitHub Release with every package, the source tarball and
`SHA256SUMS`. The release workflow,
[`.github/workflows/release.yml`](../.github/workflows/release.yml), builds
and tests all of it from a tag and leaves it as a **draft**; publishing it
is done by hand. CI never publishes a release, and nothing goes to a
package repository or registry: the release's files are the distribution.
What the workflow's jobs do: [ci.md](ci.md#releaseyml).

## Making a release

1. **Set the version.** `info.version` in
   [`build/config.yml`](../build/config.yml) is the release version,
   `X.Y.Z`. Change it in a commit of its own on the branch you release from.
2. **Try it first (optional).** Actions › Release › Run workflow, on that
   branch: the workflow builds and tests every package without releasing,
   at the version `0.0.0~git<commit>`. The packages are the run's artifacts.
3. **Tag the commit and push the tag:**

   ```sh
   git tag -s vX.Y.Z -m "PostSonde X.Y.Z"
   git push origin vX.Y.Z
   ```

   The workflow's first step checks that the tag is `vX.Y.Z` for the
   version in `build/config.yml` and stops otherwise
   (`scripts/check-release-tag.sh`): fix the version, commit, and tag that
   commit.
4. **Review the draft.** When every job has passed, the draft release
   holds the files below. Its notes are
   [`packaging/release-notes.md`](../packaging/release-notes.md) (which file
   to download, how to check it, the first start), followed by GitHub's
   generated notes since the last release. Edit them as needed, try a
   package or two, then **Publish release**. CI starts the macOS app on
   Intel only, so open the `.dmg` on an Apple silicon Mac before
   publishing.

If a job fails, nothing is released: fix the cause and push a new commit
and tag, or delete the tag and push it again on the fixed commit, before
anything was published.

## What a release holds

| File | Built by |
|---|---|
| `postsonde_X.Y.Z_amd64.deb`, `_arm64.deb` (gtk4; Ubuntu 24.04+, Debian 13+) | `linux:deb` ([packaging.md](packaging.md#deb-packages)) |
| `postsonde-gtk3_X.Y.Z_amd64.deb`, `_arm64.deb` (gtk3; Ubuntu 22.04+, Debian 12+) | `linux:deb` |
| `postsonde-X.Y.Z-1.fc44.x86_64.rpm`, `.aarch64.rpm` (Fedora 44) | `linux:rpm:fc44:<arch>` ([packaging.md](packaging.md#rpm)) |
| `postsonde-X.Y.Z-1.fc45.x86_64.rpm`, `.aarch64.rpm` (Fedora 45) | `linux:rpm:fc45:<arch>` |
| `postsonde-X.Y.Z-1.fc45.src.rpm`, the one source RPM | `linux:rpm:fc45:amd64` |
| `PostSonde-X.Y.Z-x86_64.AppImage`, `-aarch64.AppImage` | `linux:appimage:<arch>` ([packaging.md](packaging.md#appimage)) |
| `PostSonde-X.Y.Z-x86_64.flatpak` | `linux:flatpak` ([packaging.md](packaging.md#flatpak)) |
| `PostSonde-X.Y.Z-macos-universal.dmg` | `macos:dmg` ([packaging-macos.md](packaging-macos.md)) |
| `PostSonde-X.Y.Z-windows-amd64-setup.exe`, `-arm64-setup.exe` | `windows:installer` ([packaging-windows.md](packaging-windows.md)) |
| `BUILDINFO`: the toolchain the disk image was built with, for [rebuilding it](reproducible-builds.md#macos) | `macos:dmg` |
| `postsonde-X.Y.Z.tar.xz`: the tree, the frontend build and the vendored Go modules | `sources` |
| `SHA256SUMS`: every file above | the release job |

Every file but `SHA256SUMS` has a build provenance attestation, made by the
release job with GitHub's signing service: it records that the file was
built by this repository's release workflow from the tagged commit.

## Checking a release

```sh
sha256sum -c --ignore-missing SHA256SUMS
gh attestation verify PostSonde-X.Y.Z-x86_64.AppImage --repo sorrelfox/postsonde
```

The files are reproducible: [reproducible-builds.md](reproducible-builds.md)
has how to rebuild them and compare. The release workflow does that once
itself for the Linux amd64 files (the `reproducible` job).

## Signing

Signing is optional. The macOS and Windows jobs read the repository
secrets below (Settings › Secrets and variables › Actions); without them
they say so in their log and go on: the macOS app is signed ad hoc, and
the Windows installers stay unsigned.

| Secret | Job | Holds |
|---|---|---|
| `POSTSONDE_MACOS_CERTIFICATE` | `macos:dmg` | a Developer ID Application certificate with its private key, as a `.p12` file, base64-encoded |
| `POSTSONDE_MACOS_CERTIFICATE_PASSWORD` | `macos:dmg` | the `.p12` file's password |
| `POSTSONDE_MACOS_SIGN_IDENTITY` | `macos:dmg` | the certificate's name as `codesign` takes it, `Developer ID Application: <name> (<team ID>)` |
| `POSTSONDE_MACOS_NOTARY_APPLE_ID` | `macos:dmg` | the Apple ID that notarizes |
| `POSTSONDE_MACOS_NOTARY_TEAM_ID` | `macos:dmg` | its team ID |
| `POSTSONDE_MACOS_NOTARY_PASSWORD` | `macos:dmg` | an app-specific password of that Apple ID |
| `POSTSONDE_WINDOWS_CERTIFICATE` | `windows:installer` | a code-signing certificate with its private key, as a `.pfx` file, base64-encoded |
| `POSTSONDE_WINDOWS_CERTIFICATE_PASSWORD` | `windows:installer` | the `.pfx` file's password |

```sh
base64 -i developer-id.p12 | gh secret set POSTSONDE_MACOS_CERTIFICATE        # macOS
base64 -w0 codesign.pfx | gh secret set POSTSONDE_WINDOWS_CERTIFICATE          # Linux
```

- **macOS** ([`scripts/package-macos.sh`](../scripts/package-macos.sh)):
  with the certificate, the app is signed with the Developer ID and the
  hardened runtime, and so is the disk image. With the notary secrets too,
  the disk image is notarized and its ticket stapled: macOS then opens the
  app without the first-launch steps of
  [packaging-macos.md](packaging-macos.md#installing-for-users).
- **Windows** ([`scripts/sign-windows.sh`](../scripts/sign-windows.sh),
  osslsigncode in the Linux job): with the certificate, `PostSonde.exe` is
  signed before it is packed, then the uninstaller and the installer as
  makensis writes them (`!uninstfinalize`, `!finalize` in
  `build/windows/nsis/project.nsi`), each with a timestamp from DigiCert's
  server (`POSTSONDE_WINDOWS_TIMESTAMP_URL` names another).

A signature carries a timestamp, so signed `.dmg` and installer files are
no longer the same byte for byte from build to build; everything else
stays reproducible.
