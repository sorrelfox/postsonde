# JSONPath Compliance Test Suite

Unmodified copy of the JSONPath Compliance Test Suite, used by
`cts_test.go` to check `internal/core/jsonpath` against RFC 9535.
It is test data only: nothing here is compiled into or shipped with
PostSonde. Do not edit these files; replace them with a newer upstream copy
instead and update this table.

| File here | Upstream source | Upstream path | License | SHA-256 |
|---|---|---|---|---|
| `cts.json` | <https://github.com/jsonpath-standard/jsonpath-compliance-test-suite>, commit `afefcaa256fb9ed224e9dbffb28ec2262182e498` (2026-09-06) | `cts.json` | BSD-2-Clause; full text in `LICENSE` | `f0932266a108d7b927f9a3fcc56e857f96c2bcd65c2acc25b70f3666b1dce7c3` |
| `LICENSE` | same commit | `LICENSE` | BSD-2-Clause license text | `0a76d5e15eeff92346a8783de64d5164c4d527a163f8599733e4e0ab941b59c0` |

`cts.json` has 704 cases: 247 invalid selectors that must fail to parse,
and 457 queries with a document and the expected values and normalized
paths (`result`/`result_paths`, or `results`/`results_paths` listing every
valid order when object member order makes several possible).

To update:

```sh
C=<commit>
curl -fsSLO https://raw.githubusercontent.com/jsonpath-standard/jsonpath-compliance-test-suite/$C/cts.json
curl -fsSLO https://raw.githubusercontent.com/jsonpath-standard/jsonpath-compliance-test-suite/$C/LICENSE
shasum -a 256 cts.json LICENSE
go test ./internal/core/jsonpath -run TestComplianceSuite -v | grep 'compliance suite'
```
