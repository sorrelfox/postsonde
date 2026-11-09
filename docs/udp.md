# UDP

PostSonde sends UDP datagrams to `host:port` and shows the datagrams that
come back, each with its sender, in the message log (UDP-01): text with
`{{variables}}`, hex or Base64. Datagrams go through the proxy in effect
like every connection: a SOCKS5 proxy relays them with UDP ASSOCIATE
(SOCKS-07, RFC 1928 section 7); SOCKS4 and HTTP proxies cannot carry UDP,
and the request fails rather than going direct.

```
UdpEditor ─ commands/udpSessions ─ lib/udp ──▶ UDPService.Open/Send/Close
   ▲              │ MessageSource ×2               │ {{variables}} (request scopes)
   │              ◀── udp:state, udp:data ─────────┤ stream.Batcher per socket
MessageLog                                         ▼
               udpclient.Open ── netx.Dialer ("udp") ──▶ Direct: a UDP socket to host:port
                                                    └──▶ SOCKS5: TCP control connection + UDP ASSOCIATE
                                                          ══▶ relay ══▶ host:port
```

| Piece | Where |
|---|---|
| File format (kind `udp`) | [file-format.md](file-format.md#udp-request-kind-udp), `internal/core/storage/projectfile/udp.go` (`UDPRequest`, `UDPSettings`) |
| UDP through the dialer | `internal/core/netx/udp.go` (`DatagramConn`, `UnconnectedUDP`, `MaxDatagramPayload`) |
| SOCKS5 UDP ASSOCIATE | `internal/core/netx/socks5udp.go`; test proxy `netxtest.SOCKS5Server` |
| Client | `internal/core/realtime/udpclient` |
| Bound service and events | `internal/app/services/udp` (`UDPService`) |
| Binding wrappers | `frontend/src/lib/udp.ts` |
| Sessions (socket state and log per request) | `frontend/src/commands/udpSessions.ts`, `useUdpSession.ts` |
| Editor | `frontend/src/components/editor/UdpEditor.tsx` (the TCP editor's styles) |

## The request file

A `udp` request stores `address` (`host:port` with `{{variables}}`),
`sendMode` (`text`, `hex` or `base64`), the composer's `message` and
`settings`: `connected`, `ipVersion` (`ipv4`, `ipv6` or either),
`maxDatagramBytes` and `openTimeoutMs`. Defaults are written as missing
members; unknown members and values are kept. The editor auto-saves every
change through `CollectionService.UpdateRequest`; in the `RequestDTO` the
settings are the `udp` member (`UDPOptionsDTO`), their unknown members
`settings.extra`. `CreateRequest` accepts kind `udp`, and the New request
dialog and the scratch menu offer it. The kind is new in this version
without a schema change: an older PostSonde shows the file as one of an
unknown kind and leaves it alone.

## Through the dialer: `netx`

A `"udp"`, `"udp4"` or `"udp6"` dial returns a `netx.DatagramConn`: a
`net.Conn` whose `Write` sends one datagram to the dialed address and whose
`ReadFrom` returns one datagram and its sender. The guard, the route and
the connection log apply as to TCP ([network.md](network.md)); the log
keeps `ReadFrom` visible (it counts the bytes too).

- **Direct, connected** (the default). `net.Dialer` with `"udp"`: the
  name is resolved, `Options.Guard` checks the address, and the socket is
  connected: it receives only from the target, and an ICMP "port
  unreachable" for a datagram arrives as `ECONNREFUSED` on the next read
  (`netx.IsPortUnreachable`). On Windows it arrives as `WSAECONNRESET`,
  and only because netx turns that report back on for a connected socket:
  Go turns it off for every UDP socket there (`SIO_UDP_CONNRESET`), since
  an unconnected one would fail its reads for any peer that went away.
- **Direct, unconnected** (`netx.UnconnectedUDP(ctx)`). Dialed as above
  (so resolution and the guard are the same), then replaced by an
  unconnected socket bound to the same local IP address and a port the OS
  picks (`listen.ListenPacket`): it sends to the target and receives from
  anyone, for servers that answer from another address or port (TFTP,
  some DNS setups). Bound to that one address, a loopback target stays
  loopback.
- **Buffers.** UDP sockets get 256 KiB send and receive buffers (macOS
  limits a datagram to the send buffer, 9 KiB by default); the UDP server
  type does the same for its sockets.
- **Limits.** `netx.MaxDatagramPayload(conn)`: 65,507 bytes to an IPv4
  address, 65,527 to an IPv6 one, less the SOCKS header through a proxy.

### SOCKS5 UDP ASSOCIATE

SOCKS-07. A UDP dial through `netx.SOCKS5`:

1. resolves the target as for CONNECT: locally with `socks5`, not at all
   with `socks5h` (the name goes in every datagram's header);
2. opens the TCP control connection to the proxy through the forward
   dialer, runs the greeting and authentication (none or RFC 1929), and
   sends `UDP ASSOCIATE` with the address all zeros (the client does not
   know where it will send from);
3. reads the reply: `BND.ADDR:BND.PORT` is the relay. An unspecified
   address (`0.0.0.0`, `::`) means the proxy's own address, as most
   servers mean it;
4. dials the relay through the forward dialer (always connected) and
   returns a `DatagramConn` whose `Via` is the proxy.

Each datagram sent is `RSV RSV FRAG=0 ATYP DST.ADDR DST.PORT DATA` (10
bytes of header for IPv4, 22 for IPv6, 7 plus the name for a host name);
each one received is parsed back, and `ReadFrom` returns the sender the
relay names (a `*net.UDPAddr`, or `netx.HostPortAddr` for a name).

- **The control connection** stays open for the life of the socket: the
  association ends with it (RFC 1928). When the proxy closes it, reads and
  writes fail with `netx.socks_udp_association_closed`; `Close` closes it
  and the relay socket and waits for the watcher goroutine, so nothing
  leaks.
- **Fragments are not reassembled.** A datagram with `FRAG` other than 0
  is dropped: `ReadFrom` returns `netx.socks_udp_fragment_dropped`, for
  which `netx.IsDroppedDatagram` is true, and the next read goes on. A
  datagram without a valid header is dropped the same way
  (`netx.socks_udp_malformed`). The client shows both as notes.
- **Connected through the proxy.** Everything comes from the relay, so a
  connected request drops datagrams the relay reports from another sender
  than the target, when the target is an IP address or was resolved here
  (`socks5`); with `socks5h` and a name every datagram is read.
- **The hop.** `ProxyHop.Command` is `UDP ASSOCIATE` and `RelayAddr` the
  relay; the trace and the UI show it like other protocols' hops
  (SOCKS-12): "SOCKS5 proxy.lan:1080 (10.0.0.1:1080), UDP ASSOCIATE, relay
  10.0.0.1:40000, auth none, remote DNS, reply 0 succeeded, …".
- **Errors.** As for CONNECT ([network.md](network.md#errors)); reply 7 is
  "The SOCKS proxy at P does not support UDP ASSOCIATE." with a hint that
  names `ssh -D` (which never relays UDP). A relay that cannot be reached
  is `netx.socks_udp_relay_unreachable`.

## Client: `udpclient`

```go
conn, err := udpclient.Open(ctx, udpclient.Options{
	Address:         "localhost:5353",
	Dialer:          dialer,      // netx.Dialer, required
	Network:         "udp",       // or "udp4", "udp6"
	Connected:       false,       // true: the target only, ICMP errors as notes
	OpenTimeout:     0,           // DNS + the proxy's association; 0: 10 s; < 0: none
	MaxDatagramSize: 0,           // 0: 65,535; larger sends refused, longer receives cut
	OnState:    func(ch udpclient.StateChange) { ... },
	OnDatagram: func(d udpclient.Datagram) { ... },
	OnNote:     func(n udpclient.Note) { ... },
})
err = conn.Send([]byte("ping"))  // one datagram; empty is allowed
err = conn.Close()               // waits until closed
```

- **States.** `opening` (resolving, associating), `open`, `closing`,
  `closed`. Nothing ends a UDP socket from the other side, except a proxy
  that ends the association. `StateChange` carries the addresses, `Via`,
  the proxy hop, `Connected` and `MaxPayload` from `open` on.
- **Datagrams.** Each `Datagram` is one datagram: `Data`, `Size`,
  `Truncated` (longer than `MaxDatagramSize`, cut), `Peer` (the sender, or
  the target of a sent one) and `At`. A sent one is reported just before it
  is written. Callbacks are serialized, in order.
- **Notes** do not close the socket: a datagram the relay sent fragmented
  or malformed, or `udpclient.port_unreachable` for a connected socket
  that got ICMP "port unreachable" (nothing listens there; it may later).
- **Send** refuses a datagram over `MaxPayload` with
  `udpclient.datagram_too_large` (also when the OS says `EMSGSIZE`). A
  failed write is returned and the socket stays open.

| Code | Kind | When |
|---|---|---|
| `udpclient.invalid_address` | `invalid_argument` | Not `host:port`, as for TCP. |
| `udpclient.invalid_options` | `invalid_argument` | A network other than `udp`/`udp4`/`udp6`, a maximum outside 1–65,535. Details `field`, `value`. |
| `udpclient.family_mismatch` | `invalid_argument` | An IP address of the other version than the one asked for. |
| `udpclient.not_open` | `invalid_argument` | `Send` when not open. |
| `udpclient.datagram_too_large` | `invalid_argument` | See above. Details `size`, `max`. |
| `udpclient.port_unreachable` | `network` | A note: ICMP "port unreachable". |
| `udpclient.read_failed`, `udpclient.write_failed` | `network` or `timeout` | Other socket errors. |
| `netx.*` | from netx | Dial and proxy errors, unchanged. |

## Service: `UDPService`

| Method | Does |
|---|---|
| `Open({requestId, address, ipVersion, connected, maxDatagramBytes, openTimeoutMs})` | Renders the address's `{{variables}}` with the request's scopes (as an HTTP send does), checks it and the options, and returns `{connId, requestId, address}` at once; opening runs in the background, marked as the user's (PRV-05). Unknown variables fail with `template.unresolved`, an IP version other than `ipv4`/`ipv6`/"" with `udp.invalid_ip_version`. |
| `Send({connId, mode, text})` | `text`: UTF-8 with `{{variables}}` rendered; `hex`: `hexbytes.Parse`; `base64`: the WebSocket composer's Base64 rules (`ws.invalid_base64`). Returns `{bytes}`. `udp.not_found`, `udp.not_open`, `udp.invalid_mode`. |
| `Close(connId)`, `CloseAll()` | As for TCP; sockets are also closed when the app quits and when the workspace changes. |

Events, batched per socket like TCP's (50 ms or 16 datagrams):

| Event | Payload |
|---|---|
| `udp:state` | `StateEventDTO{connId, requestId, address, seq, state, at, localAddr, remoteAddr, via, proxy, connected, maxPayload, close, error}`; `proxy` is the hop (`scheme`, `version`, `proxy`, `proxyAddr`, `command`, `relayAddr`, `auth`, `remoteDns`, `reply`, `replyText`, `handshakeMs`) on `open`, and on `closed` when the proxy refused. |
| `udp:data` | `DataEventDTO{connId, requestId, seq, datagrams}`, each `{direction, at, data, size, truncated, peer, error}`; `direction` is `sent`, `received` or `note` (with `error`). |

## UI

`TabPanel` shows `UdpEditor` for a request of kind `udp`; the tree and the
tab show a `UDP` badge.

- **Address bar.** `UDP`, the address (auto-saved; Enter opens) and Open,
  Cancel (while opening) or Close. The status line: "Ready to send to
  127.0.0.1:5353 · replies from any sender · 2 datagrams sent · 2
  received", "… through socks5h://proxy:1080 …" with a proxy, or the
  error with its hint.
- **Socket options.** Receive from (Any sender, Target only (connected)),
  IP version (Either, IPv4 only, IPv6 only) and Max datagram (empty for
  65,535; an invalid number is marked and not saved). They apply when
  opening, so they are disabled while a socket is active.
- **Log.** The shared `MessageLog`, as Text or Hex (two `MessageSource`s,
  as for TCP). Each datagram is one message, decoded on its own; received
  ones have their sender as the badge (", cut" when truncated) and in the
  details. The open message names the proxy, `UDP ASSOCIATE` and the relay,
  with the hop in its details. Notes are system messages of kind
  `dropped`.
- **Composer.** Text, Hex or Base64 (auto-saved as `sendMode`), the size,
  and Send or ⌘Enter. Hex and Base64 are checked while typing; a datagram
  over the socket's maximum is marked and Send disabled (text with
  `{{variables}}` is checked by Go when sent).
- **Sessions.** `commands/udpSessions.ts`, as `tcpSessions.ts`: one per
  request, kept across tab switches, closed with the tab.

## Tests

- `internal/core/netx` (`udp_test.go`): direct connected and unconnected
  sockets (a 60 KB datagram, a reply from another port only the
  unconnected one reads, the guard on an unconnected dial), UDP ASSOCIATE
  through `netxtest.SOCKS5Server` with socks5h (the name in the header),
  socks5 with authentication and a relay on the proxy's unspecified
  address, the connected filter, a fragment dropped, header parsing, a
  proxy without UDP (reply 7), the association ended by the proxy and by
  `Close` (no association left), SOCKS4 and HTTP proxies refusing UDP.
- `internal/core/realtime/udpclient`: echo with the in-repo UDP server
  (SSRV-06) connected, unconnected, over IPv4 by name and over IPv6, peers,
  through the SOCKS5 test proxy, fragment notes, ICMP port unreachable,
  size limits and truncation, invalid options, a refusing proxy, the
  association ending, cancel and timeout, and no goroutine or association
  left after many open/close cycles.
- `internal/app/services/udp`: echo with variables, hex and Base64 through
  events (order, `seq`, peers, totals), through the proxy (`via`, the hop),
  a refusing proxy, input errors, send errors, close while opening,
  `CloseAll` and the workspace hook, golden event JSON.
- `internal/app` `TestEveryProtocolThroughProxy`: UDP through SOCKS5
  (socks5h and socks5 with auth) end to end through the app's dialer, with
  only the proxy and its relay dialed; refused by SOCKS4a and HTTP.
- projectfile golden example and defaults, collection create/auto-save
  round trip with a golden `RequestDTO`.
- Frontend: `commands/udpSessions.test.ts`, `components/editor/UdpEditor.test.tsx`.

## Not checked automatically

- A real SOCKS5 server with UDP ASSOCIATE (Dante with `udpassociate`
  allowed, 3proxy): the test proxy is in process. `ssh -D` does not relay
  UDP at all: it answers reply 7, shown as "does not support UDP
  ASSOCIATE".
- Proxies behind NAT that name a relay address the client cannot reach
  (`netx.socks_udp_relay_unreachable`, or datagrams that never come back).

## Not yet

- Reassembling SOCKS UDP fragments, multicast and broadcast, binding a
  chosen local port, saved datagrams.
