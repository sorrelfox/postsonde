# Codec test data

The `.proto` files are test schemas written for PostSonde's dynamic codec
tests. `golden/` holds reference encodings of messages of those types,
produced by protobuf-go, so the codec is compared with an independent
implementation. Nothing here is shipped.

| File | Contents |
|---|---|
| `proto3.proto` | Every scalar type, repeated fields (packed and `[packed = false]`), maps of every key type, a oneof, proto3 `optional`, a superset message for unknown fields. |
| `proto2.proto` | Defaults, `required`, groups (nested and repeated), closed enums, packed options, extensions (including a group extension), a superset message for unknown groups. |
| `editions.proto` | Edition 2023: `DELIMITED` message encoding, `EXPLICIT`/`IMPLICIT` presence, `PACKED`/`EXPANDED`, `utf8_validation = NONE` on a field and a map field, a `CLOSED` enum. |
| `editions_file_none.proto` | `utf8_validation = NONE` for a whole file. |
| `golden/<case>.bin` | The deterministic protobuf-go encoding of one message. |
| `golden/<case>.txt` | `type: <full name>`, then protobuf-go's view of the decoded message as `path: value` lines (declaration order, then extensions by number; map entries sorted by key; lists as `name[i]`; messages as `name: {}` followed by their fields; floats in shortest `g` format; strings Go-quoted; bytes as `0x` hex; enums as numbers; unknown fields as `unknown: <hex>`). `helpers_test.go` writes the same format. |

## How the goldens were generated

They were written on 2026-09-16 by a throwaway Go program outside this
repository, in its own module, using `google.golang.org/protobuf` v1.36.11
and `github.com/bufbuild/protocompile` v0.14.1 with Go 1.27.0. PostSonde does
not depend on either module; `scripts/check-core-deps.sh` also rejects them in
test code.

The program:

1. Compiled the four `.proto` files with protocompile (standard imports on)
   and registered every extension as a `dynamicpb` extension type.
2. Built each case as a `dynamicpb.Message` by field name, and encoded it
   with `proto.MarshalOptions{Deterministic: true, AllowPartial: true}`.
3. For `p3_unknown_fields` (a `Superset`) and `p2_unknown_groups` (a `Wide`),
   decoded the bytes as `Nested` and `Narrow` and required the re-encoding to
   be identical, so the unknown fields appear in the dump.
4. Decoded every encoding again with the extension registry, required the
   re-encoding and the dump to be identical, and wrote `.bin` and `.txt`.

protobuf-go's reflection-based encoding writes extensions first and, when
deterministic, members of real oneofs after the other fields; PostSonde writes
fields in number order. The schemas keep oneof members at the highest numbers
and extension numbers below the regular fields, so the orders agree.

Closed-enum unknown values are not in the goldens: `dynamicpb` decodes them as
known values, unlike protobuf-go's generated code and C++, which keep them as
unknown fields; `decode_test.go` covers them with hand-built wire data.

To add a case, write the message the same way with protobuf-go, add both
files here, and, if it is built in `TestGoldenEncode`, add it there too.
