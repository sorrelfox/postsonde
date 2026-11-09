# Third-party notices

PostSonde is released under the [MIT License](LICENSE). The shipped app also
contains third-party components, listed below with their licenses. Each of
these licenses requires its copyright notice and license text to go with any
copy of the component. This file, together with the license files named
below, does that.

Licenses were checked in September 2026 against each project's own license
file and its package registry entry (npm, the Go module proxy, pkg.go.dev).

**Status:** the Go standard library, the Wails shell, React, the Wails
runtime, the fonts, CodeMirror 6 with Lezer, the Protocol Buffers
well-known type sources, `google/rpc/status.proto` and
`google/rpc/error_details.proto`
are bundled today, at the versions pinned in `go.mod` and `frontend/package-lock.json`.

## Frontend runtime (bundled into the app's web assets)

| Component | Version | License | Copyright | Full license text |
|---|---|---|---|---|
| `react` | 18.3.1 | MIT | Meta Platforms, Inc. and affiliates | `node_modules/react/LICENSE`; <https://github.com/facebook/react/blob/main/LICENSE> |
| `react-dom` | 18.3.1 | MIT | Meta Platforms, Inc. and affiliates | `node_modules/react-dom/LICENSE` |
| `scheduler` (dependency of `react-dom`) | 0.23.2 | MIT | Meta Platforms, Inc. and affiliates | `node_modules/scheduler/LICENSE` |
| `@codemirror/state` | 6.7.5 | MIT | 2018-2021 Marijn Haverbeke and others | `node_modules/@codemirror/state/LICENSE`; <https://github.com/codemirror/dev/blob/main/LICENSE> |
| `@codemirror/view` | 6.43.12 | MIT | 2018-2021 Marijn Haverbeke and others | `node_modules/@codemirror/view/LICENSE` |
| `@codemirror/language` | 6.12.4 | MIT | 2018-2021 Marijn Haverbeke and others | `node_modules/@codemirror/language/LICENSE` |
| `@codemirror/commands` | 6.11.1 | MIT | 2018-2021 Marijn Haverbeke and others | `node_modules/@codemirror/commands/LICENSE` |
| `@codemirror/search` | 6.7.2 | MIT | 2018-2021 Marijn Haverbeke and others | `node_modules/@codemirror/search/LICENSE` |
| `@codemirror/autocomplete` | 6.20.3 | MIT | 2018-2021 Marijn Haverbeke and others | `node_modules/@codemirror/autocomplete/LICENSE` |
| `@codemirror/lang-json` | 6.0.2 | MIT | 2018-2021 Marijn Haverbeke and others | `node_modules/@codemirror/lang-json/LICENSE` |
| `@lezer/common` | 1.5.2 | MIT | 2018 Marijn Haverbeke and others | `node_modules/@lezer/common/LICENSE`; <https://github.com/lezer-parser/common/blob/main/LICENSE> |
| `@lezer/lr` | 1.4.10 | MIT | 2018 Marijn Haverbeke and others | `node_modules/@lezer/lr/LICENSE` |
| `@lezer/highlight` | 1.2.3 | MIT | 2018 Marijn Haverbeke and others | `node_modules/@lezer/highlight/LICENSE` |
| `@lezer/json` (dependency of `@codemirror/lang-json`) | 1.0.3 | MIT | 2020 Marijn Haverbeke, Arun Srinivasan and others | `node_modules/@lezer/json/LICENSE` |
| `style-mod` (dependency of CodeMirror) | 4.1.3 | MIT | 2018 Marijn Haverbeke and others | `node_modules/style-mod/LICENSE` |
| `w3c-keyname` (dependency of CodeMirror) | 2.2.8 | MIT | 2016 Marijn Haverbeke and others | `node_modules/w3c-keyname/LICENSE` |
| `crelt` (dependency of CodeMirror) | 1.0.7 | MIT | 2020 Marijn Haverbeke | `node_modules/crelt/LICENSE` |
| `@marijn/find-cluster-break` (dependency of `@codemirror/state`) | 1.0.4 | MIT | 2024 Marijn Haverbeke | `node_modules/@marijn/find-cluster-break/LICENSE` |
| Wails runtime: `@wailsio/runtime` | 3.0.0 | MIT | 2018-present Lea Anthony (The Wails Team) | `node_modules/@wailsio/runtime/LICENSE`; <https://github.com/wailsapp/wails/blob/master/LICENSE> |

## Go modules (compiled into the binary)

| Component | Version checked | License | Copyright | Full license text |
|---|---|---|---|---|
| Go standard library and runtime | 1.27 | BSD-3-Clause | 2009 The Go Authors | `$(go env GOROOT)/LICENSE`; <https://go.dev/LICENSE> |
| `github.com/wailsapp/wails/v3` | v3.0.0 | MIT | 2018-present Lea Anthony | module cache `github.com/wailsapp/wails/v3@<version>/LICENSE` |

