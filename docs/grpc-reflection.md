# gRPC server reflection and the schema cache

PostSonde can discover a server's services through the gRPC server
reflection API instead of `.proto` files (GRPC-03), and keeps what it found
in the project's local directory so that the services still work after a
restart with the server unreachable (GRPC-05). Everything is standard
library only.

```
transport.Client ──Invoke──▶ grpc.reflection.v1[alpha].ServerReflection/ServerReflectionInfo (bidi)
        ▲                         list_services, file_containing_symbol, file_by_filename
        │                                       │ reflectwire (hand-encoded messages)
reflection.Client.Fetch ────────────────────────┘
        │ Snapshot{Address, Version, FetchedAt, Services, Files [name + FileDescriptorProto bytes], Problems}
        ├──▶ Cache.Save ─▶ <local>/grpc/reflection/<address hash>.json
        ▼
reflection.Link ─▶ descproto.Link: FileDescriptorProto ─▶ .proto source ─▶ linker.Load ─▶ desc.Pool
        ▼
Schema{Pool, Services, Diagnostics, Problems, FromCache}  ─▶ method picker, dynamic codec
```

| Package | Purpose |
|---|---|
| `grpc/reflection/reflectwire` | `Request`/`Response` for both protocol versions (same wire format), `MarshalRequest`, `UnmarshalRequest`, `MarshalResponse`, `UnmarshalResponse`, `FileInfo` (name and dependencies of a FileDescriptorProto), method and service names. |
| `grpc/reflection` | `Client.Fetch`, `Link`, `Cache`, `Resolver` (`Refresh`, `LoadCached`, `Resolve`), `Schema.FindMethod`. |
| `grpc/proto/descproto` | `Link` (FileDescriptorProto bytes to a linked pool), `Marshal` (a linked file to FileDescriptorProto bytes), `Source` (one descriptor as `.proto` text). |
| `grpc/transport/grpctest` | `EnableReflection` for tests. |

## Fetching

`Client.Fetch(ctx, FetchOptions{Call, Version, IncludeReflectionServices})`
opens one reflection stream and asks, in order:

1. `list_services` (`"*"`). Services named `grpc.reflection.v1.ServerReflection`
   and `grpc.reflection.v1alpha.ServerReflection` are dropped unless
   `IncludeReflectionServices`; empty and repeated names are dropped.
2. `file_containing_symbol` for each service.
3. `file_by_filename` for every dependency that has not arrived yet.
   Servers such as grpc-go already send the file with its unsent
   dependencies; those are not asked for again. Each name is asked for at
   most once, so dependency cycles end; files are deduplicated by name (the
   first copy wins).

`Call` is an ordinary `transport.CallOptions` (address, TLS, authority,
metadata, timeout for the whole fetch); its `Method` is replaced.

**Versions.** With `Version` empty the client tries v1 and, when the call
ends with `UNIMPLEMENTED` (a grpc-status or HTTP 404), v1alpha. The version
that answered is remembered per normalized address (trimmed, lower case) and
tried first next time; a loaded cache seeds it too
(`RememberVersion`/`RememberedVersion`). `Version: "v1"` or `"v1alpha"`
forces one. If no tried version is implemented the error is
`grpcreflection.unsupported` (kind `unsupported`) wrapping the transport
error.

**Errors and problems.** The fetch fails (no snapshot) when:

| Situation | Error |
|---|---|
| The call fails (dial, TLS, deadline, cancellation, a non-OK status other than UNIMPLEMENTED) | the transport error unchanged (`grpctransport.*`, `netx.*`) |
| Neither version implemented | `grpcreflection.unsupported` |
| `list_services` answered with `error_response` | `grpcreflection.server_error`, kind `protocol`, detail `grpc_status` |
| A response of the wrong kind, a malformed message, the stream ending before an answer | `grpcreflection.protocol` |
| More than `MaxFiles` (10,000) descriptors | `grpcreflection.too_many_files` |
| Unknown `Version` | `grpcreflection.invalid_version`, kind `invalid_argument` |

