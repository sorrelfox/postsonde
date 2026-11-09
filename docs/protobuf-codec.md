# Protobuf binary codec

`internal/core/grpc/protowire` and `internal/core/grpc/dynamic` encode and
decode Protocol Buffers messages at run time from the linked descriptors of
[docs/proto.md](proto.md), without generated code and with the
standard library only. The gRPC transport, the JSON message editor,
response display and server reflection build on them.

```
[]byte ──▶ dynamic.Unmarshal ──▶ *dynamic.Message (bound to *desc.Message) ──▶ dynamic.Marshal ──▶ []byte
                 │                         │
         protowire.Consume*        Get / Set / Mutable / Range / Has / Clear
```

| Package | Purpose |
|---|---|
| `grpc/protowire` | Wire primitives: varints, tags, fixed32/64, length-delimited values, groups, zigzag. Every `Consume*` checks bounds and returns `ErrTruncated`, `ErrOverflow`, `ErrFieldNumber`, `ErrWireType`, `ErrEndGroup` or `ErrGroupDepth`; nothing panics on any input. Groups are skipped iteratively (depth limit 10,000, as in protobuf-go). |
| `grpc/dynamic` | `Message`, `List`, `Map`; `Unmarshal`/`UnmarshalOptions`, `Marshal`/`MarshalOptions`, `Size`, `Equal`, `Clone`. |

## Values

A `Message` holds the set fields sorted by number (extensions included) and
the unknown fields as raw bytes. Values are plain Go values by kind:

| Kind | Go type |
|---|---|
| `double`, `float` | `float64`, `float32` |
| `int32`, `sint32`, `sfixed32` | `int32` |
| `int64`, `sint64`, `sfixed64` | `int64` |
| `uint32`, `fixed32` / `uint64`, `fixed64` | `uint32` / `uint64` |
| `bool`, `string`, `bytes` | `bool`, `string`, `[]byte` |
| enum | `dynamic.EnumNumber` (any `int32`; unknown numbers are kept for open enums) |
| message, group | `*dynamic.Message` of the field's message type |
| repeated | `*dynamic.List` |
| map | `*dynamic.Map` (keys `bool`, `int32`, `int64`, `uint32`, `uint64` or `string`) |

API, on which the JSON mapping of [docs/protobuf-json.md](protobuf-json.md)
sits:

- `New(md)`, `Descriptor()`, `FieldByName` / `FieldByNumber` (fields of the
  type, or set extensions).