Wails pulls in further Go modules. Its `go.mod` also covers the Wails CLI and
packaging tools, which are not linked into the app, so the list below is read
from a real build with `go version -m <binary>` (the `dep` lines). It differs
per OS, so regenerate it whenever `go.mod` changes and from each platform's CI
build.

Linked into the macOS (darwin/arm64) binary, generated 2026-09-15; versions
updated for Wails v3.0.0, whose `go list -deps` lists the same modules:

| Module | Version | License | Copyright |
|---|---|---|---|
| `github.com/wailsapp/wails/v3` | v3.0.0 | MIT | 2018-present Lea Anthony |
| `github.com/adrg/xdg` | v0.5.3 | MIT | 2014 Adrian-George Bostan |
| `github.com/mattn/go-isatty` | v0.0.20 | MIT | Yasuhiro MATSUMOTO |
| `golang.org/x/sys` | v0.48.0 | BSD-3-Clause | 2009 The Go Authors |

Expected in addition on other platforms, to confirm from their builds:

| Module | Used on | License | Copyright |
|---|---|---|---|
| `golang.org/x/net` | Linux, Windows | BSD-3-Clause | 2009 The Go Authors |
| `github.com/godbus/dbus/v5` | Linux | BSD-2-Clause | 2013 Georg Reinke, Google |
| `github.com/go-ole/go-ole` | Windows | MIT | 2013-2017 Yasuhiro Matsumoto |

All of these are permissive licenses and compatible with MIT. None of them
is copyleft.

## Protocol Buffers well-known types (compiled into the binary)

**Bundled.** The gRPC client resolves imports of `google/protobuf/*.proto`
without a protobuf installation, as protoc does, so the Go binary embeds
these `.proto` sources (`internal/core/grpc/proto/wellknown`, via
`go:embed`). They are unmodified and each keeps its license header, which is
the full license text, so the notice ships inside the binary with them.

| Files | Source | Version | License | Copyright | Full license text |
|---|---|---|---|---|---|
| `internal/core/grpc/proto/wellknown/google/protobuf/`: `any.proto`, `api.proto`, `descriptor.proto`, `duration.proto`, `empty.proto`, `field_mask.proto`, `source_context.proto`, `struct.proto`, `timestamp.proto`, `type.proto`, `wrappers.proto` | Protocol Buffers, <https://github.com/protocolbuffers/protobuf> (`src/google/protobuf/`), copied from `com.google.protobuf:protobuf-java:4.34.0` (Maven Central) | v34.0 | BSD-3-Clause | 2008 Google Inc. | each file's own header |

Checksums are listed in `docs/proto.md` (Well-known types). BSD-3-Clause is
permissive and compatible with MIT; release packaging needs nothing beyond
this file, because the notice is inside the embedded files.

## gRPC status and error detail messages (compiled into the binary)

**Bundled.** `google/rpc/status.proto` and
`google/rpc/error_details.proto` are embedded next to the well-known
types, so that `google.rpc.Status` and its standard error details
(`ErrorInfo`, `BadRequest`, `RetryInfo`, ...) decode without the googleapis
tree. They are unmodified and keep their license headers.

| Files | Source | Version | License | Copyright | Full license text |
|---|---|---|---|---|---|
| `internal/core/grpc/proto/wellknown/google/rpc/status.proto`, `error_details.proto` | googleapis, <https://github.com/googleapis/googleapis> (`google/rpc/status.proto`, `google/rpc/error_details.proto`), copied from `com.google.api.grpc:proto-google-common-protos:2.63.2` (Maven Central) | 2.63.2 | Apache-2.0 | 2025 Google LLC | `internal/core/grpc/proto/wellknown/google/rpc/LICENSE` (embedded in the binary; copied from googleapis at commit `ebd1d23ac613b177828dad42ad8dfb13ba498279`); <https://www.apache.org/licenses/LICENSE-2.0> |

Apache-2.0 is permissive and compatible with MIT. It requires a copy of the
license with every distribution, which the embedded `LICENSE` file and this
notice provide; googleapis has no `NOTICE` file, so there is nothing further
to reproduce. The files were not modified (Apache-2.0 section 4(b) would
otherwise require marking changes). Checksums are in `docs/proto.md`.

## Fonts (bundled with the frontend)

**Bundled.** Both fonts ship as local woff2 files. They are loaded by
`frontend/src/styles/fonts.css`, and nothing is fetched from the network.

| Font | Version | Weights | Source | License | Copyright | Full license text |
|---|---|---|---|---|---|---|
| Instrument Sans | 1.000 (upstream commit `7fa22308a3d0c94ee2b3cd537a1196b65db34a3e`; the repo has no release tags) | 400, 500, 600 | <https://github.com/Instrument/instrument-sans> (`fonts/webfonts/`) | SIL Open Font License 1.1 (OFL-1.1) | 2022 The Instrument Sans Project Authors | `frontend/src/assets/fonts/instrument-sans/OFL.txt` |
| JetBrains Mono | 2.304 (release tag `v2.304`) | 400, 500 | <https://github.com/JetBrains/JetBrainsMono/releases/tag/v2.304> (`fonts/webfonts/` in the release zip) | SIL Open Font License 1.1 (OFL-1.1) | 2020 The JetBrains Mono Project Authors | `frontend/src/assets/fonts/jetbrains-mono/OFL.txt` |

