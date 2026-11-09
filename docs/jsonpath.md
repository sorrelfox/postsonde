# JSONPath

`internal/core/jsonpath` evaluates JSONPath queries ([RFC 9535]) on JSON
values. Response filtering (HTTP-36), assertions (TEST-02) and
variable extraction (TEST-05, [extraction.md](extraction.md)) all use it, so a query means the same
thing everywhere.

| Piece | Where |
|---|---|
| Parser, well-typedness checks | `parser.go`, `ast.go`, `functions.go` |
| Evaluator, comparisons, limits | `eval.go`, `decimal.go` |
| Value model and decoder | `value.go` |
| I-Regexp translation for `match()`/`search()` | `iregexp.go` |
| Normalized paths, API | `path.go`, `errors.go` |
| Compliance Test Suite (vendored) | `testdata/cts/` |

[RFC 9535]: https://www.rfc-editor.org/rfc/rfc9535
[RFC 9485]: https://www.rfc-editor.org/rfc/rfc9485

## Use

```go
nodes, err := jsonpath.QueryJSON(`$.store.book[?@.price < 10].title`, body)
// err: errs InvalidArgument jsonpath.invalid_query, Parse jsonpath.invalid_json,
//      or Unsupported jsonpath.too_complex
for _, n := range nodes {
    fmt.Println(n.Path, n.Value) // $['store']['book'][0]['title'] Sayings of the Century
}

// Parse once, run many times (Path is immutable and safe to share).
p, err := jsonpath.Parse(expr)          // err is *jsonpath.SyntaxError
doc, err := jsonpath.Decode(body)       // member order and number literals kept
nodes, err := p.Eval(ctx, doc, jsonpath.DefaultLimits)
nodes = p.Query(doc)                    // no limits: trusted, small input only
p.Singular()                            // at most one node (variable extraction)
```

- **`Decode`** builds `*jsonpath.Object` (members in document order),
  `[]any`, `json.Number` (the literal exactly as written), `string`,
  `bool` and `nil`. A repeated member name keeps its first position and
  its last value, like `JSON.parse`. `*Object` marshals back in order, so
  a filtered response can be shown with its numbers untouched
  (`1.50e+3` stays `1.50e+3`).
- `Query`/`Eval` also accept plain Go values: `map[string]any` (members
  visited in name order), `float64`, `int` and the other number types.
- **Results** are a `NodeList` of `Node{Path, Value}` in the order RFC 9535
  defines: arrays in index order, objects in member order as decoded,
  a descendant segment visits a node before its children.
  `NodeList.Values()` and `NodeList.Paths()` return plain slices.
- **Normalized paths** (§2.7): `NormalizedPath.String()` gives
  `$['store']['book'][0]`: brackets only, names single-quoted with the
  minimal escapes (`\'`, `\\`, `\b \f \n \r \t`, other control characters
  as a `\u` escape with lowercase hex). A normalized path is itself a query
  that selects exactly that node.

## What is supported

All of RFC 9535:

| Feature | Syntax |
|---|---|
| Root, current node | `$`, `@` |
| Name selector | `.name`, `['name']`, `["name"]` with the RFC's escapes (surrogate pairs included) |
| Wildcard | `.*`, `[*]` |
| Index | `[0]`, `[-1]` (range ±(2^53−1)) |
| Slice | `[start:end:step]`, the normative algorithm incl. negative steps and step 0 |
| Several selectors | `['a', 0, 1:3, ?@.x]` |
| Descendants | `..name`, `..*`, `..[...]` |
| Filter | `[?expr]` with `== != < <= > >=`, `&& \|\| !`, parentheses, existence tests (`?@.isbn`) |
| Literals | numbers (`-0`, `1.5e-3`), strings, `true`, `false`, `null` |
| Functions | `length()`, `count()`, `match()`, `search()`, `value()` |

Blank space (space, tab, LF, CR) is allowed where the RFC allows it and
nowhere else: `$ .a` and `$[ 1 : 2 ]` are valid, `$. a`, ` $` and `$ ` are
not.

### Comparisons

As §2.3.5.2.2 says:

- A singular query that selects nothing, and `value()` of zero or several
  nodes, is *Nothing*. Nothing `==` Nothing is true; Nothing compared with
  anything else is false for `==` and `<`.
- Numbers compare numerically, **exactly**: both sides are parsed as
  decimals (digits and exponent), never converted to `float64`. So
  `12345678901234567890 < 12345678901234567891`, `1 == 1.0 == 10e-1` and
  `-0 == 0`. Exponents beyond ±2^50 saturate. (RFC 9535 only requires
  I-JSON double precision; exact comparison agrees with it on every value
  a double represents exactly and avoids surprises on large IDs.)
- Strings compare by Unicode scalar values (UTF-8 byte order).
- Arrays and objects are `==` when deeply equal (object member order
  ignored); `<` on them is false.
- `<=` is `<` or `==`; `>` and `>=` swap the operands.

### Well-typedness

`Parse` checks the type rules of §2.4.3 and rejects, with a position:

