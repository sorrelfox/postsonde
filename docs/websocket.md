# WebSocket

PostSonde speaks WebSocket (RFC 6455) as a client, as a local mock server and
for GraphQL subscriptions. All three share one framing implementation,
`internal/core/realtime/wsframe`, written with the standard library only.
The client is `internal/core/realtime/wsclient`, used by the
[`websocket` request kind](#request-kind-websocket); the server comes later.

```
wsclient.Client
  netx.Dialer ──▶ net.Conn ──▶ TLS (wss) ──▶ HTTP upgrade ──▶ wsframe.Conn ──▶ OnMessage
                                             (by hand)            │
                                                Options.OnFrame ──┴─▶ frame inspector
```

`wsframe` never dials and never closes the transport. Connections come from
a `netx.Dialer` ([network.md](network.md)); the upgrade handshake
(`Sec-WebSocket-Key`/`Accept`, subprotocols, extension negotiation) is done
by `wsclient`. `ComputeAccept(key)` is the only handshake helper in
`wsframe`.

## Client: `wsclient`

WS-01 (connect `ws://` / `wss://`, state shown), WS-08 (close code and
reason) and WS-09 (pings with round-trip times, keepalive). One `Client` is
one connection attempt; reconnecting (WS-10) creates a new one, which the
service does ([Keepalive and reconnect](#keepalive-and-reconnect)).

```go
c, err := wsclient.New(wsclient.Options{
	URL:          "wss://api.example.com/socket",
	Dialer:       dialer,              // netx.Dialer, required
	Headers:      rows,                // []httpclient.Header, as in the HTTP editor
	Subprotocols: []string{"graphql-transport-ws"},
	Jar:          store.Jar(),         // optional, docs/cookies.md
	OnState:      func(ch wsclient.StateChange) { ... },
	OnMessage:    func(m wsclient.Message) { ... },
	OnFrame:      func(dir wsframe.Direction, f wsframe.Frame) { ... },
	OnPingPong:   func(p wsclient.PingPong) { ... }, // pings and pongs, with RTT
	PingInterval: 30 * time.Second,    // keepalive; 0: none
	PongTimeout:  10 * time.Second,    // no pong in time: wsclient.pong_timeout
})                                     // validation errors before anything is sent
err = c.Connect(ctx)                   // ctx bounds the handshake only
err = c.SendText("hello")              // Send(op, data), SendBinary, Ping
err = c.Close(ctx, 4000, "done")       // waits for the server's close frame
info := c.CloseInfo()                  // code, reason, clean, who, frames
```

| Item | Purpose |
|---|---|
| `New(Options) (*Client, error)` | Validates URL, headers and subprotocols. Panics without a `Dialer`. |
| `Options{URL, Headers, AllowHandshakeHeaders, Subprotocols, Dialer, RootCAs, InsecureSkipVerify, ClientCertificate, Jar, HandshakeTimeout, CloseLinger, MaxResponseBody, Limits, MaxMessageSize, DisableAutoPong, OnState, OnMessage, OnFrame, OnPingPong, PingInterval, PongTimeout, Clock, Rand, Now}` | See below. |
| `Connect(ctx)` | Dial, TLS, upgrade; then `open` and a read goroutine. Once per client. |
| `Send(op, data)`, `SendText`, `SendBinary`, `Ping(p)` | Concurrency-safe writes (wsframe's locking). `wsclient.not_open` unless open. `Ping` reports the ping to `OnPingPong` before writing it and matches its pong ([Pings](#pings-and-keepalive)). |
| `Close(ctx, code, reason)` | Close handshake with a chosen code; below. |
| `Abort()` | Close the transport without a handshake (1006, not a failure). |
| `State()`, `Done()`, `Err()`, `CloseInfo()`, `Handshake()`, `Subprotocol()`, `URL()` | State for the UI. |
| `State` | `idle`, `connecting`, `open`, `closing`, `closed`. |
| `StateChange{State, At, Close, Err}` | Passed to `OnState`, in order, synchronously. `Close` is set on `closed`; `Err` when the connection failed. |
| `CloseInfo{Code, Reason, WasClean, InitiatedBy, Sent, Received}`, `CloseFrame{Code, Reason}`, `Initiator` (`none`, `local`, `remote`) | How it ended. |
| `PingPong{Direction, Opcode, Payload, At, Keepalive, Answered, RTT}` | A ping or pong sent or received, passed to `OnPingPong`. |
| `Clock` (`Now`, `AfterFunc`), `SystemClock()` | The keepalive timers; the method set of `autosave.Clock`, so tests pass `internal/testutil/fakeclock`. |
| `Handshake{URL, RequestHeaders, RawRequest, RequestCookie, Response, Subprotocol, Timing, Conn, Events}`, `Response{Proto, StatusCode, Status, Headers, RawHeader, Body, BodyTruncated}` | The inspector's view of the upgrade, for failed attempts too. `Timing`, `Conn` and `Events` are `httpclient` types (`httpclient.NewTLSInfo` fills `Conn.TLS`). |

The structs are plain values with string enums, ready to be mapped to DTOs
by `WebSocketService` ([below](#service-websocketservice)).

### Handshake

The request is written by hand on the dialed connection (not `net/http`,
which cannot hand the connection over with the bytes it buffered), so it is
byte for byte what the inspector shows:

```
GET /path?query HTTP/1.1          path and query as typed (control, space, non-ASCII → %XX)
Host: api.example.com             a Host row replaces it
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Key: <16 random bytes, base64>
Sec-WebSocket-Version: 13
Sec-WebSocket-Protocol: a, b      only with Subprotocols
Authorization: Basic …            only from URL userinfo, unless a row sets Authorization
<enabled rows, in order, as spelled, duplicates kept, values trimmed>
Cookie: <jar cookies>             or appended to the first Cookie row; a row's name wins
```

- **URL:** `ws` or `wss` in any case; anything else (also `http`) is
  `wsclient.scheme_unsupported`. A fragment is an error (RFC 6455 §3).
  Default ports 80 and 443.
- **No Origin** by default: PostSonde is not a browser. Add an `Origin` row
  to test a server's origin check.
- **Reserved headers:** rows for `Upgrade`, `Connection`,
  `Sec-WebSocket-Key`, `-Version`, `-Protocol`, `-Extensions`, `-Accept`,
  `Content-Length` and `Transfer-Encoding` are `wsclient.reserved_header`.
  With `AllowHandshakeHeaders` they replace the generated header instead,
  to test how a server handles a broken handshake; a replaced key is still
  what `Sec-WebSocket-Accept` is checked against.
- **TLS (wss):** TLS 1.2 or newer, certificates verified against the system
  roots or `RootCAs` unless `InsecureSkipVerify`, SNI from the URL host,
  ALPN `http/1.1` only (WebSocket over HTTP/2, RFC 8441, is not supported).
  Same conventions as `httpclient.NewTransport`. The app's service sets
  `RootCAs` from the custom CA bundles (NET-04, `WithTrust`, read on every
  connect, [network.md](network.md#custom-ca-certificates)); a bundle that
  can no longer be read fails a wss `Connect` at once, not a ws one.
- **Client certificate (mTLS, AUTH-30):** `ClientCertificate`
  (`*httpclient.ClientCert`) is sent whenever the server asks, and the
  request adds a `client_cert` event to the handshake timeline (the
  certificate's name, or "requested by the server; none is set for this
  host"). The app's service picks it from the per-host client
  certificates (`WithClientCerts`, `httpclient.ClientCertForURL` on the
  rendered URL, wss only; GraphQL subscriptions do the same,
  [network.md](network.md#client-certificates)); a matching entry that
  cannot be loaded fails the `Connect` at once. A server that requires a
  certificate and gets none fails the handshake with
  `netx.tls_client_cert_required` (TLS 1.3) or
  `netx.tls_client_cert_rejected` (TLS 1.2), however the alert raced with
  the request write and the first read: once the server asked for a
  certificate, a reset, broken pipe or EOF before the response counts too
  (`netx.ClassifyClientCert`, [network.md](network.md#connection-and-tls-failures)).
- **Cookies:** with `Jar`, the jar's cookies for the `ws`/`wss` URL are sent
  (`wss` and loopback are secure channels) and the `Set-Cookie` headers of
  the response are stored, for a 101 and for a rejected upgrade alike.
  `cookies_sent` / `cookies_stored` events carry names only.
- **Response checks (§4.1):** status 101, `Upgrade` contains `websocket`,
  `Connection` contains `upgrade` (token lists, any case), exactly one
  `Sec-WebSocket-Accept` equal to `ComputeAccept(key)`, a
  `Sec-WebSocket-Protocol` that is one of those offered (exact match, one
  value; none is fine), and no `Sec-WebSocket-Extensions` (none are
  offered). Response headers are kept in the order received, as spelled.
- **Leftover bytes:** the status line and headers are read from a
  `bufio.Reader` up to the empty line; that same reader becomes
  `wsframe.Options.Reader`, so frames sent right behind the 101 are not
  lost.
- **Rejected upgrade:** any other status fails with
  `wsclient.upgrade_rejected` (detail `status`); `Handshake().Response` has
  the status, headers and the body, framed as HTTP and cut at
  `MaxResponseBody` (1 MiB). Redirects are not followed; the message names
  the `Location`.
- **Timeout:** `HandshakeTimeout` (30 s) bounds dial, TLS and upgrade
  together, as a context cause, so it is told apart from the caller's
  cancellation.
- **Timing:** `httpclient.Timing` with `DNS` and `Connect` (httptrace hooks
  fired by `netx.Direct`), `TLS`, `RequestWrite` (the upgrade request),
  `Wait` (to the first response byte) and `Download` (response headers, and
  the body of a rejected upgrade). Events use the `httpclient.Event*` names
  plus `upgrade_response`.

### State and closing

```
idle ──Connect──▶ connecting ──101 ok──▶ open ──close frame sent/received──▶ closing ──▶ closed
                      │                    │                                              ▲
                      └── handshake error ─┴── transport drop, protocol error, Abort ─────┘
```

- **Close from the client:** `Close(ctx, code, reason)` validates like
  wsframe (codes that may be sent, reason UTF-8 ≤ 123 bytes, `1005` with no
  reason sends an empty close frame), moves to `closing`, sends the close
  frame (`WriteClose`) and waits for `closed`. The read goroutine sees the
  server's close frame; then the client waits up to `CloseLinger` (1 s)
  for the server to close TCP (§7.1.1) and closes it. If `ctx` ends first,
  the transport is closed and the connection fails with
  `wsclient.close_timeout`. `Close` on a closed client is a no-op.
- **Close from the server:** the close frame is echoed with its status code
  (wsframe), the state goes `closing` then `closed`.
- **`CloseInfo`:** `Code`/`Reason` are the server's close frame (1005 for an
  empty one), or **1006** when none arrived; 1006 is synthesized, never
  sent. `WasClean` means close frames went both ways. `InitiatedBy` is the
  side whose close frame went first (`local` also for `Abort`, and for a
  protocol error in what the server sent, where wsframe sends 1002/1007/1009
  and the connection fails); `none` when the transport just dropped or the
  handshake failed. `Sent`/`Received` are the close frames themselves,
  recorded from the frame hook.
- **Failures** (`StateChange.Err`, `Err()`): handshake errors, a dropped
  transport (`wsframe.abnormal_closure`, `unexpected_eof`, `read_failed`),
  protocol errors from the server, `close_timeout`. A completed close
  handshake and `Abort` are not failures.
- **Callbacks:** `OnState` runs synchronously on the goroutine making the
  change, serialized, so the order is exact; it must not call `Connect`,
  `Close` or `Abort`. `OnMessage` and received `OnFrame` calls run on the
  read goroutine; reading waits for them (backpressure, not buffering).
- **Writes** have no timeout of their own; a write blocked on a stuck
  server is released by `Abort` (or by `Close`'s context).

### Pings and keepalive

WS-09. Every ping and pong goes through `OnPingPong` (as well as
`OnFrame`):

- **Sent pings** (`Ping(payload)`, at most 125 bytes, and keepalive pings)
  are reported just before they are written, so their pong can never be
  reported first, and remembered (the last 64) to match pongs.
- **Received pongs** are matched to the oldest remembered ping with the
  same payload: `Answered` is set, `RTT` is the time since that ping (with
  the client's `Now`), and `Keepalive` says whether it was a keepalive
  ping; the pings before it are forgotten too (a server may answer only
  the last one, RFC 6455 §5.5.3). A pong that matches nothing (an
  unsolicited heartbeat) is reported with `Answered` false.
- **Received pings** and the automatic pong wsframe sends back are
  reported as they are read and written.
- **Keepalive:** with `PingInterval`, the client sends a ping with an
  empty payload `PingInterval` after opening and then every
  `PingInterval` while open. With `PongTimeout`, the first unanswered
  keepalive ping starts a deadline (later pings do not push it back); any
  pong clears it. When it passes, the transport is closed and the
  connection fails with `wsclient.pong_timeout` (kind `timeout`, message
  "The server did not answer a keepalive ping within 10 s; the connection
  was closed.", `details.timeoutMs`), 1006, initiated `local`. The timers
  stop when the connection ends. `Clock` runs them; tests use a fake one.

### Client errors

| Code | Kind | When |
|---|---|---|
| `wsclient.invalid_url`, `wsclient.scheme_unsupported` | `invalid_argument` | `New`: URL without host, bad port, fragment; not ws/wss. |
| `wsclient.invalid_header`, `wsclient.reserved_header`, `wsclient.invalid_subprotocol` | `invalid_argument` | `New`: bad rows (detail `index`), reserved names, bad or duplicate subprotocols. |
| `wsclient.already_connected`, `wsclient.not_open` | `invalid_argument` | Second `Connect`; send or close when not open. |
| `netx.*` | from netx | Dial errors, unchanged. |
| `wsclient.cancelled`, `wsclient.timeout` | `cancelled`, `timeout` | `Connect`'s context, `HandshakeTimeout`, I/O timeout. |
| `netx.tls_unknown_authority`, `tls_hostname_mismatch`, `tls_certificate_expired`, `tls_certificate_invalid`, `tls_server_not_tls`, `tls_handshake_failed`, `tls_client_cert_required`, `tls_client_cert_rejected` | `tls` | Shared with every protocol client (`netx.Classify`, [network.md](network.md#connection-and-tls-failures)), with the netx hints; a plain server gets "Use a ws:// URL". |
| `netx.connection_reset` | `network` | The server reset the connection during the upgrade, or later while open (`wsframe` read or write, message "The WebSocket connection was reset by the other side."). |
| `wsclient.connection_closed`, `wsclient.connection_failed` | `network` | Closed before a response; other I/O errors. |
| `wsclient.malformed_response` | `protocol` | Not HTTP, folded headers, headers over 64 KiB. |
| `wsclient.upgrade_rejected` | `protocol` | Status other than 101 (response kept). |
| `wsclient.invalid_upgrade`, `invalid_accept`, `unrequested_subprotocol`, `unrequested_extension` | `protocol` | A 101 that fails the checks above. |
| `wsclient.close_timeout` | `timeout` / `cancelled` | `Close`'s context ended before the server's close frame. |
| `wsclient.pong_timeout` | `timeout` | No pong within `PongTimeout` of a keepalive ping (`details.timeoutMs`). |

Messages name `host:port`, never the path or query.

### Client tests

`go test -race ./internal/core/realtime/wsclient`, against `httptest`
servers that upgrade with wsframe in the server role:

- ws and wss (test certificate trusted through `RootCAs`): request headers,
  echo, close with a chosen code seen by the server, state order
  `connecting,open,closing,closed`, handshake timing and TLS info.
- Close codes 1000, 1001, 1008, 3000, 4999 and an empty close; invalid
  codes and reasons rejected; server-initiated close (code and reason
  reported, echo sent); dropped transport ⇒ 1006; `Abort`; close timeout;
  a masked frame from the server ⇒ 1002 sent and a failure.
- Handshake failures: wrong or missing accept, missing Upgrade/Connection,
  unrequested or multiple subprotocols, extensions, non-HTTP, cut
  responses, 403 with capped body and stored cookie, redirects, untrusted
  certificate, TLS to a plain server, timeout and cancellation, refused
  dial.
- The exact bytes on the wire (row order, spelling, duplicates, userinfo,
  path as typed), `AllowHandshakeHeaders`, cookies from a `cookies.Open`
  store sent and stored, a frame arriving with the 101, concurrent sends
  and pings.
- Pings (`keepalive_test.go`): a manual ping's pong with its RTT, an empty
  payload, a payload over 125 bytes refused; a server's ping and the
  automatic pong; an unsolicited pong; keepalive answered for several
  rounds on a fake clock (the deadline cleared, no timers left after
  close); a server that stops answering pongs closed at the deadline set
  by the first unanswered ping with `pong_timeout`; keepalive without a
  timeout never giving up.

## Request kind: `websocket`

A saved WebSocket request (WS-01…WS-04, WS-06…WS-11) opens in the
WebSocket editor, connects with its handshake options, sends text or JSON
messages and binary messages typed as hex or Base64 or loaded from a file,
typed or saved with the request, and shows what comes back in the message
log, binary messages as a hexdump or Base64. `{{variables}}` in the URL,
headers, subprotocols and text messages are rendered by Go on connect and
on every send.

```
WebSocketEditor ─ commands/wsSessions ─ lib/websocket ──▶ WebSocketService.Connect/Send/Close
   ▲                    │ MessageSource                     │ cookie jar of the open project
   │                    ◀── ws:state, ws:message ───────────┤ stream.Batcher per connection
MessageLog                                                  ▼
                                               wsclient.Client ── netx.Dialer ──▶ server
```

| Piece | Where |
|---|---|
| File format (kind `websocket`) | [file-format.md](file-format.md#websocket-request-kind-websocket), `internal/core/storage/projectfile` (`WebSocketRequest`) |
| Bound service and events | `internal/app/services/websocket` (`WebSocketService`) |
| Binding wrappers, `connectInput(request)` | `frontend/src/lib/websocket.ts` |
| Sessions (connection state and log per request) | `frontend/src/commands/wsSessions.ts`, `useWsSession.ts` |
| Editor | `frontend/src/components/editor/WebSocketEditor.tsx`, `SavedMessages.tsx` |
| Variables | `internal/app/services/websocket/variables.go` (`WithScopes`), wired in `internal/app/services.go` with `collection.LoadVariablesSource` and `http.ScopeChain` |

### The request file

A `websocket` request stores the `url` (query included), the `params`
rows kept in step with it as for `http`, `headers` rows, `subprotocols`,
the composer's `message` and `messageFormat` (`text`, `json`, `hex` or
`base64`), the `savedMessages` (`name`, `text`, `format`, the same four
formats; WS-07) and
`settings.connectTimeoutMs`, `verifyTls` and `sendCookies`, and the
keepalive and reconnect settings `pingIntervalMs`, `pongTimeoutMs`,
`autoReconnect`, `reconnectMaxAttempts`, `reconnectDelayMs` and
`reconnectMaxDelayMs` (WS-09, WS-10; all left out when not set, so older
files connect as before). Everything is stored with `{{variables}}` as
typed. Files written before saved messages
existed have no `savedMessages` member and read as having none. The editor
auto-saves every change through `CollectionService.UpdateRequest`
([collection.md](collection.md#auto-save)); `CreateRequest` accepts kind
`websocket`, offered by the New request dialog
([frontend.md](frontend.md#new-folder-and-new-request)). `RequestDTO`
carries `subprotocols`, `messageFormat`, `savedMessages` (`SavedMessageDTO`
`{name, text, format, extra}`; unlike the other lists it is left out when
empty) and `settings.sendCookies` for it and reuses `url`, `params`,
`headers`, `message`, `settings.verifyTls` and
`settings.connectTimeoutMs`; the keepalive and reconnect settings are
members of `RequestSettingsDTO` left out when null. The request's
`auth` (AUTH-31) is a [shared auth object](file-format.md#auth) carried in
`RequestDTO.auth`, as for HTTP ([Auth](#auth)).

The binary formats are backwards compatible: `format` and `messageFormat`
were free strings from the start, a version that does not know `hex` or
`base64` keeps them as written (and would offer the text as a text
message), and older files simply have no binary messages.

### Service: `WebSocketService`

| Method | Does |
|---|---|
| `Connect({requestId, url, headers, subprotocols, connectTimeoutMs, verifyTls, sendCookies, pingIntervalMs, pongTimeoutMs, autoReconnect, reconnectMaxAttempts, reconnectDelayMs, reconnectMaxDelayMs, allowUnresolved})` | Renders the `{{variables}}` of the URL, the enabled header rows and the subprotocols ([Variables](#variables)), validates (wsclient's URL, header and subprotocol checks), then returns `{connId, requestId, url}` (the rendered URL) at once and connects in the background. `headers` are `{name, value, enabled}` rows; disabled rows are skipped. `connectTimeoutMs` bounds dial, TLS and the upgrade (`null`: 30 s, `0`: none). `verifyTls: false` sets `InsecureSkipVerify`; otherwise wss trusts the custom CA bundles too (NET-04). The keepalive and reconnect members are optional ([Keepalive and reconnect](#keepalive-and-reconnect)). |
| `Send({connId, text, format, allowUnresolved})` | One message. `format` `""`/`text` or `json`: a text message (opcode 0x1), its `{{variables}}` rendered as they are now. `hex` or `base64` (WS-04): one binary message (opcode 0x2) of the bytes `text` spells, sent as written, no variables ([Binary messages](#binary-messages)). Returns `{bytes}` of what was sent. `ws.invalid_format` for another format, `ws.not_found` for a closed or unknown connection, `ws.not_open` while connecting or closing. The message is queued as a sent message before it is written, under a per-connection lock, so it always precedes its reply and sends keep their order. |
| `Ping({connId, payload, format})` | WS-09. A ping whose payload is `payload` as text (no variables) or, with `format` `hex` or `base64`, the bytes it spells; at most 125 bytes (`ws.ping_too_long`, `details.bytes`). Returns `{bytes}`. The ping and its pong arrive in `ws:message`. `ws.not_found` / `ws.not_open` as for `Send`. |
| `Close({connId, code, reason})` | `code` 0 means 1000. Also stops waiting to reconnect; a closed connection is never reconnected. An invalid code or reason (1005 with a reason, 1006, 1015, over 123 bytes…) fails at once with `wsframe.invalid_argument`. While connecting it cancels the handshake (the closed event then says `initiatedBy: local` with the cancel error); when open it runs the close handshake with five seconds for the server's answer (else `wsclient.close_timeout`). Waits until closed and the last events are sent. Unknown IDs do nothing. |
| `CloseAll()` | Closes every connection with 1001 (going away); returns how many. |

- **Cookies.** Unless `sendCookies` is false, the handshake uses the cookie
  jar HTTP sends use: `websocket.WorkspaceJars(cookieJars)` over the
  app's `*appcookies.Jars` created in `internal/app/services.go`, which
  picks the open project's jar or, with separate cookies per environment
  (CK-03), the active environment's ([cookies.md](cookies.md#jars-per-workspace-and-environment)).
  The jar is looked up by `Connect`; the connection and its automatic
  reconnects keep that jar when the environment changes later. Cookies are sent
  (a `Cookie` row's name wins) and the `Set-Cookie` headers of the upgrade
  response are stored.
  Without an open project, or when the jar cannot be opened, the
  connection goes ahead without cookies and a warning is logged.
- **Workspace change and shutdown.** `OnWorkspaceChanged(s)` (called from
  the wrapped workspace emitter in `services.go`, next to the TCP one) and
  `ServiceShutdown` close every connection with 1001, waiting up to two
  seconds, then save and forget the cookie jars.
- **Logs** name the scheme and host only; paths and queries may hold tokens.

#### Auth

The request's auth (AUTH-31), its own or its closest folder's, goes with
the upgrade request of every attempt, reconnects included
([auth.md](auth.md#auth-on-every-protocol-auth-31)): `WithAuth` gets it
from the app (`httpservice.HandshakeAuth`), with its fields rendered by the
connect's own variables rendering. Its headers go after the header rows,
unless an enabled row has the same name (a typed `Authorization` wins);
its query parameters go at the end of the URL unless the URL has one of
that name. Basic, Bearer, API key, JWT, Netrc and OAuth 2.0 (the stored
token, else one got as an HTTP send gets it) are sent; Digest, OAuth 1.0,
AWS Signature and Hawk refuse the connect with
`request.auth_not_applicable`. The auth's values are masked in the
handshake's URL and request headers and in the connection URL of the
events (`ConnectionDTO.url`, `ws:state`). A connect without a request ID
sends no auth.

#### Keepalive and reconnect

WS-09 and WS-10, set per request (`settings`), passed by the editor in
`Connect`; `reconnect.go`.

- **Keepalive.** `pingIntervalMs` (null or 0: none; less than 100 is
  raised to 100) and `pongTimeoutMs` (null: 10 s; 0: never give up) become
  wsclient's `PingInterval` and `PongTimeout`. A late pong fails the
  attempt with `wsclient.pong_timeout`, which auto-reconnect treats as a
  drop.
- **One ID, several attempts.** With `autoReconnect`, a connection keeps
  its `connId` across attempts; each attempt is a new `wsclient.Client`.
  `ws:state` carries `attempt` (1 for the first). The `{{variables}}` of
  the URL, headers and subprotocols are rendered again for every attempt,
  with the scopes as they are then (switching the environment changes the
  next attempt); a reconnect that no longer renders (or whose options fail,
  e.g. a CA bundle gone) ends the connection with a `closed` event carrying
  the error and `reconnect.stopped: "error"`. The dial of a reconnect is
  a user operation of kind `ws.reconnect` (PRV-05).
- **When.** After an attempt ends: not after the user closed it (or closed
  while connecting); again after a failure of kind `network` or `timeout`
  (a dropped transport, a refused or timed-out dial, a reset, a missing
  pong), or when the server closed with 1001 (going away), 1011, 1012
  (service restart), 1013 (try again later) or 1014; not after any other
  close code (1000, 1002, 1003, 1005, 1007–1010, 3000–4999: the server
  meant it) or another failure (TLS, a rejected upgrade, a protocol error).
- **Back-off.** Reconnect *n* waits `reconnectDelayMs` × 2^(n−1) (null:
  1000; at least 100), capped at `reconnectMaxDelayMs` (null: 30000). No
  jitter: one client does not stampede a server. `reconnectMaxAttempts`
  (null: 5; 0: no limit) bounds the reconnects in a row; the count starts
  over once an attempt opens.
- **Events.** Each attempt sends its own `connecting`, `open`, `closing`
  and `closed`. With auto-reconnect on, `closed` has `reconnect: {attempt,
  maxAttempts, delayMs, stopped}`: `stopped: ""` means reconnect number
  `attempt` follows after `delayMs`; otherwise it says why not (`manual`,
  `close_code`, `error`, `max_attempts`). Without auto-reconnect it is
  null. Closing while waiting stops the wait and sends one more `closed`
  with `close: null` and `reconnect.stopped: "manual"`.
- **Messages and counts.** `Send` and `Ping` while waiting fail with
  `ws.not_open`. The counts in `close` are those of the whole connection,
  all attempts.
- **Clock.** The back-off waits and the keepalive timers use one clock,
  the real one unless a test passes `withClock(fakeclock)`.

#### Variables

WS-11. `New(..., WithScopes(scopes))` gives the service the scopes of a
request: `services.go` passes `collection.LoadVariablesSource` +
`http.ScopeChain`, the chain an HTTP send renders with (global variables,
the active environment, the folders from the top down;
[templates.md](templates.md), [environments.md](environments.md)).

- **Connect** renders the URL, the name and value of every enabled header
  row (a disabled row is not sent, so its variables do not matter) and each
  subprotocol with the scopes of `requestId`.
- **Send** renders the message with the scopes of the connection's request,
  loaded again for every send: switching the environment while connected
  changes the next message. The log's sent message is the rendered text,
  what went over the wire.
- **Undefined names** of every field are collected and reported together
  as one `template.unresolved` error (`details.names`) before any other
  error; with `allowUnresolved` they are used as written instead (the
  URL then usually fails wsclient's checks). Other template problems
  (`template.cycle`, `template.empty_name`, …) always refuse.
- **Dynamic values** (`{{$uuid}}`, `{{$timestamp}}`, …) are built into
  the resolver: each reference gets a fresh value on every connect and
  send, and the chips show them as resolved (scope "Dynamic").
- Without `WithScopes`, or for an empty `requestId`, no variable is
  defined (dynamic values still are). A request that is no longer in the project fails with
  `collection.not_found`.
- **Binary messages** (`hex`, `base64`) are not rendered: braces are not
  hex digits or Base64 characters, so a `{{name}}` there could only ever
  be an error, and substituting text into bytes has no single meaning.

#### Binary messages

WS-04. `binary.go` turns the text of a `hex` or `base64` message into the
bytes of one binary message:

- **Hex** uses `internal/core/realtime/hexbytes` exactly as the TCP views
  do ([tcp.md](tcp.md)): pairs of digits, groups separated by whitespace,
  commas, colons or semicolons, `0x` and `\x` prefixes. Errors are
  `hexbytes.*` with `details.offset`.
- **Base64** (`parseBase64`) accepts what people paste: the standard or
  the URL alphabet (`+/` or `-_`, even mixed), whitespace and line breaks
  anywhere, `=` padding optional but only at the end and only as much as
  the length allows. Errors are `ws.invalid_base64` with `details.offset`
  (the offending character, counted in characters from 0) and a hint.
  `frontend/src/lib/base64Bytes.ts` follows the same rules while typing;
  the two have the same test cases.
- An empty binary message is allowed by the protocol and by the service;
  the composer does not send one.
- The sent message in `ws:message` carries the bytes (base64 in `data`),
  so the log shows what went over the wire.

#### Events

Both carry `connId` and `requestId`; `seq` counts the events of one
connection from 1. One `stream.Batcher` per connection (50 ms, at most 64
messages per event) keeps state changes and messages in the order they
happened: consecutive messages become one `ws:message`, each state change
its own `ws:state`.

| Event | Payload |
|---|---|
| `ws:state` | `{connId, requestId, url, seq, attempt, state, at, handshake, close, error, reconnect}`. `attempt` numbers the attempts from 1; `reconnect` is auto-reconnect's decision on `closed` ([above](#keepalive-and-reconnect)), else null. `state` is `connecting`, `open`, `closing` or `closed`. `handshake` is set on `open`, and on `closed` when connecting failed: `{url, requestHeaders, response: {statusCode, status, headers, body, bodyTruncated} or null, subprotocol, localAddr, remoteAddr, tlsVersion, durationMs}` (a rejected upgrade keeps the server's status and body). `close` is set on `closed`: `{code, reason, wasClean, initiatedBy, sent, received, messagesSent, messagesReceived, bytesSent, bytesReceived, durationMs}` with wsclient's meaning of code (the server's close frame, or 1006). `error` is the `ErrorDTO` when the connection failed. |
| `ws:message` | `{connId, requestId, seq, messages: [{direction, opcode, at, text, data, size, keepalive?, rttMs?}]}`. `direction` is `sent` or `received`, `opcode` `text` (content in `text`), `binary` (base64 in `data`), or `ping` / `pong` (WS-09: the payload base64 in `data`, and in `text` when it is UTF-8; `keepalive` for a keepalive ping and its pong; `rttMs` on a pong that answers a ping sent here, fractions kept). Pings and pongs are not counted as messages. |

#### Errors

| Code | Kind | When |
|---|---|---|
| `ws.not_found` | `not_found` | `Send` to a closed or unknown connection. |
| `ws.not_open` | `invalid_argument` | `Send` or `Ping` while connecting, closing or waiting to reconnect. |
| `ws.ping_too_long` | `invalid_argument` | A ping payload over 125 bytes (`details.bytes`). |
| `ws.invalid_format` | `invalid_argument` | `Send` with a `format` other than `text`, `json`, `hex` or `base64` (`details.format`). |
| `ws.invalid_base64` | `invalid_argument` | A `base64` message that spells no bytes (`details.offset`). |
| `hexbytes.*` | `invalid_argument` | A `hex` message that spells no bytes (`details.offset`; [tcp.md](tcp.md)). |
| `template.unresolved`, `template.cycle`, … | `invalid_argument` | `{{variables}}` that are undefined (unless `allowUnresolved`) or cannot be rendered ([templates.md](templates.md)). |
| `wsclient.*`, `wsframe.*`, `netx.*` | | Unchanged from the client ([Client errors](#client-errors)). |

### UI

`TabPanel` shows `WebSocketEditor` for a request of kind `websocket`; the
tree and the tab show a `WS` badge.

- **URL bar.** `WS` label, the URL as a single-line `CodeEditor` (the
  request editor's field; Enter connects), and Connect, Cancel (while
  connecting or waiting to reconnect) or Disconnect.
- **Variables.** `TabPanel` wraps the editor in a
  `TemplateVariablesProvider`, so `{{variables}}` in the URL, the header
  value cells and the composer are chips, red when they do not resolve,
  with value and scope on hover ([frontend.md](frontend.md#template-variables)).
  When Go refuses a connect or send with `template.unresolved`, `WsSession`
  asks with `confirmUnresolved`: "Connect with an undefined variable?" /
  **Connect anyway**, or "Send with … " / **Send anyway**, which calls
  again with `allowUnresolved`. Declining a connect puts the session back
  as it was (no error, nothing logged); declining a send sends nothing. A URL whose scheme is
  not `ws` or `wss` shows a hint in the status line (`https` suggests
  `wss`).
- **Status line.** A dot (grey, amber while connecting or closing, green
  when open, red after a failure) and the text: "Connected to … ·
  subprotocol … · 2 sent · 3 received", the error message (with the HTTP
  status of a rejected upgrade), or "Closed by the server: 4001 “bye”" /
  "Disconnected: 1000 Normal closure"; "· last pong 4.20 ms" once a ping
  was answered; while waiting to reconnect, why the attempt ended and
  "Reconnecting in 2 s (attempt 2 of 5)…". Beside it a ping payload (text,
  not saved) with **Ping** (enabled while open; Enter in the payload pings
  too), and the close code (default 1000) and reason that Disconnect
  sends; they are not saved. A code or ping Go refuses is shown in the
  status line until the state changes.
- **Sub-tabs** (the choice is kept per request like the request editor's):
  Params (the query rows, synced with the URL by `useUrlParamsSync`),
  Headers (with header name and value suggestions), Auth (the request
  editor's [auth editor](frontend.md#auth-editor) with `protocol`
  `websocket`, [Auth](#auth)), Subprotocols (a list of
  inputs with add and remove; entries that are not tokens or are listed
  twice are marked), Settings (connect timeout, verify TLS, use the
  project's cookies; "Keepalive ping every" and "Pong timeout" in ms;
  "Reconnect automatically" with the attempts, the first wait and the
  longest wait, disabled while it is off; empty fields use Go's defaults,
  shown as placeholders), Notes, Code (the handshake as a `websocat` or
  `wscat` command, resolved or templated, GEN-11:
  `WebSocketService.Snippet`, [snippets.md](snippets.md#websocat-and-wscat)).
- **Log.** The shared `MessageLog`: sent ↑ and received ↓ text messages,
  binary messages, state changes as system messages (connecting, connected
  with subprotocol, closing, closed with code, reason and counts, errors),
  pings and pongs (kinds `ping` and `pong`; the payload as text, or as
  bytes when it is not UTF-8; a badge with `keepalive` and the round trip,
  e.g. "12.3 ms", also in the detail's meta), and auto-reconnect notes
  (kind `reconnect`: "Reconnecting in 1 s (attempt 1 of 5)", "Reconnect
  cancelled", "Gave up reconnecting after 5 attempts", "Not reconnecting:
  …"; a reconnect attempt logs "Reconnecting to … (attempt 2)"). The
  toolbar's **Pings** and **Reconnects** toggles hide them. Its Clear
  button empties the session's log.
- **Binary view** (WS-06). The log's toolbar has a **Hex | Base64** switch
  (`binaryToggle`) for the whole log: binary rows preview the first 48
  bytes as hex bytes or as Base64. The detail pane of a selected binary
  message has its own **Hexdump | Base64** switch, starting from the log's
  choice: a classic hexdump (offset, 16 bytes, ASCII) of the first 64 KiB,
  or the Base64 of the first 96 KiB, wrapped; longer payloads say so, and
  Copy hex / Copy Base64 always copy all of it. Search matches the hex and
  the Base64 of the first KiB of a binary payload, so it finds what either
  view shows. The switch is not saved.
- **Composer.** Text/JSON/Hex/Base64 (auto-saved as `messageFormat`), a CodeMirror
  editor for the message (auto-saved as `message`, JSON highlighted),
  Beautify for JSON (the button or ⇧⌘F, through `FormatService.BeautifyJSON`)
  and Send. In JSON mode the text is checked in Go while typing
  (`useJsonCheck`), the error is marked in the editor (`errorMarker`) and
  shown as "Line 1, column 6" (a link to the position) with the message,
  and Send is disabled until it is valid. JSON is sent as a text message
  exactly as written, `{{variables}}` rendered by Go. ⌘Enter (Ctrl+Enter)
  sends: the registry's `composer.send`, scope `composer`, handled in the
  capture phase so CodeMirror's own Mod-Enter does not insert a line. The
  text is kept after sending.
- **Binary messages** (WS-04). In Hex and Base64 the composer checks the
  text while typing (`lib/hexBytes.ts`, `lib/base64Bytes.ts`), marks the
  first bad character in the editor, shows the message as a link to it and
  keeps Send disabled until the text spells at least one byte; the byte
  count is shown beside the formats. Send passes `format`, and Go sends a
  binary frame. **Load file…** opens the system file picker (an
  `<input type="file">`) and puts the file's bytes into the composer as
  Base64 with the format switched to Base64, so the message can be looked
  at, saved and sent like a typed one. Files over 1 MiB
  (`WS_FILE_MAX_BYTES`) are refused with a message beside the button: the
  text is kept in the editor and in the request file.
- **Saved messages** (WS-07). `SavedMessages` beside the composer, as tall
  as it (its list scrolls): each row has ▶ (send it with one click; disabled
  until connected; ⌘Enter in its name sends it too), the name as an input
  (typing renames it), a `JSON`, `HEX` or `B64` tag for the formats other
  than text (binary messages are sent as binary with their format), and, on hover or
  focus, Load (puts its text and format into the composer), move up, move
  down and delete. **Save** in the header stores the composer's text and
  format as "Message N" and focuses the name; **Update** appears once the
  composer differs from the message loaded last and writes the composer
  back to it. Every change is auto-saved with the request (`savedMessages`).
  A saved text or JSON message's `{{variables}}` are rendered when it is
  sent, like the composer's. A send error is shown under the list with the message's
  name.
- **Sessions.** `commands/wsSessions.ts` keeps one `WsSession` per request
  outside React, like the TCP sessions. Its status adds `reconnecting`
  (a `closed` event whose `reconnect` says another attempt follows), which
  counts as active, so Connect is not offered and Disconnect stops the
  wait; `attempt`, `reconnect` and `lastRttMs` are kept for the status
  line; `ping(payload)` calls `WebSocketService.Ping`. switching tabs keeps the connection
  and log; closing the tab closes it with 1001 and drops the log. Events
  that arrive before `Connect` resolves are buffered and replayed for the
  returned ID; Disconnect while `Connect` has not resolved closes with the
  chosen code as soon as the ID is known.
- **Read-only** files show a notice; the URL, rows, subprotocols, settings,
  message and saved messages cannot be edited, but connecting and sending
  (saved messages too) work.

### Tests

- `go test -race ./internal/app/services/websocket`, against an `httptest`
  server upgrading with wsframe in the server role: connect with a header
  row (and a disabled one), a jar cookie and two subprotocols, all seen by
  the server; the negotiated subprotocol in the handshake; text, JSON and a
  binary reply in order; close 4000 seen by the server; a server-initiated
  close 4001; cookies off (nothing sent, nothing stored); a rejected upgrade
  with its response; refused; invalid input; variables (URL, enabled and
  disabled rows, subprotocols and messages rendered with the scopes read on
  each call, one `template.unresolved` for all fields, `allowUnresolved`, a
  cycle, scopes that fail, no request ID); close code validation; timeout and close while connecting; `CloseAll` and the
  workspace hook (1001, cookies saved to `cookies.json`); golden event JSON
  (with pings, pongs, `attempt` and `reconnect`);
  binary messages (`TestSendBinary`: hex and Base64, all 256 byte values,
  the URL alphabet, braces sent as bytes, come back byte for byte from the
  echo server as sent and received binary messages; bad hex, bad Base64
  and an unknown format refuse); `TestParseBase64`.
- `reconnect_test.go`, against in-process servers and a fake clock:
  `TestPing` (the server's ping and the automatic pong, text and hex
  pings answered with `rttMs`, `ws.ping_too_long`, bad hex, unknown format
  and connection, not counted as messages);
  `TestKeepaliveTimeoutThenReconnect` (a server that stops answering pongs
  is closed with `wsclient.pong_timeout` exactly at the deadline, then
  reconnected to a server whose pong clears the deadline; Disconnect ends
  it with `stopped: manual`); `TestReconnectBackoffAndVariables` (a
  refused port: waits of 1 s, 2 s, 3 s (capped), not a millisecond early,
  the URL's variable rendered anew for each attempt, then
  `max_attempts`); `TestReconnectAfterDropResetsAndCancel` (the server
  drops TCP after the upgrade; the reconnect opens, the count starts over,
  Disconnect while waiting cancels with a `closed` event without close
  info); `TestReconnectCloseCodes` (1000, 1008, 4001 stop, 1001, 1011,
  1012, 1013 reconnect, a user close stops, no decision without
  auto-reconnect); `TestReconnectStopsWhenVariablesNoLongerRender`;
  `TestBackoff` (doubling, cap, clamping, keepalive defaults).
- projectfile golden example (with text, JSON, hex and Base64 saved messages) and defaults (the text
  format and an empty list left out, unknown members of a saved message
  kept), collection create, save, duplicate, rename and reload,
  `CollectionService` auto-save round trip with saved messages and a golden
  `RequestDTO`, `LoadVariablesSource`, `HTTPService.Variables` with
  `WithVariablesLoader`.
- Frontend: `commands/wsSessions.test.ts` (buffered early events, text and
  binary messages, rejected upgrade, close codes while connecting and on tab
  close, send errors, the format passed for hex and Base64; the
  undefined-variables questions on connect and send; pings and pongs with
  their badges, not counted; reconnecting, the reconnect notes, cancel and
  giving up; the keepalive and reconnect settings in `connectInput`),
  `lib/base64Bytes.test.ts` (Go's cases), `components/messagelog/format.test.ts`
  (Base64 previews and search text),
  `components/editor/WebSocketEditor.test.tsx` (render,
  auto-save of URL with params, subprotocols, settings and format;
  connect/send/⌘Enter/close with a code against a mocked service; JSON
  validation, blocked send and beautify; the log, tab switches and the
  server's close; saved messages: save, rename, reorder, delete, load,
  update and send with its error; chips in the URL and message and the
  connect question; scheme hint and subprotocol checks; binary messages:
  bad hex blocks Send, the byte count, hex and saved Base64 sent with
  their format, a file loaded as Base64 and one over the limit refused, the
  log's Hex/Base64 switch and the detail pane's Hexdump/Base64 switch;
  keepalive and reconnect settings saved and passed to Connect, Ping with
  its error, the Pings toggle hiding pings and pongs, the last pong in the
  status, the reconnecting status and Cancel),
  `components/messagelog/messageView.test.ts` (hidden kinds).

### Not yet

- Showing the handshake request and response headers in the UI (the
  events carry them). Digest auth on the upgrade request.

## Assertions

TEST-08. A websocket request's assertions (its Tests sub-tab) are checked
against the messages received (not the sent ones, pings or pongs) when
the connection ends for good: the last `closed` event's `tests` (none
while auto-reconnect waits; the messages of every attempt count). The
whole connection's `status` and `header` are the upgrade's answer, a
message's `Type` header is `text` or `binary`.
`WebSocketService.CheckAssertions({connId})` (Check now, under the Tests
sub-tab) checks an open connection or one of the last that closed. See
[assertions.md](assertions.md#streams).

## Framing

### Frames

| Item | Purpose |
|---|---|
| `Header{Fin, Rsv1, Rsv2, Rsv3, Opcode, Masked, MaskKey, Length, LengthSize}` | A decoded header with every wire detail. `LengthSize` is the number of extended length bytes (0, 2, 8). `RSV()`, `Size()`. |
| `Frame{Header, Payload}` | Payload is always unmasked; `MaskKey` says how it was masked. |
| `Opcode` (`OpContinuation` … `OpPong`), `IsControl`, `IsReserved`, `String` | Opcodes; reserved values are kept, not dropped. |
| `ReadFrame(r, Limits) (Frame, error)` | Reads and validates one frame. Returns the header even with an error, so an inspector can show what arrived. A clean end at a frame boundary is `io.EOF`, a cut frame `io.ErrUnexpectedEOF`. |
| `Limits{MaxPayload, AllowedRSV, AllowNonMinimalLength}` | `MaxPayload` 0 = 16 MiB, < 0 = unlimited. |
| `AppendFrame(dst, Frame)`, `WriteFrame(w, Frame)` | Encode exactly as described (one `Write`). Masks a copy of the payload. Invalid frames (fragmented pings, RSV bits, reserved opcodes, non-minimal lengths via `LengthSize`) can be crafted on purpose. |
| `Mask(key, pos, b) int` | In-place XOR masking, 8 bytes at a time; returns the next key offset. |
| `ComputeAccept(key)` | `Sec-WebSocket-Accept` for a key (§4.2.2). |

`ReadFrame` rejects, as protocol errors: reserved opcodes; RSV bits outside
`AllowedRSV`; fragmented control frames or control payloads over 125 bytes;
64-bit lengths with the top bit set; lengths not in the minimal encoding;
payloads over `MaxPayload` (close code 1009). It does not check masking,
which depends on the role.

**Non-minimal lengths are rejected by default.** §5.2 says the minimal
number of bytes MUST be used. A peer that sends `7E 00 05` for a 5-byte
payload is broken, and PostSonde is a debugging tool that should say so.
`AllowNonMinimalLength` accepts them (and `LengthSize` keeps the encoding so
it re-encodes byte for byte).

`Conn` reuses its header, control-payload and write buffers. A payload is
read in chunks of at most 1 MiB, so a header claiming a huge length cannot
make the reader allocate the whole limit before the data arrives.

### Messages: `Conn`

| Item | Purpose |
|---|---|
| `NewConn(rw io.ReadWriter, Options) *Conn` | Usually `rw` is the `net.Conn` after the upgrade. |
| `Options{Role, Limits, MaxMessageSize, DisableAutoPong, OnFrame, Rand, Reader}` | `Role` is required. `MaxMessageSize` 0 = 32 MiB. `Reader` is a `*bufio.Reader` that already holds bytes read past the handshake. `Rand` supplies mask keys (default `crypto/rand`). |
| `ReadMessage() (Opcode, []byte, error)` | The next text or binary message, reassembled. |
| `WriteMessage(op, data)` | One-frame message; text must be UTF-8. |
| `NextWriter(op) *MessageWriter` | Fragmented message: each non-empty `Write` is one frame, `Close` sends the final (empty) frame. |
| `Ping(p)`, `Pong(p)` | Control frames, payload ≤ 125 bytes. |
| `WriteFrame(Frame)` | Raw frame, no role or protocol checks (for testing peers). |
| `Close(ctx, code, reason)` | Close handshake, below. |
| `WriteClose(code, reason) (sent bool, err)` | Send a close frame (unless one was sent) without waiting, for a caller that already runs a reader goroutine. |
| `PeerClose() *CloseError`, `Role()` | State. |

**Roles and masking (§5.1).** A client masks every frame with a fresh key
and fails the connection on a masked frame from the server; a server sends
unmasked frames and fails on an unmasked frame from the client.

**Reading.** `ReadMessage` reassembles fragments. Control frames may arrive
between fragments. A continuation without a started message, or a new text
or binary frame while a fragmented message is open, is a protocol error.
`MaxMessageSize` is checked against each frame header before its payload is
read. Pings are answered automatically with a pong carrying the same payload
unless `DisableAutoPong` is set; unsolicited pongs are ignored. Read errors
are sticky: once `ReadMessage` fails, it keeps returning that error.

**UTF-8.** Text messages are validated incrementally, frame by frame, with a
carry of at most 3 bytes for a sequence split across fragments. Validation
fails as soon as a frame contains bytes that cannot be the prefix of valid
UTF-8 (a surrogate, an overlong form, a code point over U+10FFFF), without
waiting for the rest of the message (what Autobahn 6.4.x expects). A message
that ends inside a sequence is invalid too. Close reasons must be UTF-8.

**Writing and concurrency.** One goroutine reads; any number may write.
Frames are written under one lock, one `Write` per frame; data messages hold
a second lock for their whole length. So `Ping`, `Pong` and `Close` can go
out between the fragments of a `NextWriter` message, but two data messages
never interleave. `OnFrame` is called for each frame read (unmasked, after
frame validation) and written; it may call `Ping`/`Pong`.

**Close handshake.** `Close(ctx, code, reason)` sends a close frame (unless
one was sent) and waits for the peer's close frame:

- If another goroutine is in `ReadMessage`, that call receives the close;
  otherwise `Close` reads and discards messages itself.
- When `ctx` ends first, `Close` sets a past read deadline (if the transport
  has `SetReadDeadline`) so blocked reads return, and fails with
  `wsframe.close_timeout`.
- When the peer's close arrives, `ReadMessage` echoes its status code (an
  empty close gets an empty close) unless this side already sent one, and
  returns `wsframe.peer_closed` wrapping `*CloseError{Code, Reason}`.
- After a close is sent, data, ping and pong writes fail with
  `wsframe.close_sent`.
- Neither side closes the transport: close the `net.Conn` after `Close`
  returns or `ReadMessage` fails (RFC 6455 lets the server close TCP first;
  a client may wait for that).

**Close codes (§7.4).** `StatusCode.ValidOnWire` accepts 1000–1003,
1007–1014 and 3000–4999. 1004 (reserved), 1005, 1006, 1015 (never sent),
codes below 1000, 1016–2999 and 5000 and up are rejected both when sending
(`AppendClosePayload`, `Close`) and when received (`ParseClosePayload`). A
1-byte close payload is a protocol error; the reason is at most 123 bytes.
`Close(ctx, CloseNoStatus, "")` sends an empty close frame.

### Errors

Every error is an `*errs.Error` ([errors.md](errors.md)). Errors that fail
the connection make the `Conn` send a close frame with the mapped code (best
effort) before returning; `CloseCodeOf(err)` returns that code.

| Code | Kind | Close code | When |
|---|---|---|---|
| `wsframe.protocol_error` | `protocol` | 1002 | Bad header, masking, fragmentation or control frame. |
| `wsframe.invalid_close_payload` | `protocol` | 1002 | 1-byte close payload or a status code that must not be sent. |
| `wsframe.invalid_utf8` | `protocol` | 1007 | Text message or close reason not UTF-8. |
| `wsframe.too_big` | `protocol` | 1009 | Frame over `Limits.MaxPayload` or message over `MaxMessageSize`. |
| `wsframe.peer_closed` | `network` | peer's code | The peer sent a close frame (wraps `*CloseError`). |
| `wsframe.abnormal_closure` | `network` | 1006 (not sent) | The connection ended without a close frame (wraps `io.EOF`). |
| `wsframe.unexpected_eof` | `network` | 1006 (not sent) | The connection ended inside a frame. |
| `wsframe.read_failed`, `wsframe.write_failed` | from the cause (`timeout`, `network`, ...) | – | Transport errors. |
| `wsframe.close_sent` | `invalid_argument` | – | Write after this side sent a close frame. |
| `wsframe.close_timeout` | `timeout` / `cancelled` | – | `Close` context ended before the peer's close frame. |
| `wsframe.invalid_argument` | `invalid_argument` | – | Bad argument to a write or encoding function. |

### Extensions

No extension is implemented. RSV bits must be zero, and a frame with any RSV
bit set fails the connection with 1002. `Limits.AllowedRSV` is the hook for
permessage-deflate (RFC 7692): once negotiated, RSV1 will be allowed on the
first frame of a message and the message layer will inflate it. Compression
is deliberately left for later.

### Tests

- `TestRFC6455Examples`: the §5.7 examples byte for byte in both directions
  (unmasked and masked "Hello", "Hel"+"lo", unmasked ping, masked pong,
  256-byte and 64 KiB binary with 16- and 64-bit lengths).
- Header edge cases, masking against a byte-wise reference, UTF-8 at every
  split point, close codes, and the `Conn` rules over scripted input.
- Close handshake in both directions over `net.Pipe`, with and without a
  concurrent reader, with a timeout and with the peer dropping the transport.
- Fuzz: `FuzzReadFrame` (no panics, limits respected, control frame rules,
  `AppendFrame(ReadFrame(x))` reproduces the consumed bytes) and
  `FuzzConnRead` (arbitrary bytes into a client or server `Conn`: messages
  within limits, text always UTF-8, errors typed, output frames well-formed
  in the right role with at most one close frame, last).

```
go test ./internal/core/realtime/wsframe
go test -run '^$' -fuzz '^FuzzReadFrame$' -fuzztime 60s ./internal/core/realtime/wsframe
go test -run '^$' -fuzz '^FuzzConnRead$' -fuzztime 60s ./internal/core/realtime/wsframe
```
