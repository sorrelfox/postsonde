# JSON Schema validator

`internal/core/jsonschema` validates JSON values against a JSON Schema: the
validator of the JSON Schema check (TEST-07), used by the
`matches_schema` assertion operator ([assertions.md](assertions.md)). It is
pure core: the standard library plus this module's `errs` and `jsonpath`
packages. It validates values in the model of `jsonpath.Decode` (objects
`*jsonpath.Object` with member order kept, arrays `[]any`, numbers
`json.Number` exactly as written, strings, booleans, `nil`);
`map[string]any` and Go numbers are accepted too.

```go
s, err := jsonschema.CompileJSON(schemaBytes, jsonschema.Options{FormatAssertion: true})
if err != nil {
	return err // *errs.Error: jsonschema.invalid_schema, unresolved_ref, invalid_pattern
}
doc, _ := jsonpath.Decode(body)
res, err := s.Validate(ctx, doc, jsonschema.DefaultLimits)
if err != nil {
	return err // ctx's error, jsonschema.too_complex or too_deep
}
for _, e := range res.Errors {
	fmt.Println(e) // $.items[1].id: must be integer, not string
}
```

| Piece | Purpose |
|---|---|
| `Compile(doc, Options)`, `CompileJSON(data, Options)` | A `*Schema`, safe for concurrent use. Every subschema and reference is checked now, not at validation |
| `Options` | `Draft` when the schema has no (known) `$schema`, default 2020-12; `FormatAssertion` to make `format` fail (off by default, as the specification says); `Resources`, other schema documents by URI that references may point into (see [References](#references)) |
| `(*Schema).Validate(ctx, v, Limits)` | A `Result`: `Valid`, `Errors`, `Truncated`. An invalid value is not an error |
| `Limits` | `MaxSteps` (work), `MaxDepth` (nested schemas applied), `MaxErrors` (reported). `DefaultLimits`: 10 000 000, 512, 100 |
| `Error` | `InstancePath` (JSON Pointer, `""` for the whole value), `Path` (the same as a JSONPath: `$`, `$.items[0].id`, `$['a b']`), `KeywordPath` (the keyword along the evaluation, through references: `/properties/id/$ref/type`), `SchemaPath` (the keyword's place in the schema document: `#/$defs/id/type`), `Keyword`, `Message` |
| `Formats` | The format names checked with `FormatAssertion` |

## Drafts

The `$schema` of the root picks the draft: `…/draft/2020-12/…` (the
default), `…/draft/2019-09/…`, `draft-07`, `draft-06`, `draft-04`. The
keywords that changed between them follow the draft:

| Keyword | 2020-12 | 2019-09 | draft-07 and earlier |
|---|---|---|---|
| Tuples | `prefixItems` + `items`; the array form of `items` + `additionalItems` is accepted too | `items` array + `additionalItems` | same |
| `$ref` siblings | applied | applied | ignored (and `$id` beside `$ref` too) |
| Dependencies | `dependentRequired`, `dependentSchemas`; `dependencies` too, for compatibility | same | `dependencies` (array or schema values) |
| Dynamic references | `$dynamicRef`, `$dynamicAnchor` | `$recursiveRef` as a plain `$ref` | none |
| Anchors | `$anchor` | `$anchor` | `$id: "#name"` |
| `unevaluatedProperties`, `unevaluatedItems`, `minContains`, `maxContains` | yes | yes | ignored |
| `if`/`then`/`else` | yes | yes | draft-07 only |
| `exclusiveMinimum`/`Maximum` | numbers | numbers | numbers; booleans modifying `minimum`/`maximum` in draft-04 (`id` instead of `$id`) |

`definitions` works everywhere as a place for `$ref` targets, as does any
unknown keyword holding a schema (a JSON Pointer can reach it, but an
`$id` or anchor below an unknown keyword is not an identifier). A
`$schema` below the root is ignored: one schema is one draft.

## Keywords

| Group | Keywords |
|---|---|
| Any type | `type` (with `integer`: any number with a zero fraction, `1.0` included), `enum`, `const` |
| Numbers | `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf` |
| Strings | `minLength`, `maxLength` (Unicode code points), `pattern`, `format` |
| Arrays | `prefixItems`, `items`, `contains`, `minContains`, `maxContains`, `minItems`, `maxItems`, `uniqueItems`, `unevaluatedItems` |
| Objects | `properties`, `patternProperties`, `additionalProperties`, `propertyNames`, `required`, `minProperties`, `maxProperties`, `dependentRequired`, `dependentSchemas`, `unevaluatedProperties` |
| Applicators | `allOf`, `anyOf`, `oneOf`, `not`, `if`/`then`/`else` |
| References | `$ref`, `$defs`, `$id`, `$anchor`, `$dynamicRef`, `$dynamicAnchor` |
| Booleans | `true` and `false` schemas anywhere a schema goes |

Annotation-only keywords (`title`, `description`, `default`, `examples`,
`readOnly`, `writeOnly`, `deprecated`, `contentMediaType`,
`contentEncoding`, `contentSchema`, `$comment`) and unknown keywords are
ignored.

**Numbers are exact.** Numbers are compared and divided as rationals
(`math/big`), so `0.0075` is a multiple of `0.0001`, `19.99` of `0.01`, and
`9007199254740993` differs from `9007199254740992`. A literal with an
exponent beyond ±1000 is compared as a 256-bit float and divided as a
float64, so a document cannot make the validator build a gigabyte rational.

**Equality** (`enum`, `const`, `uniqueItems`) is JSON equality: `1` equals
`1.0`, objects are equal whatever their member order, `1` is not `true`.
`uniqueItems` hashes a canonical form of each item, so it is linear.

**unevaluatedProperties / unevaluatedItems** see the properties and items
evaluated by the keywords of the same schema object and by its passing
in-place subschemas (`allOf`, every passing `anyOf` branch, the one
passing `oneOf` branch, `if` when it passes, `then`/`else`,
`dependentSchemas`, `$ref`, `$dynamicRef`), as 2020-12 says. Annotations
are collected only when the schema uses one of them.

**`$dynamicRef`** resolves like `$ref` first; when that target has a
`$dynamicAnchor` of the same name, the outermost schema resource of the
dynamic scope with such an anchor is used instead.

## References

References inside the schema document are resolved:

- `#` and JSON Pointers (`#/$defs/item`, `#/properties/a~1b`,
  percent-encoded characters decoded: `#/$defs/percent%25field`);
- `#name` for `$anchor` (and `$dynamicAnchor`, draft-07 `$id: "#name"`);
- the URIs of the `$id` resources the document declares, relative ones
  resolved against the enclosing base (`"$ref": "item.json"`,
  `"https://example.com/nested/b.json#c"`), with pointers and anchors
  inside them.

So are references into the documents of `Options.Resources`, a map from
URI (absolute; a fragment is dropped) to a schema document in the value
model of `Compile`. A document there is scanned and compiled only when a
reference reaches it, with its URI as its base; its own `$id` resources
and anchors then count too, and its references may lead to further
documents of the map. The errors of a keyword in such a document give its
URI in `SchemaPath`: `https://example.com/defs.json#/$defs/id/type`.

A reference to anything else (another file, a URL, the meta-schema) is a
compile error `jsonschema.unresolved_ref` ("it points outside the
schema"): nothing is fetched or read. The `matches_schema` assertion
passes no resources, so there, put shared definitions under `$defs`.

**Cycle safety.** Recursive schemas (`{"items": {"$ref": "#"}}`, trees
through `$defs`) are fine: compiling memoizes each location, and each
round of validation goes one level into the value. A reference that comes
back to a schema already being applied to the *same* value
(`{"$ref": "#"}`, `a → anyOf → b → allOf → a`) would never end: it stops
with `jsonschema.too_deep` naming the location. `MaxDepth` bounds nesting
in general.

## Patterns

`pattern` and `patternProperties` are ECMA-262 regular expressions in JSON
Schema; Go has RE2. The pattern is translated where the two differ in
spelling, then compiled once, at compile time:

| ECMA-262 | Becomes |
|---|---|
| `\uXXXX`, `\u{X…}` | `\x{…}` |
| `\cX` (control character) | `\x{…}` |
| `\s` | the ECMA-262 white space and line terminators (RE2's `\s` is ASCII); inside a class too |
| `\S` outside a class | its complement (inside a class it stays ASCII) |
| `[]` (matches nothing), `[^]` (any character) | `[^\x00-\x{10FFFF}]`, `[\x00-\x{10FFFF}]` (RE2 reads `[]…]` as a class holding `]`) |

Not supported, and a compile error `jsonschema.invalid_pattern` (kind
`unsupported`) naming the feature: lookahead `(?=…)` `(?!…)`, lookbehind
`(?<=…)` `(?<!…)` and backreferences `\1`, `\k<name>`. Other differences
are left as they are: `.` also matches `\r`, U+2028 and U+2029 in RE2;
`\d`, `\w` and `\b` are ASCII in both; RE2 accepts some syntax ECMA-262
rejects (`\pL`, `(?i)` flags). Patterns are not anchored: use `^…$`. RE2
matches in linear time, so no pattern can make validation hang.

## Formats

With `Options.FormatAssertion` (the assertion operator sets it), these
formats fail strings that are not valid; other names, and non-strings,
pass:

| Format | Checked |
|---|---|
| `date-time`, `date`, `time` | RFC 3339: ranges, days per month and leap years, an offset required on times, a leap second only at 23:59:60 UTC |
| `duration` | RFC 3339 appendix A ABNF: `P1Y2M3DT4H5M6S`, `P2W`; units in order and contiguous (`P1Y2D`, `PT1H2S` and a bare `T` fail) |
| `email`, `idn-email` | RFC 5321 mailbox: a dot-string (`atext` atoms, single dots) or quoted-string local part (an empty `""` too), `@`, and a host name (below) or an `[IPv4]` / `[IPv6:…]` literal (the tag in any case); no display name, comments, spaces or general literals; `email` is ASCII only, `idn-email` allows UTF-8 in the local part and U-labels in the domain (RFC 6531) |
| `hostname`, `idn-hostname` | RFC 1123: labels of 1–63 letters, digits and hyphens, no leading or trailing hyphen, no empty label (so no trailing dot), at most 253 characters; `--` at positions 3–4 only in an A-label (`xn--`), which must be valid Punycode that encodes back to itself, decode to non-ASCII, and pass the U-label checks below. `idn-hostname` also allows U-labels (whose A-label must fit in 63 characters) and the ideographic full stops `。．｡` as separators. U-labels follow IDNA 2008 where the standard library's Unicode tables allow (see below) |
| `ipv4`, `ipv6` | `net/netip`: dotted quads without leading zeros; IPv6 without a zone |
| `uri`, `uri-reference`, `iri`, `iri-reference` | RFC 3986 characters and `%` escapes; `uri`/`iri` need a scheme; `[` `]` only around an IP-literal host, at most one `@` in the authority; `iri*` allow non-ASCII |
| `uri-template` | RFC 6570 level 4: literals without controls, space, `"`, `<`, `>`, `\`, `^`, `` ` ``, `{`, `\|`, `}` or a lone `%`; expressions with an optional operator and a list of variable names (single inner dots, `%` triplets), each with a `:1`–`:9999` prefix or `*` |
| `uuid` | 8-4-4-4-12 hex digits |
| `regex` | ECMA-262 syntax: compiles as a pattern would (RE2, above), except that lookaround and backreferences are valid here, and RE2-only syntax is not: `(?P<n>…)`, `(?#…)`, flag groups other than the ES2025 modifiers `(?i:…)`, and `\a`, `\A`, `\z`, `\Z`, `\Q`, `\E`, `\C`, `\p`/`\P` without braces |
| `json-pointer`, `relative-json-pointer` | RFC 6901 and the relative JSON Pointer draft |

**U-labels** (an `idn-hostname` label, or a decoded A-label in either
host name format) are checked against IDNA 2008 (RFC 5891, 5892, 5893) as
far as the standard library's Unicode tables go, without
`golang.org/x/net/idna`:

- no combining mark first, no `--` at positions 3–4, no hyphen first or
  last;
- every code point a lower-case or other letter, a decimal digit or a
  non-enclosing mark, or one of the exceptions of RFC 5892 §2.6 (`ß`, `ς`,
  `་`, `〇`… valid; `ـ`, `ߺ`, `〮`, `〱`… not), and not Hangul Jamo;
- the contextual rules of RFC 5892 appendix A: ZERO WIDTH JOINER after a
  virama, ZERO WIDTH NON-JOINER after a virama or between joining letters,
  MIDDLE DOT between two `l`, Greek KERAIA before Greek, Hebrew GERESH and
  GERSHAYIM after Hebrew, KATAKANA MIDDLE DOT with Hiragana, Katakana or
  Han in the label, and the two sets of Arabic-Indic digits not mixed;
- the Bidi rule of RFC 5893 on every label once one label has a
  right-to-left character.

Approximations: the standard library has no Bidi_Class or Joining_Type,
so a character of a right-to-left script has Bidi class R, and every
letter of a cursive-joining script (Arabic, Syriac, N'Ko, Mongolian…)
joins on both sides. Upper-case non-ASCII letters fail (IDNA 2008 does not
allow them; ASCII letters may have either case). The UTS #46 mapping
(case and width folding, removing ignored characters such as U+200B) is
not applied, and Unicode normalization is not checked: the name is
checked as written.

## Errors

Each failing keyword gives one `Error`, in the order the schema's keywords
are applied (`type`, `enum`, `const`, the type's own keywords, then the
applicators). A failure inside `properties`, `items`, `allOf`, `then`… is
reported where it happens, with the paths of the property or item.
`anyOf`, `oneOf` and `not` report one error of their own (their branches
are tried quietly), so a failed `anyOf` does not list every branch's
errors:

| Keyword | Message |
|---|---|
| `type` | `must be integer, not string`; `must be string, null or array, not number` |
| `enum`, `const` | `must be one of "a", "b"`; `must be one of the 7 values in enum`; `must equal 42` |
| `minimum` … `multipleOf` | `must be at least 5, not 3`; `must be greater than 5, not 5`; `must be a multiple of 0.5, not 1.2` |
| `minLength`, `maxLength` | `must have at least 3 characters, not 2` |
| `pattern`, `format` | `must match the pattern "^a"`; `must be a valid uuid, not "x"` |
| `minItems`, `maxItems`, `uniqueItems` | `must have at most 1 item, not 2`; `must have unique items, but items 0 and 2 are equal` |
| `contains`, `minContains`, `maxContains` | `must contain at least 1 item matching the contains schema, not 0` |
| `required`, `dependentRequired` | `missing required property "name"`; `property "a" requires property "b"` (at the object) |
| `minProperties`, `maxProperties`, `propertyNames` | `must have at least 1 property, not 0`; `property name "A" does not match the propertyNames schema` |
| `anyOf`, `oneOf`, `not` | `must match at least one of the 2 schemas in anyOf`; `must match exactly one of the schemas in oneOf, but matches 0 and 1`; `must not match the schema in not` |
| `false` schemas | `is not allowed (additionalProperties is false)` at the extra property; `is not allowed (unevaluatedItems is false)`; `is not allowed (properties)` |

Values quoted in messages are compact JSON, cut after 60 bytes.

| Code (compile) | Cause |
|---|---|
| `jsonschema.invalid_schema` | Not JSON; not an object or boolean where a schema goes; a keyword with a value of the wrong kind (`"minLength": -1`, `"type": "int"`, an empty `anyOf`, `"multipleOf": 0`); the same `$id` twice. The message names the location (`#/properties/a/minLength`) |
| `jsonschema.unresolved_ref` | A reference outside the document, to a pointer with nothing there, or to an unknown anchor |
| `jsonschema.invalid_pattern` | A `pattern` or `patternProperties` name RE2 rejects |

| Code (validation) | Cause |
|---|---|
| `jsonschema.too_complex` | More than `MaxSteps` work units (each schema applied, member and item visited, 64 bytes compared or hashed) |
| `jsonschema.too_deep` | More than `MaxDepth` nested schemas, or a reference loop on the same value |

## Conformance

`TestOfficialSuite` runs the official
[JSON Schema Test Suite](https://github.com/json-schema-org/JSON-Schema-Test-Suite)
for draft 2020-12 and draft-07, copied unchanged into
`internal/core/jsonschema/testdata/JSON-Schema-Test-Suite/` at commit
`245a45abc90190c0bff04d97e6d2b541b4bb9f12` (its `README.md` lists what was
copied, a checksum and how to update). Each directory's schemas are
compiled as its draft; `optional/format/` runs with `FormatAssertion`; the
suite's `remotes/` documents are given as `Options.Resources` under
`http://localhost:1234/…`, read from the directory, never fetched.

| Directory | Cases | Passed | Skipped | Failed |
|---|---|---|---|---|
| `draft2020-12` (required) | 1300 | 1295 | 5 | 0 |
| `draft2020-12/optional` (formats, bignum, ECMA-262 regex, compatibility…) | 1074 | 1068 | 6 | 0 |
| `draft7` (required) | 927 | 923 | 4 | 0 |
| `draft7/optional` | 950 | 942 | 8 | 0 |

The skipped cases are listed by name, with their reason, in `officialSkips`
in `suite_test.go`. They still run, and the test fails if one of them
starts passing (or no longer exists), so the list stays exact:

| Skipped | Cases | Why |
|---|---|---|
| `defs.json` "validate definition against metaschema", `ref.json` "remote ref, containing refs itself" (2020-12); `definitions.json`, `ref.json` the same (draft-07) | 4 + 4 | They `$ref` the draft's meta-schema, which is not bundled and not fetched |
| `vocabulary.json` "no validation: invalid number, but it still validates"; `optional/format-assertion.json` "invalid string" (×2) (2020-12) | 1 + 2 | They need `$vocabulary`: a custom meta-schema that turns a vocabulary off or the format-assertion vocabulary on |
| `optional/cross-draft.json`: "first item not a string is valid" (2020-12), "missing bar is invalid" (draft-07) | 1 + 1 | One schema mixing drafts (a resource with another `$schema`) |
| `optional/content.json` (draft-07): the invalid JSON and invalid base64 cases | 4 | `contentEncoding` and `contentMediaType` are annotations only |
| `optional/format/idn-hostname.json` (both): a zero width space ignored, 63 fullwidth characters, an ACE prefix produced by mapping | 3 + 3 | They need the UTS #46 mapping, which is not applied |

The rest of `testdata/` and `jsonschema_test.go` test what the suite does
not:

- `TestSuite` runs `testdata/suite`, fixtures in the suite's format
  (`<draft>/*.json`, `<draft>/optional/format/*.json`) written for this
  package, one file per keyword family: exact numbers, code-point lengths,
  ECMA-262 escapes, `$ref` through percent-encoded pointers, draft-07
  spellings, every format.
- `jsonschema_test.go` checks every keyword's error (its instance path,
  JSONPath, keyword path, schema path and message), compile errors and
  their codes, the limits (reference loops, depth, steps, error count,
  cancellation, huge exponents), the `map[string]any` model, pattern
  translation, `Options.Resources` (lazy loading, error locations),
  JSONPath rendering, draft detection and concurrent use.

## Not handled

- Fetching remote references, and meta-schema validation (a schema is not
  checked against its meta-schema beyond the keyword values above; the
  meta-schemas are not bundled, so `"$ref":
  "https://json-schema.org/draft/2020-12/schema"` is unresolved unless
  given in `Options.Resources`).
- `$vocabulary`; `$recursiveAnchor` (2019-09's `$recursiveRef` is a plain
  reference); a `$schema` below the root (one schema is one draft).
- Output formats of the specification (flag / basic / detailed): `Result`
  is a flat list close to "basic", without annotations.
- The branch errors of a failed `anyOf`/`oneOf`.
- Content keywords as assertions (`contentEncoding`, `contentMediaType`).
- The UTS #46 mapping and Unicode normalization for host names (see
  [Formats](#formats)).
