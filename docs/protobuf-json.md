# Protobuf JSON mapping

`internal/core/grpc/protojson` converts the dynamic messages of
[docs/protobuf-codec.md](protobuf-codec.md) to and from JSON with the
canonical proto3 JSON mapping, so that gRPC requests are edited as
JSON (GRPC-15) and responses, streams and `google.rpc.Status` details are
shown as JSON. It uses the standard library only.

```
JSON ──▶ protojson.Unmarshal ──▶ *dynamic.Message ──▶ dynamic.Marshal ──▶ wire bytes
wire bytes ──▶ dynamic.Unmarshal ──▶ *dynamic.Message ──▶ protojson.Marshal ──▶ JSON
```

The rules are those of protobuf-go's `encoding/protojson` (v1.36.11), the
de-facto reference implementation, down to the edge cases: 444 reference
cases written by protobuf-go must match, including byte-identical output.

## API

| Item | Purpose |
|---|---|
| `Marshal(m)`, `MarshalOptions{...}.Marshal(m)` | Message to JSON. A nil message is `{}`. |
| `Unmarshal(b, m)`, `UnmarshalOptions{...}.Unmarshal(b, m)` | Resets `m` and parses JSON into it. |
| `Resolver` | `FindMessage`, `FindExtension`, `FindExtensionByNumber`; `*desc.Pool` implements it. Finds `Any` payload types by type URL and extensions by name. |
| `Example(md, ExampleOptions{Recursion, MaxDepth})` | A message of type `md` with every field set to its default, as a template ([Example messages](#example-messages)). |
| `Complete(pool, md, text, offset)` | Field-name and enum suggestions at a byte offset of (possibly unfinished) message JSON of type `md`, for the message editor (GRPC-17; rules in [grpc.md](grpc.md#autocomplete)). |
| `Error{Offset, Line, Column, Message, Err}` | Every error. For input: the byte offset in the JSON, and the 1-based line and column (columns count characters), printed as `protojson: line 2, column 3 (offset 4): unknown field "nope"`. For output: `Offset` is -1. `Err` wraps a binary decoding error inside an `Any`. |

| MarshalOptions | Effect |
|---|---|
| `Indent` | One element per line, indented with this string (spaces and tabs only); `"name": value`; empty objects and arrays stay `{}` / `[]`. Empty means compact output. |
| `UseProtoNames` | Proto field names (`foo_bar`, group type names) instead of JSON names (`fooBar`, `json_name`). |
| `UseEnumNumbers` | Enum values as numbers. |
| `EmitUnpopulated` | Also unset fields, except oneof members and extensions: `null` for fields with presence (messages, proto2 / `optional` / editions explicit scalars), zero values, `[]` and `{}` for the others. |
| `EmitDefaultValues` | Like `EmitUnpopulated` without the `null`s. |
| `AllowPartial` | Skip the required-field check. |
| `Resolver` | Needed to write a non-empty `Any`. |

| UnmarshalOptions | Effect |
|---|---|
| `DiscardUnknown` | Skip unknown fields, unknown extension names, unknown enum names (the field stays unset, the list element or map entry is left out), and an `Any` object without `@type` (left empty). |
| `AllowPartial` | Skip the required-field check. |
| `Resolver` | Needed for a non-empty `Any` and for `[extension]` names. |
| `RecursionLimit` | Message nesting limit, default 10,000 (as protobuf-go). Skipped unknown values count too. |

## Mapping

| Proto | JSON output | JSON input also accepts |
|---|---|---|
| message | object; fields in declaration order, then extensions by full name as `"[pkg.ext]"` | JSON names and proto names (the type name for group-like fields), in any order |
| `int32`, `sint32`, `sfixed32`, `uint32`, `fixed32` | number | a string holding a number; exponents and fractions that denote an integer (`1e2`, `"1.0"`, `-0`) |
| `int64`, `sint64`, `sfixed64`, `uint64`, `fixed64` | string (`"-5"`) | numbers, parsed from the literal so no precision is lost |
| `float`, `double` | shortest number, exponent form below 1e-6 and from 1e21 (`1e-07`), `"NaN"`, `"Infinity"`, `"-Infinity"` | numbers in strings; float input must fit a float32 (`3.5e38` is an error); underflow to 0 is accepted |
| `bool` | `true` / `false` | nothing else |
| `string` | string; only `"`, `\` and control characters escaped (`\n`, ``); invalid UTF-8 is an error | any JSON escape, including surrogate pairs |
| `bytes` | standard Base64 with padding | URL-safe and unpadded Base64 |
| enum | value name; numbers the enum does not declare as numbers; aliases write the first name | names and 32-bit numbers (also for closed enums) |
| `google.protobuf.NullValue` | `null` | `null`, `"NULL_VALUE"`, `0` |
| repeated | array | `null` (field unset); `null` elements are errors |
| map | object with sorted keys (false before true, numbers ascending, strings by bytes) | keys parsed for the key type (`"01"` and `"+1"` are accepted for integers, as `strconv` does); duplicate keys (also `"1"` and `"01"`) are errors |

- `null` leaves any field unset, except `google.protobuf.Value` fields
  (`null_value`) and `NullValue` fields.
- A field given twice (also under both names) and two members of one oneof
  are errors; a `null` member does not count as set.
- Unknown binary fields are not written. Required fields must be set unless
  `AllowPartial` (also inside lists, maps and extensions, not inside `Any`).
- Message sets are not supported (an error both ways), as in protobuf-go
  without its legacy build tag.
- JSON syntax is strict RFC 8259: whitespace is space, tab, CR and LF only;
  no comments, trailing commas, leading zeros, `+`, `.5`, `NaN` literals or
  BOM; literals and numbers must end at a delimiter; strings must be valid
  UTF-8 without raw control characters. One quirk of protobuf-go is kept: a
  number in a string may be followed by a delimiter and anything else
  (`"1 2"` is 1), and `1e` (an exponent without digits) is an integer.

## Well-known types

| Type | JSON |
|---|---|
| `Any` | `{"@type": "type.googleapis.com/pkg.Msg", ...fields}`; for a well-known payload type `{"@type": ..., "value": <special form>}`. The type is the part of the URL after the last `/`, looked up exactly. `@type` may appear anywhere in the object; empty `{}` is an empty `Any`. `value` may be left out for `Empty`. On input the payload is encoded deterministically into `value`. |
| `Timestamp` | RFC 3339 in UTC, `"1972-01-01T10:00:20.021Z"`, with 0, 3, 6 or 9 fractional digits. Input accepts offsets (`+05:00`), up to 9 fractional digits and years 0001 to 9999; lower-case `t`/`z` are rejected (Go's `time.Parse`). |
| `Duration` | `"1.000340012s"`, `"-0.500s"`: seconds with 0, 3, 6 or 9 fractional digits. Input: optional sign, up to 9 fractional digits (`"1s"`, `".5s"`, `"1.s"`), at most ±315,576,000,000 s. Output requires seconds and nanos with the same sign and in range. |
| `Struct` | object |
| `Value` | the JSON value; a string is always `string_value`, a number `number_value` (NaN and infinities cannot be written); an unset kind is an error |
| `ListValue` | array |
| wrappers (`Int64Value`, `BytesValue`, ...) | the wrapped scalar with its scalar rules (`Int64Value` as a string) |
| `FieldMask` | `"a.fooBar,b"`: paths in lowerCamelCase joined by commas. Output fails for paths that do not survive the conversion back (`foo_Bar`, `foo__bar`, `foo_`). Input rejects underscores and paths that are not dotted identifiers; surrounding whitespace of the whole string is trimmed. |
| `Empty` | `{}` |

The special forms apply wherever the type appears: top level, fields, lists,
map values and `Any`. A file that redefines one of these names with other
fields gets an error instead of a guess.

`google/rpc/status.proto` and `google/rpc/error_details.proto` are embedded
with the well-known types (see [docs/proto.md](proto.md#well-known-types)),
so `google.rpc.Status` with its `Any` details (`ErrorInfo`, `BadRequest`,
...) converts without the googleapis tree ([grpc.md](grpc.md#status-and-error-details)).

## Example messages

`Example` builds the template the gRPC message editor shows for a method's
input type (GRPC-16, [grpc.md](grpc.md#message)). Write it with
`MarshalOptions{EmitDefaultValues: true}` so that zero values appear, and
`AllowPartial` in case a required message field was cut off below.

| Field | Set to |
|---|---|
| scalar, enum | its `[default = ...]` (proto2), else zero; an enum's first value |
| `optional` (proto3), proto2 / editions explicit presence | the same, set explicitly so it is written |
| message, group | expanded by the same rules |
| well-known type | left empty: its special form's zero (`{}` for `Any`, `Struct` and `Empty`, `"1970-01-01T00:00:00Z"`, `"0s"`, `[]`, `""`, `0` for wrappers); a `Value` is `null` |
| repeated | one element |
| map | one entry, keyed by the key type's zero (`""`, `0`, `false`) |
| oneof | only the first member (setting another would clear it) |
| extension, field with an unresolved type | left out |

A message type that is already being expanded further up is expanded
`Recursion` more times (default 1; negative: never again), and nothing
deeper than `MaxDepth` levels (default 16) is expanded; past either limit
message fields are unset and lists and maps are empty. So a tree node
shows one level of children, whose `children` are `[]`.

## Differences from protobuf-go

- Output whitespace is stable. protobuf-go adds a random space after commas
  (compact) or after `"name":` (multiline) to discourage byte comparisons;
  PostSonde writes the layout without it. The reference cases were
  normalised the same way.
- `Error` carries a byte offset as well as line and column; messages are
  worded similarly but not identically.
- An `[extension]` name that the resolver finds for another descriptor of
  the same message name (a different pool) is rejected, since the dynamic
  message could not hold it.

## Tests

- `golden_test.go` runs the 444 protobuf-go reference cases in
  `testdata/golden.json` (see `testdata/README.md` for coverage and how they
  were generated): the same accept/reject decision for JSON input, the same
  binary encoding of accepted input, byte-identical JSON output for six
  option sets (or the same marshal error), and JSON → binary → JSON
  stability of our own output.
- `protojson_test.go`: error offsets, lines and columns; indentation; invalid
  UTF-8 output errors; closed-enum numbers; recursion limits; `Any` and
  extensions without a resolver; wrapped binary errors; message sets; schemas
  with unresolved types or redefined well-known types (errors, no panics);
  extensions from another pool.
- `example_test.go`: `Example` for every shape of the test schemas
  (scalars, oneofs, optionals, well-known types, maps, lists, proto2
  defaults, groups and closed enums, no extensions), each written and
  parsed back; a recursive schema with the default, no and deeper
  recursion; `MaxDepth` on a non-recursive chain.
- `complete_test.go`: `Complete` at every nesting level and position of
  a schema with every shape (see [grpc.md](grpc.md#tests)), and
  `FuzzComplete`: arbitrary text and offsets never panic and the range
  stays in the text.
- `FuzzUnmarshal` parses arbitrary input into 18 message types with random
  options: it must never panic, errors must be `*Error`, and accepted input
  must convert to JSON with four option sets that parses and converts again
  to the same JSON.

  ```
  go test ./internal/core/grpc/protojson -run '^$' -fuzz FuzzUnmarshal -fuzztime 60s
  ```

- The goldens' generator was also run as a differential fuzzer against
  protobuf-go (outside the repository); see `testdata/README.md`.