- `Get(fd)` returns the value, or the default for unset scalars (the
  `[default = ...]` value, zero, or an enum's first value) and a nil
  `*Message`/`*List`/`*Map` for unset composites. `Mutable(fd)` creates and
  sets an empty message, list or map. `Set(fd, v)` type-checks `v`.
  `Has`, `Clear`, `WhichOneof`.
- `Range(fn)` visits set fields in declaration order, then set extensions by
  number.
- Presence follows the descriptor (`Field.HasPresence`): setting an
  implicit-presence field to its zero value clears it (`-0.0` is not zero);
  explicit-presence fields (proto2, proto3 `optional`, editions `EXPLICIT`,
  messages, oneof members, extensions) stay set with any value. Empty lists
  and maps count as unset. Setting a oneof member clears the other members
  (synthetic oneofs of proto3 `optional` do not).
- `List`: `Len`, `Get`, `Set`, `Append`, `AppendMessage`, `Truncate`.
  `Map`: `Len`, `Get`, `Set`, `Delete`, `Keys(sorted)`; maps remember
  insertion order, which after decoding is the wire order.
- `Unknown()` / `SetUnknown(b)`.
- `Equal(a, b)`: same type, same set fields, same unknown bytes; floats bit
  for bit except that all NaNs are equal (so a decoded message equals itself).
  `Clone()` is deep.
- A message must not contain itself (encoding would not terminate).

## Decoding

`Unmarshal(b, m)` resets `m` and decodes; `UnmarshalOptions` adds `Merge`,
`Resolver` (extensions), `DiscardUnknown`, `RecursionLimit` (default 100) and
`MaxSize` (default 64 MiB, negative for no limit). The rules match
protobuf-go and C++:

- **Scalars**: last occurrence wins. `sint32`/`sint64` use zigzag; `int32`
  keeps the low 32 bits of the varint; `bool` is any non-zero varint.
  Non-minimal varints are accepted; a tenth varint byte above 1 is an
  overflow error.
- **Repeated scalars** are accepted packed and unpacked in any mix, whatever
  the declared encoding. An empty packed run adds nothing.
- **Messages and groups**: a singular field that occurs more than once is
  merged into the existing value; each occurrence of a repeated field is a
  new element. Groups end at the end group tag with the same number.
  Editions `DELIMITED` fields are groups (`desc.KindGroup`).
- **Oneofs**: the last member on the wire wins; a message member merges only
  with itself.
- **Maps**: each entry is a message with key 1 and value 2; a missing key or
  value is the default (an empty message for message values); unknown fields
  inside entries are dropped; a repeated key replaces the value and keeps the
  key's first position.
- **Wrong wire type** for a known field (for example a varint for a string
  field): the field is kept as an unknown field, not an error. This is what
  protobuf-go does (`errUnknown` in `proto/decode.go`) and what C++ does.
- **Closed enums** (proto2, editions `CLOSED`): an unknown number goes to the
  unknown fields; in a packed run each unknown number becomes an unpacked
  varint unknown field; in a map value the whole entry becomes unknown.
  Open enums keep any number.
- **UTF-8**: `string` fields must be valid UTF-8 in proto3 and in editions
  unless `features.utf8_validation = NONE` applies (set on the field, a map
  field for its key and value, or the file; resolved from the field
  outwards); proto2 strings are not checked. Invalid UTF-8 is
  `ErrInvalidUTF8`. `bytes` are never checked. The linker keeps
  `utf8_validation` as an option, so the codec reads it from `Options`.
- **Unknown fields** (numbers the type does not declare, extensions without
  a resolver, message-set items) are kept as raw bytes, tag included, in wire
  order. Fields of messages with `message_set_wire_format` are all kept
  unknown.
- **Extensions**: with `Resolver` (a `*desc.Pool` works), numbers in an
  extension range that the resolver knows are decoded as known extensions;
  otherwise they stay unknown. Either way they re-encode losslessly.
- **Limits**: nesting deeper than `RecursionLimit` (the top-level message is
  level 1, a map entry is a level) is `ErrRecursionLimit`; inputs larger than
  `MaxSize` are `ErrTooLarge`. Field number 0, numbers above 2^29-1 and wire
  types 6 and 7 are errors.

Errors are `*dynamic.Error{Offset, Path, Err}`, printed as
`protobuf: decoding pkg.Order.items[2].name at offset 17: unexpected end of input`.
`Offset` is the byte offset in the input where the failing value (or tag)
starts; `Path` starts at the top-level message's full name and adds field
names, list indexes and map keys (`labels["k"]`). `errors.Is` works with the
`protowire` errors and `ErrInvalidUTF8`, `ErrRecursionLimit`, `ErrTooLarge`.
On error, `m` holds what was decoded before the failing field.

## Encoding

`Marshal(m)`, `MarshalOptions{Deterministic}.Marshal` / `MarshalAppend`,
`Size(m)`:

- Fields in field number order, extensions interleaved by number, then the
  unknown fields as they were decoded or set. This is the order C++ uses.
  protobuf-go writes extensions before the other fields, and `dynamicpb` in
  deterministic mode also writes real oneof members after the other fields;
  the reference test messages are laid out so that these orders agree.
- Implicit-presence fields are written only when set (non-zero); repeated
  numeric, bool and enum fields use the packed encoding when `Field.Packed`
  is set; empty lists write nothing; groups are written with start and end
  tags; map entries always contain both key and value.
- Map entries are written in insertion order, or sorted by key with
  `Deterministic` (false before true, integers ascending, strings by bytes, as
  protobuf-go's deterministic mode). Decoding keeps wire order, so decoding
  and re-encoding reproduces the input's map order either way. Use
  `Deterministic` for stable output from messages built in code.
- The only encoding error is invalid UTF-8 in a validated string field
  (`Error.Offset` is -1); it is checked before anything is written.

Decode followed by encode reproduces the input byte for byte when the input
is canonical: fields in number order, minimal varints, no zero values of
implicit-presence fields, declared packing, unknown fields last.

## Tests

- `protowire`: varint sizes and edges (10-byte negative `int32`, overflow,
  non-minimal), tag limits, fixed and length-delimited bounds, groups
  (nested, mismatched, unterminated, non-minimal end tags, depth limit),
  zigzag; `FuzzConsumeField`.
- Reference encodings in `dynamic/testdata/golden`: 31 cases written by
  protobuf-go v1.36.11 (`dynamicpb`, deterministic) from the test schemas
  `proto3.proto`, `proto2.proto`, `editions.proto` and
  `editions_file_none.proto`, each as `.bin` plus a `.txt` dump of the values
  as protobuf-go sees them. They cover every scalar type (negative `int32`,
  zigzag extremes, fixed and sfixed, NaN, ±Inf, -0, Unicode strings, bytes),
  nested messages, repeated fields packed and unpacked, maps of every key type
  with message values, oneofs, proto3 `optional` zero values, open enum
  unknown and negative numbers, proto2 defaults, groups, extensions (including
  a group extension), unknown fields and unknown groups, editions delimited
  encoding, presence, packing, closed enums and `utf8_validation = NONE`. See
  `testdata/README.md` for how they were generated.
  - `TestGoldenRoundTrip` decodes each reference, compares the value dump
    with protobuf-go's, and re-encodes it byte for byte in both modes; it
    also checks `Size`, `Clone` and decoding without a resolver.
  - `TestGoldenEncode` builds 17 of the messages through the API alone and
    compares their encodings with the references.
- Behaviour tests with hand-built wire data: mixed packed and unpacked input,
  merging and last-wins, oneofs, map entries with missing or out-of-order key
  and value, wrong wire types, closed enums, unknown field order,
  `DiscardUnknown`, `Merge`, extensions with and without a resolver, defaults
  and presence, UTF-8 rules per syntax and feature, and errors with exact
  offsets and paths (truncation, field number 0, wire type 7, lone and
  mismatched end groups, recursion limit, size limit).
- `FuzzDecode` decodes arbitrary bytes into eleven test types (seeded with the
  references) and checks that decoding never panics, that decode → encode →
  decode gives an equal message, and that the encoding is stable.
  `FuzzRoundTrip` builds messages of every shape from fuzzed values and checks
  encode → decode equality and stable re-encoding:

  ```
  go test ./internal/core/grpc/protowire -run '^$' -fuzz FuzzConsumeField -fuzztime 60s
  go test ./internal/core/grpc/dynamic -run '^$' -fuzz FuzzDecode -fuzztime 60s
  go test ./internal/core/grpc/dynamic -run '^$' -fuzz FuzzRoundTrip -fuzztime 60s
  ```

## Not yet

- Checking `required` fields, message-set item decoding, lazy decoding.
