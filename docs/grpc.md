# gRPC requests

A gRPC request (GRPC-01, GRPC-02, GRPC-10) names a server, the `.proto`
files that describe it and one of their methods, and calls it: unary,
server-streaming, client-streaming and bidi calls (GRPC-11 to GRPC-14)
with metadata (GRPC-18) and a deadline (GRPC-21), a streaming timeline of
what was sent and received (GRPC-22), showing the response headers and trailers apart
(GRPC-19) and the status with its decoded error details (GRPC-20). The
request message is JSON checked against the method's input type, with a
generated example (GRPC-15, GRPC-16). This page covers the request kind,
how its proto setup is found and loaded, the service that describes and calls it, and the
editor. Parsing and linking `.proto` files is [proto.md](proto.md); the
binary codec is [protobuf-codec.md](protobuf-codec.md), the JSON mapping
[protobuf-json.md](protobuf-json.md), the wire
[grpc-transport.md](grpc-transport.md). Server reflection exists in core
([grpc-reflection.md](grpc-reflection.md)) but is not wired into the
request yet.

```
GrpcEditor ── lib/grpc ──▶ CollectionService.InheritedGRPCProto ─┐ nearest folder's grpc.proto
   │   ▲                   CollectionService.UpdateFolderGRPCProto┘ (_folder.json)
   │   │
   │   └── services, methods, diagnostics ◀── GRPCService.Describe ── linker.Load (cached)
   │                                          GRPCService.PickProtoFiles / PickImportPath
   ├── edit() ──▶ CollectionService.UpdateRequest (auto-save of the request file)
   │
   ├── GrpcMessageEditor ──▶ GRPCService.ValidateMessage (as typing pauses) ─┐ protojson
   │                     ├─▶ GRPCService.ExampleMessage (method chosen, button)├ (.Example,
   │                     └─▶ GRPCService.CompleteMessage (autocomplete)       ┘  .Complete)
   │
   └── Invoke ─▶ commands/grpcCalls (one slot per open request, MessageSource log)
                   └─ startOperation("grpc") ─▶ GRPCService.Invoke ─▶ protojson ─▶ dynamic ─▶ transport.Client
                        ◀── grpc:op:data  {request, headers, message…, end}   ◀── operation.Emitter
                        ◀── grpc:op:done  {ok | error | cancelled}
                   send ──▶ GRPCService.SendMessage(opId, message) ─▶ Stream.Send ─┐ client streaming,
                   closeSend ▶ GRPCService.CloseSend(opId) ─▶ Stream.CloseSend ───┘ bidi ({sent, half-close})
                   Cancel ─▶ OpsService.Cancel(opId) ─▶ Stream.Close (RST_STREAM)
```

