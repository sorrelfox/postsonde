# gRPC transport

`internal/core/grpc/transport` carries gRPC calls over Go's standard
HTTP/2 client. There is no grpc-go and no `golang.org/x/net/http2`:
`net/http` has spoken h2c (HTTP/2 over plaintext with prior knowledge) since
Go 1.24, and its HTTP/2 client streams request and response bodies in both
directions at once. Messages are raw bytes, so the transport does not care
about the codec: `dynamic` (protobuf, [protobuf-codec.md](protobuf-codec.md))
or the JSON mapping sit on top.

```
CallOptions ─▶ Client.Invoke ─▶ Stream ── Send/CloseSend ─▶ io.Pipe ─▶ request body (DATA frames)
                 │                  ◀── Recv ◀── FrameReader ◀── response body
                 │                  ◀── Header / Trailer / Status ◀── HEADERS, trailers
                 ▼
http.Transport from httpclient.NewTransport, Protocols = {HTTP2, UnencryptedHTTP2}
   DialContext = netx.Dialer (guard, proxy, trace); TLS on top with ALPN "h2"
   TLSClientConfig = tlsconf.Build(Options) (custom CAs, server name, client certificate)
gRPC-Web calls: a second http.Transport of the same settings, Protocols = {HTTP1, HTTP2}
   (HTTP/1.1 in plaintext; ALPN offers h2 and http/1.1 with TLS)
```

## API

