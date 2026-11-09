# JSON-Schema-Test-Suite

Unmodified copy of part of the official JSON Schema Test Suite, used by
`TestOfficialSuite` in `suite_test.go` (#241) to check
`internal/core/jsonschema`. It is test data only: nothing here is compiled
into or shipped with PostSonde. Do not edit these files; replace them with
a newer upstream copy instead and update this file.

| | |
|---|---|
| Upstream | <https://github.com/json-schema-org/JSON-Schema-Test-Suite> |
| Commit | `245a45abc90190c0bff04d97e6d2b541b4bb9f12` (2026-11-03, "test: add missing RFC 3987 coverage to iri and iri-reference (#1249)") |
| License | MIT, Copyright (c) 2012 Julian Berman; full text in `LICENSE` (SHA-256 `837402bd25fad9b704265801ca3f92566a98157c1f9a7acd6f446299ba1c305a`) |
| Copied | `LICENSE`; `tests/draft2020-12/` and `tests/draft7/` in full (144 files); `remotes/` without its `draft3`, `draft4`, `draft6` and `v1` folders (53 files) |
| Checksum | `30cb7f5937fe15d0fb6e4ca7b151cdf23445a06529ab3532840ca0dc80f9f9ba`: the SHA-256 of the list of every file's SHA-256 (command below) |

| Directory | Files | Cases |
|---|---|---|
| `tests/draft2020-12` (required) | 46 | 1300 |
| `tests/draft2020-12/optional` | 34 | 1074 |
| `tests/draft7` (required) | 37 | 927 |
| `tests/draft7/optional` | 27 | 950 |

The test compiles each draft directory's schemas as that draft (2020-12
schemas may still pick another draft with `$schema`), turns format
assertion on for `optional/format/`, and gives the compiler every file of
`remotes/` as `Options.Resources` under the URI the suite expects,
`http://localhost:1234/<path under remotes>`. Nothing is fetched: the
documents are read from this directory. The cases the validator
deliberately does not pass are skipped by name in `officialSkips` in
`suite_test.go`, each with its reason (see "Conformance" in
`docs/jsonschema.md`); a skipped case that starts passing fails the test.

To update, from a clone of the upstream repository checked out at the new
commit (`$UP`), with `$HERE` this directory:

```sh
rm -rf "$HERE/tests" "$HERE/remotes" && mkdir -p "$HERE/tests" "$HERE/remotes"
cp "$UP/LICENSE" "$HERE/"
cp -r "$UP/tests/draft2020-12" "$UP/tests/draft7" "$HERE/tests/"
for d in "$UP"/remotes/*; do
  case "$(basename "$d")" in draft3|draft4|draft6|v1) ;; *) cp -r "$d" "$HERE/remotes/" ;; esac
done
cd "$HERE" && find . -type f ! -name README.md | LC_ALL=C sort | xargs shasum -a 256 | shasum -a 256
go test ./internal/core/jsonschema -run TestOfficialSuite -v | grep -E 'passed|skip'
```
