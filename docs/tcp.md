# Raw TCP

PostSonde talks to any TCP service (TCP-01): connect to `host:port`,
optionally over TLS (TCP-02), send text or hex, and watch what comes back
in the message log, as the chunks the operating system hands over or split
into messages by a framing (TCP-03: newline-delimited or length-prefixed).

```
TcpEditor ─ commands/tcpSessions ─ lib/tcp ──▶ TCPService.Connect/Send/Close
   ▲              │ MessageSource ×2               │ trust (CA bundles), client certificates
   │              ◀── tcp:state, tcp:data ─────────┤ stream.Batcher per connection
MessageLog                                         ▼
               tcpclient.Connect ── netx.Dialer ──▶ host:port ── TLS handshake (optional)
                    └── framing.Splitter (reads) / framing.Encode (Send)
```

| Piece | Where |
|---|---|
| File format (kind `tcp`) | [file-format.md](file-format.md#tcp-request-kind-tcp), `internal/core/storage/projectfile` (`TCPRequest`, `TCPFraming`) |
| Client | `internal/core/realtime/tcpclient` (`tls.go` for TLS) |
| Framing | `internal/core/realtime/framing` |
| Hex input | `internal/core/realtime/hexbytes`, `frontend/src/lib/hexBytes.ts` |
| Bound service and events | `internal/app/services/tcp` (`TCPService`) |
| Binding wrappers | `frontend/src/lib/tcp.ts` |
| Sessions (connection state and log per request) | `frontend/src/commands/tcpSessions.ts`, `useTcpSession.ts` |
| Editor | `frontend/src/components/editor/TcpEditor.tsx` |

## The request file

A `tcp` request stores `address`, `sendMode` (`text` or `hex`),
`lineEnding` (`none`, `lf`, `crlf`), the composer's `message`, its
`framing` and `settings` (`connectTimeoutMs`, `tls`, `verifyTls`). Files
from before TLS and framing have neither and load as plain TCP without
framing. The address is one `host:port` string rather
than separate members: it is what people paste, and it keeps
`{{variables}}` in one place. The editor auto-saves every change through
`CollectionService.UpdateRequest` like the HTTP editor
([collection.md](collection.md#auto-save)). `CreateRequest` accepts kind
`tcp`, and the New request dialog offers it
([frontend.md](frontend.md#new-folder-and-new-request)).

## Client: `tcpclient`

```go
conn, err := tcpclient.Connect(ctx, tcpclient.Options{
	Address:        "localhost:7000",
	Dialer:         dialer,                 // netx.Dialer, required
	ConnectTimeout: 5 * time.Second,        // dial + TLS handshake; 0: 10 s; < 0: none
	ReadBufferSize: 0,                      // 0: 64 KiB, the largest chunk
	TLS:            &tcpclient.TLSOptions{  // nil: plain TCP
		RootCAs:           roots,             // nil: the system's
		ClientCertificate: cert,              // *httpclient.ClientCert, sent when asked
	},
	Framing: framing.Options{Mode: framing.ModeLength, PrefixSize: 2}, // zero: chunks as read
	OnState: func(ch tcpclient.StateChange) { ... },
	OnData:  func(c tcpclient.Chunk) { ... },
})
err = conn.Send([]byte("PING"))             // framed: 00 04 50 49 4E 47
err = conn.CloseWrite()                     // FIN: the peer reads EOF, reading goes on
err = conn.Close()                          // waits until closed
info := conn.CloseInfo()                    // who, error, bytes and chunks both ways
```

| Item | Purpose |
|---|---|
| `Connect(ctx, Options) (*Conn, error)` | Validates the address and the framing (nothing is reported for invalid options), reports `connecting`, dials through the dialer, runs the TLS handshake when `TLS` is set, reports `open` and starts reading; or reports `closed` with the error and returns it. `ctx` bounds connecting only. |
| `ParseAddress(address)` | `host:port`, a host, a numeric port 1–65535; IPv6 in brackets. Not resolved. |
| `Send(b)`, `CloseWrite()`, `Close()` | Concurrency-safe. Without a framing, `Send` of nothing writes nothing; with one it sends an empty message. |
| `State()`, `Done()`, `CloseInfo()`, `Err()`, `Address()`, `LocalAddr()`, `RemoteAddr()`, `TLS()` | State. |
| `StateChange{State, At, LocalAddr, RemoteAddr, TLS, Close, Err}` | Passed to `OnState`; `TLS` (`*httpclient.TLSInfo`: version, cipher suite, SNI, the server's chain) from `open` on. |
| `Chunk{Direction, Data, At, Incomplete}` | Passed to `OnData`; `Direction` is `sent` or `received`. With a framing, one message. |
| `CloseInfo{InitiatedBy, Err, BytesSent, BytesReceived, ChunksSent, ChunksReceived, OpenedAt, ClosedAt}` | How it ended; `InitiatedBy` is `local`, `remote` or `none`. Bytes are those on the connection (prefixes and delimiters included, TLS records' overhead not); chunks are what `OnData` got (messages with a framing). |

```
connecting ──dial ok, TLS ok──▶ open ──Close──▶ closing ──▶ closed
     │                            │                            ▲
     └── dial / TLS error ────────┴── peer EOF / reset / I/O / TLS alert / oversize message
```

- **Chunks without a framing.** A received chunk is what one read
  returned: a message of the peer may arrive as several chunks, several
  messages as one. The log shows chunks as they came. With a framing, see
  [Framing](#framing).
- **Ordering.** `OnState` and `OnData` are called one at a time, in order.
  A sent chunk is reported just before its bytes are written, so it always
  comes before any reply. Reading waits for `OnData` (backpressure). A
  write blocked on a peer that does not read never blocks reading, so a
  large echo cannot deadlock; `Close` releases it. Callbacks must not call
  `Send`, `CloseWrite` or `Close`.
- **Who closed.** `Close` is `local` without an error. The peer's orderly
  close (EOF) is `remote` without an error; the connection is then closed
  completely, also after `CloseWrite`. A reset by the peer is `remote` with
  `netx.connection_reset`. A peer that resets right after accepting,
  before the dial returned (on macOS the RST can be seen by the dial), fails
  connecting instead: `Connect` returns `netx.connection_reset` (the
  dialer classifies it too), and the connection never opens. A cancelled connect is
  `local` with `netx.dial_cancelled`; other failures are `none`.
- **A failed write** fails the connection: it closes with that error, which
  `Send` also returns.

### Errors

| Code | Kind | When |
|---|---|---|
| `tcpclient.invalid_address` | `invalid_argument` | Not `host:port`, no host, a port outside 1–65535 or not a number, spaces around it. Detail `address`. |
| `tcpclient.not_open` | `invalid_argument` | `Send` or `CloseWrite` when not open. |
| `tcpclient.write_closed` | `invalid_argument` | `Send` after `CloseWrite`. |
| `tcpclient.half_close_unsupported` | `unsupported` | `CloseWrite` on a connection without it (a dialer's wrapper). |
| `netx.connection_reset` (`tcpclient.CodeConnectionReset`) | `network` | ECONNRESET, EPIPE, ECONNABORTED and their Winsock numbers (`netx.IsReset`), shared with the other protocol clients. |
| `tcpclient.read_failed`, `tcpclient.write_failed` | `network` or `timeout` | Other transport errors. |
| `netx.*` | from netx | Dial errors, unchanged: `netx.dial_timeout` for the connect timeout (the TLS handshake too), `netx.dial_cancelled`, `netx.connection_refused`, `netx.dns_not_found`, … ([network.md](network.md#errors)). |
| `netx.tls_*` | `tls` | TLS failures ([TLS](#tls)). |
| `framing.invalid_options` | `invalid_argument` | An unknown mode, delimiter or byte order, a prefix size other than 1, 2, 4, 8, a maximum outside 1 byte–32 MiB. Details `field`, `value`. |
| `framing.message_too_large` | `protocol` | A received message over the maximum (the connection fails), or a message to send that its length prefix cannot hold (`Send` fails, the connection stays open). Details `size`, `maxSize`. |

## TLS

TCP-02. With `Options.TLS` the client runs a TLS handshake (TLS 1.2 or
newer) over the dialed connection, inside the connect timeout, before the
connection opens; everything after it is the plain TCP client above, over
TLS. The app uses the same trust and client certificates as HTTP,
WebSocket and gRPC ([network.md](network.md#custom-ca-certificates)):

- **Trust.** `TCPService` gets `tcp.WithTrust(trust.Roots)`: the system's
  CAs plus the custom CA bundles of the settings, read per connect. A
  bundle that cannot be loaded fails a verified TLS connect at once with
  its `tlsconf.*` code; plain TCP and `verifyTls: false` ignore it.
- **Verification.** On by default; a request's `settings.verifyTls: false`
  accepts any certificate (chain, name, expiry), like WebSocket and gRPC
  requests. The global HTTP switch (HTTP-23) does not apply, as for those.
- **SNI and name check.** The host of the address: a name is sent as SNI
  and checked against the certificate's DNS names; an IP address sends no
  SNI and is checked against its IP addresses. `TLSOptions.ServerName`
  overrides both in the client, but requests have no setting for it, as
  HTTP requests have none.
- **Client certificates.** `tcp.WithClientCerts(pickClientCert)`: the
  certificate set for the address's `host:port` in Settings, Network,
  Client certificates (AUTH-30), sent whenever the server asks. One that
  cannot be loaded fails Connect at once with its `tlsconf.*` code.

Failures use the netx TLS codes ([network.md](network.md#connection-and-tls-failures)):
`netx.tls_unknown_authority`, `netx.tls_hostname_mismatch`,
`netx.tls_certificate_expired`, `netx.tls_certificate_invalid`,
`netx.tls_server_not_tls` (a server that answers in plain text, with the
hint "Turn off TLS for this request, or check the port"),
`netx.tls_client_cert_required` and `netx.tls_client_cert_rejected`, and
`netx.tls_handshake_failed` for anything else (a plain echo server sends
the ClientHello back). A server that refuses the client certificate does
so during the handshake with TLS 1.2 (`Connect` fails); with TLS 1.3 the
client's handshake is complete first, so the connection opens and then
closes with the server's alert (`netx.tls_client_cert_required` without a
certificate, `…_rejected` with one it does not accept). `CloseWrite`
sends a TLS close_notify, then the TCP FIN.

## Framing

TCP-03, package `framing`. A framing says where a message ends in the
byte stream. It splits what is received into messages and frames each
message sent. The same `framing.Options` configure both directions:

| Mode | Received | Sent |
|---|---|---|
| `none` (default) | Chunks as read. | The bytes as given. |
| `newline` | A message ends at LF; a CR right before the LF is part of the line ending, so LF and CRLF lines are both split, without the delimiter. A lone CR is data. An empty line is an empty message. | The message and `delimiter`: `lf` (default) or `crlf`. A line break inside the message is sent as it is (so the peer sees two lines). |
| `length` | A message is an unsigned length of `prefixSize` bytes (1, 2, 4 default, or 8), `byteOrder` `big` (default, network order) or `little`, then that many bytes. The length counts the bytes after the prefix only; zero is an empty message. | The length prefix, then the message. More than the prefix holds (255 for 1 byte, 65,535 for 2) fails `Send` with `framing.message_too_large`. |

- **Partial frames.** A `Splitter` keeps an unfinished message (and an
  unfinished prefix) between reads, so a message split across any number
  of reads, and several messages in one read, come out whole. Each message
  is a new slice.
- **Maximum size.** `maxMessageSize` (default 1 MiB, at most 32 MiB)
  limits a received message without its prefix or delimiter. A length
  prefix over it fails at once, before the body is read; a line fails as
  soon as it has grown past it without a line break. The connection then
  fails with `framing.message_too_large` (initiated by `none`), after the
  messages before it were delivered. Buffering is bounded by the maximum
  plus one read.
- **End of stream.** When the connection ends in the middle of a message,
  its bytes (with the length prefix, for `length`) are delivered as one
  last chunk with `Incomplete` set, before `closed`.
- **Not supported:** prefixes that count themselves, variable-length
  (varint) prefixes, other delimiters, and escaping. `none` or hex in the
  composer covers other protocols.

## Hex input: `hexbytes`

`hexbytes.Parse(s)` and `parseHex(text)` (TypeScript, for validation while
typing) follow the same rules, and share their test cases:

- pairs of hex digits, any case; each pair is a byte;
- groups separated by any run of whitespace, `,`, `:` or `;`
  (`DE AD`, `de,ad`, `de:ad`, `DE, AD`);
- `0x`/`0X` at the start of a group (`0xDEAD` is two bytes), `\x` before
  any pair (`\xde\xad`);
- an odd number of digits in a group is an error, not a guess.

Errors (`invalid_argument`) carry `offset`, the position of the offending
character counted in characters (code points) from 0, and a message with
the 1-based position: `hexbytes.invalid_char` ("“G” at position 5 is not a
hex digit."; invisible characters as U+XXXX), `hexbytes.odd_digits` (the
position of the lone digit) and `hexbytes.missing_digits` (a prefix without
digits). An empty or all-separator text is no bytes.

## Service: `TCPService`

| Method | Does |
|---|---|
| `Connect({requestId, address, connectTimeoutMs, tls, verifyTls, framing})` | Validates the address and the framing, and for TLS reads the trust and picks the client certificate; returns `{connId, requestId, address}` at once; connecting runs in the background. `connectTimeoutMs`: null for 10 s, 0 for no limit (dial and TLS handshake). `tls`: connect with TLS; `verifyTls`: false accepts any certificate, null verifies. `framing`: `{mode, delimiter, prefixBytes, byteOrder, maxMessageBytes}` as the request stores it (empty strings and 0 are the defaults), null for none. An address with `{{` fails with `tcp.variables_unsupported`: variables are not resolved yet. |
| `Send({connId, mode, text, lineEnding})` | `text`: `text` as UTF-8 plus the line ending; `hex`: `hexbytes.Parse(text)`, line ending ignored. With a framing the line ending is left out (the framing ends the message) and an empty message is allowed. Returns `{bytes}`, the size of the message. |
| `Close(connId)` | Stops connecting or closes, and returns once the last events are sent. Unknown or closed IDs do nothing. |
| `CloseAll()` | Closes every connection; returns how many. |

Connections are also closed when the app quits (`ServiceShutdown`, waiting
up to 2 s) and whenever the open workspace changes: `services()` wraps the
workspace service's event sender, and `workspace:changed` calls
`tcp.OnWorkspaceChanged` before it is sent.

The shared dialer (proxies, the privacy guard, the connection log) is built
once in `services()` and passed in, with `tcp.WithTrust` and
`tcp.WithClientCerts` ([TLS](#tls)).

### Events

Not operation events ([events.md](events.md)): a connection is not an
operation that finishes on its own, and it needs state events of its own.
The same principles apply.

| Event | Payload |
|---|---|
| `tcp:state` | `StateEventDTO{connId, requestId, address, seq, state, at, localAddr, remoteAddr, tls, close, error}`. `tls` (`version`, `cipherSuite`, `serverName`, `peerSubject`) from `open` on with TLS, else null. `close` (`initiatedBy`, `bytesSent`, `bytesReceived`, `chunksSent`, `chunksReceived`, `durationMs`) on `closed`; `error` is the `ErrorDTO` when the connection failed. |
| `tcp:data` | `DataEventDTO{connId, requestId, seq, chunks}`, each chunk `{direction, at, data, size, incomplete}` with `data` base64-encoded. With a framing a chunk is one message; `incomplete` marks the unfinished last one. |

- **Batching.** State changes and chunks of one connection go through one
  `stream.Batcher` in the order they happened, flushed every 50 ms or at
  16 chunks (up to about 1 MB of data per event without a framing; with
  one, 16 messages of up to the maximum size). A flush sends runs of
  chunks as one `tcp:data` and each state change as a `tcp:state`, so the
  UI sees data and states in their real order: the data a server sent
  before closing arrives before `closed`.
- **`seq`** counts all events of a connection from 1.
- **Memory.** Chunks wait in memory only until the next flush; a firehose
  is limited by how fast events can be sent, and the UI keeps at most
  50,000 log messages per view.

### Service errors

| Code | Kind | When |
|---|---|---|
| `tcp.not_found` | `not_found` | `Send` to a closed or unknown connection. |
| `tcp.not_open` | `invalid_argument` | `Send` while still connecting. Once the client reported open (from inside `tcpclient.Connect`), a `Send` waits the moment until `Connect` returned the connection, so it never fails after the `open` event. |
| `tcp.invalid_mode`, `tcp.invalid_line_ending` | `invalid_argument` | A mode other than `text`/`hex`, a line ending other than `none`/`lf`/`crlf`. |
| `tcp.empty_message` | `invalid_argument` | Nothing to send without a framing (empty text without a line ending, hex without bytes). |
| `tcp.variables_unsupported` | `unsupported` | `{{` in the address. |
| `framing.invalid_options` | `invalid_argument` | `Connect` with a framing the client refuses ([Errors](#errors)); `maxMessageBytes` outside 1–33554432 too. |
| `tlsconf.*` | from tlsconf | `Connect` with TLS when a custom CA bundle or the host's client certificate cannot be loaded ([network.md](network.md#client-certificates)). |

## UI

`TabPanel` shows `TcpEditor` for a request of kind `tcp`; the tree and the
tab show a `TCP` badge.

- **Address bar.** `TCP` label, the address field (auto-saved; Enter
  connects), a TLS switch inside the field (auto-saved as `settings.tls`,
  as in the gRPC editor), and Connect, Cancel (while connecting) or
  Disconnect. Under it a status line with a dot: grey not connected, amber
  connecting or disconnecting, green connected ("Connected to
  127.0.0.1:7000 (TLS 1.3) · 12 B sent · 30 B received"), red with the
  error message and hint when the last connection failed.
- **Connection options.** A row under the status line: "Verify
  certificate" (with TLS; unchecked saves `verifyTls: false`), Framing
  (None, Newline-delimited, Length-prefixed) and the framing's settings:
  the delimiter sent (LF, CRLF), the prefix size (1, 2, 4, 8 bytes) and
  byte order (disabled for 1 byte), and "Max message" in bytes (empty for
  1 MiB; an invalid number is marked and not saved). Defaults are saved as
  empty members. They apply when connecting, so they and the TLS switch
  are disabled while a connection is active.
- **Log.** The shared `MessageLog`: received chunks ↓, sent chunks ↑,
  state changes as system messages (`connecting`, `connected` with the TLS
  version and the session in its details, `closing`, `closed` with the
  totals, `error` with the message). With a framing each entry is one
  message, without its prefix or delimiter; the unfinished last one has
  the badge "incomplete". "Show as" Text or Hex switches the whole
  history: the session keeps two `MessageSource`s, one with text messages
  (without a framing, UTF-8 decoded with a streaming decoder per
  direction, so a character split across chunks is decoded whole; with
  one, each message decoded on its own) and one with binary messages (hex
  rows and hex dump in the detail pane). Clear empties both.
- **Composer.** A text area (auto-saved as `message`), Text/Hex (auto-saved
  as `sendMode`), the line ending select (auto-saved; disabled for hex and
  with a framing, which ends each message) and Send. ⌘Enter (Ctrl+Enter)
  in the text area sends: the registry's `composer.send`, scope
  `composer`, which overrides the global `request.send` placeholder there.
  With a framing an empty message can be sent. In hex mode the text is
  checked while typing with `parseHex`: an invalid text marks the field
  `aria-invalid`, shows the message and disables Send. Errors Go returns
  from Send are shown in the same place. The text is kept after sending.
- **Sessions.** `commands/tcpSessions.ts` keeps one `TcpSession` per
  request outside React: the connection state (`useTcpSession` reads it
  with `useSyncExternalStore`), including the TLS session and whether the
  connection is framed, and the two logs. Switching tabs unmounts the
  editor but keeps the connection and its log; closing the tab
  (`onTabClosed`) disconnects and drops them. The registry subscribes to
  `tcp:state` and `tcp:data` once and routes events by `requestId`; a
  session only applies events of its current `connId`, and events that
  arrive before `Connect` resolves are buffered and replayed for the ID it
  returns. Disconnect while `Connect` has not resolved closes the
  connection as soon as the ID is known.
- **Read-only** files show a notice; address, options, mode, line ending
  and message cannot be edited, but connecting and sending work.

## Tests

- `go test -race ./internal/core/realtime/framing`: newline and length
  framing (every prefix size, both byte orders) of byte streams fed in
  awkward chunkings (every byte alone, fixed sizes, seeded random cuts),
  the rest of an unfinished message, prefix encoding, oversize messages
  (length known from the prefix, a line growing past the maximum, CRLF
  split at the maximum), option checks, and `FuzzSplitter` (any chunking
  gives the messages of the whole stream).
- `go test -race ./internal/core/realtime/tcpclient`: text and hex echo
  against a loopback listener, a 2 MB concurrent echo through 1 KiB reads
  (no deadlock), remote close, reset (RST via `SetLinger(0)` once the client
  sent a byte, so it hits an open connection), half-close,
  refused, connect timeout and cancel with a blocking dialer, a failing
  write, invalid addresses, closing under a slow consumer. TLS against
  in-process TLS echo servers with `tlstest` certificates: echo and SNI (an
  IP address and `localhost`), a server name override, an mTLS server on
  TLS 1.2 and 1.3 with a trusted, a missing and an untrusted client
  certificate, an untrusted server and `InsecureSkipVerify`, a plain-text
  server and a plain echo server, a handshake timeout, half-close over TLS.
  Framing end to end: newline and length echo with servers writing in
  small pieces, an oversize message failing the connection, invalid
  options, framing over TLS.
- `internal/core/realtime/hexbytes`: accepted forms, error codes and
  offsets, messages, `FuzzParse`.
- `internal/app/services/tcp`: echo in text and hex through events, state
  order and `seq`, remote close with data before `closed`, refused,
  timeout, close while connecting, `CloseAll` and the workspace hook, send
  validation, and golden event JSON (`testdata/dto/events.golden`). TLS
  with the trust and client certificate options (mTLS echo, the
  certificate picked for the address, a missing one), verification off, a
  broken CA bundle or client certificate; newline framing with 2-byte reads
  (the line ending replaced by the delimiter, an empty message, an
  incomplete last line) and length framing with an oversize echo.
- projectfile golden example and defaults (old files without framing or
  TLS, unknown framing values kept), collection create/duplicate/
  rename/reload, `CollectionService` auto-save round trip with a golden
  `RequestDTO`.
- Frontend: `lib/hexBytes.test.ts` (the Go cases), `commands/tcpSessions.test.ts`
  (buffered early events, UTF-8 split across chunks, TLS in the connected
  message, framed messages decoded on their own and the incomplete badge,
  errors, disconnect while connecting and on tab close),
  `components/editor/TcpEditor.test.tsx` (render, auto-save, TLS and
  framing options saved and passed to Connect and locked while connected,
  connect/send/disconnect with a mocked service, ⌘Enter, hex validation,
  the log in text and hex, tab switches, failure status).

## Not yet

- `{{variables}}` in the address and message (WS-11 equivalent), a TLS
  server name override setting, saved messages. UDP is its own request
  kind ([udp.md](udp.md)).
