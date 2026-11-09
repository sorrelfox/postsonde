# Local servers

PostSonde runs servers on this computer so the user can test clients
against them (SRV-01 start/stop, SRV-03 traffic log, SRV-04 saved server
configs). This is the base for
every later server: the HTTP mock, the webhook receiver, the
WebSocket, TCP, SSE and UDP servers, TLS,
the gRPC mock, the Socket.IO server and the GraphQL mock.

| Piece | Where |
|---|---|
| Manager, server model, traffic events | `internal/core/servers` |
| Shared HTTP serving (connections, stop, request events) | `internal/core/servers/httpbase` |
| Echo HTTP server type | `internal/core/servers/echo` |
| Mock HTTP server type | `internal/core/servers/mockhttp` |
| WebSocket server type | `internal/core/servers/wsserver` |
| TCP server type | `internal/core/servers/tcpserver` |
| SSE server type | `internal/core/servers/sseserver` |
| UDP server type (a packet type) | `internal/core/servers/udpserver` |
| gRPC mock server type | `internal/core/servers/grpcmock` ([gRPC mock server](#grpc-mock-server)) |
| Webhook receiver type | `internal/core/servers/webhook` |
| Socket.IO server type | `internal/core/servers/sioserver` ([Socket.IO server](#socketio-server)) |
| GraphQL mock server type | `internal/core/servers/graphqlmock` ([GraphQL mock server](#graphql-mock-server)) |
| Mock route matcher (for the HTTP mock) | `internal/core/servers/mockroute` ([mock-routes.md](mock-routes.md)) |
| TLS certificates: the local CA, the user's files ([TLS](#tls)) | `internal/core/servers/servertls`, `internal/core/tlsconf/servercert.go`, `internal/core/servers/tls.go` |
| Saved server config ⇄ `Spec` | `internal/core/servers/serverdef` ([Saved server configs](#saved-server-configs)) |
| Saved server configs in the project | `internal/app/services/servers/project.go` (`SyncProject`, `CreateFolderServer`, `UpdateServer`, `Project`, `CollectionProject`), `internal/app/services/collection/servers.go` |
| Bound service and events | `internal/app/services/servers` (`ServersService`, `servers:changed`, `servers:traffic`, `servers:clients`, `servers:connections`, `servers:captures`, `servers:subscribers`, `servers:peers`, `servers:sockets`); replays go through `httpservice.SendPrepared` (`internal/app/services/http/replay.go`) |
| Binding wrapper | `frontend/src/lib/servers.ts` |
| Traffic as message logs | `frontend/src/lib/serverTraffic.ts` (`trafficToMessage`, `TrafficSources`, `toHexMessage`) |
| Store | `frontend/src/stores/servers.ts` |
| Commands | `frontend/src/commands/servers.ts` (`followServers`, `createAndStartServer`, `startServer`, `stopServer`, `removeServer`, `clearServerTraffic`, `syncProjectServers`, `createFolderServer`, `updateServerSettings`), `frontend/src/commands/mockRoutes.ts` (`editMockConfig`, `flushMockConfig`), `frontend/src/commands/wsServer.ts` (`sendToWsClient`, `broadcastToWsClients`, `disconnectWsClient`, `updateWebSocketConfig`), `frontend/src/commands/tcpServer.ts` (`sendToTcpConnection`, `closeTcpConnection`), `frontend/src/commands/webhookServer.ts` (`updateWebhookConfig`, `loadWebhookCapture`, `startWebhookReplay`), `frontend/src/commands/sseServer.ts` (`sendSseEvent`, `disconnectSseSubscriber`, `updateSseConfig`), `frontend/src/commands/udpServer.ts` (`sendUdpDatagram`, `updateUdpConfig`), `frontend/src/commands/grpcMock.ts` (`updateGrpcMockConfig`, `grpcurlListCommand`), `frontend/src/commands/sioServer.ts` (`emitToSioSockets`, `disconnectSioSocket`, `setSioSocketRooms`, `updateSioConfig`, `sioServerUrl`, `ioSnippet`), `frontend/src/commands/graphqlMock.ts` (`updateGraphQLMockConfig`, `graphQLMockUrl`, `newGraphQLMockOverride`) |
| UI | `frontend/src/components/servers/` (`TlsSettings` (`TlsFields`, `TlsSection`, `LocalCAPanel`), `TlsBadge`, `ServersPanel`, `ServerDetails`, `ServerTraffic`, `MockRoutesEditor`, `WsServerPane`, `TcpServerPane`, `WebhookServerPane`, `SseServerPane`, `UdpServerPane`, `GrpcMockPane`, `SioServerPane`, `GraphQLMockPane`, `FolderServerTab`, `useServersSync`) |

## Core

### Manager

`servers.NewManager(Options)` holds the servers of this app run, keyed by
ID (`srv_` and 16 hex digits). The Manager keeps them in memory only; the
service writes the definitions of saved servers to their project folder
([Saved server configs](#saved-server-configs)).

| Method | Does |
|---|---|
| `Add(Spec)` | defines a server, stopped. Checks the type (`servers.unknown_type`), port 0–65535 (`listen.invalid_port`), host (`listen.invalid_host`) and the type's configuration; an empty name gets the type's label |
| `Check(Spec)` | the Spec as `Add` would keep it (checked, name defaulted, configuration normalized), without defining a server: how a definition is checked before it is saved |
| `Redefine(id, Spec)` | replaces a server's definition, keeping its ID, state and traffic, and calls `OnChange`. The type never changes (`servers.type_changed`); host, port, LAN access and TLS only while it holds no socket (`servers.active`); name, owner and configuration at any time (a running server takes the configuration at once, as with `UpdateConfig`) |
| `UpdateConfig(id, config)` | replaces the configuration; a running server applies it without restarting |
| `Start(ctx, id)` | opens the socket and starts the type; returns the server running, or failed together with the error |
| `Stop(ctx, id)` | graceful stop, then force close; also when `ctx` is cancelled |
| `Remove(ctx, id)` | stops if needed and forgets the server and its traffic |
| `StopAll(ctx)` | stops every server in parallel and refuses `Add` and `Start` from then on (`servers.shutting_down`) |
| `List`, `Get`, `Types` | snapshots |
| `Traffic(id)`, `ClearTraffic(id)` | the recent events kept in Go; `ClearTraffic` also forgets the captures |
| `Captures(id)`, `Capture(id, seq)` | the requests kept whole for replay ([Captures](#captures)); `servers.capture_not_found` when that one is not kept |
| `Running(id)` | the running `Instance`, for a type with operations of its own (the WebSocket server's clients); `servers.not_running` otherwise |

`Spec` is `Type`, `Name`, `Host`, `Port`, `AllowLAN`, `Config`, `TLS`
([TLS](#tls); nil serves plain text) and `Owner`
(`{Workspace, Folder, Label}`: the project folder of a saved server, zero
for one made in the Servers view; the Manager only keeps it). `Port` 0
lets the OS pick a free port at every start, so a restarted server may get
another port.

### Configuration

`Spec.Config` is the type's own configuration (the mock's routes), an
opaque value to the Manager. A type that has one implements
`ConfigurableType.NormalizeConfig(config) (any, error)`: `Add` and
`UpdateConfig` call it, keep what it returns (nil becomes the type's
default) and reject the change on an error. The config of a type without
one is dropped. The value is treated as immutable.

`UpdateConfig` waits for a start or stop of that server in progress
(it takes the same per-server lock), stores the config for the next start
and, when the running instance implements
`Reconfigurable.Reconfigure(config)`, hands it over at once. It does not
call `OnChange`: the configuration is not a state, and the UI that made
the change has the result.

### States

```
stopped ──Start──▶ starting ──▶ running ──Stop──▶ stopping ──▶ stopped
                      │            │
                      └────────────┴──(listen or serve error)──▶ failed
```

- A failed server keeps its error (`Server.Err`) and can be started again.
- Start on a starting, running or stopping server and Stop on a server that
  is not running do nothing. Start, Stop and Remove of one server are
  serialized.
- `StartedAt` is set when the server starts running (the UI counts uptime
  from it), `StoppedAt` when it stops or fails. `Addrs`, `Scope` and
  `Warning` are set only while bound.
- `Options.OnChange(server, removed)` is called after every change, in the
  order of the changes.
- A server whose listener breaks without Stop (`Instance.Done` closes on
  its own) is cleaned up and marked failed with `servers.serve_failed`.

### Listening

Every server listens through `listen.Listen`
([network.md](network.md#listening)) with `DualStack: true`: `""` and
`localhost` bind `127.0.0.1` and `::1` on the same port. A packet type
(UDP, `PacketType`) gets its sockets from `listen.ListenPackets` with the
same rules: for localhost one `net.PacketConn` per loopback family on the
same port (one conn has one address), so a client that resolves localhost
to `::1` is answered too. Anything else
needs `AllowLAN`; without it the start fails with
`listen.lan_not_allowed` (the server is failed). With it, the server's
`Scope` and `Warning` (`listen.lan_exposed`,
`listen.all_interfaces_exposed`) are shown next to it. A port in use fails
with `listen.address_in_use`. Types never listen themselves; they serve the
listener they are given (`TestNoDirectListens`).

### TLS

SRV-05: every kind of server but UDP can serve TLS, so clients that
require it can be tested: the HTTP types (and the SSE server) become
HTTPS, the WebSocket server `wss`, the TCP server TLS over TCP, the gRPC
mock gRPC over h2. A UDP
server with `Spec.TLS` is refused by `Check`
(`servers.tls_not_supported`, detail `field` `tls`): DTLS is not
implemented. `Spec.TLS` is

| Field | Meaning |
|---|---|
| `Certificate` | `generated` (`servers.CertGenerated`) or `files` (`servers.CertFiles`); anything else is `servers.tls_invalid` in `Check` |
| `CertFile`, `KeyFile` | `files`: a PEM certificate (the leaf first, intermediates after it) and its PEM key; `KeyFile` empty when the certificate file holds the key |
| `PFXFile` | `files`: a PKCS#12 file (`.p12`, `.pfx`) instead |

`Check` trims the paths, drops those of `generated`, and refuses neither
or both kinds of file and relative paths (`tlsconf.server_cert_source`,
detail `field`); it does not read the files. TLS is part of the address:
`Redefine` changes it only while the server holds no socket
(`servers.active`).

**Serving (decision).** The Manager, not the type, adds TLS: at `Start`
it asks `Options.Certificates` (a `CertificateSource`) for the
certificate *before* opening the socket (a missing file never listens),
then wraps the listener from `listen.Listen` in `tls.NewListener`
(TLS 1.2 and 1.3). So one path serves every type, types stay unaware of
TLS, and the listening rule holds (`TestNoDirectListens`). A type may
name its ALPN protocols (`ALPNType`): the HTTP types offer `http/1.1`
only, so a client offering `h2` and `http/1.1` (browsers, PostSonde's own
client) gets HTTP/1.1, which the WebSocket upgrade needs too; the TCP
server offers none; the gRPC mock offers `h2` and `http/1.1` (gRPC needs
HTTP/2, gRPC-Web clients may speak HTTP/1.1). The `started` event says `over TLS (generated
certificate "localhost", valid until 2027-09-19)` with any warning, and
`Server.Cert` (DTO `certificate`) is the leaf while it runs.

**Certificates** come from `servertls.Provider` (the app wires it with
the data directory and `appsecrets`):

- **Generated.** A certificate made at every start, in memory, by the
  **local CA** of the data directory: ECDSA P-256, valid 365 days, for
  `localhost`, `127.0.0.1` and `::1`, plus the bind address when it is a
  specific IP, or every interface address for `0.0.0.0`/`::`. The CA
  (`servertls.Authority`) is created once per data directory, the first
  time it is needed (a generated certificate, or `LocalCA` in the UI),
  and kept in `<data>/servers/tls/`: `ca.pem` (`0644`, to hand to
  clients) and `ca-key.pem` (`0600`, directory `0700`). It is valid ten
  years and replaced when it expires within 30 days (clients must trust
  the new one). Its **name constraints** (critical) permit only
  `localhost` and loopback, private (`10/8`, `172.16/12`, `192.168/16`,
  `fc00::/7`), link-local and shared (`100.64/10`) addresses: trusting it
  can never make a certificate for a public site valid, even if its key
  leaks. A public bind address is left out of the certificate, with a
  warning (`servertls.addresses_left_out`) unless the server binds every
  interface. A broken `ca.pem` or `ca-key.pem` is `servertls.ca_invalid`,
  with the hint to delete the folder (nothing overwrites it).
- **The user's files** must be in the open project's folder or an allowed
  directory of the settings (PRV-10, `servertls.Provider.CheckFiles`):
  saving a server and starting one refuse a file outside with
  `fileaccess.not_allowed` (detail `field`), as a server definition is
  project data ([privacy.md](privacy.md#local-file-access-prv-10)). They
  are read at every start by
  `tlsconf.LoadServerCert`, the loader of the client certificates
  ([network.md](network.md#client-certificates)) with server wording: PEM
  (an encrypted PKCS#8 key with its passphrase) or PKCS#12, the key must
  match (`tlsconf.client_key_mismatch`), and the errors keep their
  `tlsconf.*` and `pkcs12.*` codes and the details `field` and `path`.
  An expired or not-yet-valid certificate (`tlsconf.server_cert_expired`,
  `tlsconf.server_cert_not_yet_valid`) or one not for server
  authentication (`tlsconf.server_cert_not_for_server_auth`) is served
  anyway, with a warning in the `started` event: testing how a client
  handles a bad certificate is a reason to serve one. The passphrase comes
  from `<data>/secrets/app.json` under
  `appsecrets.ServerCertPassphraseKey(file)` (a hash of the path of the
  PKCS#12 file, else of the key file, else of the certificate file:
  `servertls.SecretFile`).

A failed handshake is an `error` event of its connection: `TLS
handshake failed: remote error: tls: bad certificate (the client does not
trust the server's certificate)` (`servers.HandshakeSummary` explains
unknown CAs, no common TLS version, and a client that wants `h2` only).
The HTTP types take it from the `http.Server`'s error log; the TCP server
handshakes before reading (30 s limit). A plain-HTTP request to an HTTPS
server is answered `400` by net/http without a log line; its connection
shows as opened and closed.

**Trusting the local CA** is the user's step; PostSonde never installs it
anywhere:

- **PostSonde itself:** "Trust in PostSonde" in the server editors adds
  `ca.pem` to the custom CA bundles (Settings, Network;
  [network.md](network.md#custom-ca-certificates)), so its HTTP, GraphQL,
  SSE, WebSocket and gRPC clients accept the servers.
- **curl:** `curl --cacert <data>/servers/tls/ca.pem https://localhost:8443/`.
- **Node.js:** `NODE_EXTRA_CA_CERTS=<data>/servers/tls/ca.pem`.
- **Python requests:** `REQUESTS_CA_BUNDLE`, or `verify=` the file.
- **macOS:** `security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db ca.pem` (or Keychain Access, then "Always Trust").
- **Windows:** `certutil -user -addstore Root ca.pem`.
- **Linux:** copy it to `/usr/local/share/ca-certificates/postsonde.crt` and run `sudo update-ca-certificates` (Debian, Ubuntu), or `trust anchor ca.pem` (Fedora, Arch).
- **Firefox** keeps its own store: Settings, Privacy & Security, Certificates, View Certificates, Authorities, Import.

To start over, delete `<data>/servers/tls/`; a new CA is made at the next
use, and clients must trust it again.

Tests: `internal/core/servers/servertls` (`TestHTTPSWithGeneratedCertificate`:
echo, mock and webhook over HTTPS through net/http and the app's HTTP
transport trusting `ca.pem` through `tlsconf.Trust`, ALPN `http/1.1`;
`TestWSSWithGeneratedCertificate`: the app's WebSocket client over `wss`,
and the handshake error of a client without the CA; `TestTCPOverTLS`;
`TestUserCertificates`: PEM, PKCS#12 with its stored passphrase, a wrong
passphrase and a missing file fail the start without a socket; the CA
kept per data directory, its permissions, name constraints and renewal),
`internal/core/tlsconf` (`TestLoadServerCert`), `serverdef.TestTLS`,
`internal/app/services/servers/tls_test.go` (the service end to end, the
passphrase in `app.json` and never in `_folder.json`, a restart), and in
the frontend `TlsSettings.test.tsx`.

### Stopping

`Stop` gives the instance `Options.StopTimeout` (default 3 s) to finish
work in progress (for HTTP: `http.Server.Shutdown`), then closes every
connection (`Close`). It returns once nothing of the run is left, and no
event of that run is recorded afterwards: the Manager's per-run sink drops
late emits, so a late callback never logs into the next run.

The app stops every server when it quits: `ServersService.ServiceShutdown`
calls `StopAll` with a 5 s bound, then flushes the last traffic.

Servers belong to the app run, not to a project: switching to another
project, or closing it (STO-06, [workspace.md](workspace.md)), leaves the
running ones running with their traffic, so a client being tested in one
project can keep talking to a server set up while another was open. A
saved server that is stopped is dropped with its project and defined
again when the project opens ([Saved server configs](#saved-server-configs)).

### Server types

```go
type Type interface {
    Name() string  // "echo_http", the Spec.Type
    Label() string // "Echo HTTP"
    Start(spec Spec, ln net.Listener, sink Sink) (Instance, error)
}
type Instance interface {
    Done() <-chan struct{} // closed when serving ended on its own, or after Stop
    Err() error
    Stop(ctx context.Context) error
}
```

A type owns the listener from `Start` on, emits everything it sees to
`sink`, and must emit nothing after `Stop` returns. Register types in
`Options.Types` (the service does it in `servers.New`).

A type that serves datagrams implements `PacketType` as well:
`StartPacket(spec, conns []net.PacketConn, sink)`. The Manager opens its
sockets with `listen.ListenPackets` instead of `listen.Listen`, logs
"Listening for UDP datagrams on …" and calls `StartPacket`; its `Start`
is never called (the UDP type's refuses a listener).

**HTTP types** share `httpbase`: an `http.Server` (HTTP/1.1) on the
listener, connection open and close logged through `ConnState`
(connection IDs 1, 2, … per run; connections still open at stop are logged
as "Connection closed (server stopped)"), graceful `Shutdown` then `Close`,
and the request event (method, target, protocol, headers with `Host`
first, body up to the content limit; the rest is read and counted). A
connection a handler hijacks (a WebSocket upgrade) leaves httpbase's
bookkeeping without a close event: the handler owns it from then on and
logs how it ends, so it must take `ConnID(r)` before hijacking.

**Echo HTTP** (`echo`, `echo_http`) is the basic type. Every request gets
`200` with a JSON description: method, target, path, query, protocol,
host, remote address, headers, body (or `bodyBase64` when it is not UTF-8),
body size and whether it was cut.

### Mock HTTP

**Mock HTTP** (`mockhttp`, `mock_http`; MOCK-01, MOCK-02, MOCK-04,
MOCK-05, MOCK-06, MOCK-07) answers with hand-made routes. `mockhttp.Config`:

| Field | Meaning |
|---|---|
| `Routes[].ID` | identifies the route in diagnostics and traffic; an empty one gets `rt_` and 12 hex digits |
| `Routes[].Enabled` | a disabled route is not served (its pattern is still checked) |
| `Routes[].Method` | an HTTP method (case-sensitive) or `ANY` |
| `Routes[].Path` | a `mockroute` pattern: `/users/:id`, `/users/{id:[0-9]+}`, `/files/*`, `/files/**`, `/files/*path` ([mock-routes.md](mock-routes.md)) |
| `Routes[].Status` | 200–599 |
| `Routes[].Headers` | rows `{Enabled, Name, Value}`; disabled rows are not sent |
| `Routes[].Body` | `{Mode: "none" \| "raw", Type, Text}`: the none and raw modes of a request body; `Type` is `json`, `xml`, `html`, `text` or `yaml`, or `base64` for a binary body written in Base64 and sent decoded ([mock-routes.md](mock-routes.md#responses)) |
| `Routes[].Delay` | `{Mode: "none" \| "fixed" \| "random", Ms, MinMs, MaxMs}`; holds back every response of the route |
| `Routes[].Responses` | the route's other responses, `{Name, Status, Headers, Body, Rules, Templated}`, tried in order before `Status`, `Headers` and `Body` (the default response); picked by rules on the request or by the `X-PostSonde-Response` header ([mock-routes.md](mock-routes.md#responses)) |
| `Routes[].Templated` | the default response's body and header values are templates that read the request (`{{request.params.id}}`) and dynamic values (MOCK-06, [mock-routes.md](mock-routes.md#templates)); each response has its own flag |
| `StrictTrailingSlash`, `CaseInsensitive` | the matcher options of every route |

The default configuration (a new server without one) has one example
route, `GET /hello` → 200 with a JSON message.

**Limits.** At most 1,000 routes (`mockhttp.too_many_routes`), a 10 MiB
body per response (`mockhttp.body_too_large`), 100 other responses per
route and 50 rules per response (`mockhttp.too_many_responses`); each
rejects the whole configuration (`invalid_argument`).

**Diagnostics.** `mockhttp.Check(config)` lists the problems of every
route, in route order, as `Diagnostic{Code, RouteIndex, RouteID, Field,
Start, End, ResponseIndex, HeaderIndex, RuleIndex, Message}`.
`ResponseIndex` is the index in `Responses` when the problem is in one of
them (its `status`, `header`, `body`, `name` or `rule`), else -1. A route
with any diagnostic, in any of its responses too, is not served; the other
routes are.

| Code | Field | When |
|---|---|---|
| `mockroute.*` | `pattern`, `method`, `id` | the matcher's diagnostics ([mock-routes.md](mock-routes.md#validation)); `Start`–`End` is the byte range in the path or method. Duplicates count only among enabled routes (a disabled route is checked alone), and "route N" in the message is the route's number in the whole list |
| `mockhttp.invalid_status` | `status` | not 200–599. 1xx is left out: `net/http` sends it as an interim response followed by an implicit 200 |
| `mockhttp.invalid_header_name` | `header` | an enabled row whose name is not a token (`HeaderIndex` is the row); a row with empty name and value is ignored |
| `mockhttp.invalid_header_value` | `header` | a value with a line break or another control character but tab |
| `mockhttp.invalid_body` | `body` | a mode other than none and raw, an unknown raw type, a `base64` body that does not decode or is templated |
| `mockhttp.invalid_delay` | `delay` | an unknown mode, a delay outside 0–300,000 ms (5 minutes), or a random minimum above the maximum |
| `mockhttp.invalid_response_name` | `name` | a response called `default` (the route's own), or a name with a control character |
| `mockhttp.duplicate_response_name` | `name` | a name an earlier response of the route has (letter case ignored) |
| `mockhttp.invalid_rule` | `rule` | an unknown source or operator, a missing parameter or header name or JSONPath, an invalid header name, JSONPath or regular expression (`RuleIndex` is the rule) |

**Serving.** For each request, the routes are matched with
`Matcher.Match(r.Method, r.URL.EscapedPath())`:

- **Matched:** the response is picked from the route's responses
  ([mock-routes.md](mock-routes.md#responses)): the one
  `X-PostSonde-Response` names, else the first whose rules match, else the
  default. It is sent with its status, its enabled header rows and its
  body, rendered first when it is templated (a render error is a 500,
  [mock-routes.md](mock-routes.md#templates)). A raw body sets `Content-Type` from its type, the request
  body's rule (`request.RawMediaType`: `application/json`,
  `application/xml`, `text/html`, `text/plain`, `application/yaml`) with
  `; charset=utf-8` added to `text/*` types. An enabled `Content-Type` row
  (any letter case) replaces it; several such rows are all sent.
  `Content-Length` and `Transfer-Encoding` rows are ignored: the server
  sets `Content-Length` from the body. 204 and 304 get no body and no
  implicit `Content-Type`. `HEAD` gets the headers of the `GET` response
  (with its `Content-Length`) and no body.
- **405** when routes match the path but not the method: `Allow` lists
  their methods, and the JSON body is `{"error", "method", "path",
  "allowed"}`.
- **404** otherwise, with the JSON body `{"error", "method", "path"}`. Neither
  body lists the routes.
- **Delay:** a fixed delay waits `Ms`; a random one a whole number of
  milliseconds from `MinMs` to `MaxMs`, both included. The wait ends early
  when the client disconnects or the server stops: then no response is
  sent, the connection is closed (`http.ErrAbortHandler`) and an `error`
  event says why ("No response sent: the server is stopping during the 5m0s
  delay."). `Stop` cancels every delay before the graceful shutdown, so a
  long delay never holds a stop. Tests inject `Options.Sleep` and
  `Options.RandN`.

**Live changes.** `Reconfigure` compiles the new routes and swaps them in
with an atomic pointer. A request loads the routes once when it arrives, so
a request in progress (a delayed one, for example) finishes with the
routes it matched.

**Traffic.** The request event carries `HTTPInfo.Route` (`RouteMatch{ID,
Method, Pattern, Params, Wildcard}`) when a route matched; the response
event carries the route, the status, the headers as sent, the body (none
for `HEAD`) and `HTTPInfo.Delay`. The 404 and 405 responses are logged as
responses whose summary says why: "404 Not Found (no route matches)",
"405 Method Not Allowed (allowed: GET, HEAD)".

### WebSocket server

**WebSocket** (`wsserver`, `websocket`; SSRV-01) accepts WebSocket clients
on a path, lists them, sends to one or all and disconnects them. It is an
HTTP type on `httpbase`; framing is `wsframe` in the server role, the code
the client uses ([websocket.md](websocket.md)). `wsserver.Config`:

| Field | Meaning |
|---|---|
| `Path` | where clients connect, matched exactly against the request path (the query is ignored); `*` accepts any path. Default `/`; must start with `/`, no spaces, `?` or `#`, at most 2,048 bytes |
| `Subprotocols` | accepted subprotocols (tokens, no duplicates, at most 32). The **first one the client offers** that is listed is selected; none when the list is empty or nothing matches (the upgrade still succeeds, as RFC 6455 allows) |
| `Echo` | send a message back to the client that sent it when no auto-reply rule answers it (every binary message, and a text message no enabled rule matches) |
| `Rules` | the auto-reply rules, in order ([Auto-reply rules](#auto-reply-rules)) |
| `MaxMessageSize` | the largest received message and frame, in bytes; 0 means 16 MiB, at most 256 MiB. A larger one closes the connection with 1009 |

An invalid configuration is `wsserver.invalid_config` (detail `field`).
`Reconfigure` swaps it in atomically: path, subprotocols and size apply to
new connections, echo and the rules to connected clients too.

**Upgrade (RFC 6455 §4.2.1).** A request is checked in this order; each
rejection is logged as a request and a response whose summary says why
("426 Upgrade Required (not a WebSocket upgrade)"), with a short text body:

| Check | Answer |
|---|---|
| path does not match | 404 |
| no `Upgrade: websocket` token (a browser opening the address) | 426 with `Upgrade: websocket`, `Connection: Upgrade`, `Sec-WebSocket-Version: 13` |
| method other than GET | 405 with `Allow: GET` |
| HTTP/1.0 | 400 |
| `Connection` without the `upgrade` token (any case, in a list) | 400 |
| not exactly one `Sec-WebSocket-Version: 13` | 426 with `Sec-WebSocket-Version: 13` |
| not exactly one `Sec-WebSocket-Key` that is base64 of 16 bytes | 400 |
| the server is stopping | 503 |

A missing `Host` is refused by `net/http` before the handler (400, not
logged as a request). Then the connection is hijacked
(`http.ResponseController`), deadlines `net/http` left are cleared, and
`101 Switching Protocols` is written by hand with `Sec-WebSocket-Accept`
and the selected `Sec-WebSocket-Protocol`. **No extension is accepted**:
`Sec-WebSocket-Extensions` (browsers offer `permessage-deflate`) is ignored
and never answered, so frames are uncompressed. Bytes the client sent
behind the upgrade request stay in the `bufio.Reader` given to wsframe.

**Decisions.**
- *Origin is not checked.* This is a local testing tool; any page (or
  program) that can reach the port can connect, like the other local
  servers. The Origin and User-Agent are shown with the client.
- *A plain GET gets 426, not an info page:* it tells a browser and curl
  exactly what is expected, and stays in the traffic like any request.
- *Pings* are answered automatically (wsframe) and not logged; the server
  sends no pings of its own.

**Clients.** A client's ID is its connection ID as a string ("3"), the
one the traffic shows; IDs restart with the server. `Instance` implements
`wsserver.Hub`:

| Method | Does |
|---|---|
| `Clients()` | snapshots, oldest first: ID, connection ID, peer, target (path and query), subprotocol, connected at, Origin, User-Agent, messages and bytes received and sent |
| `Send(clientID, binary, data)` | one text (UTF-8, else `wsserver.invalid_message`) or binary message. The message event is logged **before** the write, so it precedes the client's answer; writes to one client are serialized and bounded by `WriteTimeout` (10 s). `wsserver.client_not_found` when the client left, `wsserver.client_closing` after a close frame was sent, `wsserver.send_failed` (with an error event) when writing fails |
| `Broadcast(binary, data)` | sends to every client in parallel; returns how many were reached and how many failed |
| `Disconnect(ctx, clientID, code, reason)` | sends a close frame (codes as wsframe accepts: 1000–1003, 1007–1014, 3000–4999, or 1005 without reason for an empty frame; reason ≤ 123 bytes of UTF-8; otherwise `wsframe.invalid_argument`), waits for the client's close frame up to `CloseTimeout` (2 s) or until ctx ends, then closes the TCP connection (the server closes first, §7.1.1). Returns once the client is gone and logged |

**Closing.** Whatever ends a connection, the client is removed from the
list first and then a `connection_close` event is logged with
`WSInfo{CloseCode, CloseReason, InitiatedBy}`:

| Case | Summary | `InitiatedBy`, code |
|---|---|---|
| the client's close frame came first (wsframe echoes the code) | "Closed by the client: 1000 (normal closure) “done”" | `client`, the client's code |
| `Disconnect` | "Disconnected by the server: 4000 “bye”", plus "; the client did not answer" when no close frame came back | `server`, the code sent |
| `Stop` | "Closed: the server is stopping (1001 (going away) “server stopping”)" | `server`, 1001 |
| a protocol error by the client (unmasked frame, bad UTF-8, too big): wsframe sends 1002/1007/1009 | an `error` event with the reason, then "Closed by the server after a protocol error: 1002 (protocol error)" | `server`, that code |
| the connection dropped | "Connection lost without a close frame (1006)" | `""`, 1006 |

**Stop.** New upgrades get 503; every client is disconnected with 1001 in
parallel (close timeout, or the Manager's stop context), the client
goroutines are awaited, then `httpbase` shuts the HTTP server down. So all
close events are logged before the `stopped` event.

#### Auto-reply rules

SSRV-02 (`wsserver/rules.go`). A `Rule` is `Enabled`, `Match`, `Pattern`,
`Reply` and `DelayMs`:

| `Match` | A text message matches when |
|---|---|
| `exact` (the default for `""`) | it equals `Pattern`, byte for byte (case and spaces count) |
| `contains` | it contains `Pattern` |
| `regex` | the Go (RE2) regular expression `Pattern` finds a match anywhere in it; anchor with `^` and `$` for the whole message, `(?i)` ignores case, `(?s)` lets `.` match newlines |

**Decisions.**
- *Rules first, echo as the fallback.* Every **text** message is checked
  against the enabled rules in order; the **first** that matches sends its
  reply to the client that sent the message, and nothing else happens. When
  no rule matches, echo (if on) sends the message back. Rules never look at
  **binary** messages (a binary pattern has no good text form); those are
  echoed when echo is on. Without echo, an unmatched message gets no answer.
- *Capture groups use Go's `regexp.Expand` syntax*, the syntax of the
  regular expressions themselves: in the reply of a `regex` rule, `$1` or
  `${1}` is a numbered group, `$name` or `${name}` a named one
  (`(?P<name>…)` or `(?<name>…)`), `$0` the whole match and `$$` a `$`. A
  group that did not take part, or does not exist, is empty. Go reads the
  longest name it can after `$` (`$1x` is the group `1x`, so empty); write
  `${1}x`. The groups are those of the first match. The replies of `exact`
  and `contains` rules are sent **as written** (a `$` stays a `$`).
- *Replies are text messages.* The reply of a rule is always a text frame.
- *Delay* (`DelayMs`, 0–60,000): the reply is sent that long after the
  message, from a timer, so the client's next messages are read and
  answered meanwhile (a later rule without delay can answer first). A
  client that left before the timer fires gets nothing and nothing is
  logged.
- *Checked when saved.* `Normalize` refuses (`wsserver.invalid_config`,
  `field` `rules`, plus `ruleIndex` (0-based) and `ruleField`: `match`,
  `pattern`, `reply` or `delayMs`) an unknown match mode, a regular
  expression that does not compile ("Rule 2: the regular expression is not
  valid: missing closing ): `(a`."), a pattern over 64 KiB, a reply over
  1 MiB, text that is not UTF-8, a delay out of range, or more than 100
  rules. **Disabled rules are checked too**, since they are saved. The UI
  shows the message under the rule `ruleIndex` names.
- The rules are compiled once per configuration (`CompileRules` →
  `RuleSet`, swapped with the config); `RuleSet.Match(msg)` returns the
  rule's index, the expanded reply and the delay.

A reply is sent like `Send` (logged before it is written, serialized with
other writes to the client, counted in the client's messages out), with
the summary "Text message to client 3 (auto-reply rule 2)" (rules are
numbered from 1 as in the UI); an echo is "… echoed to client 3".

Tests: `wsserver/rules_test.go` (matching, order, disabled rules, capture
expansion with numbered, named, missing and `$$` groups, validation and
its details), `wsserver/autoreply_test.go` (end to end with the app's
WebSocket client: exact, regex and contains replies, echo as the fallback
for text and binary, a delayed reply that does not hold up the next one,
the traffic events, rules changed while connected, a delayed reply to a
client that left).

**Traffic** of a WebSocket server: `connection_open` (TCP, from httpbase),
the upgrade `request` with its headers and the `101` `response` with the
headers sent (both with `WSInfo.ClientID`; the response also
`Subprotocol`), a `message` per data message in (`in`) and out (`out`,
"Text message to client 3", "… echoed to client 3", "… to client 3
(auto-reply rule 2)") with
`WSInfo.Opcode` `text` or `binary` (a binary message is always `Binary`,
even when its bytes are UTF-8), `error`s and the `connection_close` above.

**Command-line clients** (checked by hand, not used by tests): with a
server on `/ws` accepting `chat`, Node 22+ (`new WebSocket(url, ["x",
"chat"])`, the browser API, which offers permessage-deflate) and Python's
`websockets` 15 (`connect(url, subprotocols=["chat"])`, which offers it
too) both connected, got subprotocol `chat` and no extension, exchanged
messages, received a broadcast and saw a server-side `Disconnect` as a
clean close 4000 “bye”. `websocat ws://127.0.0.1:PORT/ws` works the same
way where it is installed.

### TCP server

**TCP** (`tcpserver`, `tcp`; SSRV-05) accepts raw TCP connections, lists
them, logs what each one sends, sends bytes to one and closes one. It has
no configuration and, like every server, listens on loopback unless LAN
access is allowed (`netx/listen`). There is no framing: like the TCP
client ([tcp.md](tcp.md)), a received message is whatever one read
returned, so one line may arrive as several messages or several as one.

- **Connections.** Connection IDs are 1, 2, … per run (`Connection.ID` is
  the string, the traffic's `ConnID`). `Instance` implements
  `tcpserver.Hub`:

| Method | Does |
|---|---|
| `Connections()` | snapshots, oldest first: ID, connection ID, peer, local address, connected at, bytes and messages (reads, sends) in and out |
| `Send(connectionID, data)` | writes the bytes as they are. The `out` message event is logged **before** the write, so it precedes the answer; writes to one connection are serialized and bounded by `WriteTimeout` (10 s). `tcpserver.empty_message` for no bytes, `tcpserver.connection_not_found` when it closed, `tcpserver.send_failed` (with an error event, and the connection is closed) when writing fails |
| `Close(connectionID)` | closes the socket and returns once the close is logged |

- **Reading.** Each connection has a reader goroutine with a 32 KiB
  buffer, lowered to the Manager's content limit so a message is never
  cut; each read is one `in` message ("5 bytes from connection 1").
  Bytes that are not UTF-8 make it `Binary`; a read that ends inside a
  character does too (the UI shows it as hex).
- **Closing.** When the client closes its side (EOF) the server closes the
  connection too: half-open connections are not kept. The
  `connection_close` event carries `TCPInfo{InitiatedBy, BytesIn,
  BytesOut}` and the totals in its summary:

| Case | Summary | `InitiatedBy` |
|---|---|---|
| the client closed (EOF) | "Closed by the client (5 bytes received, 4 bytes sent)" | `client` |
| the client reset it | "Reset by the client (…)" | `client` |
| `Close` | "Closed by the server (…)" | `server` |
| `Stop` | "Closed: the server is stopping (…)" | `server` |
| another read error | an `error` event, then "Connection lost (…)" | `""` |

- **Stop** closes the listener, waits for the accept loop, closes every
  connection and waits for their readers, so every close is logged before
  the `stopped` event. Raw TCP has no work in progress, so there is no
  graceful wait. A listener that fails on its own ends the run (`Err`).
- **Traffic:** `connection_open` ("Connection 1 opened"), `message` in and
  out ("4 bytes to connection 1"), `error`s and `connection_close`, all
  with a non-nil `TCP` (that is how the service knows the connection list
  changed).
- **Checked by hand:** `nc 127.0.0.1 PORT` connected, lines typed in nc
  showed up in the traffic, text with CRLF and hex bytes sent from the
  service reached nc, and Close ended nc; the app's own TCP client does the
  same in `internal/app/services/servers/tcp_test.go`.

### Webhook receiver

**Webhook receiver** (`webhook`, `webhook`; MOCK-08) is a catch-all HTTP
endpoint: any method on any path gets the configured response, and every
request is logged and kept whole so it can be inspected and replayed. It
is httpbase, like the echo server and the mock.

- **Configuration** (`webhook.Config`): `Status` (200–599), `Headers`
  (rows `{Enabled, Name, Value}`, sent in order; at most 100; names must
  be tokens, values without control characters; `Content-Length`,
  `Transfer-Encoding` and `Connection` are refused because net/http sets
  them) and `Body` (at most 1 MiB, sent as is; `Content-Length` from it,
  none for `HEAD`). The default answers 200 with an empty body. A bad
  config is rejected whole with `webhook.invalid_config` and
  `details.field` (`status`, `headers`, `body`). A running receiver takes
  a new config at once (`Reconfigure`).
- **Requests.** The request event is the usual one (method, target,
  protocol, headers with `Host` first, the first 64 KiB of the body) plus
  a `Capture`: method, the request target as sent, protocol, `Host`, the
  other headers (sorted by name, repeated names in order) and the body as
  received, up to `Options.MaxBody` (16 MiB). A longer body is read to
  the end and answered, but its capture keeps the first 16 MiB and is
  `Truncated`, and cannot be replayed. The log's copy of the body is a
  separate slice, so the traffic buffer never holds the whole body.
- **Tests:** `internal/core/servers/webhook` sends requests with
  `net/http` and compares the captures byte for byte;
  `internal/app/services/servers/webhook_test.go` catches a request and
  replays it through a real `HTTPService` on the guarded dialer to an
  `httptest` server, which must see the same method, headers and body.

### SSE server

**SSE** (`sseserver`, `sse`; SSRV-04) keeps `text/event-stream`
responses open on a path, lists its subscribers and pushes events to one
or all of them, from the app or on a timer. It is an HTTP type on
`httpbase`, so it serves HTTPS with the local CA like the others
([TLS](#tls)). `sseserver.Config`:

| Field | Meaning |
|---|---|
| `Path` | where subscribers connect, matched exactly against the request path (the query is ignored); `*` accepts any path. Default `/events`; the WebSocket server's path rules |
| `AllowOrigin` | CORS: `""` sends no CORS headers (a page on another origin cannot subscribe), `*` allows any origin, or one origin (`http://localhost:3000`, a trailing `/` dropped) |
| `KeepAliveSec` | a `: keep-alive` comment every N seconds (0–3,600, 0 for none; default 15), so proxies and clients keep an idle stream |
| `RetryMs` | sent as `retry:` when a subscriber connects (0–3,600,000, 0 for none): how long an EventSource waits before it reconnects |
| `AutoID` | an event sent without an ID gets the next number (1, 2, … per run), so a client can resume with `Last-Event-ID` |
| `Timers[]` | `{Enabled, Event, Data, ID, IntervalMs}` (100 ms to 24 h, at most 50): the event sent to every subscriber at that interval |

An invalid configuration is `sseserver.invalid_config` (detail `field`;
for a timer `field` `timers`, `timerIndex` (0-based) and `timerField`).
`Reconfigure` applies the path and CORS to new requests, the keep-alive
and retry to new subscribers, the timers (restarted) and automatic IDs at
once.

**Requests.** In this order: another path is 404; `OPTIONS` is a CORS
preflight answered `204` with `Access-Control-Allow-Origin`,
`-Methods: GET, OPTIONS`, `-Headers` (the requested ones) and
`-Max-Age: 600` when CORS is on (without CORS, 204 and no CORS headers);
another method than GET is 405 with `Allow: GET, OPTIONS`; a stopping
server answers 503. Each is logged as a request and a response whose
summary says why. A GET subscribes, whatever its `Accept` (curl sends
none): `200` with `Content-Type: text/event-stream`, `Cache-Control:
no-cache`, `X-Accel-Buffering: no` and the CORS headers, flushed at once
with the `retry:` line when set. With `*` the request's `Origin` is sent
back with `Access-Control-Allow-Credentials: true` (so
`withCredentials: true` works; a request without Origin gets `*`), and
`Vary: Origin` either way.

**Framing** (`sseserver.Frame`, the WHATWG "server-sent events" format):
`event:` when the type is set, `id:` when the ID is set, `retry:` when a
retry is given, one `data:` line per line of the data (CRLF, CR and LF
all end a line, so the client gets the lines joined by LF; an empty line
is `data:` alone, so empty data still dispatches an event), then a blank
line. The type and ID must be one line of at most 1,024 bytes (the ID
without U+0000, which clients ignore), the data at most 1 MiB, all UTF-8
(`sseserver.invalid_message`, detail `field`: `event`, `id`, `data`,
`retryMs`). A test parses every frame back with the client's parser
(`realtime/sse`).

**Dynamic values.** The type, data and ID of every event, typed in the
app or in a timer, go through the template engine with no variable scope
(`template.Resolver{}`, [templates.md](templates.md)): `{{$timestamp}}`,
`{{$isoTimestamp}}`, `{{$uuid}}`, `{{$randomInt 1 6}}`… are generated at
each send; other `{{names}}` stay as written, `\{{` is a literal. A
rendered type or ID is cut at a line break.

**Subscribers.** IDs are 1, 2, … per run ("subscriber 3"), not
connection IDs: after a clean end, HTTP keep-alive may carry the next
subscription on the same connection. `Instance` implements
`sseserver.Hub`:

| Method | Does |
|---|---|
| `Subscribers()` | snapshots, oldest first: ID, connection ID, peer, target, connected at, the **Last-Event-ID** it sent, Origin, User-Agent, events and bytes sent |
| `Send(subscriberID, Message{Event, Data, ID, RetryMs})` | to one subscriber, or to every one for `""`: checks and renders the message, takes the next automatic ID when needed, logs **one** `message` event (before the writes), then writes the frame to each target in parallel. Returns `{Sent, Failed, ID}`. To everyone while nobody is subscribed sends and logs nothing (no ID is used). `sseserver.subscriber_not_found` for a subscriber that left; `sseserver.send_failed` when the write to the one subscriber fails |
| `Disconnect(subscriberID)` | ends the response (the chunked body is finished cleanly) and returns once the subscriber is gone and logged. An EventSource then reconnects after its retry time, with `Last-Event-ID` |

Each subscriber's handler goroutine does all its writes (events handed
over one at a time, keep-alives), each bounded by `WriteTimeout` (10 s)
and flushed. The traffic of a subscription: the request (headers, so
`Last-Event-Id` too) and the response ("200 OK: subscriber 2
(Last-Event-ID “7”)"), both with `SSEInfo{SubscriberID, LastEventID}`;
each event pushed as one `out` `message` whose content is the data and
summary "Event “tick” id 8 to 2 subscribers (timer 1)", with
`SSEInfo{Event, ID, Retry, Recipients, Source}` (`SubscriberID` when it
went to one); keep-alive comments are not logged; and a
`connection_close` per subscriber:

| Case | Summary | `InitiatedBy` |
|---|---|---|
| the client went away | "Subscriber 3 left (4 events sent)" | `client` |
| `Disconnect` | "Disconnected by the server (4 events sent)" | `server` |
| `Stop` | "Closed: the server is stopping (4 events sent)" | `server` |
| a write failed | an `error` event, then "Connection lost (…)"; the connection is dropped | `""` |

**Stop** stops the timers, refuses new subscriptions (503), ends every
stream and waits for their handlers (so every close is logged before the
`stopped` event), then shuts httpbase down. **Decisions:** *no replay of
missed events* on `Last-Event-ID` (the server keeps no history; the ID is
shown so the client's resume can be checked); *one log entry per push*,
not per subscriber, since the content is the same (write failures are
per subscriber); *no `Accept` check*, as with the WebSocket server's
Origin.

**Tests:** `sseserver_test.go` has a client that follows the EventSource
processing model (GET with `Accept: text/event-stream`, fail on another
status or content type, the repo's `sse` parser, the last event ID kept
across reconnects and sent as `Last-Event-ID`, reconnect after the
server's `retry`): it receives events with types, multi-line and empty
data and automatic IDs, sees the CORS headers of `*` for another origin,
and after `Disconnect` comes back with `Last-Event-ID`, which the
subscriber list and the traffic show; timers with `{{$timestamp}}`, the
raw `retry:` and keep-alive lines, preflight/404/405, Stop and a client
that leaves; `TestFrame` parses frames back. `servertls.TestSSEOverTLS`
reads an event over HTTPS with the local CA.
`internal/app/services/servers/sse_test.go` drives it with **the app's own
SSE client** (`services/sse` over the HTTP service and the guarded
dialer, reconnect on): pushed events arrive, and after a disconnect it
reconnects with `Last-Event-ID`. A real browser's `EventSource` against
the server (and a page on another origin with CORS) was not run in these
tests; it is left for a manual check.

### UDP server

**UDP** (`udpserver`, `udp`; SSRV-06) receives datagrams, logs each one,
replies, and sends a datagram to a recent peer from the app. It is the
only `PacketType` ([Server types](#server-types)): the Manager gives it
one `net.PacketConn` per bound address (127.0.0.1 and ::1 for localhost).
`udpserver.Config`:

| Field | Meaning |
|---|---|
| `Echo` | send every datagram back to its sender (the default of a new server) |
| `FixedReply`, `Reply`, `ReplyFormat` | send `Reply` to the sender of every datagram, after the echo when both are on; `ReplyFormat` `text` (UTF-8, the default) or `hex` (`hexbytes` rules, as the TCP composer) |

The reply is checked even while `FixedReply` is off, since it is saved:
bad hex, text that is not UTF-8 or more than 65,507 bytes is
`udpserver.invalid_config` (detail `field` `reply` or `replyFormat`). A
running server uses a new configuration from the next datagram.

- **Datagrams.** One reader goroutine per socket with a 64 KiB buffer;
  each datagram is an `in` `message` event ("5 bytes from
  127.0.0.1:53122", peer set, `UDPInfo{Local}`, content up to the content
  limit; not UTF-8 is `Binary`). Replies go out from the socket it
  arrived on, each logged **before** it is written ("5 bytes to
  127.0.0.1:53122 (echo)", `UDPInfo.Reply` `echo` or `fixed`). A failed
  write is an `error` event. An ICMP "port unreachable" that the OS
  reports on the next read (Windows) is an `error` event and reading goes
  on; any other read error ends the run.
- **Peers.** A peer is an address that sent a datagram; the Instance
  keeps the 100 heard from most recently (the oldest is dropped), with
  first and last seen, datagrams and bytes both ways. `Instance`
  implements `udpserver.Hub`: `Peers()` (most recent first) and
  `Send(peer, data)`, which sends one datagram from the socket that peer
  last used: `udpserver.peer_not_found` for an address not in the list
  (the server only answers those who wrote to it, so it cannot be used to
  send to arbitrary hosts), `udpserver.too_large` over 65,507 bytes,
  `udpserver.send_failed` when writing fails. An empty datagram is
  allowed.
- **Stop** closes the sockets and waits for the readers and sends in
  progress, so nothing is logged after the `stopped` event. There are no
  connections, so there are no connection events.
- **Tests:** `udpserver_test.go` uses clients made with `net.DialUDP`:
  echo of text, binary and a 9,000-byte datagram (macOS limits a UDP
  send to about 9 KiB by default), the fixed reply after the echo and a
  hex reply changed while running, a send to a recent peer and the
  refusals, `::1` answered on the same port, stop, bad configurations and
  TLS refused. `internal/app/services/servers/sse_test.go`
  (`TestUDPServerEndToEnd`) does the same through the service with
  `servers:peers` and `SendDatagram` in text and hex.

### gRPC mock server

**gRPC mock** (`grpcmock`, `grpc_mock`; SSRV-07) serves the methods of
`.proto` files with canned responses, so a gRPC client can be tested
without its server. Everything is the repository's own code, standard
library only: the files are linked by the proto linker
([proto.md](proto.md)), canned JSON goes through the JSON mapping
([protobuf-json.md](protobuf-json.md)) and the codec
([protobuf-codec.md](protobuf-codec.md)), frames, timeouts, status
messages and gRPC-Web trailer frames are the gRPC transport's helpers
([grpc-transport.md](grpc-transport.md)), and reflection messages are
`reflectwire` with descriptors from `descproto.Marshal`
([grpc-reflection.md](grpc-reflection.md)). It is an HTTP type on
`httpbase`, served with `http.Server.Protocols` HTTP/1.1, HTTP/2 and
**unencrypted HTTP/2** (Go 1.24+): plaintext clients speak h2c with prior
knowledge, and with TLS ([TLS](#tls)) the Manager's listener negotiates
`h2` by ALPN. `grpcmock.Config`:

| Field | Meaning |
|---|---|
| `Files`, `ImportPaths` | the `.proto` files and their import roots, stored as a gRPC request stores them ([grpc.md](grpc.md#proto-setup)): relative to the open project's root, or absolute. The service resolves relative ones against the open project when the server starts (`WithProjectRoot`); with no project open that start fails with `grpcmock.relative_path`. Files and import paths must be in the project folder or an allowed directory of the settings, and imports are read through the same allowlist (PRV-10, `WithAllowedDirs`): any other fails the start with `fileaccess.not_allowed`. At most 100 each |
| `Reflection` | serve `grpc.reflection.v1` and `v1alpha` (on by default) |
| `Methods[]` | the canned responses, one per method (at most 1,000): `Service` (fully qualified), `Method`, `Messages` (JSON text of the output type, at most 1,000 of 1 MiB each), `Status` (0–16) and `StatusMessage`, `Headers` and `Trailers` (rows `{Enabled, Name, Value}`; keys as the transport checks them, lower case and not reserved, values printable ASCII or Base64 for `-bin` keys; at most 100 each), `DelayMs` and `IntervalMs` (0–300,000) and `Bidi` (`each`, the default, `stream` or `echo`) |

`Normalize` refuses a bad configuration whole with
`grpcmock.invalid_config` (detail `field`; for a method `methodIndex`
(0-based), `methodField` and, for a metadata row, `row`): a service or
method that is not a name, the same method twice, a status past 16, a
reserved or malformed metadata key, a value that is not printable or not
Base64, a delay out of range, an unknown bidi mode. It does no I/O:
whether the methods and messages fit the files is found when they are
linked.

**Loading.** `Start` links the files (30 s at most). Errors in them (a
missing file, a syntax error, a type that does not resolve) fail the start
with `grpcmock.proto_failed` and the first diagnostic ("The .proto files
have errors: orders.proto:4:3: …", detail `errors`, the hint to open them
in a gRPC request to see all). No files starts a server whose every call
is `UNIMPLEMENTED` (an error event says so). Each canned message is parsed
as its method's output type once per configuration; one that does not
fit, a method the files do not define, or `echo` on a method whose input
and output types differ is an `error` event ("Method 1
(/shop.v1.Orders/GetOrder): message 1 is not a valid shop.v1.Order:
unknown field "nope" (line 1, column 2); calls end with INTERNAL.") and
that method's calls end `INTERNAL` with the reason. `Reconfigure` links the
files again (a file edited on disk is picked up by any change, or by Stop
and Start) and swaps the configuration in atomically; calls in progress
finish with the one they started with. When the files now have errors, an
error event says so and the server keeps the definitions it had, with the
new responses.

**Serving.** For each request, in order:

| Case | Answer |
|---|---|
| not `POST` | `405` with `Allow: POST`, logged as a request and a response ("405 Method Not Allowed (gRPC calls are POST requests)") |
| not `application/grpc[+…\|;…]` nor gRPC-Web | `415` |
| a content subtype other than `proto` (`application/grpc+json`) | `UNIMPLEMENTED` |
| a malformed `grpc-timeout` | `INTERNAL` |
| a reflection method with reflection off | `UNIMPLEMENTED` |
| a method the files do not define | `UNIMPLEMENTED` "unknown method /pkg.Svc/M" |
| a method without a canned response | `UNIMPLEMENTED` "no canned response is set for /pkg.Svc/M in PostSonde" |
| a canned response that cannot be sent | `INTERNAL` "PostSonde mock: …" |

A call ends with a trailers-only response when nothing was sent before the
status. Otherwise:

- **Unary and server streaming:** the one request message is read (and
  the client's half-close), then the delay, then the headers; a unary call
  sends the first message (an empty message when there is none) unless its
  status fails; a server-streaming call sends every message, `IntervalMs`
  apart, **then** the status (so a stream can fail midway); then the
  status with the trailers.
- **Client streaming:** every request message until the client
  half-closes, then the delay and the answer as for unary.
- **Bidirectional:** the headers go at once (a client waiting for them is
  not stuck). `each` (the default) answers every client message, after the
  delay, with the next canned message, starting over after the last (no
  messages: no answers); `echo` sends every client message back as it
  came (input and output must be the same type); `stream` sends the
  messages as a server stream does while the client keeps sending, and
  ends once it half-closes. The status and trailers go when the client
  half-closes.
- **Deadline.** `grpc-timeout` bounds the call: a delay or wait that
  outlasts it ends the call `DEADLINE_EXCEEDED`. A client that gives up at
  its own deadline (just before the server's timer, as it rounds up what
  it sends) is logged `DEADLINE_EXCEEDED` too, not `CANCELLED`.
- **Compression.** Requests compressed with gzip (`grpc-encoding: gzip`)
  are read; responses are sent uncompressed. Request messages are limited
  to 4 MiB (`RESOURCE_EXHAUSTED`).
- **gRPC-Web** (`application/grpc-web[+proto]` and
  `application/grpc-web-text`, over HTTP/1.1 or HTTP/2, on the same port):
  the body is read whole first (gRPC-Web is half-duplex), the response
  uses the same content type and ends with a trailer frame, Base64-encoded
  for `-text`. CORS is not answered, so a browser page on another origin
  cannot call it (a proxy or PostSonde's own client can).
- **Stop** ends every call in progress with `UNAVAILABLE` ("the server is
  stopping"), then shuts httpbase down and waits for the calls, so every
  call's last event comes before `stopped`.

**Reflection.** `ServerReflectionInfo` of both versions answers from the
linked files, as grpc-go does: `list_services` lists the files' services
(their imports' too) then the two reflection services;
`file_containing_symbol` (messages, fields, enums, services, methods,
extensions), `file_by_filename` and `file_containing_extension` send the
file with its dependencies not yet sent on the stream (built-in
well-known files included, as `descproto.Marshal` writes them);
`all_extension_numbers_of_type` lists extension numbers; anything not
found is an `error_response` with `NOT_FOUND`. The descriptors are built
the first time a schema is asked for. So `grpcurl -plaintext
localhost:PORT list` and `describe` should work without `-proto`, and
PostSonde's own reflection client (`internal/core/grpc/reflection`)
fetches and links the schema.

**Traffic** of a call (every event has `GRPCInfo`; calls are numbered 1,
2, … per run):

| Event | Summary | Content |
|---|---|---|
| `request` (in) | "Call 3: /shop.v1.Orders/GetOrder (unary)", "… over gRPC-Web" | none; `HTTP` has the request headers, so the metadata, `grpc-timeout` and `user-agent` |
| `message` (in) | "Request message 1 of call 3 (shop.v1.GetOrderRequest, 5 bytes)" | the message as indented JSON of the input type, or its bytes when it does not decode ("… does not decode as …") |
| `message` (out) | "Response message 1 of call 3 (shop.v1.Order, 12 bytes)" | the message as JSON, logged before it is written |
| `response` (out) | "NOT_FOUND: no such order (call 3, 1 message received, 0 messages sent)" | none; `HTTP.Headers` is the header block as sent, `GRPCInfo` the status and the trailers |

Reflection calls log each request ("Reflection request 1 of call 4:
list_services *") and answer ("… files shop/v1/orders.proto,
google/protobuf/timestamp.proto") as text. Connections are logged by
httpbase as usual (an HTTP/2 connection carries many calls).

**Decisions.**
- *A method without a canned response answers `UNIMPLEMENTED`*, like a
  real server without that method, rather than an empty message: a client
  never mistakes a forgotten response for a real answer, and the message
  says what to do. "Mock this method" in the UI adds one with a generated
  example of the output type.
- *Client streaming answers after the half-close; bidirectional answers
  each message by default*, with `stream` and `echo` as the other modes.
  A reply chosen by the content of a request message is not offered yet.
- *A failing unary or client-streaming call sends no message*, as a real
  server's error does; a streaming one sends its messages first, which is
  how a stream that breaks midway is tested.
- *gRPC-Web on the same port*, since the transport's gRPC-Web helpers make it
  the same code path; no CORS, as the SSE server's default.
- *Paths are the gRPC request's*: the editor uses `GRPCService.Describe`,
  `PickProtoFiles` and `PickImportPath`, so the same stored paths and the
  same linker cache serve both. Proto files are picked by the user in the
  dialog; like the gRPC request's they are not limited by the file-access
  allowlist ([privacy.md](privacy.md#local-file-access-prv-10)).
- The canned messages are edited with the gRPC request's message editor
  (`GrpcMessageEditor` with `output`): `GRPCService.ExampleMessage`,
  `ValidateMessage` and `CompleteMessage` take `output: true` to use the
  method's response type ([grpc.md](grpc.md#message)).

**Tests:** `grpcmock_test.go` drives the server with **PostSonde's own
gRPC transport** over h2c: unary with headers, `-bin` trailers and the
traffic (the request decoded to JSON), an error status with no message,
server streaming with its interval and a stream that fails after a
message, client streaming, bidi `each`, `echo` and `stream`, `UNIMPLEMENTED`
for unknown and unmocked methods, a delay past the deadline, Stop ending
a call, an invalid canned message, reconfiguring (and a file that no
longer loads), gRPC-Web binary and text, a gzip request, reflection v1 and
v1alpha through the reflection client (linked back to the same method
types) and turned off, non-gRPC requests, files with errors and relative
paths failing the start, and `Normalize`. `servertls.TestGRPCMockOverTLS`:
gRPC and gRPC-Web over TLS (h2 by ALPN) with the local CA, reflection over
TLS, and a client without the CA refused.
`internal/app/services/servers/grpc_test.go` defines the server through
the service with project-relative paths and calls it with **the app's
gRPC client** (`GRPCService.Invoke`, what the gRPC request editor runs)
over h2c and TLS trusting `ca.pem`: unary, server streaming and an error
status with trailers, and the traffic. **grpcurl was not run** (it cannot
be installed here); `grpcurl -plaintext localhost:PORT list`, `describe`
and a call with `-d` are left for a manual check.

### Socket.IO server

**Socket.IO** (`sioserver`, `socketio`; SSRV-03) accepts Socket.IO v3 and
v4 clients: Engine.IO 4 over HTTP long-polling and WebSocket, with the
upgrade from one to the other and the heartbeat, and Socket.IO 5 packets
on top (CONNECT with an auth payload and namespaces, EVENT, ACK,
BINARY_EVENT and BINARY_ACK with attachments, DISCONNECT, CONNECT_ERROR).
It lists the connected sockets, emits to one socket, to the sockets of a
room or to every socket, answers events with auto-reply rules, and logs
every packet. It is an HTTP type on `httpbase` (so it serves HTTPS and
`wss` with the local CA, [TLS](#tls)); the codecs are the client's
(`realtime/engineio`, `realtime/socketio`, [socketio.md](socketio.md)) and
framing is `wsframe` in the server role. `sioserver.Config`:

| Field | Meaning |
|---|---|
| `Path` | the Engine.IO path, `/socket.io/` by default; a trailing `/` is added, and the path without it is served too. The WebSocket server's path rules |
| `Namespaces` | the namespaces accepted besides `/` (at most 100; `admin` becomes `/admin`); empty accepts every namespace. Another is refused with CONNECT_ERROR `{"message": "Invalid namespace"}`, as socket.io does |
| `Auth` | a JSON object as text, or `""`: a CONNECT is accepted only when its auth payload has each of these members with the same value (more members are fine); otherwise CONNECT_ERROR `{"message": "Not authorized", "data": {"reason": "auth"}}` |
| `Transports` | `""` (both), `websocket` or `polling`; a refused transport is `400 {"code":0,"message":"Transport unknown"}`. The handshake offers the upgrade only when WebSocket is accepted |
| `AllowOrigin` | CORS on the long-polling responses, as the SSE server's: `""` none, `*` any origin (the request's Origin is sent back with credentials allowed), or one origin; `OPTIONS` preflights are answered `204` |
| `PingIntervalMs`, `PingTimeoutMs` | the heartbeat sent in the handshake (25,000 and 20,000 by default, 100 ms to 1 hour): the server pings every interval and closes a client that does not answer within the timeout (`ping timeout`) |
| `MaxPayload` | the largest long-polling body and WebSocket message accepted, in bytes (1,000,000 by default, as socket.io's `maxHttpBufferSize`; at most 256 MiB), sent in the handshake. A larger POST is `413` and closes the client |
| `Echo` | emit an event that no rule answers back to its socket, with the same name and arguments |
| `AckMode` | how an event that asks for an acknowledgement is answered when no rule answers it: `echo` (the default: with the event's own arguments), `empty` (no arguments) or `none` (never, to test a client's ack timeout) |
| `Rules` | the auto-reply rules ([below](#socketio-auto-reply-rules)) |

An invalid configuration is `sioserver.invalid_config` (detail `field`;
for a rule `field` `rules`, `ruleIndex` and `ruleField`). `Reconfigure`
applies the namespaces, the auth and the answers to connected clients at
once, the path, transports, CORS, heartbeat and payload limit to new
clients.

**Engine.IO.** In this order: another path is `404`; `OPTIONS` is a CORS
preflight; `EIO` other than `4` is `400 {"code":5,"message":"Unsupported
protocol version"}` (Engine.IO 3 clients, socket.io v2, are not accepted:
the reference server's `allowEIO3` is not offered); an unknown or refused
transport `Transport unknown`; a `sid` no client has `{"code":1,"message":
"Session ID unknown"}`; each logged as a request and a response saying why.

- *Handshake.* A long-polling `GET` without `sid` opens a client: the
  open packet (`sid`, 20 characters of random URL-safe Base64 like
  engine.io's, `upgrades`, `pingInterval`, `pingTimeout`, `maxPayload`)
  as a payload; a WebSocket without `sid` gets it as its first message
  (`upgrades` empty). A POST handshake is `Bad handshake method`.
- *Long-polling.* One `GET` at a time is held until there are packets
  (a second one closes the client, `transport error`); `POST`s carry
  payloads (`text/plain;charset=UTF-8`, answered `ok`). Packets are
  joined with `\x1e`, binary ones as `b` + Base64.
- *Upgrade.* A WebSocket with the `sid` of a long-polling client is the
  probe: `2probe` is answered `3probe`, the pending poll is ended with a
  noop (and every 100 ms while the upgrade is in progress, as engine.io
  does), and `5` makes the WebSocket the client's transport; what was
  queued meanwhile goes over it, in order. A probe WebSocket that sends
  no `2probe` within 10 s of its `101`, or no `5` within 10 s of the
  probe, is closed; closing the client closes it too. The traffic shows the probe's request and `101`, then "Client 3
  upgraded to WebSocket".
- *Heartbeat.* The server sends `2` every ping interval and expects `3`
  within the ping timeout (Engine.IO 4: the server pings). A client that
  joins no namespace within 45 s is closed (`connect timeout`, socket.io's
  `connectTimeout`).

**Socket.IO.** Packets of a client are handled in order:

- *CONNECT* checks the namespace and the auth, then answers
  `{"sid": "…"}` (a new socket ID) or CONNECT_ERROR; a CONNECT for a
  namespace already joined is answered with the same socket.
- *EVENT / BINARY_EVENT* on a joined namespace is logged with its
  arguments (attachments as `{"$base64": "…"}`, as the Socket.IO
  editor's log shows them) and answered (below). A packet for a namespace
  not joined is logged as ignored.
- *ACK / BINARY_ACK* answers an event the server emitted with an ack; it
  is logged with that event's name and the round trip.
- *DISCONNECT* removes the socket (`client namespace disconnect`).
- A packet that does not decode (`engineio.*`, `socketio.*`) is an error
  event and closes the client (`parse error`).

**Sockets.** A socket is one client in one namespace. Its ID is the
client's number for `/` (`"3"`) and the number followed by the namespace
otherwise (`"3/admin"`); numbers restart with the server. `Instance`
implements `sioserver.Hub`:

| Method | Does |
|---|---|
| `Sockets()` | snapshots by client and namespace: ID, client number, socket ID (`sid`), namespace, Engine.IO session and its transport now, peer, handshake target, Origin, User-Agent, the CONNECT's auth payload, rooms, connected at, events and acks in and out |
| `Emit(to, event, args, ack)` | to the socket `to.SocketID`, else every socket in `to.Room`, else every socket, each in its own namespace. `[]byte` anywhere in the arguments is an attachment; `json.RawMessage` is sent as it is. With `ack` each packet gets the socket's next ack ID (from 0), and the answer is logged with its round trip. Each packet is logged before it is written. `sioserver.invalid_event` for no name or a reserved one (`connect`, `disconnect`, …), `sioserver.socket_not_found` for a socket that left |
| `Disconnect(socketID, closeClient)` | sends DISCONNECT (`socket.disconnect()`); with `closeClient` closes the client's whole session with a close packet (`socket.disconnect(true)`, `forced close`) |
| `SetRooms(socketID, rooms)` | puts the socket in exactly these rooms (besides its own, its `sid`); a name with a comma or a line break, or over 256 bytes, is `sioserver.invalid_room`. Rooms are the server's: a client cannot join one itself |

**Closing.** Whatever ends a client, its sockets go with it and one
`connection_close` event says why, with `SIOInfo{Reason, InitiatedBy}`:
`transport close` (the client closed: a close packet or its WebSocket),
`transport error`, `ping timeout`, `parse error`, `forced close`,
`connect timeout` or `server shutting down`. **Stop** refuses new clients
(503), sends every client a close packet, all clients at once (over
WebSocket, then a close frame 1001; over long-polling with the pending
poll, or the next one within 2 s), closes upgrades in progress, waits for
the readers, then stops httpbase, so every close is logged before
`stopped`. When the stop's context ends first (the manager's
`StopTimeout`), the clients' connections are cut so that one that does
not read cannot hold the stop.

#### Socket.IO auto-reply rules

A `Rule` is `Enabled`, `Match` (`exact`, the default, `contains` or
`regex`, Go RE2 anywhere in the name) and `Pattern` on the **event
name**, then the answer: `Ack` (the acknowledgement's arguments, sent when
the event asks for one; `""` acknowledges with none), `Emit` and
`EmitArgs` (an event sent back to the socket; `""` for none) and
`DelayMs` (0–60,000). Arguments are JSON text as the Socket.IO editor
takes them (an array holds one argument per element, another value is one
argument; `{"$base64": "…"}` or `{"$hex": "…"}` is binary) and may hold
dynamic values such as `{{$timestamp}}` ([templates.md](templates.md)),
rendered at every answer.

**Decisions.**
- *The first enabled rule that matches answers the event*, and nothing
  else does; without one, echo (if on) emits the event back and
  `AckMode` answers its ack request. So a rule can hold an ack back
  (a delay) or answer it with other arguments, and `none` lets a client's
  ack timeout be tested.
- *Rules match the event name only*, not the arguments: the name is what
  Socket.IO applications dispatch on; a reply chosen by the arguments is
  not offered yet.
- *Checked when saved*: an unknown match mode, a regular expression that
  does not compile, arguments that are not JSON after their dynamic
  values are rendered, a reserved event name, a delay out of range, more
  than 100 rules (`ruleField` `match`, `pattern`, `ack`, `emitArgs`,
  `emit`, `delayMs`). Disabled rules are checked too.
- *Delay*: the answer is sent from a timer, so the client's next events
  are handled meanwhile; a client gone by then gets nothing.

**Traffic** (every event of a client has `SIOInfo`): the handshake
request and its response ("200 OK: client 3 connected over long-polling
(session …)" with the open packet), or for WebSocket the upgrade request,
the `101` and "Client 3 connected over WebSocket"; then a `message` per
Socket.IO packet in and out: "CONNECT to /admin from client 3" (content
the auth payload), "CONNECT to client 3: joined /admin as socket
3/admin", "Event “chat” from socket 3 (ack 4)" (content the arguments),
"Ack 4 to socket 3 (auto-ack)", "Event “news” to socket 3/admin (ack 0)",
"Ack 0 from socket 3/admin (answers “news” after 12 ms)", with
`SIOInfo.Packet`, `Event`, `AckID`, `Attachments`, `RTT` and `Source`
(`echo`, `auto-ack`, `auto-reply rule 2`); "Client 3 upgraded to
WebSocket"; and the `connection_close` above. Pings, pongs, noops and the
polling requests themselves are not logged; httpbase logs the TCP
connections as usual.

**Tests:** `sioserver_test.go` drives the server with **PostSonde's own
Socket.IO client** (`realtime/sioclient`) over WebSocket, long-polling and
the upgrade from one to the other: auth accepted, an event with JSON and
binary arguments acknowledged with the same arguments, an event the server
emits with binary data, the client's disconnect; listed namespaces, an
unknown one and a missing or wrong token refused; rules with an ack, a
binary emit, dynamic values and a delay, echo, and the ack modes changed
while connected (`none` gives the client's ack timeout); emits to one
socket, a room and everyone across transports and namespaces, a
server-side DISCONNECT (`io server disconnect` on the client) and a forced
close; the heartbeat (the client answers pings over long-polling); Stop
with clients on both transports. A raw WebSocket client covers what
sioclient does not do: answering the server's ack request (logged with its
round trip), a stray ack, a packet for a namespace not joined, a packet
that does not decode, and a client that never answers pings (`ping
timeout`). `internal/app/services/servers/sio_test.go` runs it through the
service with the app's client upgrading from long-polling: the socket list
in `servers:sockets`, an emit with a binary argument and an ack from the
composer, a rule's answer, rooms, a DISCONNECT, and the errors.
The traffic shape the server writes is the one captured from socket.io
4.8.1 in `realtime/socketio/testdata/reference-v4.txt`. The **official
`socket.io-client`** (4.8.4) is run against the server by the opt-in
`TestInteropOfficialClient` (`scripts/sio-interop.sh`, see
[socketio.md](socketio.md#interop-with-the-official-implementations)):
long-polling only, WebSocket only and the default upgrade, `/` and
`/admin` over one connection with the required auth, the rule's ack and
event, echoed emits and acks with `Buffer` arguments, the server's event
with an ack request answered by the client's callback with a `Buffer`,
the heartbeat, the disconnects and the refusals. All pass.

### GraphQL mock server

**GraphQL mock** (`graphqlmock`, `graphql_mock`; SSRV-08) serves a
GraphQL schema so a GraphQL client can be tested without its server:
every query or mutation is answered with data shaped exactly like its
selection set. Everything is the repository's own GraphQL engine
([graphql-engine.md](graphql-engine.md)): the schema is the schema model
built by `schema.ParseSDL` or `schema.ParseIntrospection`, documents are
parsed by the parser and checked by the validator (`validate.Validate`,
the editor's rules), and the executor in `graphqlmock/exec.go` follows the
spec's Execution section. It is an HTTP type on `httpbase` (HTTP/1.1,
TLS by the Manager). `graphqlmock.Config`:

| Field | Meaning |
|---|---|
| `Path` | the endpoint (default `/graphql`); other paths answer 404 |
| `SchemaFile` | an SDL file (`.graphql`, `.graphqls`, `.gql`, …) or a saved introspection result (`.json`, or any file holding a JSON object), stored as a GraphQL request stores its schema file: relative to the open project's root, or absolute. The service reads it through `Options.ReadFile`: relative to the project, from the project folder or an allowed directory only (PRV-10, `fileaccess.not_allowed`), at most 32 MB; with no project open a relative path fails with `fileaccess.invalid_path` (`graphqlmock.relative_path` without a reader) |
| `SchemaText` | used when `SchemaFile` is `""`: pasted SDL, or introspection JSON (text that starts with `{`: `{"data":{"__schema":…}}`, `{"__schema":…}` or the `__schema` value). A new server has an example schema (users, posts, a union, an interface, an enum, an input type, a custom scalar) |
| `Introspection` | answer `__schema` and `__type` (on by default); off, a query that selects them is a request error. `__typename` always works |
| `ListLength` | items in a generated list, 0–100 (default 2) |
| `NullPercent` | how often a nullable value is generated as null, 0–100 (default 0), chosen per position from the seed |
| `Seed` | varies the generated data |
| `AllowOrigin` | the CORS origin (`*` or one origin; `""` for none): `Access-Control-Allow-Origin` on every answer and the `OPTIONS` preflight (methods `GET, POST, OPTIONS`, the requested headers) |
| `Overrides[]` | the static responses, at most 1,000, first match wins: `Enabled`, `Target` (`Type.field` of an object or interface type, or the name of a scalar or enum type), `Args` (a JSON object the field's coerced arguments must contain, compared loosely: `1` matches `"1"` for an `ID`; `""` for any call), `Mode` (`value`, the default, or `error`), `Value` (JSON text, at most 1 MiB; `""` is null), `Error` and `Code` (the field error's message and `extensions.code`) |

`Normalize` checks the shape (a path starting with `/`, the ranges, a
target that is a name or `Name.name`, only field overrides with `Args` or
`error`); whether a target exists and whether `Value` and `Args` are JSON is
checked against the loaded schema (`compile`), and an override that does
not fit is skipped and logged ("Override 2 (User.nope): the type User has
no field "nope".") as are the schema's own problems (a usable schema with
broken references). A schema that does not load (syntax errors, a missing
file, no query root) fails the start with `graphqlmock.schema_failed`; a
server with no schema at all starts and answers every request with an
error (HTTP 500) until one is set. `Reconfigure` loads the schema again (the
UI's Reload does this for a file): when it no longer loads, the error is
logged and the old schema stays, with the new settings.

**Requests** follow [GraphQL over HTTP](https://graphql.github.io/graphql-over-http/draft/):
`POST` with `application/json` (`{query, operationName, variables,
extensions}`; also `application/graphql` with the document as the body),
or `GET` with the `query`, `operationName` and `variables` (JSON)
parameters; a mutation over `GET` is 405 with `Allow: POST`. The response
is `application/graphql-response+json` when the `Accept` header prefers it
(or allows `*/*`), else `application/json` (no `Accept` header too);
`Accept` that allows neither is 406. With `application/json` every
well-formed request is 200, request errors included; with
`application/graphql-response+json` a request error (no `data`) is 400.
Malformed requests (a body that is not JSON, no query, variables that are
not an object) are 400, other content types 415, bodies over 4 MB 413,
other methods 405. Subscriptions are not served (a request error);
SSRV-08 covers queries and responses only.

**Execution.** The operation is picked by `operationName` (spec messages:
"Must provide operation name if query contains multiple operations."),
variables are coerced against their definitions (defaults, required
variables, Int range, enums, input objects with their defaults, `@oneOf`,
a single value for a list; `Variable "$n" got invalid value "ten"; Int
cannot represent non-integer value: "ten"`), and the selection set is
executed: fields merged by response key (aliases), fragments and inline
fragments that apply to the object's type, `@skip` and `@include`, every
argument coerced with its default. A value comes from, in order: the
member of an override's object for that field (an object may leave
members out: they are generated), the first enabled override of the
field (the object type's, then each interface's) whose `Args` match, and
else generated data. Leaves given as JSON are checked as the spec's result
coercion does (an enum value must exist, `Int` fits 32 bits, …); a value
of the wrong shape is a field error naming it ("Enum "Role" cannot
represent value: "OWNER" (the mock value of User.role)"). An abstract type
takes the `__typename` of its JSON object when given (it must be a
possible type), else one of its object types chosen by the seed. Field
errors make the field null and travel up to the nearest nullable
position, as the spec says, with `path` and `locations`. A response is
at most 250,000 values (long lists nested deep grow fast), then a field
error (`RESPONSE_TOO_LARGE`) ends it.

**Generated data** is deterministic: each value's seed mixes its parent's
with the field's name and its arguments as canonical JSON (or a list
item's index), so the same query gets the same response on every request
and after a restart, `user(id: 1)` and `user(id: 2)` differ, and an alias
changes nothing. `Int` is 0–999, `Float` has two decimals, `Boolean`
either, `ID` a number as a string, an enum one of its values not
deprecated. A `String` looks like its field's name, read as words
(`createdAt`, `avatar_url`): emails, URLs, phone numbers, colours, first,
last and full names, user names, titles, timestamps (RFC 3339 for `…At`,
`…date`, `…time`), sentences for descriptions and bodies, else
`"title 42"`. A custom scalar looks like its name: `DateTime`, `Date`,
`Time`, `UUID`, `URL`, `Email`, `JSON` (an object), `Long`/`BigInt`
(an integer), `Decimal` (a number), `Boolean`, else `"Scalar 42"`; a type
override (`Target: "DateTime"`) fixes the value of every one. A list has
`ListLength` items, or the length a paging argument given in the document
asks for (`first`, `last`, `limit`, `take`, `count`, `size`, `pageSize`,
`perPage`, `top`; at most 100; a default does not count).

**Introspection** answers `__schema` and `__type(name:)` from the schema
model (`introspection.go`), lazily, as deep as the query asks: the types
are the declared ones, then the built-in scalars and introspection types
the schema does not declare, as a graphql-js server lists them; the
directives likewise; `includeDeprecated` filters fields, arguments, input
fields and enum values; `deprecationReason` is null for members that are
not deprecated. A client's full introspection query (the app's
`FullIntrospectionQuery`, graphql-js's) gives back the same schema.

**Errors** are the spec's: `{"errors": [{"message", "locations": [{"line",
"column"}], "path", "extensions": {"code"}}], "data": …}` with `errors`
first. Request errors have no `data` and the codes
`GRAPHQL_PARSE_FAILED` ("Syntax Error: …"), `GRAPHQL_VALIDATION_FAILED`
(the validator's graphql-js messages), `BAD_USER_INPUT` (variables),
`OPERATION_RESOLUTION_FAILURE` and `BAD_REQUEST`; field errors from the
mock's own values are `MOCK_VALUE_INVALID`, and an error override's code
is its own.

**Traffic.** A GraphQL request is two events: the request (summary
`query Profile (POST /graphql)`, the body, the headers) and the response
(`200 OK: data, 1 error`, the body sent), both with `GraphQL`: the
request number, operation name and type, the variables, whether it is
introspection, and on the response the error count and whether it has
data. Paths other than the endpoint and preflights are logged with their
answer.

**Describe.** `graphqlmock.Describe(config, readFile)` (the service's
`DescribeGraphQLMock`) loads the schema as `Start` would and returns
what the editor shows: source and format, the counts of types and root
fields, the targets overrides can answer (root fields first, then the
fields of the other types, then the custom scalars and enums), the
problems, and the schema as SDL.

**Tests:** `exec_test.go` runs the executor directly: the response keys
in selection order with aliases, fragments on a union and `@skip`;
determinism (same query, another seed, different arguments, aliases);
each kind of generated value; `NullPercent` 100 keeping non-null fields;
overrides (arguments matched, partial objects, a disabled one, a type
override, an error with its code, path and location, `__typename`
choosing the type); null propagation through non-null fields and lists;
bad mock values; the request errors with their exact JSON; variable and
argument defaults; an introspection query's exact JSON and introspection
turned off; paging arguments; the size limit. `graphqlmock_test.go`
drives the server with **PostSonde's own GraphQL client**
(`request.BuildGraphQL`, what a GraphQL request sends, and `httpclient`):
the full and conservative introspection queries read back with
`schema.ParseIntrospection` give the served schema (also for the
schema-model fixtures: GitHub-style, countries, Star Wars), a query with
variables, an alias, a fragment, `@include` and an interface fragment over
POST and GET with its traffic events, the status codes and media types
above, CORS, schemas from an SDL file, an introspection file and pasted
introspection, schemas that fail the start, no schema, reconfiguring
(and a schema that no longer parses), **Stop with a client stalled in the
middle of a request body** (bounded by the stop timeout) and starting
again, `Describe` and `Normalize`. `internal/app/services/graphql`
`TestFetchSchemaFromGraphQLMock` runs the GraphQL service's **Fetch
schema** (the editor's button) against the mock and gets the same
schema; `internal/app/services/servers/graphql_test.go` runs it through
the service with a project-relative file, an allowed directory and a
refused one. Other clients (Apollo Client, graphql-request, GraphiQL in
a browser with CORS) were not run: left for a manual check.

### Captures

A request event may carry `Event.Capture` (only the webhook receiver sets
it). The Manager takes it out of the event before the event is kept or
passed to `OnTraffic`, stamps it with the event's `Seq`, `Time` and
`Peer`, keeps it in a per-server buffer apart from the traffic buffer, and
sets `Event.Captured`. The buffer keeps the newest captures within
`Options.Captures` (200) and `Options.CaptureBytes` (64 MiB, counting
bodies, header lines and the request line); a capture larger than the
whole budget is not kept (its event is not `Captured`). Captures outlive
a stop and a restart of the server, are forgotten by `ClearTraffic` and
with the server, and live in memory only.

**Why the Manager keeps them (decision).** A type's instance lives for one
run, so captures kept there would be lost on Stop, and the Manager's
traffic buffer caps content at 64 KiB for the UI: neither can give the
exact bytes back. The Manager already owns per-server state keyed by ID
and sequence numbers, so it keeps captures next to the traffic, found by
the request event's `Seq`, with their own budget.

### Traffic events

| Field | Meaning |
|---|---|
| `ServerID`, `Seq` | set by the Manager; `Seq` is 1, 2, 3, … per server and never reused while it exists |
| `Time` | when it happened; the Manager's clock when the type leaves it zero |
| `Peer`, `ConnID` | remote address and connection ID; empty and 0 for the server's own events |
| `Direction` | `in` (the peer sent it), `out` (the server sent it), `system` |
| `Kind` | `request`, `response`, `message`, `connection_open`, `connection_close`, `started`, `stopped`, `error` |
| `Summary` | one line: `GET /users?id=1`, `200 OK`, `Listening on 127.0.0.1:8080 and [::1]:8080` |
| `Content`, `Size`, `Truncated`, `Binary` | the payload, capped at `Options.ContentLimit` (default 64 KiB); `Size` is the full length; `Binary` when not UTF-8 (a cut inside a character still counts as text) |
| `HTTP` | method, target, protocol, status (0 on a request) and headers |
| `WS` | the WebSocket server's detail: client ID, opcode (`text`, `binary`), subprotocol, close code, reason and who initiated; nil for other servers |
| `TCP` | the TCP server's detail, set on every connection event: on `connection_close` who initiated it (`client`, `server`, `""`) and the byte totals; nil for other servers |
| `SSE` | the SSE server's detail, set on every subscription and event: the subscriber, an event's type, ID, retry, recipients and source (`timer 2`), the `Last-Event-ID` a subscriber came with, who ended it; nil for other servers |
| `UDP` | the UDP server's detail, set on every datagram: the local address and the kind of automatic reply (`echo`, `fixed`); nil for other servers |
| `SIO` | the Socket.IO server's detail, set on every event of a client: its number, Engine.IO session and transport, the socket and namespace, a packet's type, event name, ack ID, attachments, an ack's round trip, what sent it (`echo`, `auto-ack`, `auto-reply rule 2`), and on `connection_close` the reason and who ended it; nil for other servers |
| `GRPC` | the gRPC mock's detail, set on every event of a call: the call number, method path, method kind, gRPC-Web or not, a message's type and index, and on the call's response event the status (code, name, message) and the trailers sent; nil for other servers |
| `GraphQL` | the GraphQL mock's detail, set on the request and response events of a GraphQL request: the request number, the operation's name and type, the variables as JSON, whether it is introspection, and on the response the number of errors and whether it has data; nil for other servers |
| `Capture`, `Captured` | the webhook receiver's whole request, which the Manager moves into its [captures](#captures); `Captured` says it was kept |

The Manager logs `started` ("Listening on …", plus the LAN warning) before
the type starts, `stopped` after a stop and `error` when starting or
serving fails.

**Recent buffer (decision).** The UI keeps up to 50,000 messages per log,
but it loses them when the page reloads (or the project closes and the
shell remounts), while the servers keep running in Go. So Go also keeps the
most recent events per server: at most 1,000 events and 8 MiB of content
(`RecentEvents`, `RecentBytes`; the newest event is always kept).
`Traffic(id)` returns them and the UI merges them with live events by
`Seq`. It is a re-attach buffer, not history: older traffic is only in the
UI's log. `ClearTraffic` empties it, so the UI's Clear also clears Go.

### Logging and privacy

Server traffic can hold tokens, cookies and personal data, so it never goes
to the log file: the Manager logs only start, stop and failure with the
server ID, type, addresses and error. `echo` discards `net/http`'s own error
log. Traffic stays in memory (the recent buffer and the UI) and is gone when
the app quits.

## Service

`ServersService` (`internal/app/services/servers`):

| Method | Returns |
|---|---|
| `Types()` | `TypeDTO[]` (`name`, `label`, `defaultConfig`) |
| `List()` | `ServerDTO[]` in the order created |
| `Create(CreateInputDTO)` | the new `ServerDTO`, stopped; `config.mockHttp` null gets the default routes. `tls` (`ServerTLSInputDTO{certificate, certFile, keyFile, pfxFile, passphrase}`, null for plain text) is checked as in `UpdateServer` |
| `UpdateConfig(id, ConfigDTO)` | the `ServerDTO` with the new config and its diagnostics; a mock server needs `mockHttp` (`mockhttp.invalid_config`). A saved server writes it to its folder too; when that fails the change is undone and the error returned |
| `UpdateServer(id, ServerSettingsDTO)` | changes `name`, `host`, `port`, `allowLan` and `tls` (`Redefine`; `tls` null is plain text, so send the server's TLS back to keep it): `servers.active` while it runs; a saved server writes it to its folder, undone when that fails. Certificate files are read now (a bad file or passphrase rejects); a `passphrase` given is stored in `<data>/secrets/app.json` after the change succeeded, null keeps the stored one, `""` removes it |
| `PickTLSFile(kind)` | the path chosen in the native dialog for `cert`, `key` or `pfx` (`servers.tls_file_kind` otherwise), `""` when cancelled |
| `LocalCA()` | `LocalCADTO{path, subject, notAfter, sha256, pem, trusted}`: the local CA, created the first time; `trusted` when `path` is one of the CA bundles |
| `RevealLocalCA()` | shows `ca.pem` in the file manager (`servers.reveal_unavailable` without one) |
| `Start(id)`, `Stop(id)` | the `ServerDTO` after the call; a failed start rejects with the error. A saved server of the open project first takes the definition its folder holds now |
| `Remove(id)` | a saved server of the open project is removed from its folder first (nothing is removed when that fails); one of another project only from the list |
| `SyncProject()` | `ProjectServersDTO{workspaceId, problems}`: brings the list in line with the open project's saved servers, see [Saved server configs](#saved-server-configs) |
| `CreateFolderServer({workspaceId, folderId, server})` | defines a server in a folder of the open project: checked like `Create`, written to its `_folder.json`, returned stopped. `servers.folder_has_server` when the folder has one |
| `Traffic(id)` | `TrafficDTO[]`, the recent buffer |
| `ClearTraffic(id)` | |
| `Clients(id)` | `WSClientDTO[]` of a WebSocket server, oldest first; `[]` when it is not running; `servers.not_websocket` for another type |
| `SendToClient({serverId, clientId, message})` | sends `message` `{binary, text, dataBase64}` (`wsserver.invalid_message` for bad base64 or non-UTF-8 text); `servers.not_running` when stopped |
| `Broadcast({serverId, message})` | `{sent, failed}` |
| `DisconnectClient({serverId, clientId, code, reason})` | code 0 means 1000; resolves once the client is gone |
| `Connections(id)` | `TCPConnectionDTO[]` of a TCP server, oldest first; `[]` when it is not running; `servers.not_tcp` for another type |
| `SendToConnection({serverId, connectionId, mode, text, lineEnding})` | `{bytes}`. `mode` `text` sends the text as UTF-8 plus `lineEnding` (`none`/`""`, `lf`, `crlf`); `hex` sends the bytes `text` spells (`hexbytes.*` errors, as the TCP client). An unknown mode or line ending is `tcpserver.invalid_message`; `servers.not_running` when stopped |
| `CloseConnection({serverId, connectionId})` | resolves once the connection is closed and logged |
| `Subscribers(id)` | `SSESubscriberDTO[]` of an SSE server, oldest first; `[]` when it is not running; `servers.not_sse` for another type |
| `SendEvent({serverId, subscriberId, event, data, id, retryMs})` | `SSESendResultDTO{sent, failed, id}`: to one subscriber, or to all for `subscriberId` `""`; dynamic values rendered; `sseserver.invalid_message` (detail `field`), `sseserver.subscriber_not_found`, `servers.not_running` |
| `DisconnectSubscriber({serverId, subscriberId})` | resolves once the subscriber is gone |
| `Peers(id)` | `UDPPeerDTO[]` of a UDP server, most recent first; `[]` when it is not running; `servers.not_udp` for another type |
| `SendDatagram({serverId, peer, mode, text, lineEnding})` | `{bytes}`: one datagram to a recent peer, `mode` and `lineEnding` as `SendToConnection`; `udpserver.peer_not_found`, `udpserver.too_large` |
| `Sockets(id)` | `SIOSocketDTO[]` of a Socket.IO server, by client and namespace; `[]` when it is not running; `servers.not_socketio` for another type |
| `EmitToSockets({serverId, socketId, room, event, args, ack})` | `SIOEmitResultDTO{sent, failed}`: to the socket `socketId`, else the sockets in `room`, else every socket; `args` is JSON text (`socketio.invalid_args` with `offset`; dynamic values rendered); `sioserver.invalid_event`, `sioserver.socket_not_found`, `servers.not_running` |
| `DisconnectSocket({serverId, socketId, closeClient})` | DISCONNECT to the socket, or with `closeClient` the client's whole connection closed |
| `SetSocketRooms({serverId, socketId, rooms})` | the socket's rooms; `sioserver.invalid_room` |
| `Captures(id)` | `CaptureSummaryDTO[]` of a webhook receiver, oldest first (also when stopped); `servers.not_webhook` for another type |
| `Capture({serverId, seq})` | `CaptureDTO`, one kept request in full; `servers.capture_not_found` when it is not kept |
| `Replay({serverId, seq, url})` | `StartedDTO` of an **http** operation: the kept request sent to `url`, see [Replay](#replay) |
| `PickOpenAPIFile()` | the path of the OpenAPI file chosen in the native dialog, `""` when cancelled; see [Routes from OpenAPI](#routes-from-openapi) |
| `GenerateMockRoutes(MockOpenAPIInputDTO)` | `MockOpenAPIResultDTO`: the routes generated from an OpenAPI file, merged into the given routes, for a preview; changes nothing |
| `MockRouteFromExample(MockExampleInputDTO)` | `MockExampleResultDTO`: the route of a response example, merged into the given routes, for a preview; changes nothing. See [Routes from a response](#routes-from-a-response) |
| `MockTemplateNames()` | `MockTemplateNameDTO[]` (`{name, description, source}`): the names a templated mock response can use, the `request.` ones (source `request`; a name ending in `.` takes a parameter, header or JSON path) then every dynamic value (source `dynamic`), for the body editor ([mock-routes.md](mock-routes.md#templates)) |
| `DescribeGraphQLMock(GraphQLMockConfigDTO)` | `GraphQLMockDescriptionDTO{source, format, types, queryFields, mutationFields, subscriptionFields, targets [{target, type, args, root}], problems [{overrideIndex, field, message}], sdl}`: the schema of a GraphQL mock configuration loaded as the server would (the file through the allowlist), with the overrides that do not fit it (`overrideIndex` -1 for a schema problem); changes nothing. A schema that does not load rejects (`graphqlmock.schema_failed`, `fileaccess.*`). See [GraphQL mock server](#graphql-mock-server) |
| `PickGraphQLSchemaFile()` | the schema file chosen in the native dialog (SDL or introspection JSON) as it is stored: relative to the project root with `/` when inside it, else absolute; `""` when cancelled |

`ServerDTO`: `id`, `type`, `typeLabel`, `name`, `host`, `port`, `allowLan`
(as defined), `state`, `addresses` (`["127.0.0.1:8080", "[::1]:8080"]`),
`boundPort`, `scope`, `warning` (`{code, message}` or null), `startedAt`,
`stoppedAt` (RFC 3339 or `""`), `error` (`ErrorDTO` or null), `events`,
`config`, `diagnostics` and `folder` (`{workspaceId, folderId,
folderName}` of a saved server, null otherwise). `ProjectServersDTO`:
`workspaceId` (`""` with no project open) and `problems`
(`[{folderId, folderName, error}]`).

`ConfigDTO` has one nullable field per configurable type: `webhook`
(`WebhookConfigDTO{status, headers [{enabled, name, value}], body}`;
`UpdateConfig` of a webhook receiver needs it, `webhook.invalid_config`), `webSocket`
(`WebSocketConfigDTO{path, subprotocols, echo, maxMessageSize, rules}`, rules
`WSRuleDTO{enabled, match, pattern, reply, delayMs}` (null is none); `UpdateConfig`
of a WebSocket server needs it, `wsserver.invalid_config` with `ruleIndex` and
`ruleField` for a bad rule), `sse` (`SSEConfigDTO{path, allowOrigin, keepAliveSec, retryMs,
autoId, timers}`, timers `SSETimerDTO{enabled, event, data, id, intervalMs}`; `UpdateConfig` of
an SSE server needs it, `sseserver.invalid_config` with `timerIndex` and `timerField`), `udp`
(`UDPConfigDTO{echo, fixedReply, reply, replyFormat}`; `udpserver.invalid_config`), `grpcMock`
(`socketIo` (`SocketIOConfigDTO{path, namespaces, auth, transports, allowOrigin, pingIntervalMs,
pingTimeoutMs, maxPayload, echo, ackMode, rules}`, rules `SIORuleDTO{enabled, match, pattern, ack, emit,
emitArgs, delayMs}`; `UpdateConfig` of a Socket.IO server needs it, `sioserver.invalid_config` with
`ruleIndex` and `ruleField`), `graphqlMock` (`GraphQLMockConfigDTO{path, schemaFile, schemaText,
introspection, listLength, nullPercent, seed, allowOrigin, overrides}`, overrides `GraphQLMockOverrideDTO{enabled,
target, args, mode, value, error, code}`; `UpdateConfig` of a GraphQL mock needs it, `graphqlmock.invalid_config`
with `overrideIndex` and `overrideField`), `grpcMock`
(`GRPCMockConfigDTO{files, importPaths, reflection, methods}`, methods `GRPCMockMethodDTO{service,
method, messages, status, statusMessage, headers, trailers, delayMs, intervalMs, bidi}` with
`{enabled, name, value}` rows; `UpdateConfig` of a gRPC mock needs it, `grpcmock.invalid_config` with
`methodIndex` and `methodField`), all of these left out of the JSON for other types, and `mockHttp`
(`MockHTTPConfigDTO{routes, strictTrailingSlash, caseInsensitive}`, routes
`{id, enabled, method, path, status, headers [{enabled, name, value}], body
{mode, type, text}, delay {mode, ms, minMs, maxMs}}`), null for other types.
`diagnostics` is `RouteDiagnosticDTO[]` (`code`, `routeIndex`, `routeId`,
`field`, `start`, `end` as UTF-8 bytes, `headerIndex`, `message`), computed
from the config every time a `ServerDTO` is made.

`TrafficDTO`: `serverId`, `seq`, `time`, `peer`, `connId`, `direction`,
`kind`, `summary`, `text` or `dataBase64` with `binary`, `size`,
`truncated`, `sse` (`subscriberId`, `event`, `id`, `retryMs`, `recipients`, `source`,
`lastEventId`, `initiatedBy`), `udp` (`local`, `reply`) and `grpc` (`call`, `method`, `kind`, `web`,
`type`, `index`, `code`, `codeName`, `message`, `trailers` `[{name, value}]`) and `sio` (`clientId`, `session`,
`transport`, `socketId`, `namespace`, `packet`, `event`, `ackId`, `hasAck`, `attachments`, `rttMs`, `source`,
`reason`, `initiatedBy`) and `graphql` (`request`, `operation`, `operationType`, `variables`, `introspection`,
`errors`, `hasData`), each left out for other servers,
`http` (`method`, `target`, `proto`, `status`, `headers`
`[{name, value}]`, `route` `{id, method, pattern, params, wildcard}` or
null, `delayMs`) or null, `ws` (`clientId`, `opcode`, `subprotocol`,
`closeCode`, `closeReason`, `initiatedBy`) or null, `tcp` (`initiatedBy`,
`bytesIn`, `bytesOut`) or null, and `captured`. `CaptureSummaryDTO`:
`seq` (of the request event), `time`, `peer`, `method`, `target`,
`contentType`, `size`, `truncated`. `CaptureDTO`: `serverId`, `summary`,
`proto`, `host`, `path` (the target before `?`, as sent), `query` (`[{name,
value}]` in order, decoded; a part that does not decode is shown as sent),
`headers` (`Host` first) and `body`, an http `ResponseDTO` with only its
body fields set (`httpservice.BodyView`: language, charset, text or hex
dump, pretty JSON or XML, `contentType`, `size`, `bodyTruncated`), so the
UI shows it with the response body viewer. `SSESubscriberDTO`: `id`, `connId`, `peer`,
`target`, `connectedAt`, `lastEventId`, `origin`, `userAgent`, `eventsSent`, `bytesSent`.
`SIOSocketDTO`: `id`, `clientId`, `sid`, `namespace`, `session`, `transport`, `peer`, `target`, `origin`,
`userAgent`, `auth`, `rooms`, `connectedAt`, `eventsIn`, `eventsOut`.
`UDPPeerDTO`: `addr`, `local`, `firstSeen`, `lastSeen`, `datagramsIn`, `datagramsOut`,
`bytesIn`, `bytesOut`. `TCPConnectionDTO`: `id`, `connId`, `peer`,
`local`, `connectedAt`, `bytesIn`, `bytesOut`, `messagesIn`, `messagesOut`.
`WSClientDTO`: `id`,
`connId`, `peer`, `target`, `subprotocol`, `connectedAt`, `origin`,
`userAgent`, `messagesIn`, `messagesOut`, `bytesIn`, `bytesOut`. Goldens: `internal/app/services/servers/testdata/dto/`.

### Routes from OpenAPI

A mock server's routes can be generated from an OpenAPI 3.0/3.1 or
Swagger 2.0 file (MOCK-09; the generator is
`internal/core/servers/mockopenapi`,
[mock-routes.md](mock-routes.md#routes-from-openapi)). Two calls, and
nothing changes on any server until the editor applies the result:

- `PickOpenAPIFile()` shows the native open dialog (`*.yaml`, `*.yml`,
  `*.json`; `WithFilePicker`, wired to the app's file dialog in
  `internal/app/dialogs.go`) and returns the path, `""` when cancelled.
  Without a picker (tests, headless) it fails with
  `servers.pick_unavailable`.
- `GenerateMockRoutes({path, prefix, mode, config})` reads the file (at
  most 32 MiB, `servers.file_too_large`; `servers.file_unreadable` with
  the OS error's kind when it cannot be read), generates one route per
  operation and merges them into `config`, the routes the editor has now.
  `prefix` null uses the path of the document's first server; `mode` is
  `merge` (the default: keep the routes, add the generated ones that do
  not clash) or `replace` (`servers.invalid_mode` otherwise). A file that
  is not an OpenAPI document fails with the reader's error
  (`openapi.not_openapi`…, [openapi.md](openapi.md#use)).

`MockOpenAPIResultDTO`: `title`, `version`, `openapi`, `swagger`,
`defaultPrefix`, `prefix`; `routes`, one per operation in document order
(`{route, operation: "GET /pets/{petId}", operationId, summary,
response, mediaType, status: "added" | "existing" | "over_limit", id}`,
`id` being the route's ID in `config`); `diagnostics` (`{severity, code,
message, operation, routeIndex, line, column, pointer}`: the reader's,
the generator's and the merge's); `config`, the merged
`MockHTTPConfigDTO` to send with `UpdateConfig`; and the counts `added`,
`kept`, `removed`. Applying goes through `UpdateConfig` like any edit, so
a saved server's `_folder.json` gets the new routes (SRV-04) and a
running server serves them at once. The UI is in
[frontend.md](frontend.md#routes-editor).

### Routes from a response

A route can be made from a response a request got (MOCK-03; the
rules are in [mock-routes.md](mock-routes.md#routes-from-a-response)).
One call, and nothing changes on any server until the UI applies the
result:

- `MockRouteFromExample({example, method, url, path, mode, name, config})`
  takes a `ResponseExampleDTO` (a request's saved example, or a history
  entry made into one with `HistoryService.Example`), the request's
  `method` and `url` as typed, `path` (null: made from the URL; else the
  pattern to use), `mode` (`route`, the default, `response` or `replace`;
  `servers.invalid_mode` otherwise), `name` (the name of a response added
  in `response` mode; blank: the example's) and `config`, the server's
  routes as the editor has them (empty for a new server). The example is
  masked again with the secrets of `WithSecrets` (wired in
  `internal/app/services.go` to the values copied logs are masked with).

`MockExampleResultDTO`: `method`, `path`, `derivedPath` (the path made from
the URL); `response` (`MockResponseDTO`: what the route sends); `dropped`
(`[{name, value, reason}]`, reason `hop_by_hop`, `content_length`,
`content_encoding`, `date` or `invalid`); `notes` (`[{severity, code,
message}]`, `mockexample.*`); `masked`; `existing` (`{index, id, method,
path}` of the route that answers the same method and path, or null);
`status` (`added`, `existing`, `response_added`, `replaced`,
`over_limit`); `routeId` and `responseName`; `diagnostics` (the
`RouteDiagnosticDTO`s of the route added or changed); and `config`, the
merged `MockHTTPConfigDTO` to send with `UpdateConfig`, or with `Create`
for a new server. The UI is in [frontend.md](frontend.md#routes-editor).

### Events

| Event | Payload | Sent |
|---|---|---|
| `servers:changed` | `ChangedEventDTO{servers}`, every server | after every create, state change and removal, in order |
| `servers:clients` | `ClientsEventDTO{serverId, clients}`, the whole list of one WebSocket server | when its clients change (connected, a message in or out, left): every traffic event with `WS` marks its server, and the marked servers' lists go out at most every 200 ms, so counts stay current without an event per message; `[]` once the server stopped |
| `servers:connections` | `ConnectionsEventDTO{serverId, connections}`, the whole list of one TCP server | like `servers:clients`: every traffic event with `TCP` marks its server, the lists go out at most every 200 ms; `[]` once the server stopped |
| `servers:captures` | `CapturesEventDTO{serverId, captures}`, the kept requests of one webhook receiver | like `servers:clients`: every `Captured` traffic event and every `ClearTraffic` of a receiver marks it, the lists go out at most every 200 ms |
| `servers:subscribers` | `SubscribersEventDTO{serverId, subscribers}`, the subscribers of one SSE server | like `servers:clients`: every traffic event with `SSE` marks its server; `[]` once it stopped |
| `servers:peers` | `PeersEventDTO{serverId, peers}`, the recent peers of one UDP server | like `servers:clients`: every traffic event with `UDP` marks its server; `[]` once it stopped |
| `servers:sockets` | `SocketsEventDTO{serverId, sockets}`, the sockets of one Socket.IO server | like `servers:clients`: every traffic event with `SIO` marks its server; `[]` once it stopped |
| `servers:traffic` | `TrafficEventDTO{events}` | batches of traffic events of all servers, through one `stream.Batcher` (50 ms, at most 64 events per payload, since each can carry 64 KiB) |

### Replay

`Replay` builds an `httpclient.Request` from the capture and hands it to
the replayer the app gives the service (`WithReplayer`):
`httpservice.SendPrepared(httpService, req)`, which runs it as the HTTP
service's own `http` send operation: the shared registry marks its
context as a user operation (`http.send`, PRV-05), so it dials through the
guarded dialer like any send, with the same client, stages, response DTO
and body store; the UI follows it with `useOperation("http", …)` and
cancels it with `OpsService.Cancel`. It uses no environment, auth or
cookie jar (the request is sent as captured), the default timeout from
the settings, the trusted CA bundles and the TLS verification setting, and does not follow
redirects.

- **Unchanged:** the method, every header row (repeated names included,
  `User-Agent` and `Accept-Encoding` as received, none added) and the body
  bytes. `Host` comes from the replay URL, `Content-Length` from the body;
  the connection's own headers (`Connection`, `Keep-Alive`,
  `Proxy-Connection`, `Transfer-Encoding`, `TE`, `Trailer`, `Upgrade`) are
  left out. A request without a body is replayed without one, unless it
  declared `Content-Length` (then an empty body).
- **URL:** as typed, an absolute `http`/`https` URL, else
  `servers.invalid_replay_url`. It replaces the whole target: the UI
  offers `http://localhost:3000` with the request's own path and query.
- **Refused:** a truncated capture (`servers.capture_truncated`), a
  capture no longer kept (`servers.capture_not_found`), a service built
  without a replayer (`servers.replay_unavailable`).

**Why not an operation** ([events.md](events.md)): an operation is one
call's work that ends with a done event and is cancelled through
`OpsService`. A server outlives any call, is stopped and restarted by ID,
and has several states worth showing, so it has its own status event and
one traffic stream for all servers. The batching is the same.

## Saved server configs

A folder of a project can define one local server (SRV-04, in the
folder's **Server** tab), saved in its `_folder.json` as the `server` member
([file-format.md](file-format.md#saved-server-config)): kind, name, bind
address, port, LAN access and the kind's configuration (mock routes, the
WebSocket server's path, subprotocols, echo, message size and auto-reply
rules, the webhook
receiver's response, the SSE server's settings and timers, the UDP
server's replies, the gRPC mock's `.proto` files and canned responses,
the Socket.IO server's settings and auto-reply rules, the GraphQL
mock's schema (a project file or pasted text), generated-data settings and
overrides; the TCP and echo servers have none). A server config
survives a restart because it is a project file, shared with the project
like its requests.

**Model (decision).** A saved server is an ordinary Manager server whose
`Spec.Owner` names the project and the folder, so the Servers view, the
server view, the traffic, the routes, WebSocket and webhook editors and
the tree badge work on it unchanged. The file is the source of truth; the
Manager holds a copy that the service keeps in step:

- **Every change is written first.** `UpdateConfig` and `UpdateServer`
  change the server, then write the whole definition to the folder
  (`serverdef.FromSpec` over the saved one, keeping members this version
  does not know at every level, and leaving out a name equal to the kind's
  label); when the write fails (a read-only folder, a project that is not
  open: `collection.not_found`) the change is undone and the error
  returned. `CreateFolderServer` writes before it adds; `Remove` removes
  the member before the server. Writes go through
  `collection.SaveFolderServer` at once, not through the auto-saver.
- **`SyncProject`** (the UI calls it whenever the open project's tree is
  loaded, reloaded, or gets or loses folders) reads every folder's server
  (`collection.FolderServers`) and: adds the missing ones, **stopped**;
  gives a stopped one the file's definition again (a hand edit, a pull);
  keeps a running one running, taking the file's name and configuration
  but not a new address, which applies at its next start; removes a
  stopped one whose definition or folder is gone; leaves alone the server
  of a folder whose `_folder.json` did not load. A definition the Manager
  refuses (an unknown kind from a newer version, a bad port) is not
  defined and is returned in `problems`; its file is not touched. A kind
  change in the file replaces a stopped server.
- **`Start`** of a saved server of the open project reads its folder
  again first, so the server always starts from what the file holds.
- **Never listening on its own (privacy, SRV-02).** Opening a project,
  syncing, a restart of the app: none of them starts a server. Only Start,
  in the folder's Server tab or the Servers view, does.

**Project switch (decision).** Servers belong to the app run
([Stopping](#stopping)). On the sync after a switch, the stopped saved
servers of the old project are removed (their definitions are in their
project and come back when it opens); its running ones keep running and
serving, listed with "(other project)", can be stopped and removed (the
file keeps the definition), but not changed: the write needs their project
open. After they stop, the next sync removes them. Closing the project
leaves them as they are until the next project's sync.

**Where they show.** The folder's Server tab
([frontend.md](frontend.md#folder-settings)) defines, edits, starts and
stops it, with a warning when it binds beyond localhost; its traffic shows
in the response pane meanwhile. The Servers view lists the open project's
saved servers first ("In this project", with their folder), then the
others ("This session"). The tree shows a badge on the folder while its
server listens: the kind and bound port, `WS :8081`
([frontend.md](frontend.md#collection-tree)).

A saved config also holds the server's TLS (`tls`: the kind of
certificate and the paths of the user's files, never a passphrase;
[TLS](#tls)). The WebSocket server's auto-reply rules (SSRV-02) are saved
as `webSocket.rules` ([file-format.md](file-format.md#saved-server-config));
a file without them loads with none, and a rule's members a newer version
writes are kept with the rule at the same position while it matches the
same way.

Tests: `internal/core/servers/serverdef` (the example files round trip
byte for byte, defaults, unknown members, a WebSocket server saved
before auto-reply rules), `internal/core/servers`
(`TestRedefine`), `internal/app/services/collection/servers_test.go`
(written at once beside a pending settings edit, reload, read-only),
`internal/app/services/servers/project_test.go` (a saved mock server
starts from the file and serves its routes, edits are saved, a restart
brings the same definition back and nothing listens; a hand edit is what
starts; create and remove; project switch; problems), and in the frontend
`FolderServerTab.test.tsx`, `useServersSync.test.tsx`, the tree badge in
`CollectionTree.test.tsx` and the grouped list in `Servers.test.tsx`.

## UI

- **Servers view.** A rail button (server icon) opens the Servers sidebar
  view. Saved server configs live in a folder's settings
  ([Saved server configs](#saved-server-configs)); the view lists the open
  project's saved servers first, under "In this project" with their
  folder, then "This session". Each server row shows a status dot (running green,
  starting and stopping amber, failed red), name and state, the address
  (the first bound address while running, click to copy; the defined
  host and port otherwise), the LAN warning icon, the uptime ticking every
  second, and a start/stop button. A failed server shows its error.
- **New server.** The `+` button opens an inline form (no dialog component
  yet): the kind of server (Mock HTTP, the default, Echo HTTP, WebSocket, TCP, Webhook receiver, SSE, UDP, gRPC mock, Socket.IO or GraphQL mock), host
  (default `localhost`), port (empty for any free port) and "Allow
  connections from other devices (LAN)", which shows a warning while
  checked. A new mock server gets Go's default routes (`GET /hello`). Start creates the server, selects it and starts it. A server
  that cannot be created (bad host or port) keeps the form open with the
  error; one that cannot start stays in the list as failed.
- **TLS** ([TLS](#tls)). The new-server form, the Server tab of the
  server view and the folder's Server tab have "Serve over TLS (HTTPS)"
  (`wss` for a WebSocket server, `TLS` for a TCP server), then
  "Generated for localhost" (the default) or "My certificate": PEM
  certificate and key, or a PKCS#12 file, each with a path field and
  "Choose…" (the native dialog), and a passphrase field (password; "Stored
  (type to replace)" when one is kept). With the generated certificate the
  local CA shows: its `ca.pem` path (Copy path, Show file), "Trust in
  PostSonde" (or a note that PostSonde trusts it), and a line on trusting
  it elsewhere. On an existing server, turning TLS off or on with the
  generated certificate is saved at once; file choices wait for "Apply
  TLS settings" (Revert drops them), since the files are read when saved
  and a half-chosen pair would fail. Everything is locked while the server
  holds its port ("Stop the server to change its TLS settings"). A TLS
  server has a lock badge (HTTPS, wss, TLS) next to its address; the
  details list its certificate (subject, issuer, names, valid until,
  SHA-256) while it runs; the WebSocket and webhook URLs use `wss://` and
  `https://`, and the TCP pane suggests `openssl s_client -connect`.
- **Server view.** Selecting a server shows it in the work area: details
  (status, address, all bound addresses, reachable from, uptime, events,
  the LAN warning or the failure with its hint; Start or Stop, Remove,
  Close) on the left and its traffic in the message log on the right. It
  closes with Close, when the server is removed, or when a tab is
  activated. A mock server's left pane has two tabs, **Routes** (the
  routes editor, [frontend.md](frontend.md#routes-editor)) and **Server**
  (the details), with its name and Close next to them; the traffic stays on
  the right, so requests show up while routes are edited.
  A WebSocket server's left pane has **Clients**, **Settings** and
  **Server** ([frontend.md](frontend.md#websocket-server-view)): the
  connected clients with Disconnect (close code and reason) and the shared
  message composer (Text/JSON, Send to the selected client with ⌘Enter,
  Send to all); the Settings tab holds echo and the auto-reply rules;
  the traffic on the right shows each message with its
  client (`meta.client`, and on a close `close code`, `close reason`,
  `closed by`).
  A TCP server's left pane has **Connections** and **Server**
  ([frontend.md](frontend.md#tcp-server-view)): the address with a
  netcat command, the open connections with Close, and the composer
  (Text or Hex, a line ending for text, Send to the selected connection
  with ⌘Enter). Its traffic can be shown as text or hex ("Show as" above
  the log); a close shows `closed by`, `bytes received` and `bytes sent`.
  An SSE server's left pane has **Subscribers**, **Settings** and
  **Server** ([frontend.md](frontend.md#sse-server-view)): the URL with a
  line that opens it with `EventSource`, the subscribers with their
  Last-Event-ID and Disconnect, and the event composer (type, ID, retry,
  data; Send to all with ⌘Enter, Send to the selected subscriber); the
  Settings tab holds the path, CORS, keep-alive, retry, automatic IDs and
  the timers. A UDP server's left pane has **Peers**, **Replies** and
  **Server** ([frontend.md](frontend.md#udp-server-view)): the address
  with an `nc -u` command, the recent peers, the composer (Text or Hex,
  line ending) and the echo and fixed reply; its traffic can be shown as
  text or hex. A UDP server has no TLS fields.
  A gRPC mock server's left pane has **Methods** and **Server**
  ([frontend.md](frontend.md#grpc-mock-server-view)): the address with a
  `grpcurl … list` line, the `.proto` files and import paths (Add
  .proto files…, Add import path… and Reload, through the gRPC service's
  dialogs), server reflection on or off, the methods the files define
  with "canned response" or "UNIMPLEMENTED", and the selected method's
  response: Mock this method (a generated example of the output type),
  the status and its message, the delay, for streams the interval and
  the messages, for bidi the mode, headers and trailers, and Remove
  canned response. Its traffic badges a call CALL and its end with the
  status name, with the method, type, status and trailers in the detail
  pane.
  A Socket.IO server's left pane has **Sockets**, **Settings** and
  **Server** ([frontend.md](frontend.md#socketio-server-view)): the URL
  with an `io(…)` line for the official client, the sockets (one per
  client and namespace) with their transport, rooms and auth, Rooms…,
  Disconnect and Close client, and the emit composer (event, Ack, room,
  JSON arguments; Emit to the selected socket with ⌘Enter, to all, or to
  the room; when the selected socket disconnects, Emit to socket waits
  for another to be selected rather than reach a different client); the Settings tab holds the path, namespaces, required auth,
  transports, CORS, heartbeat, largest payload, the ack mode, echo and
  the auto-reply rules. Its traffic badges an event with its name and
  other packets with their type.
  A GraphQL mock server's left pane has **Schema**, **Responses** and
  **Server** ([frontend.md](frontend.md#graphql-mock-server-view)): the
  endpoint URL, the schema from a file (Choose file…, Reload) or pasted
  SDL or introspection JSON with what Go loaded from it (types, root
  fields, problems, or why it does not load), the path, introspection on
  or off and CORS; the Responses tab holds the generated-data settings
  (list length, null %, seed) and the overrides, one row each: enabled,
  the target (suggested from the schema), Value or Error, Move up, Move
  down, Remove, the arguments to match, and the JSON value or the error's
  message and code. Its traffic badges a request with its operation type
  (QUERY, MUTATION), with the operation, variables, errors and data in the
  detail pane.
  A webhook receiver's left pane has **Requests**, **Response** and
  **Server** ([frontend.md](frontend.md#webhook-receiver-view)): the URL
  to send to, the kept requests newest first, and the selected one in
  full (Body with the response body viewer, Headers, Query, and Replay:
  a URL, Replay, and the replay's response in a response view). The
  traffic on the right shows every request and response as usual.
- **Traffic.** `followServers` (run by `useServersSync` in the project
  shell) subscribes to both events, loads the list, the types and each
  server's recent traffic, then applies live traffic that arrived during the
  load. `TrafficSources` keeps one `MessageSource` per server and applies
  each `seq` once and in order, so the recent buffer and live batches can
  overlap. Mapping (`trafficToMessage`): `in` is received, `out` sent,
  `system` system; kinds `connection_open`/`connection_close` become
  `connected`/`closed`, a request's badge is its method and a response's
  its status; the payload is the content, or the summary when there is
  none; summary, peer, connection, request line, protocol, the mock route
  (`route: GET /users/:id`, `params.id`, `wildcard` when not a named
  parameter, `response: not-found` when the route has other responses or
  the request picked one, `delay: 250 ms`), "truncated" and the headers go into `meta`
  for the detail pane. The log's Clear also
  calls `ClearTraffic`.
- Servers keep running when the project closes, and reopening the project
  shows them again with their recent traffic. Stopped saved servers follow
  their project ([Saved server configs](#saved-server-configs)).

Not yet: for the GraphQL mock server: subscriptions (over SSE or
`graphql-transport-ws`), `@defer` and `@stream` answered incrementally
(a schema that declares them has them executed inline), batched requests (a JSON array), persisted
queries, file uploads (multipart), OverlappingFieldsCanBeMerged (the
validator does not have it yet: the first field of a response key gives
its arguments), dynamic values or request data in overrides, a delay, a
response chosen by a request header, and federation (`_entities`,
`_service`); for the Socket.IO server: Engine.IO 3 clients (socket.io v2,
`allowEIO3`), the msgpack parser, connection state recovery, rooms joined
by the client's own events, rules that look at the arguments, server-side
middleware other than the required auth, and WebTransport; for the gRPC mock server: a response chosen by the request
message or metadata, dynamic values in canned messages, `grpc-status-details-bin`
(rich error details), compressed responses, CORS for gRPC-Web from browser
pages, a health service, reflection without files (serving descriptors
fetched from another server); for the SSE server: replaying missed events to a subscriber
that comes back with `Last-Event-ID`, named events per path (several
endpoints), sending an event from a saved request, changing the keep-alive
of subscribers already connected; for the UDP server: DTLS, multicast and
broadcast, sending to an address that has not written first, framing
several messages in one datagram; for the webhook receiver: saving a kept request as a request
in the collection, replaying with the original `Host`, a copy-as-cURL of
a kept request, restricting it to some paths, for the TCP server: an echo option, sending to all
connections, half-closed connections and message framing (as TCP-03), binary messages from the WebSocket server's composer
(the service takes them), server pings, auto-reply rules for binary messages,
editing the host or port of a server made in the
Servers view (`UpdateServer` takes it; only the folder's Server tab offers
it), mutual TLS (asking clients for a certificate), a choice of TLS versions
or HTTP/2 on the local servers, project-relative certificate paths, the path in the row preview of a request that has a body (it is in
the detail pane).