| Piece | Where |
|---|---|
| File format (kind `grpc`, folder `grpc.proto`) | [file-format.md](file-format.md#grpc-request-kind-grpc), `internal/core/storage/projectfile` (`GRPCRequest`, `GRPCProto`, `GRPCSettings`, `FolderGRPC`) |
| Folder setups | `internal/app/services/collection/grpc.go` |
| Describe and the pickers | `internal/app/services/grpc` (`GRPCService`), dialogs in `internal/app/dialogs.go` (`pickPaths`) |
| Calls | `internal/app/services/grpc/invoke.go`, `call.go` (the running call, SendMessage, CloseSend), `invoke_dto.go`, `events.go` |
| Status details | `internal/core/grpc/rpcstatus`, `toStatusDTO` in `invoke_dto.go` |
| Message example and validation | `internal/app/services/grpc/message.go`, `internal/core/grpc/protojson/example.go` |
| Binding wrappers and helpers | `frontend/src/lib/grpc.ts` |
| Call state and log | `frontend/src/commands/grpcCalls.ts`, `useGrpcCall.ts` |
| Editor | `frontend/src/components/editor/GrpcEditor.tsx`, `GrpcMessageEditor.tsx` |
| Response | `frontend/src/components/response/GrpcResponsePanel.tsx`, `GrpcStatusPanel.tsx`, `GrpcStreamControls.tsx` |

## Request kind: `grpc`

A `grpc` request file stores the `address` (`host:port`), its `proto`
setup, the fully-qualified `service` and the `method` name, the request
`message` as JSON text (kept as typed; encoding it is the protobuf JSON
mapping's job), `metadata` rows, and `settings.tls`, `verifyTls`,
`connectTimeoutMs`, `deadlineMs`, `transport` and `compression`. Example and defaults:
[file-format.md](file-format.md#grpc-request-kind-grpc).

`RequestDTO` carries `service`, `proto` {`files`, `importPaths`,
`inherit`, `extra`}, `metadata`, `settings.tls`, `settings.deadlineMs`,
`settings.transport` and `settings.compression` for it (the last two
omitted when empty; the defaults `grpc` and `identity` are saved as missing
members), and reuses `address`, `method`, `message`, `settings.verifyTls`,
`settings.connectTimeoutMs` and `auth` ([Auth](#auth)). `CreateRequest` accepts kind `grpc` and creates
a request without an address or setup (the New request dialog offers it,
[frontend.md](frontend.md#new-folder-and-new-request)). The
tree and the tab show a `GRPC` badge.

### Proto setup

A setup is a list of `.proto` files and a list of import roots (protoc's
`--proto_path`), stored like body files: relative to the project root with
`/` when inside the project, absolute otherwise.

- **Per request.** `proto.files` and `proto.importPaths` of the request.
- **Per folder.** `_folder.json` may hold `grpc.proto` {`files`,
  `importPaths`}. A request with `proto.inherit` uses the setup of the
  **nearest folder above it that has one** (a non-empty `files` or
  `importPaths`); folders without one, and folders whose `_folder.json`
  could not be read, are skipped. The request's own lists are kept while it
  inherits, so turning inheritance off brings them back.
- New requests do not inherit; the Proto tab turns it on.

`CollectionService`:

| Method | Does |
|---|---|
| `InheritedGRPCProto(id)` | `{folderId, folderName, files, importPaths, parentId, parentName}`: the nearest folder with a setup and its lists (empty when none), and the folder the request is in, where a shared setup can be created (`""` at the top level). `collection.not_found` for an ID that is not a request. |
| `UpdateFolderGRPCProto({folderId, files, importPaths})` | Writes the folder's setup to `_folder.json` at once (empty lists remove it; the folder's other members and unknown members of `grpc` are kept) and sends `collection:changed` with op `update_folder`. Fails with `collection.not_found`, `collection.wrong_node`, `collection.read_only` or the write's error. |

## Service: `GRPCService`

| Method | Does |
|---|---|
| `Describe({files, importPaths, reload})` | Resolves the stored paths against the open project's root, runs `linker.Load` and returns `{services, diagnostics, files, errors, cached}`. Problems with the files are diagnostics, never a rejection; no files gives an empty result. |
| `PickProtoFiles()` | Native open dialog for several files, filtered to `*.proto`, starting in the project root; returns their stored paths (`[]` when cancelled). |
| `PickImportPath()` | Native folder dialog; returns the stored path (`""` when cancelled). |
| `Invoke(InvokeInputDTO)` | Starts a call of any kind; see [Calls](#calls). |
| `SendMessage({opId, message})` | Sends one message on a running client-streaming or bidi call; see [Streams from the client](#streams-from-the-client). |
| `CloseSend({opId})` | Ends the request stream of a running client-streaming or bidi call (half-close). |
| `ExampleMessage(MessageInputDTO)` | A template of the method's request message; see [Message](#message). |
| `ValidateMessage(MessageInputDTO)` | Checks message JSON against the method's input type; see [Message](#message). |
| `CompleteMessage(MessageCompleteInputDTO)` | Field-name and enum suggestions at a cursor in message JSON; see [Autocomplete](#autocomplete). |

- **Services** are those of every loaded file except the built-in
  well-known types (imports included), sorted by full name, each with its
  methods in declaration order: `name`, `fullName`, `file` (import path),
  `comment` (the leading comment, else the trailing one, lines trimmed),
  `deprecated`, and for methods `inputType`/`outputType` (full names, or the
  name as written when it did not resolve), `clientStreaming`,
  `serverStreaming` and `kind` (`unary`, `client_stream`, `server_stream`,
  `bidi`).
- **Diagnostics** are `{code, severity, file, path, line, column,
  message}`: the linker's syntax, check and link diagnostics
  ([proto.md](proto.md#linking)) with `file` the import path and `path` the
  stored path on disk, plus the service's own: `grpc.import_path_missing`
  (warning, an import path that is not an existing folder),
  `grpc.no_project` (error, a relative path while no project is open) and
  `fileaccess.not_allowed` (error, a file or import path outside the
  project folder and the allowed directories of the settings, PRV-10; the
  path is not read, and a call with it is refused). The linker reads
  through the same allowlist (`linker.Options.Access`), so an import that
  leads out through a link is not found
  ([privacy.md](privacy.md#local-file-access-prv-10)).
  `line` is 0 for problems with a whole file. `errors` counts the errors.
- **Cache.** Up to eight linked setups are kept, keyed by the resolved
  files and import paths and the allowed directories. A cached result is used while every file it was
  read from has the same size and modification time; otherwise, or with
  `reload`, the setup is linked again. A result with a missing file or
  import (`proto.file_not_found`, `proto.read_failed`,
  `proto.import_not_found`, `proto.weak_import_not_found`) is not cached,
  since creating the file changes the result without touching any file that
  was read. Adding a file to an earlier import root that would now shadow
  one is not noticed until Reload.
- A file that is under none of the import paths gets its directory as an
  implicit import path and a `proto.implicit_import_path` warning (the
  linker's rule), so a single file works without any import path.

## Auth

A grpc request's `auth` (AUTH-31), its own or its closest folder's, is
sent in the metadata of every call
([auth.md](auth.md#auth-on-every-protocol-auth-31)). `grpc.WithAuth`
takes the app's `httpservice.HandshakeAuth` and the request's variable
scopes: the auth fields are the only part of a gRPC request whose
`{{variables}}` are rendered (an undefined one refuses the call with
`template.unresolved`; the address, message and metadata rows still refuse
variables). The URL the auth sees is `http://` or `https://` (with TLS)
and the address, so a netrc auth looks the address's host up. Each auth
header becomes a metadata key in lower case after the rows; an enabled row
of the same key wins. Basic, Bearer, API key in a header, JWT and OAuth 2.0
in a header, and Netrc are sent (`authorization: Bearer …`, `x-api-key:
…`); a query parameter (API key, JWT or OAuth 2.0 with `in` `query`) and
Digest, OAuth 1.0, AWS Signature and Hawk refuse the call with
`request.auth_not_applicable`. The request item of the call shows the
metadata with the auth's values masked.

## Calls

`GRPCService.Invoke(InvokeInputDTO) → StartedDTO` starts a call as an
operation of domain `grpc` ([events.md](events.md)) and returns its `opId`
at once. A unary or server-streaming call sends `message` and half-closes
at once; a client-streaming or bidi call only opens the stream and sends
nothing ([Streams from the client](#streams-from-the-client)).

`InvokeInputDTO`: `requestId` (logs only), `files` and `importPaths` (the
*effective* setup, own or inherited, as for `Describe`), `address`,
`transport` (`grpc`, `grpc-web`, `grpc-web-text`; `""` is `grpc`),
`compression` (`identity` or `gzip`; `""` is `identity`), `tls`,
`verifyTls`, `connectTimeoutMs`, `deadlineMs`, `service`, `method`,
`message` (JSON text) and `metadata` rows `{name, value, enabled}`. The
editor builds it from the request (`invokeInput` in `lib/grpc`).

**Before anything is sent** Invoke checks the input and rejects the bound
call; no operation starts:

| Problem | Code (kind) |
|---|---|
| `{{` in the address, an enabled metadata row or (unary and server streaming) the message: variables are not resolved yet | `grpc.variables_unsupported` (`unsupported`) |
| No service or method | `grpc.no_method` (`invalid_argument`) |
| No `.proto` files | `grpc.no_proto` (`invalid_argument`) |
| Service or method not in the linked files (detail `method`) | `grpc.method_not_found` (`not_found`) |
| Input or output type did not resolve | `grpc.unresolved_type` (`invalid_argument`) |
| The message (unary and server streaming; streams check each message in SendMessage) is not valid JSON for the input type: syntax, unknown field, wrong type, missing required field (details `type`, `line`, `column`, `offset`; the message names the position) | `grpc.invalid_message` (`invalid_argument`) |
| A `-bin` value that is not Base64 (details `key`, `row`: the row's index among all rows) | `grpc.invalid_binary_metadata` (`invalid_argument`) |
| Negative `deadlineMs` or `connectTimeoutMs`, an unknown `transport` or `compression` (detail `field`) | `grpc.invalid_settings` (`invalid_argument`) |
| A client-streaming or bidi method with a gRPC-Web transport (detail `field`: `transport`; the hint says to switch to gRPC) | `grpc.web_client_streaming` (`unsupported`) |
| Invalid address, reserved or malformed metadata key | the transport's `grpctransport.invalid_address`, `reserved_metadata`, `invalid_metadata` |
| The service was built without a registry or dialer (tests) | `grpc.unavailable` (`unsupported`) |

The setup is linked through the same cache as `Describe`, so a call right
after the picker showed the method does not parse the files again.

**Encoding.** The message is parsed with `protojson` (the canonical
mapping; `Any` and extensions resolve through the setup's pool) into a
`dynamic.Message` of the input type and encoded with `dynamic.Marshal`.
Empty or blank text sends an empty message (`{}`).

**Metadata.** Enabled rows with a name, in order. Names are trimmed and
lower-cased (gRPC keys are case-insensitive; HTTP/2 needs lower case).
Values of keys ending in `-bin` are Base64 in the editor, standard or
URL-safe, padded or not, whitespace ignored, and are sent as the decoded
bytes (the transport re-encodes them). Other values go as typed; the
transport rejects reserved keys (`grpc-*`, `content-type`, `te`, …) and
non-printable values.

**Deadline and connect timeout.** `deadlineMs > 0` is the call's timeout,
sent as `grpc-timeout` (rounded up, [grpc-transport.md](grpc-transport.md#what-goes-on-the-wire))
and enforced locally; when it passes the call ends with
`DEADLINE_EXCEEDED`. `connectTimeoutMs` bounds each dial and the TLS
handshake: `null` keeps the defaults (the dialer's and httpclient's), `0`
removes the handshake limit. Connection settings (verification, connect
timeout) belong to the `transport.Client`, so the service keeps one client
per combination and reuses its HTTP/2 connections; `ServiceShutdown`
closes their idle connections.

**Transport and compression** (GRPC-24, GRPC-25). `transport` `grpc`
is gRPC over HTTP/2 (h2c or TLS with ALPN h2). `grpc-web` and
`grpc-web-text` call gRPC-Web endpoints (a gRPC-Web proxy such as Envoy,
or a server with gRPC-Web support) over HTTP/1.1, or HTTP/2 when TLS
negotiates it, with binary or Base64 framing and the status in the trailer
frame ([grpc-transport.md](grpc-transport.md#grpc-web)). gRPC-Web is
half-duplex (browsers cannot stream a request), so only unary and
server-streaming methods can be called over it. `compression` `gzip`
compresses every request message (the compressed flag in its frame) and
sends `grpc-encoding: gzip` and `grpc-accept-encoding: gzip`; responses
compressed with gzip are decompressed whatever the setting. A response in
another encoding fails with `grpctransport.unsupported_encoding`
(`UNIMPLEMENTED`, detail `encoding`). The `request` item carries the
`transport` and `compression` used.

**Events.** `grpc:op:data` items are `CallEventDTO`s, in the order things
happened (the streaming timeline, GRPC-22):

| `type` | Fields | When |
|---|---|---|
| `request` | `method` (`/pkg.Service/Method`), `metadata` sent, `json` (the message re-encoded as indented canonical JSON), `size` (bytes), `deadlineMs`, `transport` and `compression` (with the
defaults filled in), `clientStreaming` (a stream from the client: no message, `json` `""`, `size` 0) | first |
| `sent` | `index` (from 0), `json` (indented canonical JSON), `size` | per message SendMessage sent (streams from the client) |
| `half-close` | `index`: the number of messages sent | after CloseSend (streams from the client) |
| `headers` | `metadata`: the response headers (never `grpc-status`) | when they arrive; not for a trailers-only response or a call that failed before any |
| `message` | `index` (from 0), `json` (indented), `size`; or `decodeError` and `raw` (Base64) when the bytes do not decode as the output type | per response message |
| `end` | `status` `{code, name, message, details, rich}` ([Status and error details](#status-and-error-details)), `metadata`: the trailers (with `grpc-status`, `grpc-message` and `grpc-status-details-bin` as received; for a trailers-only response, all of its headers), `messages` (received), `sent` (request messages: 1 for unary and server streaming), `elapsedMs` | last, for every call that started |

Every item has `at` (RFC 3339 UTC, nanoseconds). Metadata entries are
`{key, value, binary}`; binary values are Base64. Items are batched every
50 ms, at most 64 per event. No progress events are sent.

**Outcome** (`grpc:op:done`): `ok` for an OK status; `error` for any other
status or failure, with the transport's error (`grpctransport.status` with
details `grpc_status`/`grpc_code`, `grpctransport.deadline_exceeded` kind
`timeout`, TLS and connection codes, …); `cancelled` after
`OpsService.Cancel`, which closes the stream (RST_STREAM, the server's
context ends) and still sends the `end` item with `CANCELLED`. A unary or
client-streaming call that ends OK with zero or several messages is
`INTERNAL` with `grpctransport.cardinality`, as in grpc-go. A response message that does
not decode does not fail the call.

### Streams from the client

Client-streaming and bidi calls (GRPC-13, GRPC-14) are full duplex on one
HTTP/2 stream. `Invoke` opens the stream (the request headers go out, the
`request` item has `clientStreaming: true`) and starts reading at once, so
headers and replies show up while messages are still being sent.

- `SendMessage({opId, message}) → {index, size}` parses `message` as for a
  unary call (blank is `{}`) and writes it; it returns once the transport
  has taken the bytes (HTTP/2 flow control applies) and the `sent` item is
  queued. Sends of one call are serialized; concurrent calls get
  consecutive indexes.
- `CloseSend({opId})` half-closes: the server reads end-of-stream. A
  client-streaming method answers now; a bidi call keeps receiving until
  the server ends it. Closing twice is not an error.
- Cancel is `OpsService.Cancel(opId)`, as for every call: RST_STREAM, the
  `end` item with `CANCELLED`, outcome `cancelled`. The deadline covers the
  whole stream, sends included.

| Refusal (SendMessage, CloseSend) | Code (kind) |
|---|---|
| No running call with this `opId` (never started, ended, cancelled) | `grpc.call_not_running` (`invalid_argument`) |
| A unary or server-streaming call (its message went with the request) | `grpc.not_client_streaming` (`invalid_argument`) |
| SendMessage after CloseSend | `grpc.send_closed` (`invalid_argument`) |
| `{{` in the message; the message does not fit the input type (nothing is sent) | `grpc.variables_unsupported`, `grpc.invalid_message` as for Invoke |

**Order.** The items are the timeline, so they are emitted in wire order.
A sent message's `at` is taken before its bytes are written; while they
are being written, items from the receiving side (a reply to that very
message can arrive before `Send` returns) are held and emitted right after
the `sent` item, with their `at` stamped when they are queued, so times
never go backwards. The send lock is never held while waiting to receive:
a send blocked by flow control does not stop replies from being read. The
`end` item waits for a send in progress (the ended stream makes it return
at once), so it always comes last.

## Status and error details

The `end` item's `status` (GRPC-20) is `grpc-status` with its `name`
(`NOT_FOUND`; `CODE(42)` for codes past `UNAUTHENTICATED`) and
`grpc-message` percent-decoded, read by the transport from the trailers or a
trailers-only response ([grpc-transport.md](grpc-transport.md#status));
a call that failed locally has the status the transport made up
(`UNAVAILABLE`, `DEADLINE_EXCEEDED`, `CANCELLED`, ...). The call's error
kind follows the code's class: `cancelled` for `CANCELLED`, `timeout` for
`DEADLINE_EXCEEDED`, `network` for `UNAVAILABLE`, `protocol` for the rest.

`details` is `grpc-status-details-bin` in Base64 (`""` when absent); `rich`
is the same bytes decoded as a `google.rpc.Status` by
`internal/core/grpc/rpcstatus` (null when there are none):

| Field | Holds |
|---|---|
| `code`, `name`, `message` | The Status's own, which servers normally set to `grpc-status` and `grpc-message`. The Status tab says so when they differ. |
| `details` | One `StatusDetailDTO` per `google.protobuf.Any`, in order: `typeUrl`, `type` (the part after the last `/`), `known`, `json` (the payload in the JSON mapping, indented), `error` and `raw` (Base64 of the payload, set when `json` is empty). |
| `error` | Why the bytes are not a `google.rpc.Status`; `details` is then `[]`. |

A detail's type is looked up in the request's linked `.proto` files first,
then in the built-in pool: `google/rpc/status.proto` and
`google/rpc/error_details.proto` (embedded, [proto.md](proto.md#well-known-types)),
so `ErrorInfo`, `RetryInfo`, `DebugInfo`, `QuotaFailure`,
`PreconditionFailure`, `BadRequest`, `RequestInfo`, `ResourceInfo`, `Help`
and `LocalizedMessage` decode without the googleapis tree. A type found
nowhere is `known: false` with its bytes in `raw`; a known type whose bytes
do not decode keeps `raw` and says why in `error`. Nested `Any` values
inside a detail resolve the same way.

`rpcstatus` API: `Builtin()` (the built-in pool, linked once),
`Decode(b, types)` → `Status{Code, Message, Details, Resolver}` with
`Detail{TypeURL, Type, Value, Message, Err}`, `Status.JSON(detail)`, and
`Marshal(status)` for servers and tests.

## Message

The request message is edited as JSON in the protobuf JSON mapping
([protobuf-json.md](protobuf-json.md)). Two calls serve the editor; both
take `MessageInputDTO` {`files`, `importPaths` (the effective setup),
`service`, `method`, `message`}, find the method through the same cache as
`Describe` and `Invoke`, and reject like `Invoke` when it cannot be used
(`grpc.no_method`, `grpc.no_proto`, `grpc.method_not_found`,
`grpc.unresolved_type`). Client-streaming and bidi methods are accepted:
their messages are edited the same way. With `output: true` (also on
`MessageCompleteInputDTO`) all three use the method's **response** type
instead: the gRPC mock server's canned responses are edited with the same
editor (`GrpcMessageEditor` with `output`,
[servers.md](servers.md#grpc-mock-server)).

- **`ValidateMessage` → `MessageCheckDTO`** {`type`, `valid`, `error`}
  (GRPC-15). The text is parsed exactly as `Invoke` parses it (blank text
  is an empty message; `Any` and extensions resolve through the setup), so
  a message that validates is one `Invoke` accepts. Invalid text is a
  result, not a rejection: `error` {`offset`, `byteOffset`, `line`,
  `column`, `message`} is the first problem, a syntax error, an unknown
  field, a value of the wrong type, two members of a oneof or a missing
  required field, with `offset` and `column` in UTF-16 code units as
  CodeMirror counts (`byteOffset` in UTF-8 bytes); problems at the end (the
  text ends too early, a missing required field) are at the text's length.
- **`ExampleMessage` → `ExampleDTO`** {`type`, `json`} (GRPC-16): the
  input type with every field set to its default, written with two-space
  indentation and all fields shown (`EmitDefaultValues`). The rules are
  `protojson.Example`'s ([protobuf-json.md](protobuf-json.md#example-messages)):
  defaults and first enum values, one element in each list and map, the
  first member of each oneof, well-known types in their special form
  (`"1970-01-01T00:00:00Z"`, `"0s"`, `{}`, `null`), and a recursive type
  expanded once inside itself (its lists and maps empty there).

```json
{
  "id": "",
  "total": "0",
  "kind": "KIND_UNSPECIFIED",
  "tags": [
    ""
  ],
  "counts": {
    "": 0
  },
  "category": {
    "name": "",
    "children": [
      {
        "name": "",
        "children": []
      }
    ]
  },
  "created": "1970-01-01T00:00:00Z",
  "card": ""
}
```

### Autocomplete

**`CompleteMessage(MessageCompleteInputDTO)` → `MessageCompletionDTO`**
(GRPC-17) takes the setup, `service`, `method`, the JSON `message` and the
cursor `offset` in UTF-16 code units, and rejects like the other two calls
when the method cannot be used. The linked descriptors come from the same
cache as `Describe` (re-linked only when a `.proto` file changed), so a
call costs a scan of the text; `protojson.Complete` does the work:

- **The JSON path at the cursor.** A lenient scan reads the text up to the
  cursor, and on to the end for the keys already set: unfinished keys,
  strings (a string without its closing quote ends at the end of the
  line), objects and arrays are fine, and so is a missing comma before a
  key being typed. From the input type it follows each key down: message
  fields (by JSON name, `.proto` name or `[extension.name]`), list
  elements, map values, `Any` (the type its `"@type"` names, before or
  after the cursor; for a well-known type, its `"value"`), `Struct`,
  `ListValue` and `Value` as free JSON. `context` is `key`, `value` or
  `none` (after a value or a key, in a number, an unknown field, a map's
  or `Struct`'s free keys, a plain string).
- **Keys**: the fields of the message at the cursor that are not set yet,
  in declaration order, then extensions of the setup as `"[pkg.ext]"`; a
  oneof with a member set offers no other member. Each is its JSON name,
  or its `.proto` name when that is what is typed (`order_` gives
  `order_id`). A bool-keyed map offers `"true"` and `"false"`; `Any`
  offers `"@type"`.
- **Values**: the names of an enum (quoted), `true` and `false` for bool
  and `BoolValue`, and `null` where it leaves a field unset (not in a list
  or a map value, where it is an error), for `NullValue` and in a
  `Value`; in `"@type"`, `type.googleapis.com/` and every message of the
  setup.
- **Ranking**: what starts with the typed text (`prefix`, without the
  opening quote), then what starts with it in another case, then what
  contains it; at most 300 items (`count` says how many matched).
- **Items** {`label`, `kind` (`field`, `extension`, `enum-value`,
  `keyword`, `type`, `map-key`), `detail` (the short type: `string`,
  `repeated Line`, `map<string, int32>`; an enum value's number), `type`
  (the message or enum's full name), `documentation` (the leading, else
  trailing, `.proto` comment), `deprecated`, `number`, `oneof`,
  `insertText`, `suffix`, `suffixCursor`, `retrigger`}. `from`–`to` is the
  whole key or string at the cursor, quotes included, which `insertText`
  (quoted) replaces. A key's `suffix` starts its value, with the cursor
  inside: `: {}` for a message or map, `: []` for a list, `: ""` for a
  string, bytes, enum, `Timestamp`, `Duration` or `FieldMask`, `: ` for
  the rest; none when a `:` already follows. `retrigger` says the new
  position has suggestions of its own (an enum or bool field, a message),
  so the editor asks again at once.

## UI

`TabPanel` shows `GrpcEditor` for a request of kind `grpc`.

- **Address bar.** A `gRPC` label (`gRPC-Web` with a gRPC-Web transport), the address input, a TLS toggle
  (`settings.tls`) and Invoke ("Start stream" for client-streaming and
  bidi methods), or Cancel while a call runs. Invoke (or Enter in the
  address) is disabled, with the reason as its tooltip, without an address
  or a chosen method from the loaded files, and for a client-streaming or
  bidi method with a gRPC-Web transport. The call uses the current
  draft, so unsaved edits are sent.
- **Stream controls** (GRPC-13, GRPC-14, GRPC-22, `GrpcStreamControls`).
  While a client-streaming or bidi call runs, **Send message** (sends the
  message editor's current text with `SendMessage`; edit it between sends)
  and **End stream** (`CloseSend`) sit before Cancel, in the address bar
  and in the response status line; after End stream only Cancel is left.
  A refused send (invalid message, `{{`) shows its reason under the bar
  until the next send. The Message tab of such a method explains the
  flow above the editor.
- **Method row.** A button showing `service/method` with its call-type
  badge opens the picker: a filter field (a combobox; ↑/↓ move, Enter
  chooses, Escape closes) above a listbox grouped by service (full name,
  file, first comment line). Each method shows its name, a badge (`unary`,
  `client stream`, `server stream` in `--m-ws`, `bidi` in `--m-patch`),
  `Input → Output`, a `deprecated` tag with the name struck through, and the
  first line of its comment. Choosing saves `service` and `method`. Under
  the row: the chosen method's short input and output types (full names in
  the tooltip) and comment, or a hint: add `.proto` files, the describe
  error, "… is not in the loaded .proto files" for a saved method that is
  gone, with a link to the Proto tab.
- **Sub-tabs** (kept per request): Proto (with the file count and the error
  count), Message, Metadata, Auth (the [auth editor](frontend.md#auth-editor)
  with `protocol` `grpc`: it says that a matching Metadata row wins, and
  warns about a type or location gRPC cannot send), Settings, Notes, Code
  (the call as a `grpcurl` command, GEN-10: `GRPCService.Snippet`,
  [snippets.md](snippets.md#grpcurl)). A request without files opens on
  Proto, others on Message.
- **Proto tab.** "Use the folder's proto setup" (`proto.inherit`). Off: the
  request's Proto files and Import paths lists, each with an add button
  (the native pickers; paths already listed are not added twice) and a
  remove button per row. On: which folder supplies the setup and its lists,
  read-only until "Edit folder setup" (or "Set up <folder>" when no folder
  has one yet, for the request's own folder), which edits them through
  `UpdateFolderGRPCProto`. The inherited setup is loaded again after every
  `collection:changed`. Below, the describe summary ("7 files loaded · 2
  services · 1 error · 1 warning"), Reload (describe without the cache) and
  the diagnostics as `file:line:column message`, errors and warnings
  marked.
- **Describe** runs 150 ms after the effective setup (own or inherited)
  last changed; a newer setup cancels the previous call. The last result
  stays visible while loading.
- **Message** (`GrpcMessageEditor`) is a JSON `CodeEditor`, stored as
  typed, under the input type's full name, a note on the JSON mapping and
  **Generate example**, which replaces the text with `ExampleMessage`'s
  (undo brings the old text back; disabled without a chosen method or when
  read-only). 300 ms after typing pauses, and when the method or setup
  changes, the text is checked with `ValidateMessage` (a newer check
  cancels the running one); the first problem gets the shared error
  marker (wavy underline, tinted line, the message on hover,
  `errorMarker.ts`) and a status line below, "Line 3, column 3" (a button
  that moves the cursor there) and the message. Nothing is checked
  without a chosen method; a rejected check marks nothing.
- **Autocomplete** in the message (GRPC-17, `grpcCompletion.ts`): the
  chosen method and setup sit in a `StateField` of the editor, and a
  CodeMirror completion source asks `CompleteMessage` at the cursor after
  a name character, a quote, `{`, `[`, `:` or `,` (or on Ctrl-Space); a
  newer keystroke cancels the running call. The popup is the GraphQL
  editor's (`completionPopup.ts`): Go's order, a kind badge (`F` field,
  `X` extension, `E` enum value, `K` literal, `T` type URL, `M` map key),
  the short type on the right, deprecated items struck through, the
  keyboard hints, and a side panel with the full type, the comment, the
  deprecation and "owner · Field · number 3 · oneof pay". Accepting
  inserts the key with its value hint and the cursor inside, and opens the
  values at once when there are any (an enum field's names). Nothing
  without a chosen method or when read-only; a failed call shows nothing.
- **Choosing a method fills in its example** while the message is
  untouched: blank, `{}`, or the same JSON as the example of the method
  chosen before (whitespace aside). Anything else is a user edit and is
  kept, and so is an edit made while the example loads.
- **Metadata** is a `KeyValueTable` with a note that keys are
  sent in lower case and `-bin` values are Base64. **Settings**: transport
  (gRPC (HTTP/2), gRPC-Web (binary), gRPC-Web (text, Base64), each with a
  one-line explanation), compression (None or gzip), use TLS, verify TLS
  certificates (with TLS only), connect timeout and deadline in
  milliseconds.
- **Response pane** (`components/response/GrpcResponsePanel`, GRPC-11,
  GRPC-12, GRPC-19, GRPC-20). `App.tsx` puts it in the shell's response
  slot for an active `grpc` tab, as it does `HttpResponsePanel` for HTTP.
  Before the first call, "No response yet"; a call Go refused before
  anything was sent (invalid message, bad address), an error box with
  Go's message and hint. Otherwise `GrpcResponseView`, in the HTTP
  response view's layout and styles (`ResponseView.module.css`, its `Meta`
  and `Segmented`), live while the call runs:
  - **Status line**: the status name coloured by outcome (`OK` in `--ok`,
    any other code in `--bad`, "Cancelled" in `--warn`, "Calling…" with a
    pulsing dot while running) with its numeric code, the status message,
    the time (a running timer, then the call's `elapsedMs`), the messages
    sent (streams from the client: "3 sent", bytes in the tooltip), the
    messages received, their size (uncompressed), the call type and the method
    (full name in the tooltip); Cancel while the call runs. A visually
    hidden `aria-live` summary says it in one line: "Calling
    pkg.Service/Method… 3 messages received" ("… 2 sent, 1 message
    received, request stream ended" for a stream from the client), "OK · 3
    messages · 12 ms" ("OK · 1 message · 2 sent · 30 ms"),
    "NOT_FOUND: no such order · 0 messages · 4 ms", "Cancelled · …", with
    error details counted ("INVALID_ARGUMENT: the order is invalid · 3
    error details · 0 messages · 5 ms").
  - **Tabs** Messages, Headers, Trailers and Status, each with a count
    badge (messages, entries, error details).
  - **Messages** of a unary call (the call type is recorded at Invoke)
    opens on **Response**: the first response message
    as pretty JSON in a read-only `CodeEditor` (highlighting, folding,
    selection; copy and search buttons in the status line, search is
    CodeMirror's panel), "Waiting for the response…", "The call returned no
    message" (pointing to the Status tab for a non-OK status), or a message
    that did not decode as its reason and Base64 bytes; a note when the
    server sent more than one. The **Log** switch (and a stream, always; a
    client-streaming call opens on it, with the switch to its one
    response) shows the shared `MessageLog` of the call, the **streaming
    timeline** (GRPC-22) in the order Go emitted it: the request as sent ↑
    with its method, deadline and metadata in the detail view (for a
    stream from the client an `open` item • "Stream opened"), each message
    sent ↑ with its index as badge, the headers •, each response ↓ with its
    index as badge (Base64 or a hex dump with the decode error when it did
    not decode), the `half-close` • ("Request stream ended after 3
    messages") and the status • with the trailers, the counts and the
    types of its error details. The log is
    virtualized over a ring buffer applied once per frame, so a stream of
    thousands of messages stays responsive; its search, filters, follow
    mode and detail pane (pretty JSON, copy) apply.
  - **Headers** and **Trailers** are two-column tables in the HTTP Headers
    tab's style, in the order received, binary values marked `Base64`;
    "No response headers yet." / "Trailers arrive when the call ends."
    while running.
  - **Status** (`GrpcStatusPanel`) shows the name, code and message, then
    each detail as a card: its short type (full name in the tooltip) and
    type URL over the JSON, or, for an unknown type or one that did not
    decode, a note and the Base64 bytes. Details that are not a
    `google.rpc.Status` show the reason and the raw bytes.
- **Call state** lives in `commands/grpcCalls.ts`, one slot per request
  (like the WebSocket sessions): switching tabs keeps a running stream and
  its log, closing the tab cancels the call and drops them. Each Invoke
  clears the log; a new Invoke is ignored while one runs. `useGrpcCall(id)`
  gives the slot and its state. `send(message)` and `closeSend()` drive a
  stream from the client (`canSend`: running, streaming from the client,
  not half-closed, the `opId` known); the state counts `sent`,
  `sentBytes`, sends in flight (`sending`) and keeps `halfClosed` and the
  last refusal (`sendError`; none once the call has ended, when the status
  says why).
- **Read-only** files show a notice; nothing can be edited, folder setups
  included.

## Assertions

TEST-08. A grpc request's assertions (its Tests sub-tab) are checked when
a call ends, against its response messages (each in the protobuf JSON
mapping), its status code (`status`), its headers and trailers with
`grpc-status` and `grpc-message` (`header`), its last message (`body`)
and its time: the `end` item's `tests`, shown in the response view's Tests
tab. `GRPCService.CheckAssertions({opId})` (Check now) checks a running
call (a stream before it ends) or one of the last ones that ended, with
the assertions as they are then. See
[assertions.md](assertions.md#streams).

## Tests

- projectfile: golden examples `grpc` and `folder_grpc` (also shown in
  file-format.md), defaults written as missing members, unknown members of
  `proto`, `settings` and a folder's `grpc` kept.
- collection core: create, save, duplicate, rename and reload a grpc
  request. `CollectionService`: auto-save round trip with a golden
  `RequestDTO`; `InheritedGRPCProto` from a grandparent and a nearer folder,
  `UpdateFolderGRPCProto` writing, keeping unknown members, removing a
  setup, and its errors.
- `go test -race ./internal/app/services/grpc`: the linker's multi-root
  test project copied into a temporary project (three import roots,
  streaming kinds, sorted services, cache hit and Reload), a golden
  `DescribeResultDTO` with comments and a deprecated method, a missing
  import root (the diagnostic's position and stored path; not cached), a
  missing import path and file, no files, no project, cancellation; cache
  invalidation when the file or one of its imports changes and when a
  missing import is created; the pickers with a fake dialog.
- Calls, `go test -race ./internal/app/services/grpc` (`invoke_test.go`),
  against the in-process `grpctest` server with a `calls.proto` project and
  handlers for its methods: unary over h2c and over TLS (verification off),
  an empty message, server streaming (three messages in order), metadata
  (lower-cased keys, disabled and nameless rows skipped, repeated keys,
  `-bin` values padded, unpadded and URL-safe as the server received them,
  echoed back in headers and trailers as binary), a non-OK status with
  details in a trailers-only response, cardinality (zero and two messages
  for a unary call), a response that does not decode, **deadline expiry**
  (the server sees `grpc-timeout` ≤ 200 ms and its context ends; the call
  ends `DEADLINE_EXCEEDED`, kind `timeout`), **cancellation** (the server's
  context is cancelled; done `cancelled`, end `CANCELLED`), an untrusted
  TLS certificate, one client per connect timeout, every rejection above
  (no operation started), the `CallEventDTO` JSON shape, and `seq`
  contiguity of every call's events.
- Streams from the client, `go test -race ./internal/app/services/grpc`
  (`stream_test.go`, the `Upload` and `Chat` handlers of `calls.proto`):
  client streaming (three messages, half-close, the one joined response;
  the editor's message ignored by Invoke; none at all), bidi ordering over
  h2c and TLS (sent, reply, sent, reply, half-close, end, with
  non-decreasing times and a second CloseSend harmless), four goroutines
  sending forty messages concurrently while replies arrive (indexes in
  order, every reply after its message), cancel mid-stream (`CANCELLED`,
  later sends refused), a server error mid-stream (`ABORTED` after one
  reply), the deadline of a stream that is never half-closed, and every
  refusal of SendMessage and CloseSend, with no call left registered.
- Transport and compression, `go test -race ./internal/app/services/grpc`
  (`web_test.go`): unary, server streaming and a `NOT_FOUND` status with
  details over gRPC-Web binary and text, against plaintext HTTP/1.1, TLS
  with h2 and TLS with HTTP/1.1 only (headers, trailers from the trailer
  frame, the request's content type and `transport` in the request item);
  `grpc.web_client_streaming` for client-streaming and bidi methods; gzip
  over gRPC and both gRPC-Web framings (compressed request, compressed
  replies, a gzip reply to an uncompressed request); unknown transport and
  compression values.
- Message, `go test ./internal/app/services/grpc` (`message_test.go`,
  a `shop.proto` project): the golden example of a message with an enum,
  a list, a map, a recursive type, a `Timestamp` and a oneof, which
  validates; an example for a client-streaming method; validation of
  valid, blank and proto-named input, and the positions of an unknown
  field, a syntax error, a wrong type, text that ends early, two oneof
  members and a line with an astral character (UTF-16 vs. byte offsets);
  the method errors of both calls. `protojson.Example` has its own tests
  ([protobuf-json.md](protobuf-json.md#tests)). `complete_test.go`:
  `CompleteMessage` with UTF-16 offsets around an astral character, an
  enum value, a nested list element, a set oneof member, no re-linking
  (the cached result is the same), a method error.
- Autocomplete, `go test ./internal/core/grpc/protojson` (`complete_test.go`,
  a proto2 `shop.proto` with every shape): positions at every nesting
  level (top level, nested message, list element, map value, `Any` by its
  `@type` before or after the cursor and of a well-known type), keys in
  quotes, bare and being edited (no suffix before a `:`), `.proto` and
  custom JSON names, extensions, set fields and oneof members (also after
  the cursor) left out, bool map keys, enum values quoted, bare and in
  lists and maps, `true`/`false`/`null` where they fit, `Struct`/`Value`,
  `Timestamp`, `Empty`, unknown fields, unclosed text, an escaped quote;
  items' types, full names, comments, numbers, oneofs, deprecation,
  suffixes and `retrigger`; no pool; a 5000-element message completes in
  well under 200 ms; `FuzzComplete` (ranges stay in the text).
- Status, `go test -race ./internal/app/services/grpc` (`status_test.go`,
  grpctest's `Echo/Status` method, which ends with the status sent in its
  `x-status-*` metadata): a successful call's headers (no `grpc-status`)
  and trailers (`grpc-status: 0`, no `content-type`) kept apart; every code
  from `OK` to `UNAUTHENTICATED` and code 42, with messages (status in
  trailers) and trailers-only, name, percent-decoded message and error
  kind by class; a failing call with thirteen details (all ten standard
  error details, a type from the request's files, an unknown type, a
  detail that does not decode), both with trailers and trailers-only;
  details that are not a Status. `internal/core/grpc/rpcstatus`: the
  built-in types, a Marshal/Decode round trip with the caller's types
  first, unknown types without them, bytes that are not a Status.
- Frontend `components/editor/GrpcEditor.test.tsx` against mocked
  services: render, picker grouping, badges, deprecated styling, filter and
  keyboard/mouse choice saved through auto-save, missing method warning,
  own setup edited with the pickers, diagnostics list and Reload, inherited
  setup with folder editing and reload on `collection:changed`, setting up
  the parent folder, address/TLS/settings auto-save; `lib/grpc` helpers.
  Calls (with `GrpcResponsePanel` rendered next to the editor): the
  Invoke input built from the request, events shown in the status line,
  the unary response as JSON with copy and search, the log, Headers and
  Trailers; a server stream shown live in the log, 5,000 messages
  rendering only the rows in view, cancelled through `OpsService.Cancel`;
  a rejected start as an error box and a non-OK status; a response that
  did not decode; Invoke disabled with its reason; the call kept across a
  tab switch; a bidi stream started, messages sent from the editor and
  from the response status line, the timeline in order, a refused send
  shown, End stream leaving only Cancel, the end with the sent count; a
  client stream opening on the timeline with its Response switch. `commands/grpcCalls.test.ts`: the call type, start, first
  message and bytes received kept in the call state.
  Message: the example filled in when a method is chosen with a blank
  message and when the text is still the previous method's example
  (reformatted), kept after an edit and for a message written before any
  method was chosen; Generate example and its failure; the Go check's
  marker, status line and position button, cleared once the text is
  valid; no check without a method; autocomplete asks `CompleteMessage`
  with the method and cursor and shows its items, not without a method.
  `grpcCompletion.test.ts`: Go's options in order with type and badge, the
  side panel, the trigger characters, no target or read-only, a failed
  call, a pick inserted with its hint, the values opened after an enum
  key, installed once. Status: the tab before a call, a
  failing call's details (decoded, unknown type, not decoded) with the
  count in the status line and the tab, and details that are not a
  Status.
  `commands/grpcCalls.test.ts`: events as log messages (the error
  details' types in the status item; open, sent and half-close items), one
  call at a time, cancel on tab close, send and closeSend of a bidi call
  (refusal kept, counts, nothing after the half-close), none for a server
  stream.

## Not yet

Saved messages to send on a stream (as the WebSocket editor has),
`{{variables}}` in calls,
server reflection in the request (GRPC-03), the persisted schema cache
(GRPC-05), custom CA and mTLS settings in the request (GRPC-23; the
transport supports them), gRPC-Web endpoints under a path prefix (the
address is `host:port` only), compression algorithms other than gzip, and
a gRPC section in the
folder settings view (COL-03, [frontend.md](frontend.md#folder-settings)):
a folder's shared proto setup is still created and edited from a gRPC
request's proto panel, and the folder view does not show it. Folder
headers are not sent as gRPC metadata.
