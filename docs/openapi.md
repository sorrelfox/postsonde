# OpenAPI reader

`internal/core/openapi` reads OpenAPI 3.0.x and 3.1.x documents, JSON or
YAML, into a typed model with every local `$ref` resolved. Swagger 2.0
documents are converted to the OpenAPI 3.0 model on the way
([below](#swagger-20)). It is the base for generating requests
(IMP-05/IMP-06: `internal/core/importer/openapiimport`,
[import.md](import.md#openapi-documents)) and mock routes (MOCK-09:
`internal/core/servers/mockopenapi`,
[mock-routes.md](mock-routes.md#routes-from-openapi)); it only reads, it
does not import. No third-party modules.

Example values for a schema or media type (cycle-safe, depth- and
size-limited, 3.0 and 3.1 alike, `readOnly`/`writeOnly` by direction, XML
by the `xml` hints) come from `openapiimport.SchemaExample`,
`MediaTypeExample` and `XMLExample`
([import.md](import.md#openapi-documents)).

| Piece | Where |
|---|---|
| Model: `Document`, `PathItem`, `Operation`, `Parameter`, `Schema`, ... | `openapi/model.go` |
| `Parse`, `ParseLimits`, `Limits`, fatal error codes | `openapi/parse.go` |
| JSON reader with line:column positions (into the `yaml.Node` tree) | `openapi/json.go` |
| Tree walking, `$ref` resolution, merge keys | `openapi/build.go` |
| Objects other than schemas | `openapi/objects.go` |
| Schemas, 3.0/3.1 normalisation, recursion marking | `openapi/schema.go` |
| Diagnostics and their codes | `openapi/diag.go` |
| Swagger 2.0 to OpenAPI 3.0 conversion | `openapi/swagger2.go` |
| Petstore fixtures (2.0, 3.0 and 3.1, YAML and JSON), golden dumps | `openapi/testdata/` |

## Use

```go
doc, diags, err := openapi.Parse(data)          // JSON or YAML, DefaultLimits
// err != nil: nothing usable (not JSON/YAML, not OpenAPI 3.x or Swagger 2.0,
//             too large);
//             an *errs.Error with a code and "line"/"column" details.
// diags: problems in parts of the document, with positions; the rest is read.

for _, op := range doc.Operations() {           // paths in document order
    op.Method, op.Path, op.OperationID, op.Tags
    op.Parameters                               // path-level merged with the operation's
    op.RequestBody.Content                      // Entries[*MediaType], document order
    op.Responses.Get("200")
    op.EffectiveSecurity(doc)
}
doc.Servers[0].DefaultURL()                     // {variables} at their defaults
doc.Components.Schemas.Get("Pet")
```

- **Format.** A document whose first character (after a BOM and
  whitespace) is `{` or `[` is read as JSON by `json.go`, which builds
  the same `yaml.Node` tree the YAML reader (docs/yaml.md) builds,
  with line and column positions. Everything else goes to the YAML
  reader. One document per file.
- **Versions.** `openapi: 3.0.x` and `3.1.x`. A later 3.x is read as 3.1
  with a warning. `swagger: "2.0"` (or `2.0` unquoted) is converted to
  3.0 ([Swagger 2.0](#swagger-20)); other versions are errors
  (`openapi.unsupported_version`).
- **Ordered maps.** Paths, properties, media types, responses, components
  and so on keep document order in `Entries[T]` (`[]Entry{Name, Value}`
  with `Get` and `Names`). Examples, defaults, enums and extensions are
  kept as JSON text (`json.RawMessage`, keys in document order, large
  numbers not rounded).
- **Positions.** Every object has a `Pos`: 1-based line and column (in
  characters) and its JSON pointer (`#/paths/~1pets/get`), for the
  importer's report and for jumping to the source.

## References

Only references inside the document are resolved:

| Form | Example |
|---|---|
| JSON pointer | `#/components/schemas/Pet`, `#/paths/~1pets~1{id}/get/parameters/0` |
| Escapes | `~1` is `/`, `~0` is `~`, and percent-encoding (`%20`) is decoded first |
| Whole document | `#` |
| 3.1 anchor | `#money` finds the schema with `$anchor: money` (or `$dynamicAnchor`) |
| Through references | `#/components/schemas/Alias/properties/x` when `Alias` is itself a `$ref` |

- **Shared objects.** A reference resolves to the same Go pointer as the
  object it names (memoized by tree node), so `op.RequestBody.Content[0].
  Value.Schema == doc.Components.Schemas.Get("Pet")`. YAML aliases share
  objects the same way.
- **Chains** of references (`A → B → C`) are followed to the first object
  that is not a reference.
- **Cycles.** Recursive schemas (a tree node whose `children` are tree
  nodes, `Pet ↔ User`) are graphs with cycles: `Schema.Recursive` marks
  every schema on a cycle (strongly connected components, computed after
  the build). **Code that walks schemas must keep a set of visited
  pointers**, as the tests' dumper does. References that only point at
  each other with no object at the end (`A: {$ref: B}`, `B: {$ref: A}`)
  are an `openapi.ref_cycle` error with a placeholder.
- **No recursion through references.** The target of a `$ref` is created
  empty, memoized and filled from a queue after the current object, so a
  chain of 20,000 schemas does not nest on the Go stack. Operations merge
  their parameters, and path items reached by reference get their path
  on a copy, after that queue is drained.
- **3.1 Reference Objects** with `summary`/`description` give a copy of the
  target with those replaced. In a **3.1 schema**, keywords next to `$ref`
  are kept: the result is a schema with those keywords and the target as
  the first `allOf` entry. In **3.0** they are ignored, as the
  specification says, with an `openapi.ignored` note.
- **Broken references** give a placeholder (a zero object with the
  reference's position; a `Schema` with `Ref` set to the `$ref` text) so
  consumers never meet nil entries.
- **External references** (`common.yaml#/Pet`, `https://...`) are not
  followed: an `openapi.external_ref` warning and a placeholder. `$id`
  does not change the base for resolution.

## Schemas

3.0's subset of JSON Schema and 3.1's JSON Schema 2020-12 are read into
one `Schema`:

| Keyword | Read as |
|---|---|
| `type` | `[]string`; 3.1 arrays as written; 3.0's `nullable: true` adds `"null"` (and sets `Nullable`) |
| `exclusiveMinimum`/`exclusiveMaximum` | 3.1 numbers; 3.0's `true` moves `minimum`/`maximum` there |
| numbers | `json.Number` text as written (`1.5e2` stays `1.5e2`, `0x10` becomes `16`) |
| `example` (3.0), `examples` (3.1 array) | both kept |
| `true`/`false` schemas | `Schema.Bool` |
| `items` array (draft 4 tuples) | `PrefixItems`, with a warning |
| `$defs` (and `definitions`) | `Defs` |
| `$id`, `$anchor`, `$schema`, `$comment`, `const`, `if`/`then`/`else`, `prefixItems`, `contentMediaType`, `contentEncoding`, `patternProperties`, `propertyNames` | fields of the same name |
| `discriminator`, `xml`, `externalDocs`, `readOnly`, `writeOnly`, `deprecated` | fields |
| `$dynamicRef`, `$dynamicAnchor`, `unevaluated*`, `dependent*` | not supported: an `openapi.unsupported` note |
| `x-...` | `Extensions` |

## Swagger 2.0

A `swagger: "2.0"` document is converted into an OpenAPI 3.0 tree
(`swagger2.go`) that the 3.0 builder then reads, so everything above
(references, cycles, schemas, diagnostics, limits) applies to it too. The
`Document` is the 3.0 form: `OpenAPI` is `"3.0.3"`, `Minor` 0, and
`Swagger` is `"2.0"`.

| Swagger 2.0 | OpenAPI 3.0 model |
|---|---|
| `host`, `basePath`, `schemes` | a server per scheme, `scheme://host/basePath` (a trailing `/` dropped). No `schemes`: `https` (an `openapi.missing_field` note). No `host`: the base path alone, a relative server |
| an operation's `schemes` | the operation's servers, when they differ from the document's |
| `consumes`, `produces` (document, or the operation's instead) | the media types of the request body and of each response with a `schema` or `examples`; none means `application/json` |
| `in: body` parameter | `requestBody`: `description`, `required`, the `schema` under each `consumes` type; `x-examples` by media type become examples |
| `in: formData` parameters | a request body with an object schema, a property per parameter (its `required` in `required`); `multipart/form-data` when a parameter is `type: file` or `consumes` says so, else `application/x-www-form-urlencoded`; array parameters get an encoding (`explode` for `multi`) |
| path-level body and formData parameters | merged into each operation's body; the operation's own win by name (any body parameter replaces the path's) |
| other parameters' `type`, `format`, `items`, `default`, `enum`, bounds, `pattern`... | the parameter's `schema`; `x-example` becomes `example` |
| `collectionFormat` (default `csv`) | `csv`: `form` (query) or `simple` (path, header) without explode; `multi`: `form` with explode; `ssv`: `spaceDelimited`; `pipes`: `pipeDelimited`; `tsv`, and `multi`/`ssv`/`pipes` outside the query, have no equivalent: commas, with an `openapi.unsupported` warning. On nested `items`: ignored, with a note |
| response `schema`, `examples`, `headers` | `content` per media type (with the example of that type), headers with a `schema` made from their type keywords |
| `definitions`, `parameters`, `responses`, `securityDefinitions` | `components.schemas`, `.parameters`, `.responses`, `.securitySchemes`. Body parameters become `components.requestBodies` (an operation using one with the document's `consumes` shares it, with others it gets a copy with its media types); formData parameters are only inlined |
| `$ref: '#/definitions/Pet'` (also `#/parameters/...`, `#/responses/...`, pointers into them and into body parameters) | rewritten to the converted object: `#/components/schemas/Pet`. Missing targets, external references and cycles are reported by the builder with the Swagger text of the reference |
| `type: file` | `type: string`, `format: binary` (a multipart file part, or a binary response) |
| `discriminator: petType` | `discriminator: {propertyName: petType}` |
| `x-nullable: true` | `nullable: true` |
| `type: basic` | `type: http, scheme: basic` |
| `type: apiKey` | the same (`in`: `header` or `query`) |
| `type: oauth2`, `flow` `implicit`, `password`, `application`, `accessCode` | `flows.implicit`, `.password`, `.clientCredentials`, `.authorizationCode` with the URLs and `scopes` |

**Positions.** Every node of the converted tree keeps the line and column
of the Swagger node it came from (made-up nodes, such as a request body's
media types, take those of the parameter they come from). JSON pointers
are mapped back as well: objects and diagnostics point into the Swagger
document (`#/definitions/Pet`, `#/paths/~1pets/post/parameters/0` for a
request body, `#/host` for the servers), so the importer's report and
"go to source" work as for 3.x.

**Limits.** Objects reached through YAML aliases are converted once per
use (schemas and plain values stay shared); the converted tree may have at
most twice `MaxNodes` nodes, else `openapi.too_large`.

Conversion diagnostics: `openapi.invalid_value` (a bad `host`, `basePath`
without `/`, an unknown scheme or `collectionFormat`, an unknown OAuth
flow, both body and formData parameters, formData without a form
`consumes`), `openapi.missing_field` (a body parameter without `schema`,
an `oauth2` scheme without `flow`, no `schemes`), `openapi.duplicate` (a
second body parameter), `openapi.ignored` (a top-level field that is not
Swagger 2.0), `openapi.unsupported` (above).

## Diagnostics

`[]Diagnostic{Severity, Code, Message, Pos}`; severities match the
importer's (`error`, `warning`, `info`). `HasErrors` reports errors.

| Code | Severity | When |
|---|---|---|
| `openapi.bad_ref` | error | a `$ref` target that does not exist, is not a string, or names no `$anchor`; a security requirement naming no scheme (warning) |
| `openapi.external_ref` | warning | a reference to another file or a URL |
| `openapi.ref_cycle` | error | references that only point at each other |
| `openapi.invalid_value` | error/warning | a value of the wrong type (`info: []`, `deprecated: "true"`), a bad status code key, a path not starting with `/`, an unknown parameter location, a path parameter not marked required, schema and content on one parameter |
| `openapi.missing_field` | warning | no `info`, `title`, `version`, `paths` (3.0), `responses` (3.0), parameter `name`/`in`, server `url` or variable `default`, security scheme fields for its type |
| `openapi.path_parameter` | warning | `{id}` in a path without an `in: path` parameter, or the reverse |
| `openapi.duplicate` | warning | a duplicate `operationId`, parameter or tag |
| `openapi.ignored` | info/warning | `$ref` siblings in 3.0, unknown top-level fields, `nullable` in 3.1, `name`/`in` on a header |
| `openapi.unsupported` | info/warning | a 3.x newer than 3.1, 3.1-only fields in 3.0, unsupported schema keywords |
| `openapi.limit` | error | `MaxRefs` or `MaxValueBytes` reached (reported once) |

Fatal errors (`err`), as `*errs.Error` with `line` and `column` details
where known:

| Code | errs kind | When |
|---|---|---|
| `openapi.invalid_json` | `parse` | not valid JSON (trailing commas, duplicate keys, bad escapes, invalid UTF-8) |
| `yaml.invalid_yaml`, `yaml.unsupported`, `yaml.too_large` | as in docs/yaml.md | from the YAML reader |
| `openapi.not_openapi` | `parse` | empty, not a mapping, no `openapi` field, several YAML documents |
| `openapi.unsupported_version` | `unsupported` | Swagger other than 2.0, OpenAPI other than 3.x |
| `openapi.too_large` | `unsupported` | over `MaxBytes`, or JSON over `MaxDepth`/`MaxNodes`, or a Swagger 2.0 conversion over twice `MaxNodes` |

## Limits

`ParseLimits(data, openapi.Limits{...})`; zero fields take `DefaultLimits`:

| Limit | Default | Bounds |
|---|---|---|
| `MaxBytes` | 32 MiB | input size |
| `MaxDepth` | 1000 | nesting of the document (references add none) |
| `MaxNodes` | 2,000,000 | values in the tree, and in each value expanded from YAML aliases |
| `MaxRefs` | 10,000,000 | reference work: each `$ref` hop and each JSON pointer segment |
| `MaxValueBytes` | 64 MiB | JSON text of all examples, defaults, enums and extensions (values are memoized by node, so an alias is serialized once) |

`FuzzParse` checks that any input returns a coded error or a model whose
diagnostics have positions and whose schema graph can be walked.
