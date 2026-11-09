# .proto files

`internal/core/grpc/proto` reads Protocol Buffers definition files in Go,
without `protoc`, using the standard library only. It is the first stage of
the gRPC client: parsing, then loading multi-file trees through
import paths and linking them into descriptors (GRPC-01, GRPC-02, GRPC-04,
IMP-09 for .proto). The binary codec for dynamic messages builds on
the linked model ([protobuf-codec.md](protobuf-codec.md)); server
reflection feeds descriptors from servers into the same model
([grpc-reflection.md](grpc-reflection.md)). The gRPC request editor loads a request's setup
through `GRPCService.Describe` ([grpc.md](grpc.md)).

```
files + import paths + overlay
        │
        ▼
.proto text ──▶ lexer ──▶ parser ──▶ ast.File ──▶ Check (single-file rules)
                                        │
                                        ▼
                     linker.Load: imports ──▶ symbols ──▶ types, extendees, rpc types
                                        ──▶ options, defaults ──▶ features ──▶ cross checks
                                        │
                                        ▼
                               desc.Pool of desc.File ...  + []linker.Diagnostic
```

| Package | Purpose |
|---|---|
| `proto/lexer` | Tokens with byte offsets, line/column and error tokens that never abort; string, integer and float literal decoding. |
| `proto/ast` | Syntax tree for proto2, proto3 and editions: byte ranges on every node, names as written, uninterpreted options, attached comments; `Inspect`, `Dump`, `File.Position`. |
| `proto/parser` | `Parse`: tokens to `ast.File` with error recovery (partial tree plus errors with line and column). `Check`: the rules that need only this file, as diagnostics separate from syntax errors. |
| `proto/linker` | `Load`: reads the given files and their imports through import paths (with an overlay for unsaved buffers), parses and checks them, resolves names across files and packages, checks extensions and options, applies syntax and features, and returns a `desc.Pool` with every diagnostic. |
| `proto/desc` | The linked, read-only descriptor model: `File`, `Message`, `Field`, `Oneof`, `Enum`, `EnumValue`, `Service`, `Method`, `Option`; `Pool` lookups and `Walk`. |
| `proto/wellknown` | The embedded `google/protobuf/*.proto` well-known type sources, `google/rpc/status.proto` and `google/rpc/error_details.proto`, searched after every import path. |

## Scope