Each font folder has a `SOURCE.md` with the exact download URLs and the file
checksums.

The OFL lets fonts be bundled with software under any license, as long as
the OFL text ships with the fonts and the fonts are not sold on their own.
Each font's `OFL.txt` is kept next to its font files. Vite only emits the
woff2 files into `dist/`, so release packaging must copy these two `OFL.txt`
files into the app bundle and installers, together with the other license
texts (see below). We ship the fonts unmodified, under their original names.

## Windows installer (NSIS)

The Windows installer `PostSonde-<version>-windows-<arch>-setup.exe` and
the `uninstall.exe` it writes are NSIS executables: they contain the NSIS
installer stub, its LZMA decompressor and the `nsDialogs` and `System`
plugins, from Debian's `nsis` 3.11-1 package. They are not part of
`PostSonde.exe`. The installer copies `LICENSE`, this file and both
`OFL.txt` files into `licenses\` next to `PostSonde.exe`.

| Component | Version | License | Copyright | Source |
|---|---|---|---|---|
| NSIS (stub, plugins) | 3.11 | zlib/libpng | 1999-2025 Nullsoft and Contributors | <https://nsis.sourceforge.io> |
| LZMA decompressor in the NSIS stub | as in NSIS 3.11 | CPL-1.0 with the NSIS special exception for linking | 1999-2006 Igor Pavlov, 2003-2021 Amir Szekely | <https://nsis.sourceforge.io> |

Neither license asks for a notice in the installer itself; the source of
both is on the NSIS site.

## Not listed: development-only tooling

Tools used only to build and test PostSonde are not part of the shipped app
and are not listed here: Vite, TypeScript, test runners, linters, the Wails
CLI and its packaging tools, and Go test-only modules. Their output (the
bundled JavaScript) contains only the runtime packages listed above.

## Test data (in the repository, not shipped)

Some tests use unmodified third-party files as input. They live under
`testdata/` directories, which Go never compiles into a binary, and are not
part of the shipped app. They are listed here because the source repository
redistributes them. Each file keeps its license header, and the full
license text sits next to it where the header only refers to it.

| Files | Source | Version | License | Copyright | Full license text |
|---|---|---|---|---|---|
| `internal/core/grpc/proto/parser/testdata/realworld/protobuf/descriptor.proto` | Protocol Buffers, <https://github.com/protocolbuffers/protobuf> (copy from `com.google.protobuf:protobuf-java`) | v34.0 (protobuf-java 4.34.0) | BSD-3-Clause | 2008 Google Inc. | the file's own header |
| `internal/core/grpc/proto/parser/testdata/realworld/protobuf-go/proto2/fields.proto`, `.../protobuf-go/textpb2/test.proto` | `google.golang.org/protobuf` | v1.36.11 | BSD-3-Clause | 2018, 2019 The Go Authors | `internal/core/grpc/proto/parser/testdata/realworld/protobuf-go/LICENSE` |
| `internal/core/jsonpath/testdata/cts/cts.json` | JSONPath Compliance Test Suite, <https://github.com/jsonpath-standard/jsonpath-compliance-test-suite> | commit `afefcaa256fb9ed224e9dbffb28ec2262182e498` | BSD-2-Clause | the jsonpath-compliance-test-suite project contributors (the license names no holder) | `internal/core/jsonpath/testdata/cts/LICENSE` |
| `internal/core/jsonschema/testdata/JSON-Schema-Test-Suite/tests/{draft2020-12,draft7}/`, `.../remotes/` | JSON Schema Test Suite, <https://github.com/json-schema-org/JSON-Schema-Test-Suite> | commit `245a45abc90190c0bff04d97e6d2b541b4bb9f12` | MIT | 2012 Julian Berman | `internal/core/jsonschema/testdata/JSON-Schema-Test-Suite/LICENSE` |

Source paths and checksums are in
`internal/core/grpc/proto/parser/testdata/realworld/README.md`,
`internal/core/jsonpath/testdata/cts/README.md` and
`internal/core/jsonschema/testdata/JSON-Schema-Test-Suite/README.md`.

## Keeping this file up to date

- Every new dependency that ends up in the shipped app (an npm runtime
  package, a Go module linked into the binary, a font or other asset) adds
  an entry here in the same change.
- A dependency must use a license compatible with MIT distribution
  (MIT, BSD, ISC, Apache-2.0, OFL for fonts, and similar). Copyleft licenses
  (GPL, AGPL, LGPL for statically linked code) need a separate decision.
- When versions are pinned or upgraded, check that the license hasn't
  changed, and update the version column.
- Release packaging copies the license texts of the bundled components into
  the app bundle and installers.
