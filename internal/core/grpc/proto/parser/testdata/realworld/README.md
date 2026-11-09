# Real-world proto2 test data

Unmodified third-party `.proto` files used by `realworld_test.go` to
check that the parser handles proto2 as it is written in practice. They are
test data only: nothing here is compiled into or shipped with PostSonde.
Do not edit them; replace them with a newer upstream copy instead, and update
this table and the test's expected counts.

| File here | Upstream source | Upstream path | License | SHA-256 |
|---|---|---|---|---|
| `protobuf/descriptor.proto` | Protocol Buffers v34.0, as packaged in `com.google.protobuf:protobuf-java:4.34.0` (Maven Central); <https://github.com/protocolbuffers/protobuf> | `src/google/protobuf/descriptor.proto` | BSD-3-Clause, Copyright 2008 Google Inc.; full text in the file header | `32f3df357257f556b311c7e4ad33625a7aa13de541cb53a29ae85ac746c11a07` |
| `protobuf-go/proto2/fields.proto` | Go module `google.golang.org/protobuf@v1.36.11` | `cmd/protoc-gen-go/testdata/proto2/fields.proto` | BSD-3-Clause, Copyright 2018 The Go Authors; full text in `protobuf-go/LICENSE` | `8bf4e6d44cb15244efd39957a84c93223d50add89ea288a30b6db60e3560a5c2` |
| `protobuf-go/textpb2/test.proto` | Go module `google.golang.org/protobuf@v1.36.11` | `internal/testprotos/textpb2/test.proto` | BSD-3-Clause, Copyright 2019 The Go Authors; full text in `protobuf-go/LICENSE` | `7292294b459687eb975c4e5dc2339422474751fe296743b2d3f20ed959daff8d` |
| `protobuf-go/LICENSE` | Go module `google.golang.org/protobuf@v1.36.11` | `LICENSE` | BSD-3-Clause license text | `4835612df0098ca95f8e7d9e3bffcb02358d435dbb38057c844c99d7f725eb20` |

What each file covers:

- `descriptor.proto`: the proto2 file behind every custom option import.
  `optional`/`required`/`repeated` labels, `[default = ...]` values,
  `option optimize_for = SPEED;`, `extensions 1000 to max;` in every
  `*Options` message, extension ranges with `declaration` message literals
  (multi-line, comma-separated, repeated), adjacent string concatenation
  and comments inside compact options.
- `fields.proto`: every scalar type with each label, `optional`/`required`/
  `repeated` groups and a group inside a oneof, defaults of every type
  including escaped strings and bytes, `inf`, `-inf` and `nan`.
- `textpb2/test.proto`: nested groups, extension ranges between fields,
  top-level and nested `extend` blocks for the same extendee, and message
  sets (`option message_set_wire_format = true;` with `extensions 4 to max;`).