Parts that cannot be fetched are `Snapshot.Problems` (`Code`, `Subject`,
`Message`) and the fetch goes on: `grpcreflection.symbol_not_found` (a
service's `file_containing_symbol` got an `error_response`),
`grpcreflection.file_not_found` (a dependency; not reported for
`google/protobuf/*.proto` files that have a built-in copy) and
`grpcreflection.invalid_descriptor`. The receive limit per message is the
transport's (4 MiB by default).

`all_extension_numbers_of_type` and `file_containing_extension` are encoded
and served by the test server but not used by the client yet: custom
options come with the files that use them.

## From descriptors to the model

Reflection returns serialized `google.protobuf.FileDescriptorProto`s, while
the linker builds the model from `.proto` source. `descproto.Link` bridges
the two by **rendering each descriptor as `.proto` source and linking the
sources** with `linker.Load` over an in-memory file system. A schema loaded
by reflection therefore has exactly the model, checks and diagnostics of the
same schema loaded from files: map entry types, groups, synthetic oneofs of
proto3 `optional`, JSON names, presence, packing, editions features, closed
enums, defaults and options are all derived by the one implementation, and
the codec reads the same `Option` values. `File.AST.Source` holds the
rendered text, which diagnostics refer to, and which a UI can show as the
schema.

Rendering rules:

- Syntax from `syntax`/`edition` (proto2, proto3, editions 2023 and 2024;
  anything else is `proto.invalid_descriptor`). Imports keep `public`,
  `weak` and `import option`.
- Type names are written fully qualified with their leading dot.
- A repeated field whose type is a nested `map_entry` message with `key` = 1
  and `value` = 2 is written as `map<K, V>`; the entry type is not written.
- In proto2, a `TYPE_GROUP` field whose type is a sibling message named like
  the field is written as a `group` (also inside oneofs and extend blocks).
  In editions the field is a message field whose `features.message_encoding`
  option makes it delimited.
- Nested types keep their order relative to map entries and groups; fields
  of a oneof are written together at the first one (protoc requires them to
  be contiguous); synthetic oneofs are not written.
- `json_name` is written only when it differs from the default, so
  `HasJSONName` means the same as for source. `default_value` is written
  back as a literal (bytes defaults are unescaped from protoc's C escaping).
- Reserved and extension ranges convert exclusive ends; the message-set
  maximum is written as `max`. Editions reserved names are identifiers.
- Options are decoded from their binary form with `dynamic` against the
  embedded `descriptor.proto` and written as option statements: nested
  message options flattened into dotted names
  (`features.field_presence = IMPLICIT`, `(.pkg.rule).max_len = 3`), repeated
  values as repeated statements, repeated messages and maps as aggregates.
  `uninterpreted_option` and `map_entry` are skipped.
- **Custom options** (extensions of the `*Options` messages) are unknown
  fields at first. If there are any, the files are linked once, the options
  decoded again with that pool as extension resolver, and everything
  rendered and linked a second time. Option values whose extension no file
  defines are left out with one `proto.unknown_options` warning.
- Every name is checked to be an identifier (or dotted identifiers) and
  every file name to be a clean relative path before anything is written,
  so a hostile descriptor cannot inject declarations; such a file is left
  out with `proto.invalid_descriptor`. Bytes that do not decode are also
  `proto.invalid_descriptor`; two different descriptors with one name keep
  the first (`proto.duplicate_descriptor` warning).
- Files named like a built-in well-known type (`google/protobuf/*.proto`
  that `wellknown` embeds) are not rendered: the built-in copies are used,
  so every schema agrees on those types and keeps their comments.

Descriptors carry no comments from protoc-gen-go servers (source code info
is stripped), so none are rendered.

`descproto.Marshal(file, pool)` is the reverse, used by the test server and
available for exports: fully qualified names, `json_name` on every field,
`map_entry`, synthetic oneofs, editions fields as written (label optional,
type message), defaults in protoc's text form, options re-encoded from the
linked option values including aggregates (extension names resolved through
`pool`). Extension range options and source code info are not written.

## Cache

`Cache` keeps one snapshot per address in the open project's local
directory ([workspace.md](workspace.md)):

```
<local>/grpc/reflection/<first 16 hex digits of sha256(normalized address)>.json
```

written with `atomicfile` (mode `0600`, directories `0700`) in the stable
JSON format:

```json
{
  "kind": "postsonde.grpc-reflection-cache",
  "version": 1,
  "address": "localhost:50051",
  "apiVersion": "v1",
  "fetchedAt": "2026-09-16T08:00:00Z",
  "services": ["shop.v1.OrderService"],
  "files": [{"name": "shop/v1/orders.proto", "descriptor": "<base64 FileDescriptorProto>"}],
  "problems": []
}
```

The raw descriptors are stored, not the rendered source or the model, so the
cache is lossless and re-links with whatever the current linker does.
`fetchedAt` lets the UI say how old the services are.

| Situation | `Load` | `Save` |
|---|---|---|
| No file | `grpcreflection.not_cached` (kind `not_found`) | writes |
| Not JSON, another `kind`, version below 1, another address, unknown `apiVersion`, an empty file entry | `grpcreflection.cache_corrupt` (kind `parse`, detail `path`); the file is not touched | moves it to `<file>.corrupt` (replacing an older one), writes, and returns the moved path |
| `version` above 1 (a newer PostSonde) | `grpcreflection.cache_newer` (kind `unsupported`) | refuses with the same error; the file is kept |
| Read or write failure | `grpcreflection.cache_io` | `grpcreflection.cache_io` |
| No project open | the `LocalDir` error (`workspace.none_open`) | same |

## Resolver: the service-facing API

```go
r := reflection.NewResolver(grpcClient /* *transport.Client */, workspaceManager.LocalDir)
schema, err := r.Refresh(ctx, reflection.FetchOptions{Call: transport.CallOptions{Address: addr, TLS: tls}})
schema, err := r.LoadCached(ctx, addr)   // no network
schema, err := r.Resolve(ctx, opts)      // Refresh, else the cache with a problem
method := schema.FindMethod("/shop.v1.OrderService/GetOrder")
msg := dynamic.New(method.Input)
```

- `Refresh` fetches, links (`reflection.Link`) and saves. Cache trouble does
  not fail it: `grpcreflection.cache_not_saved` (with the error's message)
  or `grpcreflection.cache_replaced` (a corrupt file moved aside) are added
  to `Schema.Problems`.
- `LoadCached` loads and links the cached snapshot with no network access;
  `Schema.FromCache` is true. It returns the `Cache.Load` errors.
- `Resolve` is `Refresh`, falling back to `LoadCached` for any fetch failure
  except cancellation and invalid options, with a `grpcreflection.offline`
  problem naming when the cache was saved. Without a usable cache it returns
  the fetch error.
- `Schema.Services` are the listed services found in the pool, in the
  server's order; a listed service whose definition did not load is a
  `grpcreflection.service_missing` problem (unless a fetch problem already
  names it). `Schema.Diagnostics` are the descriptor and linker diagnostics;
  with errors the pool is still usable as far as it linked, like partial
  `.proto` files.
- `Schema.FindMethod` accepts `/pkg.Service/Method` and `pkg.Service.Method`.

A `Resolver` is safe for concurrent use; concurrent refreshes of one address
both write the cache and the last one wins.

## Using reflection in the gRPC request editor

Guidance for the request type and method picker; nothing of this is built
here.

- The request's proto setup gets a source: `"source": "files"` (import paths
  and files, linked with `linker.Load`) or `"source": "reflection"` (the
  request's own address, TLS and metadata). Both produce a `*desc.Pool`, so
  the picker, message template, JSON editor and codec do not care which.
- Bind `LoadCached` for opening a request (instant, offline), `Refresh` for
  a "Reload from server" button and optionally before the first send, and
  `Resolve` when the UI wants "fresh if possible". Show `fetchedAt`,
  `FromCache`, `Problems` and `Diagnostics` (the rendered `AST.Source` can
  back a "view schema" panel with diagnostic positions).
- Send reflection calls with the same `transport.Client` and connection
  settings as the request, so TLS, proxies and the dialer guard apply.
- Keep one `Resolver` per app in `services()`, built with
  `workspace.Manager.LocalDir`, so the cache follows the open project.
- A method stored in a request file should be its full gRPC path, which
  resolves with either source.

## Serving reflection

The gRPC mock server (SSRV-07, [servers.md](servers.md#grpc-mock-server))
serves both versions in production code (`internal/core/servers/grpcmock`,
`reflection.go`): the descriptors are its linked `.proto` files written by
`descproto.Marshal`, indexed by the symbols of the model (`desc.Walk`)
rather than a protowire walk, and answered with `reflectwire` as the test
server answers them (dependencies sent once per stream, `NOT_FOUND`
`error_response`s). The reflection client fetches and links them back
(`grpcmock_test.go`, `TestReflection`).

## Test server

`grpctest.Server.EnableReflection(ReflectionOptions{...})` registers both
reflection methods and serves the given FileDescriptorProto bytes, indexed
by a small protowire walk (messages, nested types, enums, services,
methods, extensions):

| Option | Effect |
|---|---|
| `Files` | Serialized FileDescriptorProtos. |
| `Services` | The `list_services` answer; nil lists every service in `Files` plus the enabled reflection services, like grpc-go. |
| `NoV1`, `NoV1Alpha` | Serve only one version; the other answers UNIMPLEMENTED. |
| `WithDependencies` | Answer file requests with the file and its unsent transitive dependencies, like grpc-go; otherwise only the file. |
| `Errors` | `error_response` with a code for `"list_services"` or `"<kind>:<name>"` (`"file_by_filename:a.proto"`). |

Unknown symbols and files get a `NOT_FOUND` `error_response`;
`all_extension_numbers_of_type` and `file_containing_extension` are served.
`ReflectionRequests()` returns the requests received.

## Tests

- `descproto`: the test schema in `descproto/testdata/schema` (custom
  options of every options type with scalar and aggregate values, maps,
  oneofs, proto3 `optional`, `json_name`, public and well-known imports,
  proto2 defaults of every kind, groups in messages, oneofs and extensions,
  message sets, editions features) with **protocompile goldens**
  (`testdata/golden`, see its README): linking the goldens in reverse order
  gives the same model as linking the sources (a dump of every descriptor's
  names, kinds, types, JSON names, presence, packing, defaults, ranges,
  oneofs, streaming flags), and `Marshal` of the sources equals the goldens.
  Round trips source → `Marshal` → `Link` → `Marshal` are byte-identical
  with equal models for that schema, the linker's multi-root test project
  and the vendored protobuf-go proto2 files. Problem cases: undecodable
  bytes, injection attempts in names and type names, invalid file names,
  unsupported syntax and edition, duplicates, missing dependencies, built-in
  well-known files, unknown custom options, cancellation. `FuzzLink` checks
  that any input links without panicking and renders parseable source.
- `reflectwire`: every request and response kind against **grpc-go
  encodings** (v1 and v1alpha produce the same bytes; `testdata/README.md`),
  oneof replacement, merged repeated fields, packed and unpacked extension
  numbers, wrong wire types, malformed input, `FuzzUnmarshal`.
- `reflection`, all under `-race` against `grpctest` over h2c and TLS: fetch
  over v1 with and without dependencies included (no request repeated, the
  expected number of `file_by_filename`), v1alpha fallback and remembered
  version, forced versions, no reflection at all, unreachable servers,
  `error_response` for `list_services`, a service and dependencies,
  reflection services included, dependency cycles, protocol violations,
  too many files, cancellation. Methods loaded by reflection (v1, v1alpha,
  dependencies included) match the linked `.proto` sources field by field.
  The offline scenario: refresh over TLS, check the file (path, `0600`),
  stop the server, create a new transport client and resolver on the same
  directory, `LoadCached` (services, remembered version, `FindMethod`,
  dynamic encode/decode with the cached descriptors) and `Resolve` falling
  back with `grpcreflection.offline`. Cache problems: not cached, seven kinds
  of corrupt file left untouched by `LoadCached` and moved aside by
  `Refresh`, a newer cache neither used nor replaced, no project open; the
  exact JSON format.
- `TestExternalServer` loads services from a real server at
  `$POSTSONDE_GRPC_REFLECTION_ADDR` (`host:port`, optionally `,v1alpha`) and
  is skipped otherwise. It passed against a grpc-go v1.78.0 server with the
  health and `grpc.testing.TestService` services, over v1 and v1alpha.

## Limits and later work

- No UI or bound service yet.
- Extension range options are not kept by the model, so `Marshal` cannot
  write them; `Link` renders them from descriptors.
- `import option` dependencies and edition 2024 `export`/`local` are
  rendered but not covered by tests (protocompile v0.14.1, used for the
  goldens, has no edition 2024).
- The cache is not invalidated automatically; the UI decides when to
  refresh. Settings, Privacy, Local data removes it with the cached
  GraphQL schemas ([privacy.md](privacy.md#clearing-local-data)).
