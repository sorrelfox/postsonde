# GraphQL engine

`internal/core/graphql` is PostSonde's GraphQL engine, written in Go with the
standard library only. It is built bottom-up: the lexer first, then the
parser, validation and autocomplete on top of it. This page grows with each
part.

```
source text ──▶ lexer ──▶ parser ──▶ ast ──▶ validation, completion
                                              ▲
introspection result ──▶ schema model ─────────┤
                                              │
SDL text ──▶ parser.ParseSchema ──▶ ast ──▶ schema.FromSDL (type system checks)

ast + comments ──▶ printer ──▶ prettified or minified text (executable or SDL)
```

The GraphQL mock server (`internal/core/servers/graphqlmock`,
[servers.md](servers.md#graphql-mock-server)) builds on the schema model,
the parser and the validator, and adds an executor (variables, fields
merged by response key, fragments, `@skip`/`@include`, null propagation)
and an introspection resolver over the model.

## Lexer

`internal/core/graphql/lexer` turns a document into tokens. It follows the
October 2021 spec plus the current working draft (the variable-width
`\u{...}` escape and surrogate-pair rules).

### API

| Item | Purpose |
|---|---|
| `New(src, mode) *Lexer`, `(*Lexer).Next() Token` | Pull tokens one at a time. After the end, `Next` keeps returning `EOF`. |
| `Tokenize(src, mode) []Token` | All tokens, ending with `EOF`. |
| `Token{Kind, Err, Start, End, Line, Column, Message}` | `src[Start:End]` is the token (`Token.Text(src)`); no text is copied. |
| `Mode`: `SkipComments`, `EmitIgnored` | The zero mode returns comments and skips whitespace, line terminators, commas and the BOM. |
| `StringValue(text) (string, error)` | The value of a `String` or `BlockString` token's text: escapes decoded, or the spec's block string dedent. Fails with `*ValueError` for anything else. |
| `BlockStringValue(raw) string` | The spec's `BlockStringValue` on the text between `"""` delimiters. |
| `NewUTF16Index(src)`: `ToUTF16`, `ToByte`, `Len16` | Converts between byte offsets and UTF-16 offsets (CodeMirror, JavaScript). |

Token kinds: `EOF`, `Error`; the punctuators `! $ & ( ) ... : = @ [ ] { | }`;
`Name`, `Int`, `Float`, `String`, `BlockString`; `Comment`; and the ignored
tokens `Whitespace` (a run of spaces and tabs), `LineTerminator` (`\n`,
`\r\n` or `\r`), `Comma` and `BOM` (U+FEFF, anywhere in the document).

### Positions

- `Start` and `End` are **byte** offsets into the source.
- `Line` and `Column` are 1-based. `Column` counts **Unicode code points**
  from the start of the line; an invalid UTF-8 byte counts as one. `\r\n` is
  one line break, and so is a lone `\r`.
- The UI works in UTF-16 offsets. Build one `UTF16Index` per document and
  convert token offsets with `ToUTF16`, cursor positions with `ToByte`.
  Conversions are O(log k) for k non-ASCII characters.
- Position tracking is linear in the document size, also for one-line
  minified documents.

### Errors and recovery

The lexer never stops at an error. Malformed input becomes an `Error` token
with an `ErrCode` and a message, and lexing continues after it:

| `ErrCode` | Token covers | Example |
|---|---|---|
| `ErrUnexpectedChar` | one character, or `.`/`..` | `?`, `é`, `..` |
| `ErrInvalidUTF8` | one byte; or a whole closed string with a bad byte | `\xff` |
| `ErrInvalidNumber` | the number and any directly following name characters and dots | `1.`, `0x1F`, `123abc`, `-`, `1e+`, `01` |
| `ErrUnterminatedString` | from `"` to the end of the line (the next line lexes normally) | `"abc` |
| `ErrUnterminatedBlockString` | from `"""` to the end of input | `"""abc` |
| `ErrInvalidEscape` | the whole closed string | `"\q"`, `"\uD800"`, `"\u{110000}"` |

Strings are always delimited first and checked second, so a bad escape
never throws the rest of the line out of sync. Comments and block strings
accept any character; outside strings, only ASCII punctuators, names,
numbers and ignored characters are valid.

### Tests

- Table-driven tests for every token kind, the ignored tokens, positions and
  error messages, and malformed input of every error code.
- `StringValue` and `BlockStringValue` tests follow the spec's examples.
- `FuzzTokenize` checks that the lexer never panics, that `EmitIgnored`
  tokens cover the input contiguously, that positions match a naive recount,
  that valid tokens relex identically, that `StringValue` accepts exactly
  string tokens, and that `UTF16Index` matches `unicode/utf16`. Run it with
  `go test ./internal/core/graphql/lexer -run '^$' -fuzz FuzzTokenize -fuzztime 60s`.

## Parser and syntax tree

`internal/core/graphql/parser` parses executable documents into the tree
defined in `internal/core/graphql/ast`. The tree is its own package so the
validator and the completion engine depend on node types and
helpers, not on the parser's internals.

### API

| Item | Purpose |
|---|---|
| `parser.Parse(src) (*ast.Document, []parser.Error)` | Parse with the default limits. The document is never nil, also for garbage. |
| `parser.ParseSchema(src)`, `Config.ParseSchema(src)` | Parse a type system document (SDL); see [Type system documents](#type-system-documents-sdl). |
| `parser.Config{MaxDepth, MaxErrors}.Parse(src)` | Parse with other limits (defaults 256 and 100). |
| `parser.Error{Code, LexCode, Start, End, Message}` | A syntax error on the byte range `[Start, End)`. Errors are sorted by `Start`. |
| `(*ast.Document).Position(off) ast.Position` | 1-based line and column (code points, like the lexer). |
| `ast.NodePathAt(root, off) []ast.Node` | The nodes containing `off`, root first, innermost last. |
| `ast.Children(n)`, `ast.Inspect(n, f)` | Child nodes in source order; depth-first walk. |
| `ast.Dump(n)` | Deterministic text form for golden tests and debugging. |
| `(*ast.Document).Comments []*ast.Comment` | The `#` comments in source order: `Loc` and `Text` (from `#` to the end of the line, without the line terminator). Not part of the tree: `Children`, `Inspect` and `NodePathAt` skip them. The printer keeps comments with them. |

### Grammar

The whole executable part of the spec: operations (`query`, `mutation`,
`subscription` and the `{ ... }` shorthand) with variable definitions (type
references `Name`, `[T]`, `T!`, default values, directives), directives and
selection sets; fields with aliases, arguments, directives and selection
sets; fragment spreads; inline fragments with and without a type condition;
fragment definitions; and every value kind (variable, int, float, string,
block string, boolean, null, enum, list, object). Keywords are keywords only
where the grammar puts them: `{ query fragment on }` selects three fields.
The experimental fragment variables (`fragment F($a: Int) on T`) are not
supported and are reported as syntax errors.

Const-ness: default values and the directive arguments on a variable
definition are constant. A variable there stays in the tree and is reported
as `ErrVariableInConst`.

Type system definitions (`type`, `schema`, `extend`, a leading description
string, ...) are not executable. The parser does not fail on them: it skips
the definition (through its `{ ... }` block, or up to the next top-level
keyword for `scalar`, `union` and `directive`, which have no block), adds an
`ast.TypeSystemDefinition` with the keyword and range, and reports
`ErrNotExecutable` over that range. A pasted schema does not hide the
operations after it. Schema files are parsed with `ParseSchema` instead
([Type system documents](#type-system-documents-sdl)).

### Positions

- Every node embeds `ast.Loc{Start, End}`, a half-open **byte** range. A
  parent's range contains its children's, and siblings are in source order
  without overlap.
- Delimiters are kept as offsets (`LBrace`/`RBrace`, `LParen`/`RParen`,
  `LBracket`/`RBracket`, `Colon`, `Equals`, `At`, `Dollar`, `Spread`, `On`,
  `Bang`, `Keyword`), `-1` when absent, so the completion engine can tell
  "before `(`" from "inside the argument list".
- `NodePathAt` counts both ends as inside: a cursor right after `use` is in
  the name `use`. When two siblings touch at the offset, the earlier wins.
- Convert offsets for the editor with `lexer.UTF16Index`.

### Partial trees

A half-typed document gets placeholders instead of a stop:

| Input | Tree |
|---|---|
| `{ user { } }`, `{` | the empty selection set holds one `Field` whose `Name` is missing (`Value == ""`), spanning the inside of the braces |
| `{ user(id: ` | `Argument` `id` whose value is `InvalidValue{Missing: true}` up to the end of input |
| `{ ...` | `FragmentSpread` with a missing name |
| `{ ... on `, `fragment F on` | `TypeCondition` whose `NamedType` has a missing name |
| `query Q($` | `VariableDefinition` with a missing variable name and a missing type |
| `{ a @` | `Directive` with a missing name |
| `{ a(s: "abc` | the malformed token is an `InvalidValue` covering it |
| `query Q` | `SelectionSet` is nil: its `{` never came |

A missing name, value or type spans the gap between the previous token and
the next one, so a cursor anywhere in that whitespace lands on it. A list
without its closing delimiter (`RBrace == -1` and so on) extends to the start
of the token where parsing stopped: to the end of input for a trailing
`{ user(`, so a cursor at the very end is still inside.

### Errors and recovery

| `Code` | Meaning |
|---|---|
| `ErrLexical` | A lexer `Error` token; `LexCode` has the lexer's code. |
| `ErrUnexpectedToken` | Something else was expected: `expected value, found ")"`. Also empty `{}` and `()`. |
| `ErrUnclosed` | `{`, `(` or `[` without its closer; the range is the opening delimiter. |
| `ErrVariableInConst` | A variable in a constant value. |
| `ErrReservedName` | A fragment named `on`. |
| `ErrNotExecutable` | A type system definition, skipped. |
| `ErrTooDeep` | Nesting beyond `MaxDepth`; reported once. |
| `ErrTooManyErrors` | The last error once `MaxErrors` is reached; later errors are dropped. |
| `ErrNotTypeSystem` | An operation or fragment in a schema document (`ParseSchema`); parsed and dropped. |

- A missing piece becomes a placeholder, and parsing goes on with the same
  token. Inside a list, a token that cannot start an element is reported and
  skipped. Every loop consumes a token or ends, so parsing is linear in the
  document size.
- A closing delimiter that belongs to an enclosing list (a `}` inside an
  unclosed `(`) ends the inner lists, which are reported as unclosed. A `{`,
  `@` or the end of input also ends an argument or variable list.
- Inside an unclosed list, a new definition is recognised by `query`,
  `mutation` or `subscription` followed by a name, `(`, `@` or `{`, or by
  `fragment` followed by a name, **at column 1**. These are valid field names
  too, but formatted documents indent fields, and the rule keeps a half-typed
  operation from swallowing the definitions after it.
- At the top level, tokens that start no definition are skipped up to the
  next one as a single error.
- One error per start offset: `query Q($` reports the missing variable name,
  not also the missing `:` and type at the same place.
- Selection sets, list and object values and list types count towards
  `MaxDepth`. Beyond it the nested part is skipped up to its matching closer
  without recursion, so `{a{a{a...` 100,000 levels deep is safe.

### Tests

- Spec examples of the executable grammar with golden tree dumps
  (`testdata/valid`), value and type shapes, and exact error codes, ranges
  and messages, including SDL skipping.
- Half-typed inputs with golden partial trees (`testdata/partial`), and
  cursor-context checks through `NodePathAt` the way the completion
  engine uses it.
- 100,000-deep nesting for every nesting construct, the error cap, and a
  large garbage document parsed in linear time.
- `FuzzParse` checks that the parser never panics and terminates, that node
  ranges nest within their parents and the source without sibling overlap,
  that names and delimiter offsets match the source, that errors are in
  range, sorted and capped, that `NodePathAt` returns a nested chain, and
  that an error-free document has no placeholders. Run it with
  `go test ./internal/core/graphql/parser -run '^$' -fuzz FuzzParse -fuzztime 60s`.
- Rewrite golden files with `-update` and review the diff.

### Type system documents (SDL)

`parser.ParseSchema(src)` parses a schema file (GQL-11) into the same
`ast.Document`, whose definitions are then the type system nodes of
`ast/typesystem.go`:

| Node | Holds |
|---|---|
| `SchemaDefinition` | `Description`, `Extend`, `Directives`, `OperationTypes` (`RootOperationTypeDefinition{Operation, Type}`), `LBrace`/`RBrace` (`-1` for `extend schema @d`). |
| `TypeDefinition` | One node for the six kinds (`Kind`: `KindScalar`, `KindObject`, `KindInterface`, `KindUnion`, `KindEnum`, `KindInputObject`; `String()` is the keyword): `Description`, `Extend`, `Name`, `Implements` + `Interfaces`, `Directives`, `Equals` + `Members`, `LBrace`/`RBrace` + `Fields` (`FieldDefinition{Description, Name, Arguments, Type, Directives}`), `EnumValues` (`EnumValueDefinition`) or `InputFields` (`InputValueDefinition{Description, Name, Type, DefaultValue, Directives}`). |
| `DirectiveDefinition` | `Description`, `Name`, `Arguments` (`ArgumentDefinitionList`), `Repeatable`, `On`, `Locations` (names). |

`Extend`, `Implements`, `Equals`, `Repeatable` and the delimiters are
offsets, `-1` when absent (`IsExtension()` tests `Extend`). The grammar is
the spec's (September 2025): descriptions (strings and block strings) on
definitions, fields, arguments, input fields and enum values, but not on
extensions; a leading `&` or `|`; `implements A & B`; constant default
values and directive arguments (a variable is `ErrVariableInConst`); no
enum value named `true`, `false` or `null` (`ErrReservedName`); only the
19 spec directive locations; empty `{}`/`()` rejected; an extension must
add something. Type definitions without a block (`type Query`) parse;
the schema builder reports them.

Recovery is the executable parser's: placeholders for missing pieces,
unexpected tokens skipped inside lists. Inside an unclosed block a type
system keyword **at column 1** followed by a name (`type B`, `extend
type`, `directive @`, `schema {`, or a description before one of them)
starts the next definition, so a missing `}` does not swallow the rest of
the file. Operations and fragments are parsed, reported as
`ErrNotTypeSystem` and dropped; other stray tokens are skipped up to the
next definition as one error.

Tests (`sdl_test.go`): the spec's type system examples and grammar corner
cases (keywords as names, comments and commas, leading separators) with
golden tree dumps in `testdata/sdl`, node shapes, a table of errors with
codes, positions and messages, recovery at the next definition, and
`FuzzParseSchema` with the same tree invariants as `FuzzParse` (run it
with `go test ./internal/core/graphql/parser -run '^$' -fuzz FuzzParseSchema -fuzztime 60s`).

## Schema model (`graphql/schema`)

The schema model represents a GraphQL schema built from an introspection
result (GQL-10) or from an SDL file (GQL-11, [From SDL](#from-sdl-gql-11)).
It feeds the docs explorer (GQL-14), autocomplete (GQL-13), validation
(GQL-12), the schema cache (GQL-21), SDL export (GQL-20,
[SDL export](#sdl-export-gql-20)) and, later, schema diffs.

```
server ──FullIntrospectionQuery──▶ response JSON
   │ (errors: retry with ConservativeIntrospectionQuery)
   ▼
ParseIntrospection ──▶ *Schema ──Introspection / MarshalIntrospection──▶ {"__schema": …}
        │                  │                                            (cache, round-trip tests)
        │                  └─ Type, Field, Directive, PossibleTypes, … lookups
        └─ Problems (broken references), with a usable Schema
```

### Model

| Item | Purpose |
|---|---|
| `Schema{Description, QueryType, MutationType, SubscriptionType, Types, Directives}` | The schema. Root types by name; types and directives in the server's order. |
| `Type{Kind, Name, Description, SpecifiedByURL, IsOneOf, Fields, Interfaces, PossibleTypes, EnumValues, InputFields}` | A named type. Lists apply by kind; other types are referenced by name, so the model has no pointer cycles and encodes to JSON as is. |
| `Field{Name, Description, Args, Type, Deprecation}`, `InputValue{…, DefaultValue *string, Deprecation}`, `EnumValue`, `Directive{Name, Description, Locations, Args, IsRepeatable}` | Members. `DefaultValue` is the GraphQL literal as sent (`10`, `{a: 1}`, `null`), nil when there is none. |
| `Deprecation{IsDeprecated, DeprecationReason}` | On fields, arguments, input fields and enum values. |
| `TypeRef{Kind, Name, OfType}`, `Named`, `ListOf`, `NonNullOf` | A type reference. `String()` gives `[User!]!`; `NamedType`, `Nullable`, `Elem`, `IsList`, `IsNonNull`, `Equal`. |
| `Kind`, `DirectiveLocation`, `Operation` constants | `__TypeKind` and `__DirectiveLocation` values, `query`/`mutation`/`subscription`. Unknown locations are kept. |

### Lookups

| Method | Returns |
|---|---|
| `Type(name)`, `HasType(name)`, `TypeOf(ref)` | A declared type, else a built-in scalar or introspection type; `HasType` counts declared types only. |
| `Directive(name)` | A declared directive, else a spec directive. |
| `RootType(op)`, `Query()`, `Mutation()`, `Subscription()` | Root types, nil when absent or undeclared. |
| `Field(type, field)`, `FieldsOf(type)` | Fields of a composite type including the meta fields: `__typename` everywhere, `__schema` and `__type(name:)` on the query root (`TypenameField`, `SchemaField`, `TypeField`). |
| `PossibleTypes(abstract)`, `IsPossibleType`, `Implementations(iface)`, `IsSubType(super, sub)` | Union members; interface object types (as reported, else derived from declarations); implementers including interfaces; fragment-spread applicability. |
| `Builtins()` | The built-ins alone: `Int Float String Boolean ID`, `__Schema __Type __TypeKind __Field __InputValue __EnumValue __Directive __DirectiveLocation`, `@include @skip @deprecated @specifiedBy @oneOf` (September 2025 spec, checked against graphql-js output). |

Built-ins are fallbacks, not additions: they never appear in `Types` or
`Directives` unless the server listed them, so the round trip stays exact.

### Introspection queries

Servers reject introspection fields their GraphQL library does not know
(`Cannot query field "isRepeatable" on type "__Directive"`), so there are two
queries:

| Constant | Asks for | Works with |
|---|---|---|
| `FullIntrospectionQuery` | Schema description, `specifiedByURL`, `isRepeatable`, `isOneOf`, `args`/`inputFields(includeDeprecated: true)` with `isDeprecated`/`deprecationReason`, 9 `ofType` levels | Current servers (recent graphql-js 16 and equivalents) |
| `ConservativeIntrospectionQuery` | Types, fields (with deprecated), args, input fields, enum values (with deprecated), interfaces, possible types, directives, descriptions, 7 `ofType` levels | Any server |

A client sends the full query and, when the response has errors and no
`__schema` (`CodeServerErrors`), retries with the conservative one.
`IntrospectionQuery(IntrospectionOptions{…})` builds other combinations;
the constants equal its output for `FullIntrospectionOptions()` and
`ConservativeIntrospectionOptions()` (checked by a test). Both queries were
checked against the real servers the fixtures come from.

### Conversion

`ParseIntrospection(data)` accepts `{"data":{"__schema":…}}` (errors next to
data are ignored), `{"__schema":…}` or the `__schema` value itself;
`FromIntrospection(*IntrospectionSchema)` takes the decoded form. Fields a
conservative result lacks default to false/empty. `specifiedByUrl`
(graphql-js 15) is read as `specifiedByURL`.

Every reference is checked and each inconsistency becomes a `Problem{Path,
Message}`: undeclared types (in fields, arguments, input fields, interfaces,
possible types, directive arguments and root types), kinds that do not match
the declared type, output types used as inputs and the reverse, interfaces
that are not interfaces, possible types that are not objects, truncated
wrappers (deeper than the query's `ofType` depth), `NON_NULL` inside
`NON_NULL`, lists on kinds they do not apply to, unknown kinds, missing and
duplicate names. Paths read `Query.user(id:)`, `ReviewInput.stars`,
`Episode.JEDI`, `@include(if:)`, `User implements`, `schema.queryType`.

Errors are `*errs.Error`:

| Code | Kind | When | Schema |
|---|---|---|---|
| `graphql.schema.malformed_json` | parse | Not a JSON object | nil |
| `graphql.schema.malformed_introspection` | parse | JSON of the wrong shape | nil |
| `graphql.schema.no_schema` | parse | No `__schema` and no errors | nil |
| `graphql.schema.server_errors` | protocol | Errors instead of `__schema`; `Details["errors"]` has all messages, hint suggests the conservative query | nil |
| `graphql.schema.invalid` | parse | Problems found; wraps `Problems` (`errors.As`), `Details["problems"]` is the count | **usable**; broken references resolve to nil |

### Round trip and fixtures

`Schema.Introspection()` / `MarshalIntrospection()` write the model back in
the full query's shape, with null where a field does not apply to a kind
(like graphql-js). The converter is tested for loss by converting each
fixture in `internal/core/graphql/schema/testdata` and back, and comparing
the JSON semantically:

- null, false, `[]` and a missing key are the same (older servers omit newer
  fields; `interfaces: null` on old interfaces);
- `""` equals null for descriptions, deprecation reasons and
  `specifiedByURL`;
- `types`, `directives`, `possibleTypes` and directive `locations` are sets;
  every other list (fields, arguments, input fields, enum values,
  interfaces) must keep its order, and a second test checks the model's
  declaration order directly.

Fixtures (provenance in `testdata/README.md`): three real public schemas
(countries, full query; SWAPI and Rick and Morty, conservative query), the
real rejection of the full query by an older server, and a hand-authored
GitHub-style subset covering what the real ones lack (interfaces
implementing interfaces, unions, `@oneOf`, `specifiedByURL`, deprecated
arguments/input fields/enum values, repeatable directives, nested lists,
mutation and subscription roots).

### From SDL (GQL-11)

`ParseSDL(src)` parses a schema file with `parser.ParseSchema` and builds
the model with `FromSDL(doc)`:

| Result | When |
|---|---|
| schema, nil | A valid type system. |
| nil, `graphql.schema.sdl_syntax` (kind parse) | Syntax errors. The error wraps `Problems`, one per syntax error with its position; the message names the first ("… at line 3, column 5: …"). |
| schema, `graphql.schema.invalid` (kind parse) | Type system errors. The error wraps `Problems` sorted by position; the schema is usable, as for `ParseIntrospection`. |

`Problem` has `Start`/`End` (byte range) and `Line`/`Column` (1-based,
code points) for SDL problems; `String()` is `3:5: message`. Messages
are graphql-js's (`Unknown type "Foo".`).

Building: every definition is declared first, so references resolve in
any order; extensions are merged into their type in source order
(fields, values, input fields, members, interfaces, directives); without
a schema definition the roots are the types named `Query`, `Mutation` and
`Subscription`. `@deprecated(reason:)` (default "No longer supported"),
`@specifiedBy(url:)` and `@oneOf` set `Deprecation`, `SpecifiedByURL` and
`IsOneOf`. Descriptions are the decoded strings (block strings dedented).
Default values are GraphQL literals in graphql-js's print style (`"x"`,
`{a: 1, b: [1, 2]}`), so they match introspection. Interfaces get their
object implementations as `PossibleTypes`. Built-in scalars, the
introspection types and the spec directives stay fallbacks: `Types` and
`Directives` list what the file declares.

Checks (the spec's type validation and the SDL rules of graphql-js's
`assertValidSDL`/`validateSchema`), each at the position of the name or
type it is about:

- names: unique types, directives, fields (across extensions), arguments,
  input fields, enum values and root operation types; one schema
  definition; no `__` prefix outside introspection; no built-in scalar
  redefined as another kind;
- references: known types; output types for fields, input types for
  arguments and input fields; extensions only of defined types of the
  same kind (`Cannot extend non-enum type "User".`);
- objects and interfaces: at least one field; implemented types are
  interfaces, once each, not the type itself; the interfaces of an
  implemented interface are implemented too; every interface field is
  provided with a covariant type, its arguments with equal types, and
  extra arguments are optional;
- unions: at least one member, object types only, each once; enums: at
  least one value; input objects: at least one field, no cycle of
  non-null input fields, `@oneOf` fields nullable without defaults;
  required arguments and input fields not deprecated;
- roots: a query root; roots are object types and all different;
- directive uses: known, allowed at the location, non-repeatable ones
  once per location (a type with its extensions counts as one), known and
  unique arguments, required arguments given. Argument values are not
  type-checked.

Tests (`sdl_test.go`): every introspection fixture printed as SDL and
loaded back gives the same introspection JSON without problems (the
checks accept real schemas); the SDL files in `testdata/sdl` (Star Wars,
GitHub-style, an Apollo Federation subgraph, the spec's examples) load
without problems and convert to introspection and back; the model
(descriptions, defaults, deprecations, extensions, roots, possible
types, `@oneOf`, `@specifiedBy`, repeatable directives); a table of type
system errors with their exact positions and messages; syntax errors.

### SDL export (GQL-20)

`SDL(s)` (`schema/print.go`) prints a model as a schema document, so an
introspected schema can be saved as a `.graphql` file
([graphql.md](graphql.md#save-as-sdl-gql-20)) and loaded back with
`ParseSDL`:

- **Contents:** a `schema { … }` definition only when `ParseSDL` would
  pick other roots without one (a root not named `Query`, `Mutation` or
  `Subscription`, or a type of that name that is not the root) or the
  schema has a description; then the directives and the types in the
  model's order. Built-in scalars, introspection types and the spec
  directives are left out (`IsBuiltin`), as `ParseSDL` supplies them.
  Interfaces' possible types are not written: `ParseSDL` derives them from
  the implementations. A type of an unknown kind becomes a comment.
- **Members:** `implements A & B`, union members, `@oneOf`,
  `@specifiedBy(url:)`, `repeatable`, defaults as the literals the model
  holds, and `@deprecated` (bare for the default reason, else with
  `reason:`). A type without fields, values or members is written without
  braces. Applied directives the model does not hold (anything else a
  server's SDL had) are not written.
- **Descriptions** are block strings when that reads back exactly
  (`blockStringSafe`: no `"""`, no control characters but line feeds and
  tabs, first and last lines not blank, and some unindented line, so the
  block string's dedent removes only the printer's indentation; a single
  line also must not end with `"` or `\`), else quoted strings with
  escapes.
- **Layout:** the text goes through `printer.PrettifySchema`
  ([Printer](#printer-graphqlprinter)): graphql-js's `printSchema` style,
  argument lists broken over lines past 80 columns or when an argument
  has a description; it ends with one line break. `SDL(nil)` and an
  empty schema are `""`.

Tests (`print_test.go`): every introspection fixture goes introspection
JSON → model → SDL → `ParseSDL` with no problems and the same
introspection JSON (`assertSameIntrospection`, without the built-ins),
and printing the loaded schema gives the same text; the SDL files of
`testdata/sdl` likewise; descriptions with blank, indented, quoted,
escaped, tabbed, `\r\n`, control-character and non-ASCII text on every
member come back exactly, block strings where safe; the schema definition
rules; and the exact layout of a small schema.

### Schema cache (`graphql/schemacache`)

`schemacache.Store{Dir}` keeps fetched schemas in a workspace's local
directory, one file per endpoint (GQL-21): `Save(*Entry)`,
`Load(endpoint) *Entry` (nil for a missing or unusable file), `Path`.
`Parse(data)` is `ParseIntrospection` that accepts a schema with broken
references and returns their count; `TypeCount` counts named types
without the introspection ones. Fetching is the app's GraphQL service
([graphql.md](graphql.md#schema-gql-10-gql-21)).

## Validation (`graphql/validate`)

`internal/core/graphql/validate` checks an executable document against a
schema model and returns editor diagnostics (GQL-12). It works on the
parser's partial trees, so it can run on every keystroke next to the syntax
errors.

```
parser.Parse(src) ──▶ *ast.Document ──┐
                                      ├──▶ Validate ──▶ []Diagnostic{Code, Severity, Start, End, Message, Suggestions}
schema model ─────────────────────────┘      │
                                             └─ one Walk: TypeInfo + every rule's Visitor in parallel
```

### API

| Item | Purpose |
|---|---|
| `Validate(schema, doc) []Diagnostic` | Runs `DefaultRules()`. Diagnostics are sorted by `Start`; a nil schema or document gives nil. |
| `ValidateRules(schema, doc, rules)` | Runs another rule list, such as a subset of `DefaultRules()` or custom rules. |
| `Diagnostic{Code, Severity, Start, End, Message, Suggestions}` | Byte range `[Start, End)` of the precise node; convert with `lexer.UTF16Index`. JSON tags for the Wails binding. |
| `Rule{Name, New func(*Context) Visitor}`, `Visitor{Enter, Leave}` | A rule builds a visitor per validation. `Enter` returning false skips the subtree for that rule only. |
| `Context{Schema, Doc, TypeInfo}`, `Report`, `Fragment(name)` | Shared state: report a diagnostic, look up a fragment definition (the first of that name). |
| `Context`: `FragmentSpreads(def)`, `RecursivelyReferencedFragments(op)`, `VariableUsages(def)`, `RecursiveVariableUsages(op)` | Document-level facts for the fragment and variable rules, computed once per validation and shared by the rules (graphql-js's `ValidationContext`). A `VariableUsage` holds the variable, the expected type, the default of the argument or input field, and the enclosing input type. |
| `Walk(root, typeInfo, visitors...)` | Depth-first walk keeping a `TypeInfo` in step with parallel visitors. |
| `TypeInfo`: `Enter`, `Leave`, `EnterPath` | Schema context of the current node, reusable outside validation (autocomplete: `EnterPath(ast.NodePathAt(doc, cursor))`). |
| `TypeInfo`: `ParentType`, `Type`, `FieldDef`, `Directive`, `Argument`, `InputType`, `ParentInputType`, `DefaultValue`, `EnumValue` | The composite type of the enclosing selection set; the output type of the operation, fragment or field; the definitions in effect; the expected type of a value and the default of the argument or input field it is for. |
| `TypeFromAST(schema, astType)` | `[ID!]!` in a document to a `*schema.TypeRef`. |
| `SuggestionList(input, options)`, `MaxSuggestions` | graphql-js's "Did you mean" ranking. |

### TypeInfo

`TypeInfo` mirrors graphql-js's. An operation pushes its root type (nil when
the schema has no such root); a fragment definition or inline fragment its
type condition (an inline fragment without one keeps the enclosing type); a
selection set its composite type; a field its definition and type
(including `__typename`, and `__schema`/`__type` on the query root); a
directive its definition; an argument its definition and input type; a list
value its item type; an object field its input field type. Arguments and
object fields also push their definition's default value (list items and
variable defaults push none). Fragment spreads
are not followed: a fragment definition is checked once, against its own
type condition. Anything unresolvable is nil, and so is everything under
it. Arguments inside a directive belong to the directive even when the
directive is unknown, never to the field it is on.

### Rules

| Rule (graphql-js name) | Code | Range | Message (graphql-js wording) | Suggestions |
|---|---|---|---|---|
| FieldsOnCorrectType | `graphql.validate.unknown_field` | field name | `Cannot query field "logn" on type "User". Did you mean "login"?`; on an interface or union: `Did you mean to use an inline fragment on "Starrable" or "Repository"?` | similar field names |
| KnownArgumentNames (fields and directives) | `graphql.validate.unknown_argument` | argument name | `Unknown argument "iff" on directive "@include". Did you mean "if"?` | similar argument names |
| ProvidedRequiredArguments (fields and directives) | `graphql.validate.missing_argument` | field or directive name | `Field "repository" argument "name" of type "String!" is required, but it was not provided.` | — |
| ValuesOfCorrectType | `graphql.validate.invalid_value` | value | `Expected value of type "Int", found "10".`; `…; Int cannot represent non 32-bit signed integer value: 2147483648`; `…; Enum "SearchType" cannot represent non-enum value: "ISSUE". Did you mean the enum value "ISSUE"?` | enum values, for a string where an enum is expected |
| | `graphql.validate.unknown_enum_value` | value | `Value "ISSUES" does not exist in "SearchType" enum. Did you mean the enum value "ISSUE"?` | similar enum values |
| | `graphql.validate.unknown_input_field` | input field name | `Field "directon" is not defined by type "IssueOrder". Did you mean "direction"?` | similar input field names |
| | `graphql.validate.missing_input_field` | object value | `Field "IssueOrder.direction" of required type "OrderDirection!" was not provided.` | — |
| | `graphql.validate.one_of` | object value, or the null | `OneOf Input Object "RepositoryLookup" must specify exactly one key.`, `Field "RepositoryLookup.id" must be non-null.` | — |
| KnownTypeNames (variable types, type conditions) | `graphql.validate.unknown_type` | type name | `Unknown type "Strin". Did you mean "String" or "URI"?` | similar type names, built-ins included |
| ScalarLeafs | `graphql.validate.selection_on_leaf` | selection set | `Field "login" must not have a selection since type "String!" has no subfields.` | — |
| | `graphql.validate.missing_selection` | field name | `Field "viewer" of type "User!" must have a selection of subfields. Did you mean "viewer { ... }"?` | — |
| UniqueOperationNames | `graphql.validate.duplicate_operation_name` | name of every later operation | `There can be only one operation named "A".` | — |
| LoneAnonymousOperation | `graphql.validate.anonymous_operation` | keyword of each anonymous operation, or `{` of a shorthand | `This anonymous operation must be the only defined operation.` | — |
| KnownOperationTypes | `graphql.validate.unknown_operation_type` | keyword, or `{` | `The mutation operation is not supported by the schema.` | — |
| SingleFieldSubscriptions | `graphql.validate.single_field_subscription` | name of the first field of each extra response key; name of an introspection field | `Subscription "S" must select only one top level field.`, `Anonymous Subscription must not select an introspection top level field.` | — |
| FragmentsOnCompositeTypes | `graphql.validate.fragment_on_non_composite` | type name | `Fragment "F" cannot condition on non composite type "String".` (inline: `Fragment cannot…`) | — |
| VariablesAreInputTypes | `graphql.validate.non_input_variable_type` | the variable's type (`[User!]!`) | `Variable "$a" cannot be non-input type "User".` | — |
| UniqueFragmentNames | `graphql.validate.duplicate_fragment_name` | name of every later fragment | `There can be only one fragment named "F".` | — |
| KnownFragmentNames | `graphql.validate.unknown_fragment` | spread name | `Unknown fragment "UserFeilds". Did you mean "UserFields"?` (graphql-js has no suggestions) | similar fragment names of the document |
| NoUnusedFragments | `graphql.validate.unused_fragment` | fragment name | `Fragment "C" is never used.` | — |
| PossibleFragmentSpreads | `graphql.validate.impossible_fragment_spread` | inline fragment's type name, or spread name | `Fragment "I" cannot be spread here as objects of type "User" can never be of type "Issue".` | — |
| NoFragmentCycles | `graphql.validate.fragment_cycle` | name of the spread that closes the cycle | `Cannot spread fragment "A" within itself via "B", "C".` | — |
| UniqueVariableNames | `graphql.validate.duplicate_variable` | every later definition's variable (`$a`) | `There can be only one variable named "$a".` | — |
| NoUndefinedVariables | `graphql.validate.undefined_variable` | every usage (`$t`) | `Variable "$t" is not defined by operation "Q".` (anonymous: `Variable "$t" is not defined.`) | — |
| NoUnusedVariables | `graphql.validate.unused_variable` | the variable in its definition | `Variable "$b" is never used in operation "Q".` (anonymous: `Variable "$b" is never used.`) | — |
| KnownDirectives | `graphql.validate.unknown_directive` | directive name | `Unknown directive "@inclde". Did you mean "@include"?` (graphql-js has no suggestions) | similar directive names, without `@`, spec directives included |
| | `graphql.validate.misplaced_directive` | directive name | `Directive "@skip" may not be used on QUERY.` | — |
| UniqueDirectivesPerLocation | `graphql.validate.duplicate_directive` | name of every later use | `The directive "@include" can only be used once at this location.` | — |
| UniqueArgumentNames (fields and directives) | `graphql.validate.duplicate_argument` | name of every later argument | `There can be only one argument named "owner".` | — |
| VariablesInAllowedPosition | `graphql.validate.variable_position` | the usage | `Variable "$q" of type "String" used in position expecting type "String!".` | — |
| | `graphql.validate.one_of` | the usage | `Variable "$id" is of type "ID" but must be non-nullable to be used for OneOf Input Object "RepositoryLookup".` | — |
| UniqueInputFieldNames | `graphql.validate.duplicate_input_field` | name of every later field | `There can be only one input field named "field".` | — |

All are `SeverityError`. `Suggestions` are replacements for the text in the
range, best first, at most five (quick-fixes). Values in messages are printed
compactly (`{field: CREATED_AT}`) and cut at 60 characters.

Values: literals are checked in argument values and variable default values.
`Int` takes an int literal in the 32-bit range; `Float` an int or a finite
float; `String` a string or block string; `Boolean` true or false; `ID` a
string or an int; an enum an existing enum value (a string is reported with
the matching enum values as suggestions); custom scalars accept any literal.
`null` fails only for non-null types. A list type takes a list of items or a
single item (input coercion). An input object takes an object with known
fields and every non-null field without a default, and a `@oneOf` input
object exactly one field, not null. Variables are skipped here: whether a
variable fits its position is `VariablesInAllowedPosition`.

Suggestions follow graphql-js's `suggestionList`: optimal string alignment
distance (Levenshtein plus adjacent transpositions) on lower-cased names, a
case-insensitive match scoring 1, kept when the distance is at most
`floor(len(input) * 0.4) + 1`, ordered by distance and then naturally (`a2`
before `a10`), five at most. An unknown field on an interface or union
suggests the possible types, and their interfaces, that have the field: most
used first, interfaces before their implementations (graphql-js's
`getSuggestedTypeNames`).

Ranges: graphql-js reports some errors with several locations (every
duplicate, every spread of a cycle). `Diagnostic` has one range, so each
rule picks the node an editor should underline: the later duplicates (one
diagnostic each, the first occurrence stays clean), the usage of an
undefined variable (one diagnostic per usage), the name of an unused
variable or fragment, and the spread that closes a cycle. A usage in a
fragment shared by several operations that lack the variable is reported
once per operation, with the operation's name in the message.

Document rules:

- `DefaultRules()` keeps the rules from FieldsOnCorrectType to ScalarLeafs
  first, then the document rules in
  graphql-js's `specifiedRules` order (KnownOperationTypes with the other
  operation rules); diagnostics on the same `Start` come in that order.
- ExecutableDefinitions is not a rule here: the parser already reports type
  system definitions as `not executable`.
- Operations and fragments: fragments and operations have separate
  namespaces. KnownOperationTypes makes the whole operation unresolvable,
  so nothing below it is reported except name-based variable and fragment
  rules. SingleFieldSubscriptions collects root fields through
  inline fragments and spreads by response key (alias or name), without
  evaluating `@skip`/`@include` or type conditions; it only runs when the
  schema has a subscription root.
- Fragment spreads: PossibleFragmentSpreads checks that some object type is
  a possible type of both the parent type and the type condition
  (`doTypesOverlap`). A spread of an unknown fragment or on an unknown or
  non-composite type is left to the rule for that. NoUnusedFragments counts
  fragments reachable from any operation, so fragments used only by unused
  fragments are unused too. NoFragmentCycles runs graphql-js's depth-first
  search: every fragment is entered once and each back edge is one
  diagnostic; the message lists at most ten fragments, then `and N more`.
- Variables: usages come from a separate `TypeInfo` walk of each operation
  and fragment definition, cached, so a fragment's usages are typed once by
  its own type condition and shared by every operation that reaches it.
  Variables in variable definitions (defaults, directives) are not usages.
  Transitive usages follow each fragment once per operation (cycles are
  cut). VariablesInAllowedPosition follows `allowedVariableUsage`: a
  nullable variable may go to a non-null position when the variable has a
  non-null default or the argument or input field has a default (`@defer(if:
  $d)`, `@cost(weight: $w)`); otherwise the types must be compatible
  (`isTypeSubTypeOf`; `ID!` does not fit `[ID!]!`). It also reports nullable
  variables for a `@oneOf` field. Undefined variables, and variables of
  unknown or non-input types, are left to the other rules.
- Directives: the location comes from the node the directive is on
  (`QUERY`/`MUTATION`/`SUBSCRIPTION`, `FIELD`, `FRAGMENT_DEFINITION`,
  `FRAGMENT_SPREAD`, `INLINE_FRAGMENT`, `VARIABLE_DEFINITION`). Spec
  directives count as known even when the server did not list them.
  Repeatable and unknown directives are not checked for duplicates.

Follow-up: OverlappingFieldsCanBeMerged (fields with the same response key
that differ in name, arguments or type) is not implemented yet.

### Half-typed documents

- Placeholders are never reported: a missing name (field, argument, input
  field, type, directive) or an `InvalidValue`; the parser already reported
  them. Every diagnostic has a non-empty range.
- An unknown field, type condition or root type makes the types below it
  nil, and the rules skip them: `{ viewr { login bogus } }` reports `viewr`
  only. An inline fragment with a known type condition inside such a
  subtree is checked again, since its type is known.
- A field whose argument list is unclosed is not reported for a missing
  selection set: the parser cannot have reached it.
- Unused variables are not reported for an operation whose selection set is
  missing or unclosed, or that reaches a spread without a name (`...`) or of
  an unknown fragment: the part not typed yet may use them. Unused
  fragments are not reported while the document has a spread without a
  name.
- Variables, fragments, operations and directives with a placeholder name
  are skipped by every document rule (`$` alone is not an undefined
  variable).
- Transitive work is bounded: spreads, referenced fragments and usages are
  computed once per definition; cycle detection enters each fragment once
  (a 2000-fragment cycle shared by 300 operations validates in well under a
  second, `TestLargeFragmentGraph`).
- An input object that is unclosed, holds a placeholder name or value, or
  lost tokens to the parser (`{: "x"}` parses as `{}`) is not checked for
  missing required fields or the `@oneOf` key count; the fields present
  still are.

### Tests

- One table per rule against the stored schema fixtures (countries, SWAPI,
  Rick and Morty, the GitHub subset). Expected ranges are marked inline, as
  in `{ viewer { ⟦logn⟧ } }`; `⟦⟦repository⟧⟧` expects two diagnostics on the
  same range. Each case asserts the code and exact range, and the message
  and suggestions where they matter.
- A realistic valid document per fixture (fragments, inline fragments,
  variables with defaults, directives, lists, input objects, `@oneOf`, meta
  fields, mutation and subscription), and a multi-operation document with
  fragments shared across operations and variables used only through
  fragments, produce no diagnostics.
- Document rules: one table per rule with multi-operation documents and
  shared fragments (a usage reported once per operation), every directive
  location, a repeatable directive (the `github+tag` variation of the GitHub
  fixture), and a long fragment cycle shared by many operations.
- Half-typed documents; `TypeInfo` at cursor offsets through
  `ast.NodePathAt` and `EnterPath`, and balanced stacks after `Walk`;
  graphql-js's `suggestionList`, `naturalCompare` and `didYouMean` cases.
- `FuzzValidate` validates whatever the parser makes of arbitrary input
  against the fixtures, seeded with the parser's golden sources and fuzz
  corpus: no panic, known codes, non-empty in-range sorted diagnostics,
  deterministic output, and `TypeInfo` at any offset. Run it with
  `go test ./internal/core/graphql/validate -run '^$' -fuzz FuzzValidate -fuzztime 60s`.

## Completion (`graphql/complete`)

`internal/core/graphql/complete` works out what can be typed at the cursor:
the Go side of schema-aware autocomplete (GQL-13). The CodeMirror editor
calls it through `GraphQLService.Complete`, which converts the offsets and
uses the parsed schema cached in memory ([graphql.md](graphql.md#ui)).

### API

| Item | Purpose |
|---|---|
| `Complete(s *schema.Schema, src string, offset int) Result` | Suggestions at byte offset `offset`. A nil schema completes syntax only (keywords, built-in scalars and directives). Offsets are clamped. |
| `Result{Context, From, To, Prefix, ParentType, Owner, ExpectedType, Items}` | `[From, To)` is the byte range an item replaces; `Prefix` is the text before the cursor the items were filtered by. `ParentType` is the selection set's type (fields, spreads, type conditions) or the input object (object fields); `Owner` is `Type.field` or `@directive` for arguments; `ExpectedType` is the value's type, such as `[ID!]!`. |
| `Item{Label, Kind, Detail, Documentation, Deprecated, DeprecationReason, InsertText, Suffix, SuffixCursor, SortText}` | `Detail` is a type string (`[User!]!`, `Int = 10`, `on User`, `(if: Boolean!)`, `input object`); `Documentation` is the schema description. |
| `Context` | `document`, `selection`, `fragment-spread`, `type-condition`, `argument`, `value`, `variable`, `object-field`, `variable-type`, `directive`, `keyword`, and `none`, `comment`, `string` without items. |
| `ItemKind` | `field`, `argument`, `type`, `enum-value`, `directive`, `variable`, `fragment`, `keyword`, `input-field`. |

**Offsets.** Everything is in bytes. The editor converts its UTF-16 cursor
with `lexer.NewUTF16Index(src).ToByte` before calling, and `From`/`To` back
with `ToUTF16` (see `TestUTF16`).

**Replace range.** With the cursor in or right after a name, the range is the
whole name (`us|er` replaces `user`) and the prefix is the part before the
cursor; for variables it includes the `$`. Elsewhere it is empty at the
cursor.

**Insert text and hints.** `InsertText` is plain: the label. `Suffix` is an
optional hint an editor may add, with the cursor `SuffixCursor` bytes in:
`()` for a field or directive with required arguments, ` {  }` for a field
of a composite type, `: ` for an argument or input field. A hint is left out
when the delimiter is already the next token (`user|(id: 1)`).

### Contexts

The document is lexed (to find the name at the cursor and rule out comments,
strings and positions right after a number, string, `)`, `]`, `}` or `!`) and
parsed; `ast.NodePathAt` gives the nodes around the cursor, placeholders
included, and delimiter offsets tell "before `(`" from "inside".

| Cursor | Context | Suggestions |
|---|---|---|
| `\|`, `que\|`, between definitions | `document` | `query`, `mutation` and `subscription` (when the schema has the root), `fragment`, `{` |
| `fragment F \|`, `fragment F on\|` | `keyword` | `on` |
| `{ \|`, `{ us\|`, `{ user { \| }`, after a selection | `selection` | Fields of the parent type, then `__typename` (and `__schema`, `__type` on the query root) |
| `{ ...\|`, `{ ...Fr\|` | `fragment-spread` | Fragments defined in the document whose type condition can apply to the parent type (unknown types are offered), not the enclosing fragment; `on` |
| `{ ... on \|` | `type-condition` | The parent type, object types, then interfaces and unions whose possible types overlap the parent's |
| `fragment F on \|` | `type-condition` | All composite types |
| `{ user(\|`, `{ user(id: 1, \|`, `@include(\|` | `argument` | Arguments not provided yet (the one being edited stays) |
| `{ user(id: \|`, `[\|`, `{a: \|`, `$v: T = \|` | `value` | Enum values; `true`/`false` for Boolean; `null` when nullable; variables of the operation that fit |
| `(id: $\|` | `variable` | Variables of the operation that fit |
| `(input: {\|` | `object-field` | Input fields not provided yet |
| `query Q($id: \|`, `[\|` | `variable-type` | Scalars, enums and input objects, including undeclared built-in scalars |
| `@\|` | `directive` | Directives allowed at the location (`QUERY`, `MUTATION`, `SUBSCRIPTION`, `FIELD`, `FRAGMENT_DEFINITION`, `FRAGMENT_SPREAD`, `INLINE_FRAGMENT`, `VARIABLE_DEFINITION`), without non-repeatable ones already present; spec directives the server did not list are included |
| comments, strings | `comment`, `string` | none |
| names being declared (operation, fragment, variable, alias), SDL, after literals | `none` | none |

Rules worth knowing:

- Types come from a small local resolver that follows only the path to the
  cursor (`typeinfo.go`): operation root, field types, type conditions,
  argument and input field types, list item types (a non-list type keeps its
  type, as input coercion allows) and variable definition types. Unknown
  names make the types unknown and the dependent suggestions empty. The
  validator has its own full walker; the two can be unified later.
- Variables fit by the spec's type compatibility, except that a nullable
  variable is allowed in a non-null position (it is valid with a default).
  Variables whose type is unknown are offered. Default values and the
  directive arguments of a variable definition are constant: no variables.
  Fragment definitions offer no variables.
- A missing `:` (`user(id |`) means another argument name, not a value.

### Ranking

Items are filtered by the prefix and sorted by:

1. match: exact-case prefix, then case-insensitive prefix, then fuzzy. Fuzzy
   ignores case and needs the first typed character to start a word of the
   name (its start, a capital after a lower-case letter, or after `_`), with
   the rest in order: `ur` matches `mirrorUrl`, `na` does not match
   `hasWikiEnabled`;
2. `__` names last;
3. deprecated items after the rest (still included);
4. the context's class: required arguments and input fields first; enum
   values and booleans, then variables, then `null`; for type conditions the
   parent type, then objects, then abstract types;
5. schema declaration order (document order for fragments and variables).

`SortText` numbers the result (`0000`, `0001`, ...) for editors that sort by
their own key.

### Tests

- `TestComplete`: table-driven, with a `|` cursor in the query text, against
  the GitHub subset, countries and SWAPI fixtures and no schema. Each case
  checks the context, the replaced text, the labels (exact, leading, present
  or absent) and the parent type, owner or expected type. It covers every
  context above and half-typed documents: `{ user(`, `{ user(id: `,
  `query Q($id: `, `{ ... on `, `{ user { na`, sets unclosed at the end of
  input, placeholders, and the cursor right after `{`, `(`, `[`, `$`, `@` or
  `...`.
- `TestItemDetails` (types, descriptions, deprecation, hints, sort text),
  `TestRanking`, `TestRepeatableDirective`, `TestUTF16`.
- `FuzzComplete` runs every offset (sampled for long inputs) of fuzzed
  documents and checks that nothing panics, the range holds the cursor and
  lies in the source, the prefix precedes the cursor, and items are well
  formed. Run it with
  `go test ./internal/core/graphql/complete -run '^$' -fuzz FuzzComplete -fuzztime 60s`.

## Docs search (`graphql/docs`)

`internal/core/graphql/docs` is the search of the docs explorer (GQL-14,
[graphql.md](graphql.md#docs-explorer-gql-14)).

| Item | Purpose |
|---|---|
| `NewIndex(s) *Index` | Indexes every type of the schema (server order, duplicates once) with its fields, their arguments, input fields and enum values, names lowered in advance. Read-only and safe for concurrent searches. |
| `(*Index).Search(query, limit) ([]Hit, int)` | The best `limit` hits, best first, and how many entries matched. A blank query matches nothing. |
| `Hit{Kind, Name, Type, Member, Parent, Detail, Description, Deprecated}` | `Kind` is `type`, `field`, `input-field`, `enum-value` or `argument`; `Type` and `Member` say what to open (an argument opens its field); `Parent` is `Type` or `Type.field`; `Detail` the kind of a type or the type of a member; `Description` its first line, at most 140 runes. |
| `NormalizeQuery`, `Summary` | The query lowered without spaces; a description's first line, shortened with "…". |

**Matching** ignores case and spaces. Tiers, best first: the whole name,
a prefix, a substring at a word start (camelCase hump, after `_`), any
substring, then the letters in order with the first one starting a word
(`rpo` finds `Repository`). `Type.member` matches the part before the last
dot against the parent type's name and the rest against the member (tiers
add up); `Type.` lists the members of matching types. Types and members of
introspection types (`__Type`, …) take part only when the query starts
with `__`.

**Ranking:** the tier, then types before fields, input fields, enum values
and arguments, then the shorter name, then by name and parent. The best
`limit` are kept in a bounded heap while every entry is scanned, so a
search allocates only for its hits: about 5 ms for 380 000 entries.

## Generated documents (`graphql/generate`)

`internal/core/graphql/generate` writes documents from a schema for the
editor's schema actions ([graphql.md](graphql.md#schema-actions-gql-15-gql-16-gql-17)):
a whole operation or fragment from the docs explorer (GQL-16), the
fields to fill in at a cursor (GQL-17), and what a name in a document
refers to, for the jump to the docs (GQL-15).

| Item | Purpose |
|---|---|
| `Operation(s, op, field, opts, taken) Result` | `query User($id: ID!) { user(id: $id) { … } }` for a root field of `op`; the name is the field's, capitalised, numbered while `taken` reports it used. |
| `Fragment(s, type, field, opts, taken) Result` | `fragment UserFields on User { … }` for a composite type, or `fragment UserPosts on User { posts { … } }` for one field. |
| `Fill(s, src, off, opts) FillResult` | The fields of the selection set at byte offset `off` that are not selected yet, and the byte range `[From, To)` they replace. |
| `TargetAt(s, src, off) (Target, bool)` | What the name at `off` refers to: `Kind` (`type`, `field`, `argument`, `input-field`, `enum-value`, `directive`), the `Type` page and `Member` to show (an argument shows its field), or the `Directive`; with the name's byte range. |
| `Options{Depth}` | Selection sets written below the start (1: its leaf fields only); `DefaultDepth` 2, at most `MaxDepth` 5. |
| `Result{Text, Name, Truncated, Reason}`, `Reason` | `Text` is in Prettify's layout. `Reason` says why nothing was written: `unknown-type`, `unknown-field`, `required-arguments` (a field fragment cannot declare variables), `not-in-selection`, `nothing-to-add`. |

**Selection.** Depth counts selection sets: at the last one only leaf
fields (scalars, enums) go in, and a composite field is selected only
when something fits inside it; a set that would be empty at the start
gets `__typename`. **Recursion** is cut: a field whose type is already
on the way down (`User.friends` inside `User`, `Query.query`) is left
out, so cycles never repeat, whatever the depth. Deprecated fields and
optional arguments are left out. **Abstract types** select `__typename`,
an interface's own fields, and an inline fragment per possible type with
the fields it adds; a field that would not merge with a same-named one
of another fragment (other type or arguments) gets an alias
(`valueB: value`). **Arguments:** in an operation, every required
argument (non-null without a default) of any selected field becomes a
variable named after the argument, or `fieldArg` when that is taken;
fragments and filled fields cannot declare variables, so they leave out
fields with required arguments. **Size:** at most `MaxFields` (1500)
selections per action; leaf fields get the budget before composite ones,
and `Truncated` says the cut happened.

**Fill** needs the cursor between the braces of a selection set, not
inside a token, argument list, string or comment, and at most at the
start or end of a selection. It leaves out the fields selected already
(by field name), the inline fragments whose type is there, and names an
alias already uses. The fields go on lines of their own, indented like
the set's first field on its own line (else one level past the `{`
line): a blank cursor line is replaced, text after the cursor moves to
the next line.

**Tests** (`generate_test.go`): exact output for examples (variables,
depth, recursion, required arguments, unions and interfaces with
aliases); on a generated SDL schema of 300 object types full of cycles
(next, parent, self, lists, interfaces, unions of types whose fields
share names with other types, recursive inputs, all three roots), every
operation and type fragment at the default depth and a sample at depths
1 and 3, and field fragments, are parsed and validated with the default
rules (fragments without NoUnusedFragments) plus a strict
fields-can-merge check the validator does not run; Fill on every
composite type at depths 1 and 2 validates and keeps Prettify's layout;
the budget; `TargetAt` for every kind of name.

## Printer (`graphql/printer`)

Prettify and minify for GraphQL documents (GQL-18). The printer
formats a document from its tree, not from its text, so the output always
has the same meaning as the input.

### API

| Item | Purpose |
|---|---|
| `Prettify(src) (string, []parser.Error)` | Parse and lay out, keeping comments. |
| `Minify(src) (string, []parser.Error)` | Parse and print on one line without comments. |
| `PrettifySchema(src) (string, []parser.Error)` | Parse SDL with `parser.ParseSchema` and lay it out, keeping comments. |
| `Print(doc)`, `PrintMinified(doc)` | The same on a document already parsed. |
| `MaxLineLength` (80), `Indent` (two spaces) | Layout constants. |

Only documents without syntax errors are formatted: with errors, both
functions return them and no text, and nothing is half-formatted. Type
system definitions are syntax errors (`ErrNotExecutable`) for `Prettify`
and `Minify` (`PrettifySchema` formats SDL), so a pasted
schema is refused too. A document with no definitions (blank, or only
comments) is valid: blank stays blank, comments stay.

### Prettify layout

- The graphql-js `print` style: definitions separated by a blank line;
  one selection per line, indented by two spaces; `query Q($a: Int = 1)
  @d {`; `alias: field(arg: $v) @include(if: true) {`; `... on T {`;
  object values as `{a: 1, b: [1, 2]}`; `query ($a: Int)` for an anonymous
  operation with variables. The query shorthand `{ ... }` and an explicit
  `query` keyword are kept as written.
- Argument and variable definition lists stay on one line unless the line
  would be longer than 80 code points; then they get one entry per line
  with the closing `)` on its own line. List and object values stay on one
  line.
- Strings, block strings and numbers are printed as written (`"a\u00e9"`,
  `1.50e+3`, block string lines and indentation).
- Commas, the byte order mark and blank lines inside definitions are
  dropped. The output does not end with a line break.

### SDL layout

`Print` and `PrintMinified` also print type system documents, in
graphql-js's `print` style: a description on its own line before what it
describes, printed as written; `type User implements Node & Entity
@key(fields: "id") {`, one field, enum value or input field per line;
`union U = A | B`; `directive @d(a: Int) repeatable on FIELD | OBJECT`;
`extend …` as written. A leading `&` or `|` is dropped, unless a comment
follows it. Argument definitions stay on one line unless one has a
description, a comment lies inside, or the line would pass 80 code
points. Minified, block entries are separated by commas. The SDL round
trip (`sdl_test.go`) checks the same properties as for executable
documents on the parser's SDL golden sources and comment-heavy documents,
plus that extensions, `implements`, `=` and `repeatable` survive;
`FuzzPrintSchema` runs it on fuzzed input
(`go test ./internal/core/graphql/printer -run '^$' -fuzz FuzzPrintSchema -fuzztime 60s`).

### Comments

The parser records comments in `Document.Comments`; the printer writes each
one before the first token that follows it in the source, so comments stay
in order and in the same list, argument or selection set.

- A comment that follows a token on its source line (a *trailing* comment,
  `name # shown`) stays at the end of the output line of that token.
- A comment on its own line stays on its own line, indented like the code
  around it. Before a definition it sticks to that definition, after the
  blank line.
- A list (arguments, variable definitions, list or object value) with a
  comment inside is printed one entry per line, since a comment ends its
  line.
- A comment in the middle of a field or definition header
  (`a # x` then `(y: 1)` on the next line) breaks the line there and indents
  the rest one more level. This also keeps any `query` or `fragment` name
  off column 1, where the parser's recovery would read it as a new
  definition.
- Trailing spaces and tabs of a comment are removed.

### Minify

Tokens are written without whitespace except where two would lex as one:
between name characters and digits (`query Q`, `...F ...on T`), before a
`-` or `.` after one, and between two strings. List entries and arguments
are separated by `,`. Comments are dropped. A block string keeps its line
breaks, so a document with one is not strictly on one line.

### Tests

- Table-driven tests of the layout, of comments in every position (before
  and after definitions, after `(` and `{`, before `}`, inside object and
  list values, inside a field header), of the BOM and `\r\n`, and of
  minified output, including the spaces minify must keep.
- Round-trip properties, checked on the source of every golden file in
  `parser/testdata/valid` plus documents with a comment after every token:
  the prettified and the minified document parse without errors to the
  same tree as the input (compared without positions); Prettify is
  idempotent and keeps every comment in order; Minify is idempotent; and
  minifying the prettified document equals minifying the input.
- `FuzzPrint` checks the same properties on every fuzzed input that parses
  without errors. Run it with
  `go test ./internal/core/graphql/printer -run '^$' -fuzz FuzzPrint -fuzztime 60s`.

### In the app

`FormatService` (`internal/app/services/format`) binds it next to the JSON
tools: `PrettifyGraphQL(text)` and `MinifyGraphQL(text)` return
`GraphQLFormatDTO{valid, text, error, errorCount}`. A document with syntax
errors comes back unchanged with `valid` false, the first error as
`GraphQLErrorDTO{offset, endOffset, byteOffset, line, column, message}`
(offsets and column in UTF-16 code units, for CodeMirror) and the number
of errors. In the frontend, `lib/format.ts` wraps the calls
(`prettifyGraphql`, `minifyGraphql`), and `formatGraphqlQuery(query,
"prettify" | "minify")` in `commands/graphql.ts` formats a query through a
`{get, set}` pair: it skips blank queries, leaves a query with errors alone
and returns the error for the editor's marker, and drops a result when the
text changed while Go worked.

`CheckGraphQL(text)` returns `GraphQLCheckDTO{valid, operations, error,
errorCount}`: the operations (`{name, type}`, in source order) and the
first syntax error. The GraphQL request editor uses
`GraphQLService.Diagnostics` instead, which adds validation against the
cached schema ([graphql.md](graphql.md#ui)).

The shortcut is `graphql.prettify`: ⇧⌘F (Ctrl+Shift+F), scope `graphql`, so
inside an element marked `data-shortcut-scope="graphql"` it takes the chord
from the global Beautify JSON body. The GraphQL request editor
([graphql.md](graphql.md#ui)) has Prettify and Minify buttons in its Query
tab, marks the query editor's frame with the scope, matches the chord with
`matchesShortcut(event, "graphql.prettify")` in the capture phase and calls
`formatGraphqlQuery`; an `invalid` outcome marks the first syntax error
and moves the cursor there, a `failed` one shows its
message under the editor.
