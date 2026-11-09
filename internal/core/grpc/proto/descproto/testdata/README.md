# descproto test data

`schema/` is a small multi-file schema written for these tests:
custom options of every options type with scalar and aggregate values
(`shop/v1/options.proto`), enum aliases and reserved ranges
(`shop/v1/common.proto`), a service with all four call kinds, maps, oneofs,
proto3 `optional`, `json_name`, a public import and well-known type imports
(`shop/v1/orders.proto`), proto2 defaults of every kind, groups (in a
message, in a oneof, as an extension), extension ranges and a message set
(`legacy/catalog.proto`), and edition 2023 features
(`shop/v1/editions.proto`). Nothing here is shipped.

`golden/<file>.pb` is the serialized `google.protobuf.FileDescriptorProto`
of each schema file as produced by an independent compiler, so `Link` and
`Marshal` are compared with it rather than with themselves.

## How the goldens were generated

They were written on 2026-09-16 by a throwaway Go program outside this
repository, in its own module, using `github.com/bufbuild/protocompile`
v0.14.1 and `google.golang.org/protobuf` v1.36.11 with Go 1.27.0. PostSonde
does not depend on either module; `scripts/check-core-deps.sh` also rejects
them in test code.

The program compiled the five files with protocompile (standard imports on,
no source code info), took each result's `FileDescriptorProto()` and wrote it
with `proto.MarshalOptions{Deterministic: true}`.

protobuf-go writes the extensions of a message before its other fields;
PostSonde writes fields in number order. `TestGoldenMarshal` therefore
accepts either identical bytes or equal messages when both are decoded
without an extension resolver.

To change the schema, edit the `.proto` files, regenerate the goldens the
same way and run `go test ./internal/core/grpc/proto/descproto`.
