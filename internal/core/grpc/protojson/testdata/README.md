# protojson test data

The `.proto` files are test schemas written for PostSonde's JSON mapping
tests. `golden.json` holds reference results produced by protobuf-go's
`encoding/protojson`, so the mapping is compared with the de-facto reference
implementation. Nothing here is shipped.

| File | Contents |
|---|---|
| `jsontest.proto` | proto3: every scalar type, field names with underscores, digits and `json_name`, repeated fields, maps of every key type (with enum, bytes, float, message, `Value` and `NullValue` values), a oneof with scalar, message, enum, `Value`, `NullValue` and wrapper members, proto3 `optional`, every well-known type singular, repeated and as map values, `google.rpc.Status`, and a payload type for `Any`. Enum with an alias and a negative value. |
| `jsontest2.proto` | proto2: required fields (nested, in lists, maps and extensions), defaults, groups, a closed enum, top-level and nested extensions (including a group extension). |
| `jsontest_editions.proto` | Edition 2023: delimited fields with and without a group-like name, implicit, explicit and legacy-required presence. |
| `golden.json` | 444 cases (see below). |

Each case in `golden.json` has:

- `name`, `type` (the message type), and either
  - `input` (or `inputHex` when the input is not valid UTF-8): JSON parsed by
    protobuf-go with `discardUnknown` / `allowPartial`; `error: true` if
    protobuf-go rejects it, otherwise `binary`, the deterministic encoding of
    the parsed message; or
  - no input, only `binary`: a message written in the text format and encoded
    by protobuf-go, for values JSON cannot express (out-of-range timestamps
    and durations, unset `Value`, irreversible field masks, unresolvable or
    corrupt `Any`, missing required fields).
- `outputs`: protobuf-go's JSON for the binary decoded again, for the option
  sets `default`, `protoNames` (UseProtoNames), `enumNumbers`
  (UseEnumNumbers), `emitUnpopulated`, `emitDefaultValues` and `indent`
  (Multiline with two spaces); `allowPartial` applies to them too. Each entry
  is `{"json": ...}` or `{"error": true}`. An option set that gives the same
  result as `default` is left out.

Coverage: the proto3 JSON mapping table for every scalar type (64-bit
integers as strings, numbers in strings and with exponents, NaN and
infinities, float32 range, Base64 variants), enums (names, numbers, aliases,
unknown values), null handling, maps (key parsing, duplicates, sorting),
oneof conflicts, duplicate fields, repeated nulls, extensions, groups,
required fields, JSON syntax errors (commas, literals, numbers, escapes,
surrogates, invalid UTF-8, whitespace), every well-known type (Any with
message and well-known payloads, nested Any, `@type` anywhere in the object;
Timestamp edges from 0001 to 9999, offsets and fraction lengths; Duration
edges, signs and normalisation; Struct, Value, ListValue, NullValue; all nine
wrappers; FieldMask round-trip rules; Empty) at the top level and inside
messages, and output formatting (float formats, escaping, indentation).

## How the goldens were generated

They were written on 2026-09-16 by a throwaway Go program outside this
repository, in its own module, using `google.golang.org/protobuf` v1.36.11
and `github.com/bufbuild/protocompile` v0.14.1 with Go 1.27.0. PostSonde does
not depend on either module; `scripts/check-core-deps.sh` also rejects them in
test code.

The program:

1. Compiled the three `.proto` files with protocompile (standard imports on,
   plus `google/rpc/status.proto` from `proto/wellknown`), converted the
   result to `FileDescriptorProto`s and rebuilt it with `protodesc.NewFiles`,
   so protobuf-go's own descriptors decide presence and required fields. It
   registered every message, enum and extension as `dynamicpb` types in a
   `protoregistry.Types`, used as the resolver throughout.
2. For each case with JSON input, ran `protojson.UnmarshalOptions` and, on
   success, `proto.MarshalOptions{Deterministic: true}`. For text-format
   cases, ran `prototext.Unmarshal` and the same encoding.
3. Decoded the binary again and ran `protojson.MarshalOptions` with each
   option set.
4. Normalised protobuf-go's deliberately unstable whitespace: compact output
   went through `encoding/json.Compact` (which only removes whitespace outside
   strings), and in multiline output the random second space after `"name":`
   was removed. What remains is protobuf-go's layout, which PostSonde
   reproduces byte for byte.
5. Checked each case against an expectation written with it (accepted or
   rejected, marshal error or not) and stopped on any surprise.

protobuf-go's reflection-based encoding writes extensions first and, when
deterministic, members of real oneofs after the other fields; PostSonde writes
fields in number order. The schemas keep oneof members at the highest numbers
and extension numbers below the regular fields, so the orders agree, also for
the bytes inside `Any` values built from JSON.

Not in the goldens, and covered by `protojson_test.go` instead: message sets
(`protodesc` refuses them), closed-enum unknown numbers in binary input
(`dynamicpb` keeps them as known values; PostSonde keeps them as unknown
fields, as generated code does), invalid UTF-8 in string fields (protobuf-go
refuses to decode it, so it cannot reach the JSON encoder), and the recursion
limit.

The same program was also used for differential fuzzing: arbitrary JSON (from
these inputs, mutated) parsed by both implementations, requiring the same
accept/reject decision, the same binary and the same JSON output; and
arbitrary binary input accepted by protobuf-go, requiring the same JSON. Over
40 million executions found two differences, both fixed and added as cases
(`[message.Name]` extension names, and `Any` type names and extension names
with a leading dot).

To add a case, write it the same way with protobuf-go, add it to
`golden.json`, and run `go test ./internal/core/grpc/protojson`.
