# Socket.IO

PostSonde talks to Socket.IO v3 and v4 servers, and to v2 servers with the
version switch ([Socket.IO v2](#socketio-v2-engineio-3), SIO-02) (SIO-01…SIO-07). This page
covers the packet codec, the client that connects over WebSocket or
long-polling ([Client](#client-sioclient)), the `socketio` request kind,
the service that runs a connection as an operation and the editor. All of
it is written with the standard library only.

```
Socket.IO  socketio.Packet ──Encode──▶ "2/admin,456[\"project:delete\",123]" + attachments
              ▲                                   │
              └─── Decoder.Feed ◀── message packets (text, then one binary per attachment)
                                                  │
Engine.IO  engineio.Packet ──EncodePacket──▶ one WebSocket message per packet
                           ──EncodePayload─▶ one long-polling body: packets joined by \x1e
```

Two packages under `internal/core/realtime/`:

- `engineio`: Engine.IO protocol v4, the transport layer
  ([engine.io-protocol](https://socket.io/docs/v4/engine-io-protocol/)).
- `socketio`: Socket.IO protocol v5, the protocol of Socket.IO v3 and v4
  servers ([socket.io-protocol](https://socket.io/docs/v4/socket-io-protocol/)).
  It imports `engineio` only to wrap packets in message packets.

## Engine.IO: `engineio`

| Type | Digit | Data |
|---|---|---|
| `Open` | `0` | handshake JSON (server) |
| `Close` | `1` | none |
| `Ping` | `2` | none, or `probe` during the upgrade |
| `Pong` | `3` | same as the ping |
| `Message` | `4` | text, or binary |
| `Upgrade` | `5` | none |
| `Noop` | `6` | none |

```go
// WebSocket: one packet per message.
p, err := engineio.DecodePacket(msg, isBinaryMessage, engineio.Options{})
msg, isBinary, err := engineio.EncodePacket(engineio.Text(engineio.Message, "hello"))

// HTTP long-polling: one body, packets separated by \x1e,
// binary messages as "b" + Base64.
packets, err := engineio.DecodePayload(body, engineio.Options{})
body, err := engineio.EncodePayload(packets)

// The open packet.
h, err := engineio.ParseHandshake(open.Data) // SID, Upgrades, PingInterval, PingTimeout, MaxPayload
```

- **Text packets** are the type digit and the data (`4hello`, `2probe`).
- **Binary messages** go in a WebSocket binary message as the raw bytes,
  without a type prefix (v3 had a `0x04` byte first; v4 does not). Over
  long-polling they are `b` followed by standard Base64 with padding
  (`bAQIDBA==`). A `b` packet in a WebSocket text message is decoded too,
  as engine.io-parser does.
- **Payloads** are split on every `\x1e`; encoding refuses text data that
  contains one, since it would split the packet.
- **Handshake.** `sid` must be a non-empty string, `upgrades` an array of
  strings (empty on a WebSocket-only connection), `pingInterval` and
  `pingTimeout` positive integers up to 2^31-1 ms; `maxPayload` is
  optional. Other fields are ignored. `Handshake.Packet` writes the fields
  in the reference server's order.
- **Decoding copies**: packets never alias the input.

| Code | When |
|---|---|
| `engineio.empty_packet` | An empty WebSocket message, body or record between separators (details `index`, `offset` in a payload). |
| `engineio.unknown_type` | The first character is not `0`-`6` or `b`. The hint suggests another protocol version. |
| `engineio.invalid_utf8` | A text packet that is not UTF-8. |
| `engineio.invalid_base64` | A `b` packet with invalid, unpadded, non-canonical or line-broken Base64. |
| `engineio.unexpected_data` | A close, upgrade or noop packet with data. |
| `engineio.too_big` | A message or body over `Options.MaxSize` (default 16 MiB, negative = none). Detail `limit`. |
| `engineio.invalid_handshake` | The open packet's data is not a valid handshake. Detail `field`. |
| `engineio.invalid_argument` | Encoding a packet that cannot be written (kind `invalid_argument`). |

All decoding errors have kind `protocol`.

### Engine.IO 3

`engineio/v3.go` is protocol 3 (engine.io 3.x, socket.io v2, and v3/v4
servers with `allowEIO3`), whose packet types and text form are those of
protocol 4. What differs is binary data and the long-polling payload
(engine.io-parser 2):

```go
msg, isBinary, err := engineio.EncodePacketV3(p) // binary: the byte 4, then the data
p, err := engineio.DecodePacketV3(msg, isBinary, engineio.Options{})
body, err := engineio.EncodePayloadV3(packets)   // 6:4hello2:40 … binary as "b4" + Base64
body, err := engineio.EncodeBinaryPayloadV3(packets) // XHR2: 0|1, length digits, 255, packet
packets, err := engineio.DecodePayloadV3(body, engineio.Options{}) // either form
engineio.LooksLikeV3Payload(body) // "<digits>:" first: an Engine.IO 3 server's answer
```

- **Text payload**: every packet as `<length>:<packet>`, the length in
  UTF-16 code units (JavaScript's `String.length`), so `€` counts 1 and
  `😀` 2; a binary message as `b4` and standard Base64.
- **Binary payload** (XHR2, the answer to a client that did not send
  `b64=1`): per packet a marker byte (0 text, 1 binary), the length as
  one byte per decimal digit, the byte 255, then the packet (a binary one
  with the type byte 4 first). The client asks for text payloads
  (`b64=1`) and reads both.
- A malformed length or marker is `engineio.invalid_payload` (details
  `index`, `offset`).

## Socket.IO: `socketio`

A packet's text form is

```
<type>[<attachments>-][<namespace>,][<ack id>][<JSON payload>]
```

| Type | Digit | Payload | Ack ID |
|---|---|---|---|
| `Connect` | `0` | none, or an object (auth from the client, `{"sid":...}` from the server) | no |
| `Disconnect` | `1` | none | no |
| `Event` | `2` | array, first element the event name (string or number) | optional |
| `Ack` | `3` | array | required |
| `ConnectError` | `4` | object (`{"message":...,"data":...}`) or string (v3) | no |
| `BinaryEvent` | `5` | as `Event`, with placeholders | optional |
| `BinaryAck` | `6` | as `Ack`, with placeholders | required |

Examples from the spec: `0/admin,{"token":"123"}`, `2/admin,456["project:delete",123]`,
`3/admin,456[]`, `51-["hello",{"_placeholder":true,"num":0}]` followed by one
binary message.

```go
// Decoding one direction of a connection: feed every Engine.IO message.
d := socketio.NewDecoder(socketio.Options{})
p, err := d.Feed(eioPacket) // nil, nil while attachments are still expected
args, err := p.Args()       // []any with []byte where the placeholders were
name := p.EventName()

// Building and encoding.
p, err := socketio.NewEvent("/admin", "upload", map[string]any{"name": "f.bin"}, fileBytes)
p.ID, p.HasID = 2, true      // ask for an acknowledgement
msgs, err := socketio.EncodeMessages(p) // []engineio.Packet: text + one binary per attachment
// 451-2["upload",{"name":"f.bin"},{"_placeholder":true,"num":0}]  then the bytes
```

- **Exact round trip.** `Packet.Data` is the JSON payload as it was on the
  wire, placeholders included, so `Encode` gives back the captured bytes.
  `Value` and `Args` rebuild the value on demand: objects as
  `map[string]any`, numbers as `json.Number`, attachments as `[]byte`.
- **Binary.** `Deconstruct` (used by `NewEvent`, `NewAck`, `NewConnect`)
  replaces each `[]byte` in `nil`/`[]any`/`map[string]any` values with
  `{"_placeholder":true,"num":N}` in order and switches `Event`/`Ack` to
  `BinaryEvent`/`BinaryAck`, like socket.io-parser. `json.RawMessage`
  arguments are copied as they are; other Go values go through
  `encoding/json`. HTML characters are not escaped (as `JSON.stringify`).
  Placeholders are only special in binary packets.
- **Namespaces.** The main namespace `/` is left out. When decoding, the
  namespace runs to the first `,` or to the end, as in socket.io-parser, so
  `1/admin` is accepted (and encodes as `1/admin,`).
- **Strict checks** beyond socket.io-parser: no leading zeros in the
  attachment count or ack ID; ack IDs at most 2^53-1 and only on events and
  acks; acks must have one; events, acks and CONNECT_ERROR must have a
  payload; placeholder `num` must be an integer literal.

| Code | When |
|---|---|
| `socketio.empty_packet` | An empty text packet. |
| `socketio.unknown_type` | The first character is not `0`-`6`. |
| `socketio.invalid_attachments` | A malformed attachment count, or `Decode` got fewer attachments than declared. |
| `socketio.too_many_attachments` | More than `Options.MaxAttachments` (default 256, negative = none). |
| `socketio.invalid_namespace` | Encoding a namespace without a leading `/` or with a `,`. |
| `socketio.invalid_ack_id` | Leading zeros, above 2^53-1, on a type without ack IDs, or missing on an ack. |
| `socketio.invalid_payload` | Not JSON, data after the value, the wrong shape for the type, or missing. |
| `socketio.reserved_event` | An event named `connect`, `connect_error`, `disconnect`, `disconnecting`, `newListener` or `removeListener`. |
| `socketio.invalid_placeholder` | A placeholder whose `num` is not an index of an attachment. |
| `socketio.unexpected_text` | A text packet while attachments were expected (the decoder resets). |
| `socketio.unexpected_binary` | A binary message while none was expected, or extra attachments to `Decode`. |
| `socketio.not_message` | `Feed` got an Engine.IO packet other than a message. |
| `socketio.too_big` | A text packet over `Options.MaxPacketSize` (default 16 MiB, negative = none). |
| `socketio.invalid_argument` | Encoding a value that cannot be written. |
| `socketio.invalid_args` | `ParseArgs`: arguments typed as JSON that are not JSON, or a `$base64`/`$hex` marker that does not decode (detail `offset`). |

Decoding errors have kind `protocol` and a detail `offset` (byte offset in
the text packet); encoding errors have kind `invalid_argument`.

## Client: `sioclient`

`internal/core/realtime/sioclient` connects to one namespace of a Socket.IO
v4 server and never reconnects (a new `Client` connects again).

```go
c, err := sioclient.New(sioclient.Options{
	URL:       "https://chat.example.com/admin?room=lobby", // path = namespace unless Namespace is set
	Path:      "/socket.io/",                              // "" = /socket.io/; a trailing / is added
	Auth:      json.RawMessage(`{"token":"abc"}`),         // the CONNECT packet's auth (SIO-07)
	Transport: sioclient.TransportAuto,                     // auto, websocket, polling, upgrade
	Dialer:    dialer,
	OnTrace:   func(t sioclient.Trace) { … },               // everything, in order
})
info, err := c.Connect(ctx)                  // handshake, upgrade/fallback, namespace CONNECT
res, err := c.Emit("upload", []any{"f.bin", data}, true) // []byte = attachment; ack ID 0, 1, …
c.Disconnect(ctx)                            // DISCONNECT, Engine.IO close, WebSocket close
```

- **URL.** `http`, `https`, `ws` or `wss`: the scheme only says whether
  TLS is used, and both transports are derived from it
  (`<scheme>://<host><path>?<query>&EIO=4&transport=polling|websocket[&sid=…]`).
  The URL's query goes with every request, as typed. Its path is the
  namespace unless `Namespace` is set, as with `io("https://host/admin")`;
  a namespace without a leading `/` gets one. Userinfo becomes Basic
  auth unless a header row sets `Authorization`.
- **Transports** (SIO-01). `websocket` opens a WebSocket (`wsclient`: the
  headers, cookies, TLS settings, CA bundles and client certificate of a
  WebSocket request) and reads the open packet. `polling` runs the
  handshake as a GET and then keeps one GET open at a time (the server
  answers when it has packets) while writes are POSTs of a payload
  (`text/plain;charset=UTF-8`, one at a time); requests go through
  `httpclient.NewTransport`, redirects are not followed, cookies are taken
  from and stored in the jar by hand. `upgrade` is socket.io-client's
  default: polling, then, when the handshake offers `websocket`, a probe
  (`2probe` → `3probe`, within the server's ping timeout; a failed probe
  is a `TraceUpgradeFailed` and polling goes on), a pause of polling (the
  server ends the pending GET with a noop), and the upgrade packet `5`
  over the WebSocket, which carries everything after it. `auto` (the
  default) tries WebSocket and falls back to polling when that connection
  cannot be made (refused, a rejected upgrade such as a server with
  WebSocket off, TLS), reporting the error as a `TraceFallback`.
- **Heartbeat.** Every ping is answered with a pong on the current
  transport. No ping for `pingInterval + pingTimeout` fails the connection
  with `sioclient.ping_timeout` (reason `ping timeout`); when no ping ever
  came, the hint says a v2 server never pings.
- **Namespace** (SIO-06). After the Engine.IO handshake the client sends
  CONNECT with the auth payload (kept as typed, compacted) and waits for
  the server's CONNECT (`{"sid":…}`, the socket ID) or CONNECT_ERROR,
  which fails `Connect` with `sioclient.connect_error` (kind
  `permission_denied`, details `namespace`, `message` and `data`: an
  unknown namespace is "Invalid namespace", a middleware's `next(new
  Error(…))` its message). Packets of other namespaces are reported, not
  acted on.
- **Emit and acks** (SIO-03, SIO-05). `Emit` builds the event with
  `socketio.NewEvent` (reserved names are refused) and, with an ack,
  numbers it from 0 like socket.io-client. The matching ACK is reported
  with the emit's event name and the round trip (`Trace.Matched`, `RTT`);
  none within `AckTimeout` (default 10 s) is a `TraceAckTimeout`. The
  server's own ack requests are shown but not answered.
- **End.** `Reason` gives socket.io-client's names: `io client
  disconnect` (Disconnect, no error), `io server disconnect` (a
  DISCONNECT), `transport close` (an Engine.IO close packet, or the
  server closed the WebSocket), `transport error`, `ping timeout`, `parse
  error` (with the `engineio.*`/`socketio.*` error), `connect failed`.
  `Disconnect` sends the DISCONNECT and then the Engine.IO close in a
  write of its own (over long-polling, a second POST), as engine.io-client
  does: socket.io handles a DISCONNECT on the next tick, so a close in the
  same payload would reach the server as `transport close`.
- **Timeouts.** `HandshakeTimeout` (default 20 s, negative none) bounds
  the handshake, the upgrade and the namespace CONNECT together:
  `sioclient.timeout`, or `sioclient.cancelled` when the caller cancels.
- **The dial guard.** The context values of `Connect` are kept for every
  later dial (long-polling requests open new connections), so the
  user-operation mark (PRV-05) covers the whole connection.

| Code | When |
|---|---|
| `sioclient.invalid_url`, `sioclient.scheme_unsupported`, `sioclient.invalid_path`, `sioclient.invalid_namespace`, `sioclient.invalid_auth`, `sioclient.invalid_header`, `sioclient.invalid_transport`, `sioclient.invalid_version` | `New`: options that cannot be used (kind `invalid_argument`). |
| `sioclient.already_connected`, `sioclient.not_open` | `Connect` twice; `Emit` while not connected. |
| `sioclient.handshake_rejected` | The long-polling handshake got a status other than 200 (details `status`, `body`; the message has the server's `message`, e.g. "Transport unknown"). |
| `sioclient.version_mismatch` | A v3/v4 client got an Engine.IO 3 handshake: the server is socket.io v2 (detail `version`). |
| `sioclient.invalid_handshake` | The first packet is not an Engine.IO open packet. |
| `sioclient.connect_error` | The server refused the namespace. |
| `sioclient.ping_timeout` | No ping in time. |
| `sioclient.transport_error` | A long-polling request failed or got a status other than 200. |
| `sioclient.upgrade_failed` | The probe failed (only in a `TraceUpgradeFailed`). |
| `sioclient.timeout`, `sioclient.cancelled` | Connecting took too long, or was cancelled. |

WebSocket failures keep their `wsclient.*` codes and dial and TLS failures
their `netx.*` codes.

### Socket.IO v2 (Engine.IO 3)

`Options.Version` is `v4` (also `""` and `v3`: Socket.IO v3 and v4
servers) or `v2` (SIO-02): Engine.IO 3 and Socket.IO protocol 4, spoken by
socket.io 2.x servers and by v3/v4 servers with `allowEIO3`. The version is
chosen per request (`version` in the file); the client does not probe for
it, but each wrong choice fails with a clear error:

| Case | Error |
|---|---|
| v3/v4 client, v2 server, long-polling | `sioclient.version_mismatch` (the handshake is an Engine.IO 3 payload), hint "Choose Socket.IO v2 in the Options" |
| v3/v4 client, v2 server, WebSocket | the handshake works, but no ping comes: `sioclient.ping_timeout` with the hint that a v2 server never pings |
| v2 client, v3/v4 server without `allowEIO3` | `sioclient.handshake_rejected` "Unsupported protocol version", hint to choose v3 / v4 |

With `v2`:

- **URLs** carry `EIO=3`, and long-polling ones `b64=1` too.
- **Encoding** is Engine.IO 3's ([above](#engineio-3)): WebSocket binary
  messages with the type byte, `<length>:` payloads, `b4` Base64.
- **Heartbeat.** The client pings every `pingInterval` (a `ping` trace
  with `Sent`, a sent item) and the server answers (a received `pong`);
  every packet received restarts the watchdog of `pingInterval +
  pingTimeout`, as engine.io-client 3 does. No answer fails the connection
  with `sioclient.ping_timeout`.
- **Namespaces.** The server joins the main namespace at once (`40`), so
  no CONNECT is sent for `/`; another namespace is joined with a CONNECT
  whose namespace carries the URL's query (`0/admin?token=abc,`, as
  socket.io-client 2 sends it; a `,` in the query is written `%2C`). The
  server's CONNECT has no payload: the socket ID is the Engine.IO session
  ID, after the namespace and `#` for another namespace (socket.io 2's
  `socket.id`). A refusal is `4/admin,"Invalid namespace"`: a string,
  reported as `sioclient.connect_error` with that message.
- **No auth payload** (it came with Socket.IO 3): `New` refuses one with
  `sioclient.invalid_auth` and the hint to put the token in the URL's
  query, which goes with the handshake and the namespace's CONNECT. The
  Auth tab's credentials with `authIn` `payload` go there on their own
  ([Auth](#auth)).
- Events, acks, binary events and acks and DISCONNECT are written as in
  v3/v4 (socket.io-parser 3 and 4 agree on them).

## Request kind `socketio`

A `socketio` request file ([file-format.md](file-format.md#socketio-request-kind-socketio))
holds the URL with its query rows, headers, `path`, `namespace`,
`authPayload`, `transport`, `version` (`v2`, left out for v3/v4), the
composer's `event`, `args` and `ack`, the `savedEmits`, the `listen`
filter, settings (`connectTimeoutMs`, `ackTimeoutMs`, `verifyTls`,
`sendCookies`), and the request's `auth` and `authIn` ([Auth](#auth)). In
`RequestDTO` the arguments are `message` and the saved emits are
`savedMessages` with `event` and `ack`.

**Arguments** are JSON text: an array holds one argument per element,
another value is one argument, empty text none. An object whose only
member is `$base64` or `$hex` is a binary argument (an attachment),
anywhere in the value, e.g. `["avatar", {"name": "a.png", "data":
{"$base64": "iVBORw0KGgo="}}]`. Arguments without markers are sent as
typed (compacted, member order kept). The log shows binary attachments
the same way. The parsing and the log's form are
`socketio.ParseArgs` and `socketio.DisplayArgs` in the codec package,
shared with the local Socket.IO server's composer and rules.

## Service

`internal/app/services/socketio` (`SocketIOService`, domain `sio`):

- `Connect(ConnectInputDTO)` renders the `{{variables}}` of the URL, the
  enabled headers, the path, the namespace and the auth payload with the
  request's scopes (undefined ones together as `template.unresolved`
  unless `allowUnresolved`), builds the client (the project's cookie jar,
  the CA bundles for TLS, the host's client certificate) and runs it as a
  `sio` operation. Its items ([events.md](events.md#socketio-connections))
  are the traces: `open`, `fallback`, `upgrade`, `upgrade_failed`,
  `packet`, `connected`, `event`, `ack`, `ack_timeout`, `ping`, `pong`,
  `closed`. Cancelling the operation disconnects gracefully (2 s at most).
- `Emit(EmitInputDTO)` renders the event name and the arguments' text as
  they are now, parses the arguments (`sio.invalid_args` with the
  offset), and emits; the event, as sent, comes back as an item before any
  reply. `sio.not_found` for a connection that ended, `sio.invalid_event`
  for no name.
- Opening another project cancels every connection.

## Auth

A socketio request has the same `auth` as an HTTP request (AUTH-31), its
own or its closest folder's ([auth.md](auth.md#auth-on-every-protocol-auth-31)),
next to the hand-written `authPayload`, which is unchanged. `authIn` says
where the auth goes:

- `"handshake"` (the default, a missing member): the auth's headers go with
  every transport request (each long-polling request and the WebSocket
  upgrade) after the header rows, an enabled row of the same name winning;
  its query parameters (an API key, JWT or OAuth 2.0 token with `in`
  `query`) are added to the URL's query, which is sent with every request.
- `"payload"`: the credentials become members of the CONNECT packet's
  auth payload, for servers that read `socket.handshake.auth` (browsers
  cannot set headers on a WebSocket): `token` for a bearer token, a JWT or
  an OAuth 2.0 token (without the prefix), `username` and `password` for
  Basic and Netrc, the key's name for an API key. They are added after the
  members typed in `authPayload`, which win; an empty payload becomes an
  object. A payload that is not a JSON object is refused with
  `request.auth_payload_invalid`. Nothing goes in the headers then.

Digest, OAuth 1.0, AWS Signature and Hawk refuse the connect with
`request.auth_not_applicable`, and an `authIn` this version does not know
with `sio.invalid_auth_in`. The `open` item's URL and the CONNECT packet
item (and a v2 CONNECT's namespace, which carries the query) are shown
with the auth's values masked.

**With `version` `v2`** there is no CONNECT auth payload: socket.io v2
servers read credentials from `socket.handshake.query`, which v2 clients
fill from the URL's query. So `"payload"` sends the same members (`token`,
`username` and `password`, or the API key's name) as query parameters of
the URL instead: they go with every transport request of the handshake
and, for a namespace other than `/`, in its CONNECT (`0/admin?token=…,`).
A parameter of that name typed in the URL wins. `"handshake"` works as
with v3/v4 (headers and query of the transport requests). A hand-typed
`authPayload` is still refused on v2 (`sioclient.invalid_auth`).
`TestConnectSendsAuthV2` checks both namespaces against `siotest` in V2
mode.

## UI

The Socket.IO editor ([frontend.md](frontend.md#socketio-editor)) has
the URL bar with Connect/Disconnect, the sub-tabs Params, Headers, Auth
(the [auth editor](frontend.md#auth-editor) with `protocol` `socketio`,
then "Send the credentials above in" the handshake or the auth payload
(`authIn`, disabled for No auth), then the auth payload), Options (the
server version, path, namespace, transport, the events to show) and
Settings, the log and the emit composer (event, Ack, JSON
arguments, Emit) with the saved emits. The listen filter (SIO-04) keeps
received events of other names out of the log; they are counted in the
status line. `commands/sioSessions.ts` keeps one session per request.

## Assertions

TEST-08. A socketio request's assertions (its Tests sub-tab) are checked
against the events received when the connection closes: the `closed`
item's `tests`. An event is a message whose body is its arguments as a
JSON array (`$[0].id`) and whose `Event` header is its name; emits sent,
acks and packets are not messages. `SocketIOService.CheckAssertions({opId})`
(Check now, under the Tests sub-tab) checks a running connection or one
of the last that closed. See [assertions.md](assertions.md#streams).

## Local server

PostSonde also runs a Socket.IO server on this computer (SSRV-03,
`internal/core/servers/sioserver`), built on the same two codecs: see
[servers.md](servers.md#socketio-server). Its tests connect the client of
this page to it over every transport.

## Tests

- `sioclient/siotest` is an in-process Socket.IO v4 server built on the
  codecs: long-polling and WebSocket sessions, the upgrade (probe, noops
  while upgrading), the heartbeat, namespaces and auth checks with
  CONNECT_ERROR, echo, acks, server-sent DISCONNECT and close. The client
  tests connect over every transport and cover the fallback, a probe the
  server ignores, namespaces from the option and the URL, a custom path,
  auth accepted and refused, emits with binary arguments and acks, ack
  timeouts, the heartbeat, ping timeouts, the server ending the
  connection, no reconnect, headers, query, cookies and the user-operation
  mark on every dial. The service tests run the same server through a
  guarded dialer and check the items (golden
  `internal/app/services/socketio/testdata/dto/items.golden`).
- Table tests with the examples of both protocol documents (packet
  encoding, handshake, long-polling payloads, namespaces, acks, binary
  packets) and every error code.
- `socketio/testdata/reference-v4.txt` is traffic captured from a
  socket.io 4.8.1 server (engine.io 6.6.4, socket.io-parser 4.2.4) on
  loopback, driven by a raw client (`testdata/capture.js`): long-polling,
  WebSocket and an upgrade session, with every Engine.IO and Socket.IO
  packet type, namespaces, an auth failure, ack IDs, binary events and acks
  in both directions and non-ASCII text. `TestReferenceRoundTrip` decodes
  every frame and checks that it, and every Socket.IO packet, encodes back
  to the captured bytes. To regenerate it, run `capture.js` with socket.io
  and ws in `NODE_PATH` (see the comment at its top).
- `FuzzDecode` in both packages: decoded input must encode, and encode
  back to the same packet.

```
go test ./internal/core/realtime/engineio/ ./internal/core/realtime/socketio/
go test -run '^$' -fuzz FuzzDecode -fuzztime 60s ./internal/core/realtime/socketio/
go test -run '^$' -fuzz FuzzDecode -fuzztime 60s ./internal/core/realtime/engineio/
```

- **Socket.IO v2 and v3 (SIO-02).** `siotest` has a socket.io v2 mode
  (`Config.V2`: the EIO query not checked, as engine.io 3 does not;
  Engine.IO 3 payloads, text or XHR2 binary by `b64`; WebSocket binary
  with the type byte; the client pings; `/` joined at once with a CONNECT
  without payload; CONNECT with the namespace's query; string refusals)
  and a v3 mode (`Config.SocketIOv3`: no `maxPayload` in the handshake),
  both written from the protocol documents. `v2_test.go` connects over
  every transport, emits binary events with acks, reads XHR2 binary
  payloads, joins a namespace with a query and gets refusals, times out
  a server that stops answering pings, and checks both version
  mismatches; `TestV3Server` runs the v3 mode. The service test
  `TestSocketIOv2` runs the version through `SocketIOService`. The real
  socket.io 2.x and 3.x servers are covered by the opt-in interop tests
  ([below](#interop-with-the-official-implementations)).
  `engineio/v3_test.go` has the protocol 3 examples.

## Interop with the official implementations

Opt-in tests run PostSonde against the official Node packages. They are
skipped unless `POSTSONDE_SIO_INTEROP` is set, so
`go test ./...`, `make check-go` and CI never need Node or the network.

```
scripts/sio-interop.sh <install dir> [go test flags...]   # e.g. ~/.cache/postsonde-sio-interop -v
```

The script copies `scripts/sio-interop/v2`, `v3` and `v4` (a
`package.json` with exact versions and its `package-lock.json`) into
`<install dir>/v2`, `v3` and `v4`, outside the repository, runs `npm ci
--ignore-scripts` in each, and then `go test -run Interop` on the two
packages with `POSTSONDE_SIO_INTEROP=<install dir>` (`POSTSONDE_NODE`
picks the node binary). The harness scripts stay in
`scripts/sio-interop/` and load the packages from the install dir
(`createRequire`), so no `node_modules` sits next to them. Everything
listens on 127.0.0.1. `internal/testutil/nodeharness` starts a script,
reads its JSON lines and stops it by closing its stdin.

- **`TestInteropReferenceServers`** (`sioclient/interop_test.go`):
  `scripts/sio-interop/server.js` is a socket.io server (2.x, 3.x,
  4.x, or 4.x with `allowEIO3`; `pingInterval` 500 ms) with `/` and
  `/admin`, the latter behind a middleware that wants the token `s3cret`
  (auth payload, or the query for Engine.IO 3). PostSonde's client
  connects over long-polling only, WebSocket only and the upgrade, to
  each namespace, and checks: the server's `welcome` event with an object
  and a `Buffer`; an emit with text, JSON (non-ASCII) and binary arguments
  and an ack, whose arguments say how the server saw them (the binary
  argument as a `Buffer`) and over which transport; the server's echo of
  it; a 160 KB non-ASCII argument (UTF-16 lengths in Engine.IO 3
  payloads); the heartbeat over 1.3 s; the client's disconnect, which the
  server must see as `client namespace disconnect`. Then the refusals (a
  wrong or missing token, an unknown namespace: `sioclient.connect_error`
  with the server's message) and a server-side disconnect (`io server
  disconnect`).
- **`TestInteropOfficialClient`** (`sioserver/interop_test.go`):
  `scripts/sio-interop/client.js` runs the official socket.io-client
  against PostSonde's server (namespaces `/` and `/admin` joined over one
  connection, both needing the auth `{"token":"s3cret"}`, echo, acks
  echoing, a rule for `hello`; heartbeat 500 ms) with `transports:
  ["polling"]`, `["websocket"]` and the default upgrade. For each
  namespace: the rule's ack (`"hi"` and a `Buffer`) and event (`greeting`
  with a `Buffer`); an emit with a `Buffer` echoed back and acknowledged
  with the same arguments; the server's `ask` event (from `Hub.Emit`, with
  a binary argument and an ack request) answered by the client's callback
  with a `Buffer`, checked in the server's traffic log as a BINARY_ACK
  with two attachments; the client's disconnect (`Socket … left …`). Then
  a wrong token, no token and an unknown namespace, refused with `Not
  authorized` and `Invalid namespace` (`err.message` and `err.data`).

Results (2026-11-08, Node 26.9.0, Linux):

| Peer | Versions | Polling only | WebSocket only | Upgrade |
|---|---|---|---|---|
| socket.io 2.x server, client `v2` | socket.io 2.5.1, engine.io 3.6.2 | pass | pass | pass |
| socket.io 3.x server, client `v4` | socket.io 3.1.2, engine.io 4.1.2 | pass | pass | pass |
| socket.io 4.x server, client `v4` | socket.io 4.8.4, engine.io 6.6.11 | pass | pass | pass |
| socket.io 4.x server with `allowEIO3`, client `v2` | socket.io 4.8.4, engine.io 6.6.11 | pass | pass | pass |
| socket.io-client 4.x against PostSonde's server | socket.io-client 4.8.4, engine.io-client 6.6.7 | pass | pass | pass |

Each cell covers both namespaces (with auth on `/admin`), events and acks
both ways where the peer can (PostSonde's client does not answer a
server's ack requests), binary arguments, the heartbeat, the
disconnects and the refusals. They also pass with `-race`.

Found and fixed by them: over long-polling the client sent its DISCONNECT
and the Engine.IO close in one payload, and every socket.io version
(2.x to 4.x, which handles a DISCONNECT on the next tick) closed the
transport first, so the server's `disconnect` handler got `transport
close` instead of `client namespace disconnect`. `Disconnect` now sends
the close in a request of its own once the DISCONNECT's went through, as
engine.io-client does (`TestDisconnectPollingCloseApart`).

To test other versions, change the `package.json` of a version directory,
run `npm install --ignore-scripts` there (outside the repository, then
copy the lockfile back) and run the script.

Not covered yet: probing a server for its version, the WebTransport
framing of engine.io-parser 5, custom parsers (msgpack), several
namespaces over one connection, answering the server's ack requests,
reconnection, and splitting long-polling payloads at the server's
`maxPayload`.