- a literal used as a test (`$[?1]`), a non-singular query in a comparison
  (`$[?@.* == 1]`);
- a `ValueType` function used as a test (`$[?length(@)]`), a
  `LogicalType` function compared (`$[?match(@.a, 'x') == true]`);
- wrong arguments: `count(1)`, `length(@.*)`, `value(@.a == 1)`, a wrong
  argument count, an unknown function.

### `match()` and `search()`

Patterns are I-Regexp ([RFC 9485]). They are checked against the I-Regexp
grammar and translated to Go's RE2 syntax, then compiled; `match()` is
anchored at both ends (`^(?:…)$`), `search()` is not.

- Supported: literals, `.`, `|`, groups `( )`, `* + ?`, `{n}`, `{n,}`,
  `{n,m}`, classes `[...]`/`[^...]` with ranges, the single-character
  escapes (`\n \r \t` and escaped metacharacters) and `\p{..}`/`\P{..}`
  with the general categories I-Regexp lists (`L`, `Lu`, …, `Cn`).
- `.` matches any character except `\n` and `\r` (it matches U+2028).
- Rejected (the pattern then matches nothing, as the RFC says): `\d \w \s`,
  back-references, `(?:`, lazy quantifiers, `[]`, reversed ranges, block
  names (`\p{IsBasicLatin}`), `\p{Cs}`.

Limitations and deviations:

- `^` and `$` outside classes are **anchors** (start and end of the
  string). RFC 9485's grammar lists them as ordinary characters, but the
  Compliance Test Suite and the usual ECMAScript/PCRE mapping treat them as
  anchors, so users' patterns behave as they expect.
- Category tables are Go's (the Unicode version of the Go release), not
  necessarily the version an XSD engine uses.
- RE2 limits: a repetition count over 1000, or a pattern that compiles to a
  too-large program, makes the pattern invalid (matches nothing). Patterns
  over 4 KiB (`MaxRegexpLength`) are invalid too.
- A pattern given as a string literal is compiled once at parse time; one
  taken from the document is compiled on use and cached per evaluation.

### Other decisions

- **Blank space inside a singular query's brackets** (`@[ 'a' ] == 1`) is
  accepted. The RFC's `singular-query` grammar has no blank space there,
  while ordinary bracketed selections allow it; rejecting it would be a
  confusing error.
- **Duplicate member names** (not I-JSON) keep the first position and the
  last value.
- Nesting is limited: 64 levels of parentheses, filters and function calls
  in a query (`MaxNesting`), 10000 levels of arrays and objects in a
  document (`MaxDecodeDepth`).

## Response filter

The response view's filter bar (HTTP-36) runs queries through
`HTTPService.FilterBody` ([http-client.md](http-client.md#response-filter-http-36)):
`Parse` first (a `SyntaxError`'s `Offset16` marks the position in the
filter box), then `Decode` of the bytes received (a body that is not
JSON is reported with jsonfmt's line and column), then `Eval` with
`DefaultLimits`. The node list is shown as a JSON array of the values,
written from the value model so member order and number literals stay
as in the body, and `Paths()` gives the Paths view.

## Errors

`Parse` returns `*SyntaxError` with `Offset` (bytes), `Offset16` (UTF-16
code units, for CodeMirror) and `Message`:

```
jsonpath: unexpected ']': expected a selector (name, '*', index, slice or '?' filter) at 8
jsonpath: only a singular query (names and indexes only) can be compared at 3
jsonpath: the result of length() is a value and must be compared at 3
```

`SyntaxError.Err()` is the errs form: `invalid_argument`, code
`jsonpath.invalid_query`, details `offset` and `offset16`.

## Limits

A query can do far more work than the document's size: every `..` and
every filter containing a query multiplies it. `Eval` counts work units
(nodes produced or visited, filter tests, values compared, string bytes
examined, pattern bytes compiled) and stops over `Limits.MaxSteps` with
`unsupported` / `jsonpath.too_complex`, or when the context is done.
`DefaultLimits` (20 million) runs any ordinary query on a multi-megabyte
body; on a 2 MB document `$..*..*..*` takes about 0.1 s, and
`$[?count($..*) > 0]` is stopped after about 0.3 s. `Query` has no limit
and is for trusted input only.

## Tests

- `cts_test.go` runs the whole [JSONPath Compliance Test Suite]
  (vendored in `testdata/cts`, see its README): **704 of 704 cases pass**,
  results and normalized paths both checked.
- `rfc_test.go` has the RFC's own examples (Tables 2, 11, 12, 14 and the
  examples of §2.3, §2.5, §2.6 and §2.7).
- `jsonpath_test.go`: error positions and messages, exact numbers, the
  decoder, Go values, functions, I-Regexp translation, limits.
- `fuzz_test.go`: `FuzzParse` (no panic, errors inside the query, every
  result's normalized path selects it again), `FuzzQuery` (random queries
  on random documents within limits), `FuzzIRegexp`.

[JSONPath Compliance Test Suite]: https://github.com/jsonpath-standard/jsonpath-compliance-test-suite