| Item | Purpose |
|---|---|
| `New(d netx.Dialer, Options{Transport, MaxRecvMessageSize}) *Client`, `CloseIdleConnections()` | One client per connection settings; calls to one address share an HTTP/2 connection. `Transport` is `httpclient.TransportOptions` (CA roots, verification, `TLSConfig`, idle limits, handshake timeout). |
| `NewWithTLS(d, Options, tlsconf.Options, baseDir) (*Client, *tlsconf.Result, error)` | A client whose TLS calls use the built TLS settings ([Transport security](#transport-security)); the result carries warnings for the UI. Build errors are `tlsconf.*`. |
| `CallOptions{Address, Protocol, TLS, Authority, Method, Metadata, Timeout, ContentSubtype, UserAgent, Compression, MaxRecvMessageSize}` | One call. `Protocol` is `ProtocolGRPC` (or `""`), `ProtocolGRPCWeb` or `ProtocolGRPCWebText` ([gRPC-Web](#grpc-web)). |
| `(*Client).Invoke(ctx, CallOptions) (*Stream, error)` | Starts any kind of call. Fails only for invalid options (nothing sent) or a deadline already past. |
| `Stream.Send(msg)`, `CloseSend()` | Write a message; half-close. `Send` blocks under HTTP/2 flow control and returns `io.EOF` once the call has ended (then `Recv` has the reason). |
| `Stream.Recv() ([]byte, error)` | Next message; `io.EOF` at the end of an OK call, otherwise an error carrying the status. Repeats the same result afterwards. |
| `Stream.Header() (Metadata, error)` | Waits for the response headers. |
| `Stream.Trailer() Metadata`, `Stream.Status() (Status, bool)` | Available once `Recv` returned an error. |
| `Stream.Close()` | Cancels the call (RST_STREAM) if it is still running. Call it, or read until `Recv` fails. |
| `(*Client).Unary(ctx, o, msg) (*UnaryResult, error)` | Send one, receive one; the result (headers, message, trailers, status) is returned with the error too. |
| `Status{Code, Message, Details}`, `Code` (`OK` … `Unauthenticated`, `String()` gives `NOT_FOUND`), `StatusError`, `StatusFromError(err)` | The outcome. |
| `Metadata []MDEntry{Key, Value}`, `Get`, `IsBinaryKey`, `ReservedKey`, `ValidateMetadata` | Ordered metadata; `-bin` values hold the raw bytes. `ValidateMetadata` is the check a call makes before sending, also used by the gRPC mock server for the metadata it sends ([servers.md](servers.md#grpc-mock-server)). |
| `AppendFrame`, `NewFrameReader(r, max).Next()`, `NewWebFrameReader`, `AppendTrailerFrame`, `NewWebTextReader`, `WebContentType`, `EncodeTimeout`/`ParseTimeout`, `EncodeStatusMessage`/`DecodeStatusMessage`, `EncodeBinaryValue`/`DecodeBinaryValue`, `CodeForHTTPStatus` | Wire helpers, shared with the test server and the gRPC mock server ([servers.md](servers.md#grpc-mock-server)). |

All four call kinds use the same `Stream`: unary and server streaming send
one message and `CloseSend`; client streaming and bidi send any number;
reading and writing may happen from two goroutines. `GRPCService` drives
it that way for client-streaming and bidi calls (GRPC-13, GRPC-14): the
call's operation reads (`Header`, then `Recv` until it fails) from the
start, while `SendMessage` and `CloseSend` bound calls write from other
goroutines, one at a time ([grpc.md](grpc.md#streams-from-the-client)).

## What goes on the wire

| Field | Value |
|---|---|
| `:method`, `:scheme`, `:path` | `POST`, `http` (h2c) or `https`, the method name as given |
| `:authority` | `Authority`, else `Address`. It does not change the TLS server name. |
| `content-type` | `application/grpc`, or `application/grpc+<ContentSubtype>` |
| `te` | `trailers` |
| `grpc-timeout` | Only with a deadline: the earlier of `Timeout` and ctx's, measured when the call starts. |
| `user-agent` | Only when `UserAgent` is set. net/http's `Go-http-client/2.0` is suppressed, as in httpclient. |
| `grpc-encoding`, `grpc-accept-encoding` | `gzip`, only with `Compression: "gzip"`. |
| metadata | Every entry, in order within a key. The order *between* different keys is not kept: `http.Header` is a map and the HTTP/2 client writes it in map order. |

Nothing else is added: no `accept-encoding`, no `content-length` (the body
is a stream).

**Metadata rules** (checked before sending, `invalid_argument`, details
`row` and `key`): keys are 1+ characters of `a-z 0-9 - _ .` (upper case is
rejected rather than silently lowered); keys starting with `grpc-` and
`content-type`, `te`, `user-agent`, `host`, `content-length`, `trailer` and
the connection-level fields are reserved (`grpctransport.reserved_metadata`);
values of ordinary keys are printable ASCII; `-bin` values are any bytes,
sent as unpadded standard base64. Received `-bin` values are decoded padded
or unpadded, each comma-separated element as its own entry; a value that is
not base64 is kept as received rather than failing the call.

**grpc-timeout** uses at most 8 digits and the finest unit that fits
(`n`, `u`, `m`, `S`, `M`, `H`), rounding up so the server never sees a
shorter deadline: 150 ms is `150000u`, 2 minutes `120000m`.

## Framing

Each message is a 1-byte compressed flag, a 4-byte big-endian length and
the payload. `FrameReader` reads with `io.ReadFull`, so frames split across
DATA frames or reads in any way are reassembled (tested with 3-byte chunks
and `iotest.OneByteReader`; `FuzzFrameReader` checks that anything accepted
re-encodes to the input).

- **Receive limit:** `MaxRecvMessageSize`, default 4 MiB as in grpc-go,
  negative for none, overridable per call. It is checked on the declared
  length before reading and again after decompression; over it the call
  ends with `RESOURCE_EXHAUSTED` and the stream is reset. Payloads over
  1 MiB are read incrementally, so a lying length never allocates up front.
- **Flag:** 0 or 1; anything else is `INTERNAL`. gRPC-Web readers also
  accept the trailer bit (0x80, 0x81).
- **Compression** (GRPC-24): gzip only, with `compress/gzip`.
  `Compression: "gzip"` compresses every message sent (flag 1) and sends
  `grpc-encoding: gzip` and `grpc-accept-encoding: gzip`. Received messages
  flagged compressed are decompressed when the response's `grpc-encoding`
  is `gzip`, whatever the call's own setting. The flag with no or
  `identity` encoding is `INTERNAL` (`grpctransport.compression`); any
  other encoding is `UNIMPLEMENTED` with `grpctransport.unsupported_encoding`,
  detail `encoding` and a hint to configure the server for gzip or none.
  The receive limit applies after decompression.

## Status

| Situation | Status | Error code |
|---|---|---|
| `grpc-status` in the trailers | as sent; `grpc-message` percent-decoded (a `%` without two hex digits is kept); `grpc-status-details-bin` base64-decoded into `Details`, not parsed (`internal/core/grpc/rpcstatus` decodes the google.rpc.Status, [grpc.md](grpc.md#status-and-error-details)) | `grpctransport.status` (non-OK) |
| Trailers-only response (status in the headers, no body) | as sent; `Trailer()` holds the headers. Accepted with any content-type. | `grpctransport.status` |
| HTTP status other than 200 | 400 `INTERNAL`, 401 `UNAUTHENTICATED`, 403 `PERMISSION_DENIED`, 404 `UNIMPLEMENTED`, 429/502/503/504 `UNAVAILABLE`, else `UNKNOWN` (gRPC's HTTP mapping); any `grpc-status` is ignored, as in grpc-go | `grpctransport.http_status`, detail `http_status` |
| 200 without `application/grpc[+…\|;…]` | `UNKNOWN` | `grpctransport.not_grpc` |
| Body ended with no trailers / trailers without `grpc-status` | `INTERNAL` / `UNKNOWN` (grpc-go's choices) | `grpctransport.missing_status` |
| `grpc-status` not a number | `INTERNAL` | `grpctransport.status` |
| Body ended inside a message, bad flag | `INTERNAL` | `grpctransport.malformed_frame` |
| Message over the limit | `RESOURCE_EXHAUSTED` | `grpctransport.recv_too_large` |
| Compressed message that cannot be decoded (corrupt gzip, the flag without an encoding) | `INTERNAL` | `grpctransport.compression` |
| Compressed message in an encoding other than gzip | `UNIMPLEMENTED` | `grpctransport.unsupported_encoding`, detail `encoding` |
| gRPC-Web: 200 without `application/grpc-web[-text][+…\|;…]` | `UNKNOWN` | `grpctransport.not_grpc`, hint `HintWeb` |
| gRPC-Web: body ended without a trailer frame / a trailer frame without `grpc-status` | `INTERNAL` / `UNKNOWN` | `grpctransport.missing_status` |
| gRPC-Web: a malformed trailer frame, text body that is not Base64 | `INTERNAL` | `grpctransport.malformed_frame` |
| RST_STREAM from the server | by HTTP/2 code as grpc-go: `REFUSED_STREAM` `UNAVAILABLE`, `CANCEL` `CANCELLED`, flow control / `ENHANCE_YOUR_CALM` `RESOURCE_EXHAUSTED`, `INADEQUATE_SECURITY` `PERMISSION_DENIED`, else `INTERNAL` | `grpctransport.stream_reset` |
| Call deadline passed (client side) | `DEADLINE_EXCEEDED` | `grpctransport.deadline_exceeded` |
| ctx cancelled or `Close` | `CANCELLED` | `grpctransport.cancelled` |
| Dial failed (netx) | `UNAVAILABLE` (`DEADLINE_EXCEEDED`, `CANCELLED`, `PERMISSION_DENIED` for those kinds) | the netx code, message and hint unchanged |
| TLS handshake or certificate | `UNAVAILABLE` | `netx.tls_*` or `grpctransport.tls_client_cert_*` (kind `tls`), see [Transport security](#transport-security) |
| Server reset the connection (TLS calls) | `UNAVAILABLE` | `netx.connection_reset` (kind `network`); a plaintext call only sees "client conn could not be established" and stays `connection_failed` |
| Server does not speak HTTP/2 (TLS without ALPN h2, an HTTP/1 answer to h2c) | `UNAVAILABLE` | `grpctransport.not_http2` |
| Other connection failures | `UNAVAILABLE` | `grpctransport.connection_failed`; for a plaintext call the hint suggests turning TLS on (a TLS server drops an h2c preface without answering) |
| Unary call with zero or several response messages | `INTERNAL` | `grpctransport.cardinality` |

Every error after `Invoke` is an `*errs.Error` wrapping a `*StatusError`
(`StatusFromError`), with details `grpc_status` (`NOT_FOUND`) and
`grpc_code` (`5`). The kind is `timeout` for `DEADLINE_EXCEEDED`,
`cancelled` for `CANCELLED`, `network` for `UNAVAILABLE` and `protocol` for
every other server status; local failures keep their own kind (`tls`,
`timeout`). `errors.Is(err, context.Canceled)` and
`errors.Is(err, context.DeadlineExceeded)` hold for the context cases.
Options errors from `Invoke` are `invalid_argument` (`unsupported` for an
unknown compression) with codes `invalid_address`, `invalid_method`,
`invalid_metadata`, `reserved_metadata` and `invalid_option` (detail
`field`).

## gRPC-Web

GRPC-25. `Protocol: ProtocolGRPCWeb` or `ProtocolGRPCWebText` calls
a gRPC-Web endpoint (an Envoy or other gRPC-Web proxy, or a server with
gRPC-Web support) the way a browser's gRPC-Web client does, over a second
`http.Transport` of the client that speaks HTTP/1.1, and HTTP/2 when TLS
ALPN picks it. The dialer, TLS settings and client certificate are the
same as for gRPC.

| | gRPC-Web binary | gRPC-Web text |
|---|---|---|
| `content-type` | `application/grpc-web+proto` (`+<ContentSubtype>`) | `application/grpc-web-text+proto` |
| `accept` | `application/grpc-web` | `application/grpc-web-text` |
| Body | the frames | the frames in standard Base64 |

Also sent: `x-grpc-web: 1`, `grpc-timeout`, `grpc-encoding` and
`grpc-accept-encoding` with gzip, `user-agent` when set, and the metadata;
not `te`. Field names are sent in their canonical form over HTTP/1.1.

- **Half-duplex.** Browsers cannot stream a request, so neither does
  gRPC-Web: `Send` collects the frames and `CloseSend` sends the request
  with a `Content-Length`; `Header` and `Recv` wait for it. The transport
  does not refuse several messages (a proxy may pass them on as a client
  stream); the app refuses client-streaming and bidi methods
  (`grpc.web_client_streaming`, [grpc.md](grpc.md#calls)). A call closed or
  past its deadline before `CloseSend` never reaches the server and ends
  `CANCELLED` or `DEADLINE_EXCEEDED`.
- **Response.** The response's content type decides the framing
  (`application/grpc-web` or `application/grpc-web-text`, with `+subtype`
  or parameters). A text body is decoded as it arrives
  (`NewWebTextReader`): every write of the server may be padded on its own,
  so padding in the middle is accepted, and whitespace is skipped. Messages
  are frames as in gRPC; the frame with the trailer bit (0x80) holds the
  trailers as `key: value` lines (CRLF, a bare LF accepted) and ends the
  call: its `grpc-status`, `grpc-message` and `grpc-status-details-bin`
  give the status as for gRPC, and the other lines are the `Trailer()`
  metadata. Anything after the trailer frame is ignored. A trailers-only
  response (status in the headers) works as for gRPC. A compressed trailer
  frame (0x81) is decompressed like a message.

## Transport security

GRPC-23. The mode is per call; the TLS settings are per `Client`
(they belong to the `http.Transport`), so one client can make plaintext and
TLS calls.

| Mode | How | Notes |
|---|---|---|
| Plaintext (h2c) | `CallOptions.TLS` false | HTTP/2 with prior knowledge through `http.Protocols.SetUnencryptedHTTP2`; no HTTP/1.1 upgrade. |
| TLS | `CallOptions.TLS` true | ALPN offers only `h2`. Verification against the system roots by default; custom CAs added to them or replacing them; server name override; TLS 1.2+ (or 1.3+). |
| mTLS | TLS plus a client certificate in `tlsconf.Options`, or `Options.Transport.ClientCertificate` | PEM certificate (with optional intermediates) and unencrypted PEM key, from files or pasted; or a per-host client certificate (AUTH-30: PEM, encrypted PKCS#8 or PKCS#12) loaded by `tlsconf.ClientCerts`. |

`NewWithTLS` runs `tlsconf.Build` ([http-client.md#tls-options](http-client.md#tls-options):
options, validation codes, warnings) and sets `Options.Transport.TLSConfig`.
Callers that build the config themselves may set `TLSConfig` and call `New`.
The server name override changes SNI and the verified name only;
`:authority` still comes from `Authority` or `Address`.

**Detecting mTLS failures.** Servers rarely say why a handshake failed,
and with TLS 1.3 the client's side of the handshake is complete before the
server checks the certificate: the alert arrives on the first read and
net/http reports only "client conn could not be established", a broken pipe
or a reset. So `New` wraps the transport's `GetClientCertificate` (keeping
the configured certificate) to note, in the call's context, that the server
asked for a certificate. A round trip that then fails before any response
is reported as a client certificate problem. Alerts that do arrive are
used directly.

The server certificate classes come from `netx.Classify`, so the codes are
the ones every protocol client uses (DBG-09); the Go constants
(`CodeTLSUnknownAuthority`, …) equal them. Messages and hints stay gRPC's,
which name its server name setting and h2c.

| Situation | Code (kind `tls`, status `UNAVAILABLE`) | Hint |
|---|---|---|
| Server certificate not from a trusted CA (also the macOS verifier's "certificate is not trusted") | `netx.tls_unknown_authority` | add the CA, or turn verification off |
| Certificate not valid for the host / server name | `netx.tls_hostname_mismatch` | connect by a certificate name, or set the server name |
| Certificate expired or not yet valid | `netx.tls_certificate_expired` | check the certificate and the clock |
| Other verification failures | `netx.tls_certificate_invalid` | |
| Server asked for a client certificate and got none (alert `certificate required`; TLS 1.2 `bad certificate` or `handshake failure` after a request; TLS 1.3 dropped connection) | `grpctransport.tls_client_cert_required` | configure a client certificate and key |
| Server refused the configured certificate (same signals with a certificate configured) | `grpctransport.tls_client_cert_rejected` | CA trusted by the server, validity, client-auth usage |
| TLS chosen, server answered in plaintext (`tls.RecordHeaderError`, `http.ErrSchemeMismatch`) | `netx.tls_server_not_tls` | turn TLS off or check the port |
| TLS chosen, no `h2` in ALPN | `grpctransport.not_http2` | |
| Any other TLS failure (versions, ciphers, other alerts) | `netx.tls_handshake_failed` (`CodeTLSFailed`) | |
| Plaintext chosen, TLS server | `grpctransport.connection_failed` (kind `network`) | "If the server uses TLS on this port, turn TLS on." |

### Persisting TLS settings

Decisions for the gRPC request type and the settings wiring:

- **In the project file** (shareable): `tls.enabled` (the call mode),
  `insecureSkipVerify`, `serverName`, `minVersion`, `replaceSystemRoots`,
  `caFiles` and `caPem`. CA certificates are public; `caFiles` should be
  relative to the project root (the `baseDir` passed to `Build`) so a
  committed development CA works for everyone.
- **Never in the project file:** anything about the client certificate.
  Its paths point at private material and differ per machine. The
  per-host client certificates (AUTH-30) are app settings
  (`network.clientCerts`, absolute paths) with their passphrases in the
  app's local secret storage (`<data>/secrets/app.json`), and apply to gRPC
  TLS calls like to every other protocol
  ([network.md](network.md#client-certificates)); a per-request override
  is not offered.
- **Key contents are never persisted anywhere**: only paths. `ClientKeyPEM`
  has no JSON name, so it cannot be saved by accident; a pasted key lives in
  memory for the session only. Password-protected keys and PKCS#12 are
  read for the per-host client certificates only (`tlsconf.LoadClientCert`,
  [pkcs12.md](pkcs12.md)).
- **Clients:** build one `Client` per distinct (`tlsconf.Options`,
  `baseDir`) and reuse it; files are read when it is built, so a changed
  certificate file takes effect when the settings change or the client is
  rebuilt. Show `Result.Warnings` next to the response.

## Test server

`internal/core/grpc/transport/grpctest` (test support only) runs
`net/http`'s server through `httptest`. `NewServer(tb, withTLS)` or
`NewServerWith(tb, ServerOptions{Security, CA, ServerCert, ClientCA,
ClientAuth, MaxVersion})`:

- `H2C`: `Protocols` with `UnencryptedHTTP2` (and HTTP/1.1).
- `TLS`: ALPN h2 with a certificate for localhost, 127.0.0.1 and ::1 from
  a CA generated at test time (`Server.CA`, `Server.RootCAs`); `ServerCert`
  replaces it (other names, expired).
- `MTLS`: TLS requiring a client certificate verified against a generated
  client CA (`Server.ClientCA`, with a valid `Server.ClientCert`).
  `PeerCertificates()` returns what the latest request presented.

Certificates come from `internal/core/tlsconf/tlstest`; no private key is
committed.

- Echo methods: `MethodUnary`, `MethodServerStream` (`x-count` copies),
  `MethodClientStream` (concatenation), `MethodBidi` (echo as they arrive),
  `MethodStatus` (status from `x-status-code`, `x-status-message`,
  `x-status-details-bin`; `x-trailers-only: 1`), `MethodSleep`. Every `x-`
  metadata entry comes back in the headers and, prefixed `t-`, in the
  trailers.
- `Handle(method, func(*ServerStream) Status)`: `Recv`, `Send`, `SendRaw`,
  `SetHeader`, `SetTrailer`, `SendHeader`, `Context()` (done at the
  `grpc-timeout` deadline, parsed into `Timeout`). Status goes in trailers
  (`http.TrailerPrefix`), or as a trailers-only response when nothing was
  sent. Unknown methods answer `UNIMPLEMENTED`.
- `HandleHTTP(method, http.HandlerFunc)` for protocol violations, with
  `HTTPStatus(code)`, `BadContentType()`, `Frames(body, chunk, header,
  trailer)` (any bytes, flushed in chunks, optional trailers) and
  `FrameHeader(flag, length)`, `Gzip`.
- `LastRequest()` returns the headers of the latest request, for wire
  assertions.
- gRPC-Web: every handler also answers `application/grpc-web[-text]`
  requests, over HTTP/1.1 (h2c servers accept it) or HTTP/2 (TLS), with the
  status in a trailer frame (or trailers-only) and a Base64 body for text;
  `ServerOptions.HTTP1Only` serves HTTP/1.1 only (TLS without h2 in ALPN).
  The request body is read whole first (an HTTP/1.1 handler cannot read it
  after writing). `ServerStream.Web()` tells the handler which it got.
- gzip: compressed requests are read; responses are compressed when the
  request was (as grpc-go does), or as the `x-response-encoding` metadata
  (`gzip`, `identity`) says, with `grpc-encoding: gzip`.
- `EnableReflection(ReflectionOptions)` serves server reflection v1 and
  v1alpha from FileDescriptorProto bytes; `ReflectionRequests()` lists what
  was asked. See [grpc-reflection.md](grpc-reflection.md#test-server).

Tests cover all four call kinds over h2c and TLS, metadata including
`-bin`, trailers with status and custom metadata, trailers-only errors,
percent-encoded messages, client deadlines and the `grpc-timeout` format,
cancellation mid-stream (the server sees the reset), `Close` unblocking a
`Send` stuck on flow control, receive limits (per client, per call, after
gzip), HTTP 404/503/401/500 mapping, the malformed-response cases above, a stream reset,
frames split across reads, and connection, TLS and non-HTTP/2 failures;
all under `-race`. `web_test.go` makes gRPC-Web binary and text calls
against plaintext HTTP/1.1, TLS h2 and TLS HTTP/1.1 servers: unary (the
wire headers and HTTP version), server streaming, status errors with
details in a trailer frame and trailers-only, deadlines, a call cancelled
before `CloseSend`, half-duplex client streaming, gzip both ways, and the
malformed cases (gRPC content type, no trailer frame, a trailer frame
without status or malformed, invalid Base64, truncation, HTTP 404);
`TestGzipBothDirections` checks the compressed flag and gzip payload on
the wire and gzip replies to compressed and uncompressed gRPC requests,
and `TestUnsupportedEncoding` the `zstd` error for both protocols.
`web_internal_test.go` covers the Base64 reader (padded segments, line
breaks, one byte at a time, invalid input), the trailer frame and its
parser, and the content types. Security tests (`security_test.go`) make unary, server
streaming and bidi calls over h2c, TLS with a custom CA (added to the
system roots, and as the only root from a relative file path) and mTLS
with TLS 1.2 and 1.3 (certificate files), one client mixing plaintext and
TLS calls, and check each failure code: no CA, wrong CA, wrong server
name, the server name override (SNI seen by the server), expired server
certificate, verification off, missing, untrusted and expired client
certificates on TLS 1.2 and 1.3, TLS to an h2c port and plaintext to a TLS
port. `tlsconf` has its own tests for every validation code, key formats
(PKCS#8, PKCS#1, SEC 1), combined and chained PEM, warnings and JSON.

## Limits and later work

- Timing and connection info (httptrace, as in httpclient's `Timing`) is
  not collected yet.
- Client certificates come from the per-host list (AUTH-30): the app's
  `GRPCService` (`WithClientCerts`) looks up the call's address (port 443
  when it has none) for TLS calls only and keeps one transport client per
  certificate (at most 32 clients, then the cache starts over); a matching
  entry that cannot be loaded fails the call before it connects.
  `TestPerHostClientCertificate` (PEM, Windows 3DES and Ed25519 PKCS#12,
  TLS 1.3 and 1.2) and the service's `TestInvokeClientCertificates` cover
  it. The request editor sets the mode and verification only;
  custom CAs and the server name in the request are still to come. The app-wide custom CA bundles (NET-04) already apply: the app's
  `GRPCService` (`WithTrust`) gives TLS calls `RootCAs` with them, keyed
  per pool like its other client settings; h2c calls ignore them
  ([network.md](network.md#custom-ca-certificates)).
- gRPC-Web endpoints are reached at `host:port` with the method path;
  a path prefix before the method (a proxy mounted under `/api/`) is not
  supported yet.
- Reflection and the gRPC request type build on this package.