| Dialect | Support |
|---|---|
| `syntax = "proto3";` | Full grammar and single-file checks. |
| `syntax = "proto2";` (or no syntax statement) | Full grammar: `required`/`optional`/`repeated`, groups, `extensions` ranges with options, `extend`, `default` values, `message_set_wire_format`. Real imports such as `descriptor.proto` are proto2, so they must parse. A missing syntax statement is a warning, as in protoc. |
| `edition = "2023"` / `"2024"` | Full grammar: `features.*` options, identifier reserved names, `import option` and `export`/`local` (2024). The parser keeps features as options like any other; the linker applies the ones listed under [Syntax and features](#syntax-and-features). Other editions parse and are reported by `Check`. |

Everything that needs another file or option semantics is done by the
linker (see [Linking](#linking)): type resolution, `import` loading, option
interpretation (including `json_name`, `packed`, feature resolution), JSON
name conflicts, extension number clashes and the extendee checks for
proto3 `extend`.

## Lexer

| Item | Purpose |
|---|---|
| `New(src, mode)`, `Next()`, `Tokenize(src, mode)` | Tokens one at a time or all at once, ending with `EOF`. `SkipComments` drops comments. |
| `Token{Kind, Err, Start, End, Line, Column, Message}` | `Token.Text(src)` is the raw text. |
| `StringValue(text)` | The bytes of a quoted string. Escapes: `\a \b \f \n \r \t \v \\ \' \" \?`, octal `\o` to `\ooo` (up to `\377`), hex `\xh`/`\xhh` (also `\X`), `\uhhhh` and `\Uhhhhhhhh` as UTF-8; a `\u` surrogate pair is one code point, a lone surrogate is an error. |
| `IntValue(text)` | Decimal, `0x` hex, leading-`0` octal; `ErrIntRange` above 64 bits. |
| `FloatValue(text)` | Floats (`1.5`, `.5`, `1.`, `1e9`, text-format `1.5f`), and integers used as floats; out-of-range values become ±Inf, as in protoc. |
| `IsIdent(s)` | Whether `s` is one identifier (used for reserved names). |

Kinds: `EOF`, `Error`, the symbols `; , . = : / - + ( ) [ ] { } < >`,
`Ident`, `Int`, `Float`, `String` and `Comment`. Keywords are plain `Ident`
tokens because protobuf keywords are contextual (`message message = 1;` is
legal). `inf` and `nan` are identifiers; signs are separate tokens.

Numbers follow protoc's tokenizer: `0x` must be followed by hex digits, a
leading `0` makes an octal integer (`08` is an error), and a number running
into a letter or another `.` (`12ab`, `1.2.3`) is one `ErrInvalidNumber`
token. Strings end at their line. Strings are byte strings, so invalid UTF-8
inside them is accepted; outside strings and comments only ASCII is valid.

**Positions.** `Start`/`End` are byte offsets; `Line`/`Column` are 1-based
and `Column` counts Unicode code points (protoc counts bytes and expands tabs
to 8 columns; editors want code points). `\r\n`, `\n` and a lone `\r` end a
line. A UTF-8 byte order mark at the start is skipped.

| `ErrCode` | Example |
|---|---|
| `ErrUnexpectedChar` | `@`, `é` outside a string |
| `ErrInvalidUTF8` | a `0xFF` byte outside a string |
| `ErrInvalidNumber` | `0x`, `08`, `1e+`, `12ab`, `0x1.` |
| `ErrUnterminatedString` | `"abc` up to the end of the line |
| `ErrInvalidEscape` | `"\q"`, `"\400"`, `"\uD800"` (the whole string) |
| `ErrUnterminatedComment` | `/*` without `*/` (to the end of input) |

## Syntax tree

- Every node embeds `ast.Loc{Start, End}`; `File.Position(off)` gives the
  line and column. `RBrace`, `LBrace` hold offsets, `-1` when absent.
- Declarations are `Decl` values in source order: `Syntax`, `Package`,
  `Import` (`ImportDefault`/`Public`/`Weak`/`Option`), `Option`, `Message`,
  `Field`, `MapField`, `Group`, `Oneof`, `Enum`, `EnumValue`, `Service`,
  `RPC`, `Extend`, `Extensions`, `Reserved`, `Empty` (`;`). The comment on
  `ast.Decl` lists which appear in which body.
- Names keep what a linker needs: `FullName{LeadingDot, Parts}` for type
  references (`.pkg.Msg`, `Msg.Nested`), `OptionName` parts with
  `Extension` for `(my.ext).field`. Scalars are type names like any other;
  as in protoc, the linker treats an unqualified scalar keyword such as
  `int32` as the scalar type even if a message has that name (`.pkg.int32`
  reaches the message).
- Numbers are `IntLit{Negative, Raw, Value}` (magnitude, so `-2^63` fits);
  `Int64()` gives the signed value. Option values are `IdentLit` (with
  `Negative` for `-inf`), `IntLit`, `FloatLit`, `StringLit` (adjacent
  literals concatenated, decoded), or a text-format `MessageLit` with
  `MessageLitField`s named by `FieldRef` (`name`, `[ext.name]` or
  `[type.googleapis.com/pkg.Type]`) whose values may also be `ListLit`s and
  `< >` messages. An integer too large for 64 bits becomes a `FloatLit`, as
  in protoc.
- `Message.Visibility`/`Enum.Visibility` hold edition 2024 `export`/`local`.

### Comments

Declarations embed `ast.Comments{Leading, Trailing, Detached}` computed the
way protoc fills `SourceCodeInfo`, so documentation shows the same text as
other tools:

- A comment on the line of a declaration's `;` (or its `{`) is trailing;
  so is a comment block on the following lines that is followed by a blank
  line or by a `}`.
- The comment block directly above a declaration is leading; earlier blocks
  separated by blank lines are detached.
- A single comment between two tokens on the same line belongs to neither.
- `//` is removed and each line keeps its `\n`; consecutive line comments
  join. In block comments `/*`, `*/` and one leading `*` per continuation
  line are removed.

`File.Comments` lists every comment token with its range, for highlighting.

## Parser

| Item | Purpose |
|---|---|
| `Parse(name, src) (*ast.File, []Error)` | Parse with the default limits. The file is never nil. |
| `Config{MaxDepth, MaxErrors}.Parse(name, src)` | Explicit limits (defaults 128 and 100). |
| `Error{Code, LexCode, Start, End, Line, Column, File, Message}` | `Error()` is `file:line:column: message`. Sorted by position; a final `ErrTooManyErrors` stays last. |
| `Check(file) []Diagnostic` | Single-file rules (below). |
| `Diagnostic{Code, Severity, Start, End, Line, Column, File, Message}` | Sorted by position. `SeverityWarning` does not make the file invalid. |
| `MapEntryName(field)` | protoc's generated map entry type name (`foo_bar` → `FooBarEntry`). |

| `Code` | Meaning |
|---|---|
| `ErrLexical` | A lexer error token; `LexCode` has the lexer's code. |
| `ErrUnexpectedToken` | Something else was expected: `expected a field number, found ";"`. |
| `ErrUnclosed` | `{`, `[` or `<` without its closer; the range is the opener. |
| `ErrInvalidLiteral` | An integer beyond 64 bits where one is required, an `f` suffix outside a message literal, `-` before a string, message or identifier other than `inf`/`nan`. |
| `ErrMisplaced` | A `syntax`/`edition` statement that is not first. |
| `ErrTooDeep` | Nesting beyond `MaxDepth`; the block is skipped. Reported once. |
| `ErrTooManyErrors` | The last error once `MaxErrors` is reached. |

Contextual keywords: `message`, `enum`, `oneof` and `extend` start a
declaration unless followed by `name =` (then they are a field type);
`option` starts an option when followed by an identifier or `(`;
`reserved` and `extensions` when followed by a number or a name list;
labels when followed by a type; `map` when followed by `<`; `group` when
followed by `Name =`; `stream` when followed by a type; `export`/`local`
when followed by `message` or `enum` and a name.

### Recovery

- A missing identifier becomes an `Ident` with `Value ""` at the end of the
  previous token; other missing pieces (type, number, value) are nil.
- After an unexpected token the statement is abandoned. If the token is on
  a later line than the last consumed token it starts the next statement,
  so `int32 a =` followed by a complete field on the next line loses only
  the half-typed one. Otherwise tokens are skipped through the next `;`, or
  over a `{ ... }` block, stopping before `}`.
- A missing `;` is reported as a zero-width error right after the previous
  token when the next token is on a later line or is `}`.
- `}` ends the innermost body; a stray `}` at the top level is skipped.
  Bodies still open at the end of input are reported at their `{`.
- A list literal missing `,` or `]` ends at that token.
- One error per start offset. Every loop consumes a token or ends, so
  parsing is linear; nesting is capped and deeper blocks are skipped
  without recursion (100,000 nested messages are safe).

## Check

`Check` runs on any tree, including partial ones (it skips missing pieces),
and reports:

| `DiagCode` | Rule |
|---|---|
| `DiagSyntax` | No syntax statement (warning); unknown syntax; edition other than 2023 or 2024. |
| `DiagDuplicateDecl` | A second `package`; the same import twice. |
| `DiagFieldNumber` | Field numbers outside 1 to 536,870,911, or in 19000 to 19999. Extensions may go up to 2^31-1 here (message sets); the linker checks the extendee. |
| `DiagDuplicateNumber` | A field number used twice in one message (oneof fields included). |
| `DiagDuplicateName` | A name defined twice in one scope: fields, oneofs, nested messages and enums, map entry types (`FooBarEntry`), group types and their lower-case fields, extensions in the enclosing scope, enum values as siblings of their enum (C++ scoping), rpcs in a service. |
| `DiagReserved` | A field or enum value using a reserved number or name. |
| `DiagRange` | Reserved or extension ranges out of bounds, reversed or overlapping; a field inside an extension range. `max` is 536,870,911 for fields (2^31-1 with `message_set_wire_format`) and 2^31-1 for enums. |
| `DiagLabel` | `required` in proto3; `optional`/`required` in editions; labels in oneofs and on maps; required extensions; a missing label in proto2. |
| `DiagGroup` | Groups outside proto2; group names not starting with a capital letter. |
| `DiagDefault` | `default` in proto3. |
| `DiagExtensions` | Extension ranges in proto3. |
| `DiagMapField` | Map keys other than integral types, `bool` and `string`; maps in oneofs or extend blocks. |
| `DiagEmptyOneof` | A oneof without fields. |
| `DiagEnumValue` | Empty enums; a non-zero first value in proto3 and in editions unless `features.enum_type = CLOSED` (enum or file); numbers outside int32; duplicate numbers without `allow_alias = true`; `allow_alias` without aliases. |
| `DiagReservedName` | Quoted reserved names in editions, unquoted ones in proto2/proto3, names that are not identifiers. |
| `DiagVisibility` | `export`/`local` before edition 2024. |

## Linking

`linker.Load(ctx, Options) (*Result, error)` is the entry point. The error is
only for invalid options (no files, a bad import path or overlay path) and a
cancelled context; everything wrong with the files is a diagnostic, and
every file that could be read is linked as far as it goes.

| Item | Purpose |
|---|---|
| `Options{Files, ImportPaths, FS, Overlay, NoWellKnown}` | Inputs, import roots in search order, an optional `fs.FS` instead of the OS file system, file contents that replace the disk (unsaved editor buffers; they need not exist on disk), and a switch for the built-in well-known types. |
| `Result{Files, Pool, Diagnostics, ImportPaths}` | The linked inputs in order, every loaded file (dependencies first, `descriptor.proto` included), diagnostics by file and position, and the import roots actually used. |
| `Result.HasErrors()`, `Result.Err()` | `Err` is an `errs.Parse` error with code `proto.link_failed`, the first error as message and the count in the `errors` detail, or nil. |
| `Diagnostic{Code, Severity, File, DiskPath, Start, End, Line, Column, Message}` | `File` is the import path. `Line` is 0 for file-level diagnostics. `Error()` is `file:line:column: message`, with `warning: ` for warnings. |

### Files and import paths

- A file is identified by its **import path**: its path relative to the
  import root it was found under, with forward slashes
  (`google/api/http.proto`). Imports are resolved by trying each import
  root in order, then the embedded well-known types.
- An entry of `Options.Files` that is a valid import path found under an
  import root is loaded by that path. Otherwise it is a file path: under an
  import root it gets the path relative to the first such root; under none
  it gets its base name and **its directory is added as an import path**
  after the configured ones, with a `proto.implicit_import_path` warning
  (protoc refuses such files; a GUI opening one file should still work, and
  sibling imports then resolve).
- An input that has the same import path as a file in an earlier root
  (protoc's "input is shadowed") is an error, `proto.input_shadowed`; the
  input's own content is still loaded under that path, so an open editor
  buffer is what gets linked. An input whose import path is already loaded
  from another file is the same error, and that input is skipped.
- An import found under more than one configured root uses the first and
  warns (`proto.import_shadowed`); a root that shadows a built-in
  well-known type is expected and not reported.
- Import paths must be clean, relative and use forward slashes
  (`proto.invalid_import_path`).
- A missing import is an error at the import's string,
  `"google/api/annotations.proto" not found: searched api, third_party, the
  built-in well-known types`; a missing `import weak` is only a warning.
- Import cycles are reported at the import that closes them, with the chain
  (`import cycle: "a.proto" -> "b.proto" -> "a.proto"`); the edge is dropped
  and linking continues.
- Overlay keys use the same form as `Files` (absolute after cleaning on the
  OS file system, slash paths in an `fs.FS`).

### Well-known types

`proto/wellknown` embeds the eleven files protoc ships for the well-known
types, unmodified with their BSD-3-Clause headers, from Protocol Buffers
v34.0 (`com.google.protobuf:protobuf-java:4.34.0`); see
`THIRD_PARTY_NOTICES.md`. `plugin.proto` and the language feature files
(`cpp_features.proto`, `java_features.proto`, `go_features.proto`) are not
embedded: they describe code generators, not message types; a project that
uses them must provide them in an import path. The embedded files are parsed
once per process and shared. `descriptor.proto` is always loaded, because
options are interpreted against it whether or not a file imports it.

It also embeds `google/rpc/status.proto`, the message gRPC servers put
in the `grpc-status-details-bin` trailer, so that error details decode and
show as JSON without the googleapis tree. The file is unmodified from
googleapis as packaged in `com.google.api.grpc:proto-google-common-protos:2.63.2`
(Maven Central, jar SHA-1 `da5f87b9b7ade71856fe569f89c6af036d429888`), under
the Apache License 2.0 (Copyright 2025 Google LLC); the license text is
embedded next to it as `google/rpc/LICENSE`, copied from the googleapis
repository at commit `ebd1d23ac613b177828dad42ad8dfb13ba498279`.

`google/rpc/error_details.proto`, from the same package and
unmodified, is embedded too, so that the standard error details
(`ErrorInfo`, `RetryInfo`, `DebugInfo`, `QuotaFailure`,
`PreconditionFailure`, `BadRequest`, `RequestInfo`, `ResourceInfo`, `Help`,
`LocalizedMessage`) decode by type URL (`internal/core/grpc/rpcstatus`,
[grpc.md](grpc.md#status-and-error-details)). `google/rpc/code.proto` is not embedded; a project that uses it provides a
googleapis tree in an import path, whose `status.proto` and
`error_details.proto` then shadow the built-in ones like any well-known
type.

| File | SHA-256 |
|---|---|
| `any.proto` | `bcf5de6ce463b1a38ff76b77955aa7e580b4ec12af34a927b1d45c9efb0faf66` |
| `api.proto` | `6d47d6272eb687c806b6feb08ef3925243c64ea076cd9440b783e27a7aec5502` |
| `descriptor.proto` | `32f3df357257f556b311c7e4ad33625a7aa13de541cb53a29ae85ac746c11a07` |
| `duration.proto` | `a3f7301ff2956ec2e30c2241ece07197e4a86c752348d5607224819d4921c9fe` |
| `empty.proto` | `ecef3d54cc9e079673b9816c67bac770f7f3bf6dada2d4596ba69d71daa971e6` |
| `field_mask.proto` | `9f031219ae54383afa84299fdd747e119b5f83c8e3dab060930ebb126af4460f` |
| `source_context.proto` | `37d3401de89af1d1496fc949924a5f2011bd2fadfbeb0da1caa277159a96776d` |
| `struct.proto` | `1bf07ba8b9a24e3a0390dddac3ad0db41c63287d252788b02398220b9abc900f` |
| `timestamp.proto` | `fa6c44dca2c90a2aaf3a1e4776cef2e9177f70fba365f236269f0f41ac166c96` |
| `type.proto` | `50a3b67b4d894e828173319aafcd6ac440c15340048452bd13a13e8b47cc6637` |
| `wrappers.proto` | `6878dc7534cf9805eee56345cdc38b5e9ca39dece246710c663c7007a67fef49` |
| `google/rpc/error_details.proto` | `da3f5ac1304abfd6ab2f959b4ac3e6cb0758b52fca388c226bf3b92af95e07ed` |
| `google/rpc/status.proto` | `3b5c712455570ac4342dd3c521c4c11011652ae9a0fbca75ba22fcc45c6e1991` |
| `google/rpc/LICENSE` | `c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4` |

### Names

- Every message, enum, enum value, service, method, field, oneof and
  extension is registered by full name; packages are registered with each
  parent (`google`, `google.api`). A name defined in two files, or a package
  that is also another kind of symbol, is `proto.duplicate_symbol`
  (duplicates inside one file are left to `Check`). When the two files are
  the same file reached through two import paths, the message says so; for
  enum values it explains C++ scoping.
- Resolution follows protoc: a leading dot means fully qualified; otherwise
  the first name component is searched from the innermost scope outwards,
  and the rest must resolve inside what it found. Field types skip
  non-types on single-part names; extendees and rpc types do not.
  `foo.Bar` found as `pkg.foo` but without `Bar` is reported as
  `"foo.Bar" resolved to "pkg.foo.Bar", which is not defined; names are
  searched from the innermost scope outwards, so use ".foo.Bar" ...`.
- **Visibility**: a file sees itself, its direct imports and, transitively,
  what those import publicly. Edition 2024 `import option` files are
  visible only to option names. A name defined in a file that is not
  visible is `proto.not_imported` with the file to import.
- Unknown names are `proto.unknown_type`, with `add import
  "google/protobuf/timestamp.proto";` for an unimported well-known type or a
  "did you mean" suggestion (the closest visible type by edit distance on the
  last name component, for the first 50 unknown names).
- A field type that is not a message or enum, or an extendee or rpc type
  that is not a message, is `proto.wrong_kind`.

### Model

`desc` mirrors protoc's descriptors, with pointers instead of type names:

- `File{Path, DiskPath, Root, Builtin, AST, Syntax, Edition, Package, Imports, Messages, Enums, Services, Extensions, Options, Deprecated}`.
- Every element embeds `Base{Name, FullName, File, Loc, NameLoc, Comments, Options, Deprecated}`; `Base.Position()` is the name's line and column.
- `Message{Parent, Fields, Oneofs, Messages, Enums, Extensions, ExtensionRanges, ReservedRanges, ReservedNames, MapEntry, MessageSetWireFormat}`: map fields get a generated `<Name>Entry` message (`MapEntry`, fields `key` = 1 and `value` = 2); groups get a message plus a field named in lower case with `KindGroup`; proto3 `optional` fields get a synthetic oneof (`_name`) after the real ones.
- `Field{Number, Kind, Cardinality, JSONName, HasJSONName, Message, Enum, TypeName, Parent, Oneof, IsExtension, Extendee, ExtendeeName, Proto3Optional, HasPresence, Packed, Default}`; `IsMap`, `MapKey`, `MapValue`. `Kind` numbers match `FieldDescriptorProto.Type`.
- `Enum{Parent, Values, Closed, ReservedRanges, ReservedNames}`, `EnumValue{Enum, Number}`, `Service{Methods}`, `Method{Service, Input, Output, InputName, OutputName, ClientStreaming, ServerStreaming}`.
- `Option{Name, Path, Value, Scalar, Loc}`: `Path` holds the field or extension each part of the name resolved to; `Value` is the value as written; `Scalar` is the checked value for scalar and enum options.
- `Pool`: `Files`, `FindFile`, `Find`, `FindMessage`, `FindEnum`, `FindService`, `FindExtension`, `Extensions(message)`, `FindExtensionByNumber`, `Services`; `Walk` visits a file's elements in declaration order.

Partial input: a declaration without a name is skipped; an unresolved field
type leaves `Kind` `KindUnknown` with nil `Message`/`Enum` and `TypeName` as
written; an unresolved rpc type leaves `Input`/`Output` nil.

### Extensions

- The extendee must be a message and declare the number in an extension
  range (`proto.extension_number`); above 536,870,911 only for message sets,
  whose extensions must be optional messages (`proto.invalid_extension`).
- The same number for the same extendee in any two loaded files is
  `proto.duplicate_extension`, reported at the later one in load order.
- proto3 files may only extend `google.protobuf.*Options`.

### Options

Options are interpreted against the options messages of the loaded
`descriptor.proto` (`FileOptions`, `MessageOptions`, `FieldOptions`,
`OneofOptions`, `EnumOptions`, `EnumValueOptions`, `ServiceOptions`,
`MethodOptions`, `ExtensionRangeOptions`). With `NoWellKnown` and no
`descriptor.proto` in any import path, option names and values are not
checked (only `json_name` and `default` are).

- Names: plain parts are fields of the current message; `(name)` parts are
  resolved like types from the element's scope (option imports included) and
  must be extensions of the current message; every part but the last must be
  a non-repeated message (`proto.unknown_option`).
- `json_name` and `default` are pseudo-options: `json_name` must be a string
  and is not allowed on extensions; `default` is checked against the field
  type (not on repeated or message fields; proto3 defaults are left to
  `Check`). `map_entry` and `uninterpreted_option` cannot be set.
- Values (`proto.option_value`) are type-checked for scalars and enums:
  `true`/`false`; strings for `string` and `bytes`; integers within the
  field's range; integers, floats, `inf` and `nan` for `float`/`double`;
  value names of the enum. Message-typed options take an aggregate
  `{ ... }` value, which is kept as written; its field names (`name`,
  `[ext.name]`, `[type.url/pkg.Type]` inside `Any`) and the message/list
  shape of each value are checked, but scalar values inside aggregates are
  not.
- Setting the same non-repeated option twice, including a field already set
  inside an aggregate, is `proto.duplicate_option`.
- Interpreted: `deprecated` (`Base.Deprecated`, `File.Deprecated`),
  `packed` (only on repeated numeric, bool or enum fields, not in editions),
  `message_set_wire_format`, and the edition features below.

### Syntax and features

Features are applied from file to message to field (and file or message to
enum) for `field_presence`, `enum_type`, `repeated_field_encoding` and
`message_encoding`. `features` options outside editions are an error.

| | proto2 | proto3 | editions default |
|---|---|---|---|
| Presence (`HasPresence`) of singular scalar fields | explicit | implicit (explicit with `optional`) | `EXPLICIT`; `IMPLICIT`; `LEGACY_REQUIRED` makes the field `Required` |
| Messages, oneof members, extensions | explicit | explicit | explicit |
| Repeated numeric, bool and enum fields (`Packed`) | `[packed = true]` | packed unless `[packed = false]` | `PACKED`; `EXPANDED` |
| Enums (`Closed`) | closed | open | `OPEN`; `CLOSED` |
| Message fields | length-prefixed | length-prefixed | `DELIMITED` turns the field into `KindGroup` |

Other features (`utf8_validation`, `json_format`, language features) are
kept as options and not interpreted.

### Cross checks

- **JSON names** (`proto.json_name_conflict`): two fields of one message
  with the same JSON name (the `json_name` option, or the name without
  underscores with the following letter upper-cased). In proto2 only
  conflicts involving a `json_name` option are errors, as in protoc.
- **Closed enums** (`proto.closed_enum`): a proto2 enum used by a field of a
  proto3 message.

Not checked yet: the proto3 rule that enum value names must stay distinct
after removing the enum-name prefix, unused imports, and feature support
windows by edition.

## Tests

- Realistic files in `parser/testdata/files`, written for these tests and
  modelled on common public APIs: a Google-API-style resource service with
  HTTP and resource annotations, a gRPC health service, a route-guide
  streaming service, an inventory schema with maps, oneofs, nested types,
  reserved ranges, aliases, custom options and every literal form,
  equivalents of the well-known types, a proto2 file with groups and
  extensions, and an editions file. They must parse without errors or
  diagnostics; their trees are golden dumps.
- Real-world proto2 files in `parser/testdata/realworld`, vendored
  unmodified with their license headers; sources, versions, licenses and
  checksums are in its `README.md` and in `THIRD_PARTY_NOTICES.md` (test
  data only, not shipped): `google/protobuf/descriptor.proto` from
  Protocol Buffers v34.0, and protobuf-go v1.36.11's
  `cmd/protoc-gen-go/testdata/proto2/fields.proto` and
  `internal/testprotos/textpb2/test.proto`. `realworld_test.go` requires
  zero syntax errors and zero `Check` diagnostics (warnings included) and
  checks the tree: label, message, enum, extension range and `default`
  counts; `option optimize_for = SPEED;` and `[default = false]` as raw
  identifiers; `extensions 1000 to max;` in every `*Options` message;
  extension `declaration` message literals; groups with each label, nested
  and in a oneof; string, bytes, `inf`/`-inf`/`nan` defaults; top-level and
  nested `extend` blocks; and a message set. No golden dumps, to keep the
  test data small; to update a file, copy the newer upstream version and
  adjust the counts.
- Syntax errors with exact `line:column`, code and message
  (`errorCases`), partial trees as golden files (`testdata/partial`), every
  `Check` rule with exact positions (`checkCases`), comment attachment
  against protoc's documented example, option value shapes, deep nesting and
  the error cap.
- `TestExternalCorpus` parses and checks every `.proto` file under
  `$POSTSONDE_PROTO_CORPUS` and fails on any error; it is skipped when the
  variable is unset. All 162 files in the Go module cache
  (`google.golang.org/protobuf`, `grpc` and others, proto2, proto3 and
  editions 2023/2024) pass.
- `FuzzTokenize` checks token order, whitespace-only gaps, positions against
  a naive recount and literal decoding. `FuzzParse` checks that `Parse` and
  `Check` never panic, node ranges nest within their parents without
  sibling overlap, identifiers match the source, errors and diagnostics are
  in range with matching line:column, errors are sorted and capped, and an
  error-free file has no placeholders:

  ```
  go test ./internal/core/grpc/proto/lexer -run '^$' -fuzz FuzzTokenize -fuzztime 60s
  go test ./internal/core/grpc/proto/parser -run '^$' -fuzz FuzzParse -fuzztime 60s
  ```

Linker (`linker/*_test.go`):

- `TestProject` links `linker/testdata/project` from disk: three import
  roots (`api` with a service, custom options and a proto2 extension file;
  `third_party` with Google-API-style `annotations`, `http`,
  `field_behavior`, `money` and `date` files written for the test, not
  copies of googleapis; `vendor` with a proto2 library), plus the built-in
  well-known types. It requires zero diagnostics and checks resolved types
  across packages and roots, a type reached only through `import public`,
  map entries, oneofs, synthetic oneofs, JSON names, presence and packing,
  streaming flags, extension and aggregate options, proto2 defaults, groups
  and extensions of a message in another root.
- Diagnostics with exact code, position and text: missing and weak imports
  (and `Err()`), missing inputs, cycles, visibility through public import
  chains, unknown types with suggestions and the well-known type hint, the
  innermost-scope trap, wrong kinds, duplicate symbols (including one file
  through two import paths), extension ranges, duplicate extension numbers,
  message sets, proto3 extensions, option names, values, aggregates and
  duplicates, JSON name conflicts, closed enums, invalid import paths.
- Import paths on a real temporary directory: implicit roots, shadowed
  imports and inputs, OS overlays; `fs.FS` overlays that replace and add
  files; editions features with `import option`; partial files with syntax
  errors still linking; `Load` errors and cancellation.
- `TestWellKnownTypes` links all embedded files with no diagnostics at all;
  `TestRealWorldParserData` links the vendored real-world proto2 files.
- `TestExternalCorpus` links every `.proto` file under
  `$POSTSONDE_PROTO_CORPUS`, guessing import roots from the corpus's own
  import strings; it fails on any error and skips files whose imports are
  outside the corpus. All 162 files in the Go module cache link without
  errors (14 custom options).
- `FuzzLoad` links arbitrary input next to a dependency with custom options
  and checks that `Load` never panics, diagnostics stay inside their files
  and resolved kinds have their types:

  ```
  go test ./internal/core/grpc/proto/linker -run '^$' -fuzz FuzzLoad -fuzztime 60s
  ```
