# YAML reader

`internal/core/yaml` reads the YAML that importers meet (Insomnia v5
exports, IMP-04; OpenAPI and Swagger documents, IMP-05) without a YAML
library. It parses into a tree with positions and converts that tree to
JSON, so an importer decodes YAML and JSON files with the same
`encoding/json` structs.

| Piece | Where |
|---|---|
| Tree, kinds, styles, tags, limits | `yaml/node.go` |
| Parser (recursive descent over the text) | `yaml/parse.go` |
| Core schema, JSON conversion, `Decode`, `Unmarshal` | `yaml/json.go` |
| Errors and codes | `yaml/errors.go` |
| Insomnia v5 and OpenAPI fixtures with JSON equivalents | `yaml/testdata/` |

## Use

```go
var export insomniaV5                       // json-tagged structs
err := yaml.Unmarshal(data, &export)        // Parse + JSON + json.Unmarshal
// err: *yaml.Error for YAML problems (yaml.AsErr gives the errs form),
//      encoding/json errors for type mismatches

root, err := yaml.Parse(data)               // one document; ParseAll for "---" streams
root.Get("info").Get("title").Value         // walk the tree; Line and Column on every node
js, err := root.JSON()                      // compact JSON, keys in document order
v, err := root.Decode()                     // map[string]any, []any, json.Number, ...
docs, err := yaml.ParseAllLimits(data, yaml.Limits{MaxBytes: 8 << 20})
```

- **Tree:** `Node` has `Kind` (scalar, mapping, sequence, alias), `Style`
  (plain, quoted, literal, folded, flow), `Tag` (`!!null`, `!!bool`,
  `!!int`, `!!float`, `!!str`, `!!map`, `!!seq`), `Value` (scalar text
  after escapes and folding), `Anchor`, `Pairs` (mapping entries in
  order), `Items`, `Target` (an alias's node) and 1-based `Line` and
  `Column` (characters) of the content. `Resolve` follows aliases, `Get`
  finds a mapping entry by key text.
- **JSON:** aliases are expanded, merge keys applied, scalar keys become
  strings (`200:` and `'200':` are both `"200"`, and duplicates after that
  are errors). Numbers keep their text when it is a valid JSON number and
  are normalized otherwise (`0x1F` is `31`, `+1` is `1`, `.5` is `0.5`),
  so large integers are not rounded. HTML characters are not escaped.

## Supported YAML

| Construct | Notes |
|---|---|
| Block mappings and sequences | compact `- key: value` entries, `- - x`, sequences at their key's indentation |
| Flow mappings and sequences | over several lines, trailing commas, `[a: b]` single pairs, `{a, b}` keys without values, JSON |
| Plain scalars | over several lines (folded), `:` and `#` inside words (`http://x:80/a#f`) |
| Quoted scalars | `'...'` with `''`; `"..."` with every YAML escape (`\t \n \x41 é \U0001F600 \N \_ \L \P`, escaped line breaks); folding of line breaks |
| Block scalars | `\|` and `>` with `-`/`+` chomping and 1-9 indentation indicators, more-indented lines in folded scalars |
| Comments | full-line and after values (a `#` must follow a space) |
| Anchors and aliases | on any node; an alias refers to the latest complete node with that anchor; aliases as keys (`*k : v`) |
| Merge keys | `<<: *base` and `<<: [*a, *b]`; explicit keys win, earlier merged mappings win over later ones |
| Tags | `!!str !!int !!float !!bool !!null !!map !!seq`, `!!binary` and `!!timestamp` (kept as strings), `!` (string), `!<tag:yaml.org,2002:...>` |
| Documents | `---`, `...`, `%YAML 1.x`, several documents (`ParseAll`), UTF-8 with or without a BOM, CRLF |

Plain scalars are typed with the **YAML 1.2 core schema**:

| Text | Type |
|---|---|
| empty, `~`, `null`, `Null`, `NULL` | null |
| `true`, `True`, `TRUE`, `false`, `False`, `FALSE` | boolean |
| `[-+]?[0-9]+`, `0o[0-7]+`, `0x[0-9a-fA-F]+` | integer (`010` is 10) |
| `1.5`, `.5`, `1e3`, `-1.5E+3`; `.inf`, `.nan` | float (`.inf`/`.nan` cannot become JSON) |
| anything else | string, including YAML 1.1's `yes`, `no`, `on`, `off`, `1_000`, `12:30` and timestamps |

## Errors

Every error is a `*yaml.Error` with `Code`, `Message`, `Line` and `Column`
(0:0 only for whole-input problems: size, encoding). `Error()` reads
`yaml: line 3, column 7: unknown anchor "base" ...`; `AsErr` converts it:

| Code | errs kind | When |
|---|---|---|
| `yaml.invalid_yaml` | `parse` | not valid YAML: bad indentation, tabs used for indentation, `key: - a`, `a: b: c`, unterminated quotes or flow collections, bad escapes, unknown aliases, duplicate keys, a `!!int` that is not an integer, control characters, invalid UTF-8 |
| `yaml.unsupported` | `unsupported` | explicit `? key` entries, collections or empty values as keys, custom and local tags (`!Ref`, `!foo`), `!!set`/`!!omap`, `%TAG`, YAML 2, UTF-16/32, `.inf`/`.nan` in JSON, several documents given to `Parse` |
| `yaml.too_large` | `unsupported` | over `Limits`: `MaxBytes` (32 MiB), `MaxDepth` (1000 levels), `MaxNodes` (2,000,000, counted again after alias expansion, which stops "billion laughs") |

Unsupported errors carry a hint to convert the file to JSON. Compared with
the YAML 1.2 spec the reader is slightly stricter where real files never go
(it wants a space before `#` after a quoted value and rejects duplicate
keys) and slightly more lenient on the indentation of quoted and flow
continuation lines.

## Tests

`yaml_test.go` has table tests for every construct above, every error
code with its position, the tree API, limits and `ParseAll`.
`TestRealWorldFixtures` checks that an Insomnia v5 export and an OpenAPI
3.0 document parse into the same data as their JSON equivalents in
`testdata/`. The JSON files were produced with PyYAML set to the 1.2 core
schema (PyYAML defaults to YAML 1.1, which types `1.5e3` and timestamps
differently); regenerate them the same way if a fixture changes.
`FuzzParse` checks that no input panics, that errors carry positions and
that parsed trees convert to valid JSON:

```sh
go test ./internal/core/yaml -run XXX -fuzz FuzzParse -fuzztime 60s
```
