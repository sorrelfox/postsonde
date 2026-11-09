# Network connections

Every connection PostSonde opens goes through one dialer interface,
`netx.Dialer`, in `internal/core/netx`. Proxies (SOCKS5, SOCKS4, HTTP CONNECT), the
privacy guard and the connection timeline are applied there, so they apply
to every protocol the same way: HTTP/REST, GraphQL (including WebSocket and
SSE subscriptions), gRPC, WebSocket, Socket.IO, SSE, raw TCP/UDP and OAuth
token calls.

```
protocol client (HTTP, gRPC, WebSocket, TCP, OAuth ...)
        │  DialContext(ctx, "tcp", "api.example.com:443")      ctx carries the user op and the Trace
        ▼
  netlog.Log.Middleware  records the dial in the connection log (PRV-06), refused ones too
        │
  Guarded(RequireUserOp) refuses dials whose ctx has no user operation (permission_denied)
        │
  Routed(ProxyRouter)    picks a dialer per target: no proxy, loopback or on the
        │                bypass list → Direct, else the proxy of the active
        │                environment, the project, or the settings: the OS's,
        │                ALL_PROXY or a manual one (SOCKS-04, SOCKS-08, SOCKS-09,
        │                NET-01)
        │
        ├──▶ Direct ───────────────────────────▶ api.example.com:443
        │     resolves locally; Options.Guard checks each resolved IP;
        │     reports DialStart / DialDone
        │
        └──▶ SOCKS5, SOCKS4 or HTTP CONNECT ──▶ Direct ──▶ proxy:1080 ══▶ api.example.com:443
              target sent to the proxy unresolved (socks5h, socks4a, http); reaches
              the proxy through the Dialer it was given; reports the target
              hop. http:// requests of the HTTP client go to an HTTP proxy
              in absolute form instead (see HTTP proxy)
        ▲
  TLS is layered on the returned net.Conn by the protocol client (end to end)
```

## The interface

```go
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
```

Its method set is `net.Dialer`'s, so it plugs in wherever Go takes a dial
function: `http.Transport{DialContext: d.DialContext}`, gRPC's context
dialer, WebSocket client options. `TestDirectHTTPTransport` proves the HTTP
case.

Implementations must be safe for concurrent use, honour cancellation and
deadlines of `ctx`, pass `ctx` on to the dialer they wrap (the trace travels
in it), and return `*errs.Error` values (`netx.WrapDialError`).

## Rule for protocol clients

**Open connections only through a `netx.Dialer` passed to you.** Outside
`internal/core/netx`, non-test Go code must not use:

- `net.Dial`, `net.DialTimeout`, `net.DialTCP`, `net.DialUDP`, `net.DialIP`,
  `net.DialUnix` or `net.Dialer` (including `(&net.Dialer{}).Dial`),
- `http.DefaultClient`, `http.DefaultTransport`, or `http.Get`, `http.Head`,
  `http.Post`, `http.PostForm` (which use the default client),
- `tls.Dial`, `tls.DialWithDialer` or `tls.Dialer`,
- `net.DefaultResolver` or the `net.Lookup*` functions (`LookupHost`,
  `LookupIP`, ...): a name is resolved by the dialer that connects to it,
  so a lookup never happens without a user operation (PRV-05).

Build your own `http.Transport` (or client) with `DialContext` set to the
dialer. `TestNoDirectDials` in `internal/core/netx/rule_test.go` parses the
module's Go files and fails on any of these; test files are exempt (they may
use `httptest` and `net.Listen` freely). Listening has its own rule, see
[Listening](#listening). To look by hand:

```sh
git grep -nE '\bnet\.(Dial|Lookup|DefaultResolver)|http\.Default(Client|Transport)|tls\.Dial' -- '*.go' ':!*_test.go' ':!internal/core/netx/'
```

## Direct dialer

`netx.NewDirect(netx.Options{...})` wraps `net.Dialer`:

| Option | Zero value | Negative |
|---|---|---|
| `Timeout` (DNS + connect of one dial) | 30 s (`DefaultTimeout`) | no limit besides `ctx` |
| `KeepAlive` (TCP keep-alive period) | 30 s (`DefaultKeepAlive`) | keep-alives off |
| `FallbackDelay` (Happy Eyeballs, RFC 6555) | 300 ms (net default) | fallback off |
| `LocalAddr` | OS picks | |
| `Resolver` | `net.DefaultResolver` | |
| `Guard` (checks resolved IPs) | allow all | |
| `ControlContext` (raw socket hook) | none | |
| `Now` (clock for durations) | `time.Now` (monotonic) | |

A `ctx` that is already done fails at once, before any lookup and without
trace events.

## SOCKS5 proxy

SOCKS-01, SOCKS-02, SOCKS-03, SOCKS-13. `netx.SOCKS5` is an in-house SOCKS5
client (standard library only): the RFC 1928 greeting and CONNECT with IPv4,
IPv6 and domain-name addresses, and RFC 1929 username/password or "no
authentication".

```go
socks, err := netx.NewSOCKS5(netx.SOCKS5Config{
	Address:   "proxy.lan:1080",
	RemoteDNS: true,                    // socks5h: the proxy resolves host names
	Username:  "alice", Password: "…", // optional, RFC 1929
}, direct) // forward: how to reach the proxy; nil means NewDirect(Options{})
route := func(ctx context.Context, network, address string) (netx.Dialer, error) {
	if bypass(address) {
		return direct, nil
	}
	return socks, nil
}
d := netx.Chain(netx.Routed(route), connLog.Middleware(), netx.Guarded(netx.RequireUserOp(logger)))
```

| `SOCKS5Config` | Zero value | Notes |
|---|---|---|
| `Address` | required | the proxy's `host:port` |
| `RemoteDNS` | off (`socks5`) | on (`socks5h`): host names go to the proxy unresolved (ATYP 3); off: resolved locally with `Resolver`, the first address is sent |
| `Username`, `Password` | no authentication | at most 255 bytes each; a password needs a username |
| `Timeout` (greeting, auth and CONNECT) | 30 s (`DefaultTimeout`) | negative: no limit besides `ctx`; the forward dialer bounds the connect to the proxy |
| `Resolver` | `net.DefaultResolver` | local resolution, and service-name ports |
| `Now` | `time.Now` | clock for trace durations |

An invalid config fails `NewSOCKS5` with `invalid_argument` /
`netx.socks_invalid_config`. The app builds it from a proxy URL of its
settings or of `ALL_PROXY` ([Proxy settings](#proxy-settings)).

- **Where it sits.** Below `Routed`, so the connection log and the privacy
  guard (above it) see one dial: the target as the protocol client asked
  for it, whatever the route. The SOCKS5 dialer reaches the proxy through
  its forward dialer (normally `Direct`), which does not pass through the
  log or `Guarded` a second time; `Options.Guard` on that `Direct` sees the
  proxy's resolved IP, not the target's. A refusal from the forward
  dialer's guard is returned as is (with the `via` detail).
- **Methods.** Without a username it offers only "no authentication";
  with one it offers "no authentication" and username/password and lets
  the proxy choose.
- **Address types.** An IP literal is sent as IPv4 (ATYP 1) or IPv6
  (ATYP 4), with an IPv6 zone dropped and IPv4-mapped IPv6 sent as IPv4. A
  host name is sent as a domain name (ATYP 3, at most 255 bytes) with
  `RemoteDNS`, or resolved locally first (`tcp4`/`tcp6` pick the family); a
  failed local lookup fails the dial with the DNS codes before the proxy
  is contacted.
- **Networks.** `tcp`, `tcp4`, `tcp6` with CONNECT; `udp`, `udp4`, `udp6`
  with UDP ASSOCIATE (SOCKS-07, [udp.md](udp.md#socks5-udp-associate)):
  the control connection stays open for the socket's life and the
  datagrams go to the proxy's relay with the SOCKS UDP header. Others fail
  with `unsupported` / `netx.socks_unsupported_network`.
- **Cancellation and timeouts.** `ctx` cancels the handshake at any step
  (the connection's deadline is pulled in), giving `netx.dial_cancelled`
  (or `netx.dial_timeout` for a `ctx` deadline); a proxy that does not
  answer within `Timeout` gives `netx.socks_timeout`.
- **The tunnel.** The reply, including `BND.ADDR`, is read fully, so the
  returned connection starts at the target's first byte. Its `RemoteAddr`
  is the proxy's; it keeps `CloseWrite`/`CloseRead` of the TCP connection
  and has `Unwrap() net.Conn`. `netx.ViaOf(conn)` returns the proxy as
  `socks5h://host:port` (or `socks5://`), never with credentials, and looks
  through wrappers with `Unwrap() net.Conn`.
- **Trace.** It reports its own hop: `DialStart` with `Via` set before
  `Direct`'s events for the proxy connection, and `DialDone` (target,
  `Via`, the proxy as `RemoteAddr`, duration of connect plus handshake,
  and the hop's details in `Proxy`, [The proxy hop](#the-proxy-hop))
  after them.
- **Connection log.** `netlog.Conn.Via` is set from `ViaOf` for an open
  connection and from the error's `via` detail for a failed one;
  `RemoteAddr` is the proxy's.
- **Secrets.** The password is sent only to the proxy and appears in no
  error, detail or trace event.

`socks5_test.go` runs an in-process SOCKS5 server covering every address
type (and `BND.ADDR` type), both methods, a wrong password, a proxy that
wants credentials or accepts no offered method, every reply code (and
unknown ones), a proxy that is not SOCKS5 or hangs up, a hanging proxy with
cancel, `ctx` deadline and handshake timeout, and the trace. `netlog`'s
`TestRecordsProxiedDials` checks the log through a relaying proxy.

## SOCKS4 proxy

SOCKS-06. `netx.SOCKS4` is an in-house client for legacy SOCKS4
proxies and their SOCKS4a extension, for `socks4://` and `socks4a://`
proxy URLs:

```go
s, err := netx.NewSOCKS4(netx.SOCKS4Config{
	Address:   "legacy.lan:1080",
	RemoteDNS: true,  // socks4a: the proxy resolves host names
	UserID:    "bob", // the URL's user name; SOCKS4 has no password
}, direct)
```

- **The request.** `VN 4, CD 1` (CONNECT), the port, an IPv4 address, the
  user ID and a NUL. With `RemoteDNS` (socks4a) a host name goes to the
  proxy unresolved: the address `0.0.0.1`, then the name and a NUL.
  Without it (socks4) a name is resolved here, IPv4 only (`ip4`), and a
  failed lookup fails the dial with the DNS codes before the proxy is
  contacted.
- **IPv4 only.** SOCKS4 has no IPv6: an IPv6 target, or `tcp6`, fails
  before anything is sent (`netx.socks4_ipv6_unsupported`,
  `netx.socks_unsupported_network`); UDP is refused the same way (SOCKS4
  has no UDP ASSOCIATE).
- **The reply.** Eight bytes: `VN` 0 (4 is accepted too), the code, and
  an address PostSonde ignores. 90 is granted; 91 (rejected or failed), 92
  (identd unreachable) and 93 (identd reported another user ID) are
  errors ([SOCKS4 errors](#socks4-errors)).
- **Credentials.** The user ID is sent in the clear, as the protocol
  does; a SOCKS4 URL with a password is invalid (`netx.proxy_invalid_url`)
  and a manual SOCKS4 proxy never gets the stored proxy password.
- **Everything else** is as for SOCKS5: below `Routed`, reaching the proxy
  through `Direct`; the trace with `Via` (`socks4://host:port`,
  `socks4a://host:port`, never the user ID) and the hop (`SOCKS4` or
  `SOCKS4a`, auth `user ID` or `none`, reply 90 `granted`); `ViaOf`, the
  connection log, cancellation and the handshake timeout (`Timeout`, 30 s
  by default: `netx.socks_timeout`).

Tests run `netxtest.SOCKS4Server`, an in-process SOCKS4/4a proxy that
records each CONNECT (name or IP, user ID), relays to loopback servers
through its `Hosts` map, checks a user ID and can answer with any reply:
`TestSOCKS4`, `TestSOCKS4Errors`, `TestProxyRouterSOCKS4` (netx) and
`TestDialerSOCKS4` (the app's dialer, from the settings and `ALL_PROXY`).

## HTTP proxy

NET-02. `netx.HTTPProxy` is an in-house HTTP proxy client
(standard library only) for `http://` and `https://` proxy URLs, with Basic
proxy authentication.

```go
p, err := netx.NewHTTPProxy(netx.HTTPProxyConfig{
	Address:  "proxy.lan:3128",
	TLS:      false,                 // true for an https:// proxy
	Username: "alice", Password: "…", // optional, Basic
	RootCAs:  trust.Roots,           // CAs of an https:// proxy's certificate
}, direct)
conn, err := p.DialContext(ctx, "tcp", "api.example.com:443") // a CONNECT tunnel
```

| `HTTPProxyConfig` | Zero value | Notes |
|---|---|---|
| `Address` | required | the proxy's `host:port` |
| `TLS` | off (`http://`) | on (`https://`): TLS to the proxy itself before anything is sent, verified against `RootCAs`; the target's own TLS then runs inside, end to end |
| `ServerName` | the host of `Address` | the name the proxy's certificate must be valid for |
| `Username`, `Password` | no credentials | sent as `Proxy-Authorization: Basic …` with every CONNECT and absolute-form request, preemptively; the user name cannot contain a colon (RFC 7617) |
| `Timeout` (TLS handshake with the proxy and the CONNECT exchange) | 30 s (`DefaultTimeout`) | negative: no limit besides `ctx`; the forward dialer bounds the connect to the proxy |
| `RootCAs` | the system roots | the app passes `tlsconf.Trust.Roots`: the system's plus the custom CA bundles ([Custom CA certificates](#custom-ca-certificates)); an error (a bundle that broke) fails the dial |
| `Now` | `time.Now` | clock for trace durations |

### CONNECT for everything, absolute form for http:// requests

Two ways to send a request through an HTTP proxy, and PostSonde uses both:

- **CONNECT tunnel** (`HTTPProxy.DialContext`): `CONNECT host:port
  HTTP/1.1` with `Host` and the credentials, a 2xx answer, then the tunnel.
  Every protocol that is not a plain HTTP request uses it: https:// URLs
  (TLS end to end, so the proxy sees only the host and port), WebSocket
  (`ws://` and `wss://`), Socket.IO, SSE over https, gRPC, GraphQL
  subscriptions, raw TCP with or without TLS, OAuth token calls over
  https. It sits where SOCKS5 sits: below `Routed`, so nothing in the
  protocol clients changes.
- **Absolute form** (`HTTPProxy.DialForward`, `GET http://host/path
  HTTP/1.1`): for `http://` URLs sent by `httpclient` (HTTP sends, GraphQL
  requests and schema fetches, SSE over http), as
  curl and browsers do. Many company proxies only allow CONNECT to port
  443, and they want to see (and may cache or filter) plain HTTP.

**Why in-house and not `http.Transport.Proxy`.** net/http can do both
itself, but then it dials the *proxy's* address through `DialContext`: the
guard and the connection log would see the proxy instead of the target,
the route would have to recognise the proxy's own dial, the timeline would
not show the target, an `https://` proxy would get the target's TLS
settings (client certificates, `verifyTls`), and WebSocket, gRPC, TCP and
Socket.IO, which do not use `http.Transport`, would still need a CONNECT
client. So CONNECT is ours, in the dialer, for every protocol, and
net/http is used only for what it is good at, writing the absolute form:

1. `httpclient.New` looks for a `netx.ForwardProxier` (the app's dialer is
   one: `netx.NewForwardProxier(chain, router.ForwardProxy)`). For each
   `http://` hop, `forwardRouter` (`httpclient/proxy.go`) asks
   `ForwardProxy(ctx, host:port)`, which returns the HTTP proxy in effect,
   or nil for no proxy, a SOCKS proxy, a loopback target or a proxy
   setting that cannot be used (the dial then fails with the reason).
2. With a proxy, the hop goes through a pool of that proxy (at most 8
   pools per transport): an `http.Transport` whose `Proxy` is
   `HTTPProxy.ForwardURL(host:port)`, an `http://` URL whose host is the
   **target** and whose user info holds the credentials. net/http writes
   the absolute form, adds `Proxy-Authorization`, and dials the target's
   address through the app's whole chain, with `netx.ForwardContext(ctx,
   proxy)` on the dial: the connection log and the guard see the target,
   and `ProxyRouter.Route` connects it to the proxy itself
   (`DialForward`, TLS for an https:// proxy) instead of routing it. The
   pool is HTTP/1.1 only; connections are kept alive and reused per target.
3. The request as sent (DBG-01) shows the absolute-form target
   (`GET http://api.example.com/items HTTP/1.1`) and **not** the proxy's
   `Proxy-Authorization` field: the recorder drops the field whose value is
   the proxy's credentials (it is the proxy's, not the request's; a copy as
   cURL or a HAR never carried it). A `proxy` timeline event names the
   proxy for the hop.
4. A `407` answer to an absolute-form request is the proxy's, so it is
   turned into the same error as a 407 to a CONNECT (below), not shown as
   the response.

Exceptions that use a CONNECT tunnel for `http://` too: a request with a
`Host` header of its own (net/http would put it into the absolute URL, so
the proxy would connect to that host), and HTTP/2 with prior knowledge
(`httpVersion: "2"` on http, h2c). A redirect from http to https switches
from the absolute form to a tunnel at the next hop, and back.

- **Networks.** `tcp`, `tcp4`, `tcp6`; host names always go to the proxy
  unresolved (nothing is looked up locally), IP literals as they are (IPv6
  in brackets, without a zone). Others fail with `unsupported` /
  `netx.http_proxy_unsupported_network`, UDP included. A host that could
  break the request line (spaces, control characters, `/?#@[]\"`) is
  refused with `netx.http_proxy_invalid_address`.
- **The tunnel.** The answer's head (at most 64 KiB) is read; bytes the
  proxy sent right after it (a server that speaks first) are read first
  from the returned connection. Its `RemoteAddr` is the proxy's,
  `netx.ViaOf` returns `http://host:port` or `https://host:port` (never
  with credentials) and `Unwrap` returns the connection to the proxy.
  Half-close (`CloseWrite`/`CloseRead`) is kept for a plain http:// proxy
  without leftover bytes; through TLS to the proxy it is not available.
- **https:// proxies.** The TLS connection to the proxy uses the app's
  trust (system roots plus the custom CA bundles), TLS 1.2 or newer, ALPN
  `http/1.1`, and is always verified: a request's or the global
  `verifyTls: false` applies to the target, not to the proxy. A proxy with
  a private CA works once its CA is added under Settings, Network, Custom CA
  certificates. Client certificates are not sent to the proxy.
- **Trace.** Like SOCKS5: `DialStart` with `Via` before `Direct`'s events
  for the proxy connection, `DialDone` with `Via` after the CONNECT (or,
  for `DialForward`, after the connection to the proxy). The HTTP
  timeline writes them as `dial_start tcp api.example.com:443 via
  http://proxy.lan:3128` and `dial_done 10.0.0.2:51000 → 10.0.0.1:3128 via
  http://proxy.lan:3128`, and `got_conn` shows the proxy's address. The
  `DialDone` carries the hop's details, which the timeline shows with the
  proxy's own phase ([The proxy hop](#the-proxy-hop)).
- **Connection log.** `Via` is the proxy for an open connection and for a
  failed one (the error's `via` detail); `RemoteAddr` is the proxy's.
- **Secrets.** The password goes only to the proxy, in `Proxy-Authorization`.
  It is in no error, detail, trace event, timeline, request as sent,
  history entry, cURL command, HAR file or log line; errors name the proxy
  as `host:port`, and the `Proxy-Authenticate` detail keeps only the
  scheme names. `TestHTTPProxyErrors` (netx), `TestProxyAbsoluteForm`,
  `TestProxyAuthErrors` (httpclient), `TestSendThroughHTTPProxy`,
  `TestSendThroughHTTPProxyAuthErrors` (HTTP service: response, timeline,
  history files, HAR of the history entry, cURL with secrets, debug log),
  `TestConnectThroughHTTPProxy` (WebSocket) and `TestDialerHTTPProxy`
  (the app's dialer: errors, connection log, debug log) check it.

Tests run `netxtest.HTTPProxyServer`, an in-process proxy that handles
CONNECT and absolute-form requests, with or without Basic authentication
(and a configurable `Proxy-Authenticate` challenge), optionally over TLS
with a `tlstest` CA, and records each request with its target and
`Proxy-Authorization`. HTTP, HTTPS and WebSocket go through it end to end
through the app's services, with and without credentials, over http://
and https:// proxies.

## Proxy settings

SOCKS-03, SOCKS-04, NET-02. Which proxy the app's connections go through comes
from the settings ([settings.md](settings.md#the-file)) or from the
environment, and is read again on every dial, so a change applies to the
next connection.

### Proxy URLs

`netx.ParseProxyURL` reads `scheme://[user[:password]@]host[:port]` into a
`netx.ProxyURL` (comparable; `String()` never shows the password):

| Part | Rules |
|---|---|
| scheme | `socks5h` (the proxy resolves host names), `socks5` (resolved here, the proxy gets an IP), `socks4a` (the proxy resolves them) and `socks4` (resolved here, IPv4 only; [SOCKS4 proxy](#socks4-proxy)), `http`, `https` ([HTTP proxy](#http-proxy); `https` is TLS to the proxy); any case. |
| user info | percent-decoded; a password needs a user name; for SOCKS each is at most 255 bytes (RFC 1929), no control characters; `socks4`/`socks4a` take a user ID but no password; for `http`/`https` the user name cannot contain a colon (Basic) |
| host | a name, an IPv4 address or an IPv6 address in brackets (`[::1]`) |
| port | 1 to 65535; without one 1080 (443 for `https`), as curl |
| rest | spaces around are ignored; a path of `/` is allowed; any other path, a query or a fragment is refused |

Errors are `invalid_argument` / `netx.proxy_invalid_url`, with a hint
showing the syntax. `url.Parse`'s own errors quote the whole URL, password
included, so they are never passed on: no message, hint or detail of a
proxy error contains the password (`TestParseProxyURLErrors`).

### Where the proxy comes from

`network.proxyMode` in `settings.json` (`coresettings.Network.EffectiveProxy`):

| Mode | Connections go |
|---|---|
| `"system"` (default) | through the operating system's proxies ([System proxy](#system-proxy)): http:// requests through its web (HTTP) proxy, every other connection through its secure web (HTTPS) proxy, either falling back to its SOCKS proxy; directly when it sets none. |
| `"env"` | through the proxy in `ALL_PROXY`, or `all_proxy` when that is unset (`netx.ProxyFromEnv`, as curl reads them); directly when neither is set. `NO_PROXY` (or `no_proxy`) adds to its bypass list ([Bypass list](#bypass-list)); `HTTP_PROXY` and `HTTPS_PROXY` are not read. The environment is the one PostSonde started with (`app.Config.Getenv`, `os.Getenv`); a second start that opens a window in the running app does not change it. |
| `"manual"` | through `network.proxyUrl`. The app keeps that URL without its password; the password is in the local secret storage (`<data>/secrets/app.json`, key `proxy.password`, `appsecrets.ProxyPasswordKey`) and is added when the URL has a user name and no password. A password written into `settings.json` by hand is used as it is, and moves to the secret storage the next time the proxy is changed on the settings screen. |
| `"off"` | directly, whatever the environment says. |

This is the app's proxy, the outermost scope: the open project and its
environments can replace it ([Proxy scopes](#proxy-scopes)). The admin
policy can lock `network.proxyMode`, `network.proxyUrl` and
`network.proxyBypass` like any setting ([settings.md](settings.md#settings-locked-by-your-administrator-set-02)),
for example to force a company proxy or no proxy at all. The scopes are
local side files, not settings, so a lock does not reach them: a policy
that must force a proxy for every project cannot yet.

### System proxy

NET-01. `network.proxyMode` `"system"`, **the default**: follow the
operating system's proxy settings without any setup in PostSonde. It
replaced `"env"` as the default because a desktop app is started from
the Dock, Start menu or launcher, not from a shell with `ALL_PROXY`, and
the OS settings are where users and administrators set a proxy. A
settings file written before keeps the `"env"` it has (every known key
is written), so an existing setup does not change; `"env"` stays
available for `ALL_PROXY`.

`internal/core/sysproxy` reads the settings with the standard library
only, and never in tests (`app.Config.SystemProxy` is nil there; the
parsers take fixtures):

| OS | Read from | Notes |
|---|---|---|
| macOS | `/usr/sbin/scutil --proxy` (the SystemConfiguration proxies of the primary service; no cgo) | `HTTPEnable`/`HTTPProxy`/`HTTPPort`, `HTTPS…`, `SOCKS…` (SOCKS5, remote DNS: `socks5h`); `ExceptionsList` and `ExcludeSimpleHostnames` (`<local>`); the per-interface `__SCOPED__` entries are ignored |
| Windows | `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`, with `syscall.RegOpenKeyEx`/`RegQueryValueEx` (no `golang.org/x/sys`) | `ProxyEnable`, `ProxyServer` (`host:port` for every protocol, or `http=…;https=…;socks=…`; `socks=` is SOCKS4 as WinINet reads it: `socks4a`), `ProxyOverride` (`<local>`, `*.corp`, `10.*` as `10.0.0.0/8`), `AutoConfigURL` |
| Linux, others | `gsettings list-recursively org.gnome.system.proxy` when `gsettings` is there and the mode is `manual` or `auto`; else the environment | GNOME: the http, https and socks hosts and ports, `use-same-proxy`, the HTTP proxy's `authentication-user`/`-password` when `use-authentication` is on, `ignore-hosts`. Environment: `http_proxy`, `https_proxy`, `all_proxy` (lower case first, then upper case; no scheme means `http://`) and `no_proxy`. GNOME's mode `none` (its default, also outside GNOME) falls back to the environment, as does a `gsettings` that fails (no session bus). |

**Per scheme.** The OS names a proxy per kind of traffic; the route
gets both (`netx.ProxyConfig.SplitHTTP`): http:// requests use the web
(HTTP) proxy, sent to it in absolute form (`ForwardProxy`) or, for a
SOCKS one, tunnelled (the HTTP client marks those dials with
`netx.PlainHTTPContext`); every other connection (HTTPS, WebSocket, gRPC,
TCP, OAuth) uses the secure web (HTTPS) proxy through CONNECT. Each falls
back to the SOCKS proxy, then to `all_proxy`; with only a web proxy set,
other connections go directly, as browsers do. An https:// URL in the
settings is an HTTPS proxy. The bypass list is the app's
(`network.proxyBypass`) plus the system's, whose entries PostSonde
cannot use (`*contoso*`) are skipped and listed on the settings screen.
`NO_PROXY` is not read in this mode (the Linux fallback reads `no_proxy`
as the system's list).

**PAC and WPAD are not supported.** A proxy auto-configuration file is
a JavaScript program; PostSonde has no JavaScript engine and adds no
dependency for one. A PAC URL without a manual proxy next to it fails
every connection with `sysproxy.pac_unsupported` (kind `unsupported`,
detail `pac`) and a hint to choose Manual or No proxy: going direct
could leak traffic the network expects through a proxy. When manual
proxies are set too, they are used. WPAD auto-discovery on its own
(macOS "Auto Proxy Discovery", GNOME `auto` without a URL) is ignored,
and said so on the settings screen; Windows' "Automatically detect
settings" is not read (it is on by default on most machines, with no
proxy behind it).

**Credentials.** macOS and Windows keep proxy passwords in the keychain
or credential manager, which PostSonde does not read: an authenticated
system proxy answers 407, and the fix is a manual proxy (or a project
scope) with the password. GNOME keeps them in its settings, and they are
used. A system proxy's password is never shown or logged.

**When it is read.** Only in the `"system"` mode and when no project or
environment scope decides, through a cache (`sysproxy.Cache`,
`internal/app/proxy.go` `systemProxy`): read on first use, again after
every `settings:changed`, and every 30 s by `ProxyScopesService`'s timer,
which then makes the router read the proxy in effect again, so a change
in the OS closes the idle pools like any other proxy change. A command
that fails (macOS) or a registry that cannot be read fails the
connections (`sysproxy.read_failed`, detail `source` `system`); an OS
proxy that cannot be parsed is `sysproxy.invalid_proxy`.

**On the settings screen:** **System** is the first mode. Its line says
where the settings come from ("macOS network settings", "Windows proxy
settings", "GNOME proxy settings", "http_proxy and https_proxy
environment variables") and which proxy http:// requests and the other
connections use, the system's bypass list, WPAD and skipped entries, or
why connections fail. `SettingsService.Proxy` returns them in `system`
and `effectiveHttp`; **In effect now** shows both proxies.
**Test connection** tests the secure web (or SOCKS) proxy, or the web
proxy when it is the only one.

`TestParseScutil`, `TestParseWindows`, `TestRegistryDecoding`,
`TestParseGSettings`, `TestFromEnv`, `TestReader`, `TestPerSchemeChoice`
and `TestCache` (sysproxy, from `testdata/` fixtures and fake command
runners and registry), `TestProxyRouterSplitHTTP` (netx),
`TestEffectiveProxySystem` (settings), `TestProxySystemMode` (settings
service) and `TestSystemProxy` (internal/app: the default mode, an
http:// request in absolute form to the web proxy, a TCP dial through
SOCKS, the OS dropping the web proxy, a PAC file) cover it.

### Proxy scopes

SOCKS-08, SOCKS-10. A proxy can be set at three levels; the most
specific one that sets a proxy, or says "no proxy", wins:

```
active environment  >  project  >  app (network.proxyMode, settings.json)
```

| Level | Where it is kept | Modes |
|---|---|---|
| App | `settings.json`: `network.proxyMode`, `network.proxyUrl`, `network.proxyBypass`; password in `<data>/secrets/app.json` | `system`, `env`, `manual`, `off` ([above](#where-the-proxy-comes-from)) |
| Project | `<local>/proxy-scopes.json`, `project` | `inherit` (the app decides; the default), `manual` (its own URL and bypass list), `off` (no proxy, whatever the app says) |
| Environment | the same file, `environments.<environment id>`; only the **active** environment's applies | `inherit` (the project decides, then the app), `manual`, `off` |

`<local>` is the open project's local side-file directory
([workspace.md](workspace.md#local-side-files)), never inside the
project. **Scopes are not in project files on purpose:** which proxy
reaches a network is a property of this computer and where it is, not of
the API; a teammate on another network must not inherit it, and a
committed file must never hold a proxy's credentials. A copy of the
project has its own local directory, so it starts without scopes.

```json
{
  "version": 1,
  "project": {"mode": "manual", "url": "http://bob@proxy.corp:3128", "bypass": ["*.corp"]},
  "environments": {"env_7f3a…": {"mode": "off"}}
}
```

**Credentials (SOCKS-10).** A scope's URL is kept without its password.
A password typed into the URL or the password field goes to the
project's local secret side file of `requestsecrets`
(`<local>/secrets/requests/proxy_scopes.json`, keys `project.password`
and `environment.<id>.password`): the store the typed auth secrets use,
so it is `0600`, masked in copied logs (DBG-07) and encrypted with the
other secret files when a master password is set
([privacy.md](privacy.md#encrypted-secrets)); while they are locked, a
scope that needs its password fails its connections, never going
without it. It never comes back to the UI
(`hasPassword` only), is in no project file, export, `settings.json`,
message or log line, and is sent only to the proxy. Another user name in
the URL forgets it; a URL without a user name, or a SOCKS4 one, drops it.
`TestScopeCredentialsNeverInProjectFiles` (services/proxyscopes) sets
passwords both ways, then reads every file of the project folder and
every DTO.

**Why not folders and requests.** SOCKS-08 lists them too; they are left
out deliberately, and documented as such:

- the scope in effect is one per open project (its active environment),
  so the pools of kept-alive connections stay correct by being closed when
  it changes ([Pools and proxy changes](#pools-and-proxy-changes)). A
  folder or request proxy would make two requests to the same host use
  different proxies at the same time; net/http, gRPC and the GraphQL
  clients would then need a pool per proxy, or a connection opened
  through one proxy would be reused for a request meant for another;
- a folder or request lives in project files, and a proxy there would be
  shared with the team, which the local-only rule above refuses.

A request that must go direct uses the bypass list; one that must use
another proxy belongs in an environment with that proxy. The route still
receives each dial's `ctx`, so a later per-request scope has its hook.

**When it is read.** On every dial, like the app's setting:
`proxyScopes` (`internal/app/proxy.go`) reads the active environment's ID
(`environments.Store.ActiveID`, without loading it) and the scopes file,
and the scope passwords only when a manual scope has a user name;
`proxyscope.Resolve` picks the level. A scopes file that cannot be parsed,
or a manual scope whose URL cannot be used, fails every dial
(`proxyscope.local_data_failed`, `netx.proxy_invalid_url` with detail
`source` `project` or `environment`), never going direct. No project open:
the app's proxy.

**Changes.** Saving a scope (`proxyscopes:changed`), another active
environment or a renamed or deleted one (`environments:changed`) and
another project (`workspace:changed`) make the router read the proxy
again (`refreshProxyOn`), so the idle pools are closed at once when it
changed. Saving a scope drops the scopes (and passwords) of environments
that no longer exist.

**Go.** `internal/core/proxyscope`: `Store` (`Read`, `Active(envID)`,
`Set(Input, keep)`, `Passwords`), `Resolve(network, appPassword, getenv,
active)` → `Effective{Config, Level, Source, NoProxyVar}`. The bound
`ProxyScopesService` (`internal/app/services/proxyscopes`):

| Method | Returns |
|---|---|
| `Scopes()` | `ScopesDTO` `{open, project: ScopeDTO, environments: [{id, name, active, scope}], effective: {proxy, level, source, remoteDns, bypass, noProxyVar, error}, error}`; `ScopeDTO` is `{mode, url, hasPassword, bypass}`, the URL without its password. `effective` is what the dialer uses now and which level decided (`app`, `project`, `environment`). Without an open project only `effective` is filled in. |
| `SetScope(input)` | `input` is `ScopeInputDTO` `{level: "project" \| "environment", environmentId, mode, url, password: string \| null, bypass: string[]}`; checks like `SetProxy` (`netx.proxy_invalid_url`, `netx.proxy_invalid_bypass`, `proxyscope.invalid`, detail `field`), `environments.not_found` for an unknown environment; then `Scopes()` and `proxyscopes:changed`. |

### Bypass list

SOCKS-09. Targets that connect directly although a proxy is in
effect, shared by every kind of proxy (SOCKS and HTTP) and every protocol.
`netx.ParseBypass` checks a list entry by entry
(`netx.proxy_invalid_bypass`, detail `entry`); `Bypass.Match` is the
route's check, done without any DNS lookup:

| Entry | Matches |
|---|---|
| `*` | every target |
| `example.com` | that host and every name under it (`api.example.com`), as curl's and Go's NO_PROXY |
| `.example.com`, `*.example.com` | only the names under it, not `example.com` itself |
| `<local>` | plain host names without a dot (`intranet`), as Windows writes it |
| `10.1.2.3`, `::1`, `[2001:db8::1]` | that IP address (an IPv4-mapped IPv6 target too) |
| `10.0.0.0/8`, `fd00::/8`, `169.254/16` | the addresses of that CIDR range (a short IPv4 form as macOS writes it) |
| any of them but a range, then `:port` | only connections to that port: `example.com:8443`, `*.corp:443`, `10.1.2.3:80`, `[::1]:8080`, `*:8080` |

Names compare without case and without a trailing dot. Nothing is
resolved: an IP entry matches only a target given as an IP address, a
name entry only a target given as a name (with remote DNS the target is
never resolved here at all). localhost and loopback addresses are always
direct, whatever the list says.

Which lists apply:

- `network.proxyBypass` (the app's list, settings screen **Bypass list**)
  applies to the proxy of every level;
- a manual project or environment scope adds its own `bypass`;
- the `ALL_PROXY` proxy (mode `env`) adds `NO_PROXY` (or `no_proxy`),
  comma- or space-separated; entries that cannot be used are skipped.
  `NO_PROXY` is read only in that mode: it belongs to that proxy.

A changed list is a change of the proxy in effect, so the idle pools are
closed. `TestBypassMatch`, `TestParseBypassErrors`, `TestParseBypassList`
and `TestProxyRouterBypass` (netx), `TestEffectiveProxyBypass` (settings)
and `TestDialerProxyScopes` (internal/app) cover it.

### The route

`newDialer` (`internal/app/services.go`) puts a `netx.ProxyRouter`
(`NewProxyConfigRouter`) inside the guard and the connection log.
`proxyResolver` (`internal/app/proxy.go`) gives it the proxy of each dial
and its bypass list (`netx.ProxyConfig`): the active environment's or the
project's scope ([Proxy scopes](#proxy-scopes)), else the settings in
effect (admin locks applied), the stored password (read only for a manual
proxy) and the environment. `ProxyRouter.Route` then picks:

- `Direct` when there is no proxy;
- `Direct` for a target on this machine, without a DNS lookup
  (`netx.IsLoopbackTarget`): `localhost` and names under `.localhost`,
  `127.0.0.0/8` and `::1`. localhost is always bypassed, so PostSonde's own
  local servers keep working through a proxy (and a remote proxy is never
  asked for its own "localhost");
- `Direct` for a target on the proxy's bypass list ([Bypass
  list](#bypass-list), `netx.ProxyConfig.Bypasses`);
- otherwise the proxy's dialer (`ProxyURL.Dialer`, a `SOCKS5` for
  `socks5`/`socks5h`, a `SOCKS4` for `socks4`/`socks4a`, an `HTTPProxy`
  for `http`/`https`, with the app's
  trust for an https:// proxy), reaching the proxy through `Direct`. It is
  built once and reused while the proxy stays the same.
- for a dial marked with `netx.ForwardContext` (an absolute-form HTTP
  request, [HTTP proxy](#http-proxy)): that proxy's `DialForward`, loopback
  targets still direct. `ProxyRouter.ForwardProxy` answers the HTTP
  client's question beforehand with the same rules, the bypass list
  included.

**Fail closed.** A proxy that is set but cannot be used never falls back
to a direct connection: an invalid `ALL_PROXY` or `network.proxyUrl`, a
manual mode without a URL (`netx.proxy_invalid_url`, detail `source`:
`settings` or the variable's name) or a secret file that cannot be
read fail every dial before anything is connected or looked up, with the
dial's `network` and `address` details. They are in the connection log as
failed dials.

**Remote DNS.** With `socks5h` the target's host name is sent to the proxy
as a name (ATYP 3) and nothing is resolved locally: not the target, and
not the proxy either when it is given as an IP address. With `socks5` the
name is resolved by the local resolver first and the proxy gets the
address. `TestProxyRouterRemoteDNS` (netx) and `TestDialerProxyRemoteDNS`
(internal/app, through the app's own dialer, with the proxy from the
settings and from `ALL_PROXY`) prove both with `netxtest.SOCKS5Server`,
which records whether it was given a name or an IP, and
`netxtest.RecordingResolver`, which records every name the local resolver
is asked for: none in `socks5h` mode. An HTTP proxy always gets the name
(in the CONNECT target and the absolute URL); `TestDialerHTTPProxy`
checks that nothing is looked up locally.

**Already open connections** keep the route they were opened with: an
open WebSocket, Socket.IO, TCP or SSE connection, a GraphQL subscription
or a gRPC stream stays on the proxy (or the direct connection) it was
opened with until it ends. Idle kept-alive connections do not: see
[Pools and proxy changes](#pools-and-proxy-changes).

### Pools and proxy changes

SOCKS-05. net/http keeps idle connections for reuse (up to 90 s,
`DefaultIdleConnTimeout`), and a reused connection does not dial, so it
would keep the route of the proxy it was opened with. When the proxy in
effect changes, the idle connections of every pool are closed:

- `netx.ProxyRouter` remembers the proxy in effect it read last (on every
  `Route`, `ForwardProxy` and `Refresh`) and calls its `OnChange` hooks
  when it differs: another proxy, another password, a proxy set or
  removed, a setting that became invalid or valid again. The first
  reading only records it.
- The settings service's events go through `refreshProxyOnSettings`
  (`internal/app/proxy.go`): after every `settings:changed` (the settings
  screen, the proxy password, a hand edit of the file) the router reads
  the proxy again (`Refresh`), so the pools are closed as soon as the
  proxy changes, before any new request could reuse a connection.
  `ALL_PROXY` is read once at start and does not change.
- The hook (`internal/app/services.go`) closes the idle connections of
  every client the HTTP service caches (HTTP and GraphQL requests, SSE,
  GraphQL over SSE, schema fetches, OAuth token calls;
  `httpservice.OnProxyChanged`) and of every gRPC transport client
  (`grpc.OnProxyChanged`), and logs "proxy changed: idle connections
  closed". Requests in progress keep their connection. WebSocket, TCP and
  Socket.IO connections are not pooled.
- The HTTP client's absolute-form requests also have a pool per HTTP
  proxy, so a new proxy (or a new password) gets new connections for them
  anyway.

`TestProxyChangeClosesIdlePools` (internal/app) changes the proxy on the
settings service between two requests and checks that the second one goes
through the new proxy on a new connection, and, as a control, that
without the hook the old connection and the old proxy would be reused;
`TestProxyRouterOnChange` (netx) covers when the hooks run.

### Every protocol through the proxy

SOCKS-05. Every protocol client connects through the one dialer,
so the proxy in effect applies to all of them the same way. What was
checked end to end, through the app's own dialer (the proxy from the
settings, the connection log, the guard, the route), by
`TestEveryProtocolThroughProxy` (internal/app) against local servers
that only the proxies can name, through SOCKS5 (socks5h, and socks5 with
a username and password and local DNS), SOCKS4a and an HTTP proxy with
Basic authentication:

| Protocol | Client | Through the proxy |
|---|---|---|
| HTTP/1.1 | `httpclient` | SOCKS tunnel; absolute form to an HTTP proxy |
| HTTPS, HTTP/2 | `httpclient` | tunnel, TLS and ALPN h2 end to end |
| GraphQL over HTTP (also schema fetches, GraphQL over SSE) | `httpclient` | as HTTP |
| GraphQL subscriptions over WebSocket | `transportws` over `wsclient` | tunnel, wss end to end |
| SSE | `httpclient.SendStream` | as HTTP |
| WebSocket, ws and wss | `wsclient` | tunnel |
| Socket.IO, long-polling and WebSocket | `sioclient` | as HTTP and WebSocket |
| gRPC, h2c and TLS | `grpc/transport` | tunnel, h2c or TLS end to end |
| raw TCP, plain and TLS | `tcpclient` | tunnel |
| OAuth 2.0 token and authorization calls | `httpclient` (the HTTP service) | as HTTP |
| UDP | `udpclient` | SOCKS5 UDP ASSOCIATE (SOCKS-07, [udp.md](udp.md)): the datagrams go to the proxy's relay, only the proxy and the relay are dialed. **Refused** by SOCKS4/4a (`netx.socks_unsupported_network`) and HTTP proxies (`netx.http_proxy_unsupported_network`), never going direct. |

**Nothing dials directly while a proxy is set.** The test's direct dialer
has a guard (`netx.Options.Guard`) that records every address it
connects to: after all the protocols, every recorded address is the
proxy's, and with remote DNS (socks5h, socks4a, HTTP) nothing was looked
up locally. Every connection in the connection log names the proxy.
`TestDirectDialsRecorded` shows that the record catches a direct dial.
Loopback targets are the one exception by design: they are always
connected to directly ([The route](#the-route)).

### On the settings screen

Network, Proxy: the app's proxy first. **System** (the default, [System proxy](#system-proxy)), **From ALL_PROXY**, **Manual** or **No proxy**, and a
line saying where connections go now (the proxy, and whether host names
are resolved by it or here; for an HTTP proxy, that http:// requests go to
it in absolute form and everything else through CONNECT), or why they
fail. Manual shows **Proxy URL**
(saved on Enter or when the field loses focus) and **Proxy password**
(stored in the secret storage, never shown again: the field says "Stored";
**Remove password** clears it). A password typed into the URL is moved to
the secret storage too. The screen uses `SettingsService.Proxy` and
`SetProxy` ([settings.md](settings.md#go)), not `Get` and `Update`, so the
password never travels to the UI (`frontend/src/components/settings/ProxySettings.tsx`,
`lib/settings.ts` `getProxy`/`setProxy`).

**Bypass list** (`network.proxyBypass`, one entry per line, saved when
the field loses focus with `SettingsService.SetProxyBypass`; Go's reason
for a refused entry shows under it), with the value of `NO_PROXY` noted
in `env` mode.

Below the app's rows (`components/settings/ProxyScopes.tsx`,
`lib/proxyScopes.ts`): **In effect now**, the proxy connections use and
which level decided ("…, this project's proxy", "…, the proxy of the
active environment, Staging", or direct because a level says No proxy),
how it resolves host names and the whole bypass list; **Test connection**
while a proxy is in effect ([Testing the proxy](#testing-the-proxy)); then
**This project** and one row per environment (the active one marked),
each with **Use app setting** / **Use project setting**, **Manual** or
**No proxy**, and for Manual its URL, password (stored locally, "Stored"
once set, **Remove password**) and its own bypass list (**Also bypass**).
They reload on `proxyscopes:changed`, `settings:changed`,
`environments:changed` and `workspace:changed`. Without an open project
the screen says to open one.

### Proxy indicator

NET-06. Wherever a connection shows, a marker says when it went
through a proxy and which one:

- **The response** of an HTTP or GraphQL send: "via SOCKS5 proxy" (or
  SOCKS4, SOCKS4a, HTTP, HTTPS) in the status line, the proxy in its
  tooltip, from the final exchange's connection. `httpclient.Conn.Via`
  is `netx.ViaOf` of the connection net/http hands over (`GotConn`), so a
  reused connection, TLS included (`ViaOf` looks through `*tls.Conn`), is
  marked too; `ConnInfoDTO.via` carries it, and the Timeline tab names it
  on a reused connection ("Through proxy").
- **The connection log** (every protocol): `ConnDTO.via`, a "via SOCKS5"
  mark after the remote address (which is the proxy's), the filter
  matches it and a copied line has `via=`.
- **The status bar**: while an open connection goes through a proxy, the
  network indicator adds "via HTTP proxy" (the kinds of all of them), with
  the proxies in its tooltip.

The kind is the scheme of `via` (`lib/network.ts` `proxyKind`); the
marker never shows credentials, which `via` never holds.

### The proxy hop

SOCKS-12. Every proxy dialer reports how a dial went through the
proxy in `DialDoneInfo.Proxy`, a `netx.ProxyHop`, on success and on
failure (as far as the dial got). It holds no credential:

| Field | What |
|---|---|
| `Scheme`, `Version` | `socks5`/`socks5h` and `SOCKS5`; `http`/`https` and `HTTP/1.1` |
| `Proxy`, `ProxyAddr` | the proxy's `host:port` as configured, and the address connected to (`""` when that failed) |
| `Target` | the `host:port` asked of the proxy |
| `Auth` | `none`, `username/password` (RFC 1929) or `basic` (HTTP); `""` when the dial failed before one was chosen |
| `RemoteDNS` | the target's host name went to the proxy unresolved (socks5h, HTTP proxies); else it was resolved here (`Lookup`) |
| `Replied`, `Reply`, `ReplyText` | the proxy's answer to the connect request: the SOCKS reply code and its meaning (`0 succeeded`, `5 connection refused`), or the CONNECT's status (`200 Connection established`, `403 Forbidden`). None for an absolute-form connection (`Forward`) or a failure before the connect request |
| `Lookup`, `Connect`, `TLS`, `Handshake` | the local lookup of the target (socks5), the connection to the proxy, the TLS handshake with an https:// proxy, and the proxy protocol (greeting, authentication and CONNECT; the CONNECT exchange) |

`ProxyHop.String` is its one-line English form, the detail of the HTTP
timeline's `proxy_hop` event (and of the WebSocket handshake's):
`SOCKS5 proxy.lan:1080 (10.0.0.1:1080), auth username/password, remote
DNS, reply 0 succeeded, connect 1.2ms, handshake 800µs`. The HTTP client
keeps the hop of a new connection in `Conn.Proxy` and times a `proxy`
phase between the connect (to the proxy) and the target's TLS handshake:
the proxy's TLS plus the handshake (`ProxyHop.Setup`). The Timeline tab
shows the phase on the waterfall and the hop under the connection: the
proxy and the address connected to, authentication, where host names are
resolved, the reply and the timings ([http-client.md](http-client.md#phases)).
`TestProxyHopSOCKS5`, `TestProxyHopHTTP` (netx), `TestProxyHopTimeline`
(httpclient) and `TestSendThroughHTTPProxy` (the HTTP service's DTO) check
it.

### Testing the proxy

SOCKS-11. **Test connection** on the settings screen (shown while
connections go through a proxy) connects to a `host:port` the user types
(`example.com:443` at first) through the proxy in effect, then closes
the connection; nothing is sent to the host. It shows the result:
"Connected to … through … in 12 ms" with the hop ([above](#the-proxy-hop)),
or where it failed and Go's error with its hint.

- **Go.** `SettingsService.TestProxy({target})` marks the call as the
  user's action (`netx.WithUserOp`, kind `settings.proxy_test`), bounds it
  to 20 s and calls `netx.ProxyRouter.Test` through `proxyTester`
  (`internal/app/proxy.go`): the proxy in effect, read as for every dial
  (the active environment's or the project's scope, else the settings,
  stored password, `ALL_PROXY`), with the connection log and the
  user-action guard around the proxy's dialer. So the test is in the
  connection log with its proxy, and it never goes direct: a loopback
  target, or one on the bypass list, goes through the proxy too
  (`bypassed: true` says that other connections to it would not, and the
  screen says so), and no proxy is `netx.proxy_none`.
- **Result.** `ProxyTestDTO{ok, proxy, target, durationMs, step, hop,
  error, bypassed}`; a failed test resolves, it does not reject. `step` says where
  it failed (`netx.ProxyFailureStep`, from the error's code):

  | `step` | Failures |
  |---|---|
  | `setup` | no proxy (`netx.proxy_none`), a target that is not `host:port` (`netx.proxy_test_target`), an invalid or unsupported proxy URL, a refused dial |
  | `dns` | the target's name could not be resolved here (socks5): `netx.dns_*` |
  | `connect` | the proxy cannot be reached: `netx.socks_proxy_unreachable`, `netx.http_proxy_unreachable` |
  | `tls` | the TLS handshake with an https:// proxy: `netx.tls_*` |
  | `handshake` | the proxy is silent or does not speak its protocol: `netx.socks_timeout`, `netx.socks_protocol_error`, the HTTP ones |
  | `auth` | credentials missing, refused or of an unsupported kind: `netx.socks_auth_*`, `netx.http_proxy_auth_*` |
  | `target` | the proxy could not or would not connect: SOCKS reply codes, `403`, `502` and other CONNECT answers |
  | `timeout`, `cancelled` | the test ran out of time, or was cancelled |

`TestProxyRouterTest` (netx) provokes each step with in-process proxies;
`TestProxyConnectionTest` (internal/app) runs it through the app's wiring
(stored password, connection log, loopback target through the proxy);
`TestTestProxy` (settings) and `ProxySettings.test.tsx` cover the DTO and
the screen.

## Hook points

### Route: which dialer for this target

```go
type Route func(ctx context.Context, network, address string) (Dialer, error)
func Routed(r Route) Dialer
```

The route runs on every dial and returns the dialer to use: `Direct` for
localhost (`IsLoopbackTarget`) and the bypass list (`netx.Bypass`,
SOCKS-09) and a proxy dialer otherwise. The app's route is
`netx.ProxyRouter.Route` ([Proxy settings](#the-route)), whose source
returns a `netx.ProxyConfig` (proxy and bypass list) per dial; it gets
`ctx`, so a proxy could differ per dial, though the app's scopes are per
project and environment ([Proxy scopes](#proxy-scopes)). A route error (an
invalid proxy URL) fails the dial and should be an `*errs.Error`; a nil
dialer fails with `internal` / `netx.no_route`.

### Middleware: wrap a dialer

```go
type Middleware func(next Dialer) Dialer
func Chain(d Dialer, mws ...Middleware) Dialer // Chain(d, a, b) == a(b(d)): a sees the dial first
```

Proxy dialers are middleware-shaped: they take the dialer that reaches the
proxy itself (`Direct`, so the guard and trace apply to the proxy hop too).
`TestRouted` shows the shape with a test-only fake proxy.

### Guard: refuse dials

```go
type DialCheck struct { Network, Address string; Resolved netip.AddrPort }
type Guard func(ctx context.Context, c DialCheck) error
func Guarded(g Guard) Middleware        // before routing; Resolved is zero
Options{Guard: g}                        // in Direct, per resolved IP, right before connect
func AllowAll(context.Context, DialCheck) error
func Deny(c DialCheck, hint string) *errs.Error
```

A guard sees every dial before it happens. It returns nil to allow it or an
error to refuse it, best built with `Deny`, which returns kind
`permission_denied` and code `netx.dial_denied`. Any other error is wrapped
that way too. The app uses it to guarantee only user-initiated traffic
(PRV-05), and later for offline mode and blocked hosts. Put `Guarded`
outermost so it sees the target as the client asked for it, whatever the
route. `Options.Guard` on `Direct` also sees the resolved IP (it runs in
`ControlContext`, once per address tried), e.g. to block private ranges.
With remote DNS through a proxy the target is never resolved locally, so
only the name-based check applies to it. Guards must be cheap and safe for
concurrent use. The default is to allow everything.

### User-initiated dials only

PostSonde connects only when the user sends a request, connects a client,
runs a runner, completes an OAuth flow or introspects a schema (PRV-05).
Every such action marks its context, and the app's dialer refuses dials
without the mark:

```go
type UserOp struct { Kind, ID string }            // Kind "http.send", "grpc.call", "tcp.connect", ...
func WithUserOp(ctx context.Context, op UserOp) context.Context
func UserOpFrom(ctx context.Context) (UserOp, bool) // false without a mark or with an empty Kind
func RequireUserOp(logger *slog.Logger) Guard
```

`newDialer` in `internal/app/services.go` builds the shared dialer as
`Chain(Routed(proxyRouter.Route), connLog.Middleware(), Guarded(RequireUserOp(logger)))`:
the [connection log](#connection-log) outermost, then the guard, then the
[proxy route](#the-route) over `NewDirect(...)`. A refused dial
fails with kind `permission_denied`, code `netx.not_user_initiated`
(`CodeNotUserInitiated`), a hint asking to report it (it is always a bug)
and details `network` and `address`, and is logged at warn level with the
target. Allowed dials are logged at debug level with the operation kind and
ID. Nothing reaches the network for a refused dial.

Who marks what:

| Path | Mark | Where |
|---|---|---|
| Anything run by `operation.Start` (HTTP send, with its OAuth 2.0 token request, AUTH-08; GraphQL schema fetch `graphql.introspect`, and later runners) | `<domain>.<kind>` and the operation ID | `internal/app/operation` |
| OAuth 2.0 token got by hand in the Auth tab (Get new token, Refresh; AUTH-11) | `http.oauth2_token`, request ID | `HTTPService.OAuth2GetToken` |
| gRPC call (dialed before its operation starts) | `grpc.call`, request ID | `GRPCService.Invoke` |
| Raw TCP client | `tcp.connect`, connection ID | `TCPService.Connect` |
| WebSocket client (handshake, including its cookies) | `ws.connect`, connection ID | `WebSocketService.Connect` |
| Proxy connection test (SOCKS-11) | `settings.proxy_test`, a new ID | `SettingsService.TestProxy` |

Rules:

- Call `WithUserOp` only on a path the user started. A background task
  (update check, prefetch, schema refresh) must not mark its context; if
  one is ever added, it needs its own explicit, opt-in user setting.
- Pass the marked `ctx` on. `net/http` dials with the request's context
  values (cancellation detached), so an HTTP or gRPC client dials with the
  mark when its request carries it.
- Listening is not dialing: local servers ([Listening](#listening)) are not
  affected.
- Core packages keep taking any `Dialer`; their tests use `NewDirect`
  without the guard. The service tests of HTTP, gRPC, TCP and WebSocket use
  the guarded dialer, so a path that loses the mark fails them.
  `TestDialerRefusesNonUserDials` (`internal/app`) and `TestRequireUserOp`
  prove the refusal.

### Trace: dial events for the timeline

```go
ctx = netx.WithTrace(ctx, &netx.Trace{
	DialStart: func(i netx.DialStartInfo) { ... }, // Network, Address, Via
	DialDone:  func(i netx.DialDoneInfo) { ... },  // + RemoteAddr, LocalAddr, Duration, Err
})
```

There is no global state: the trace travels in `ctx`, like `httptrace`, so
every middleware layer passes it through. A second `WithTrace` on the same
context composes: both traces are called, the newer first (e.g. the request
timeline and the network activity log, PRV-06). Each dialer reports the
connection it opens: `Direct` reports its TCP connection, which is the
proxy's address when proxied, and the SOCKS5 and HTTP proxy dialers report
the target hop around it, with `Via` naming the proxy (`DialStartInfo.Via`,
`DialDoneInfo.Via`) and put the hop's details in `DialDoneInfo.Proxy`
([The proxy hop](#the-proxy-hop)). Durations come from a monotonic clock (`Options.Now` can be
replaced in tests). Hooks run synchronously, possibly concurrently: keep
them cheap, non-blocking and concurrency-safe.

Because `Direct` hands `ctx` to `net.Dialer`, `net/http/httptrace` hooks
(`DNSStart`, `ConnectStart`, ...) keep firing for the HTTP client's timing
breakdown (DBG-03).

## Connection log

PRV-06: users can check for themselves that PostSonde connects only where
they asked. `internal/core/netlog` keeps a log of every outgoing connection
the shared dialer opens, and the status bar shows how many are open
([frontend.md](frontend.md#network-activity)).

```go
log := netlog.New(netlog.Options{})              // Size: 500 by default
d := netx.Chain(netx.NewDirect(o), log.Middleware(), netx.Guarded(netx.RequireUserOp(logger)))
log.Conns()          // []netlog.Conn, oldest first
log.Active()         // connecting or open right now
log.Subscribe(func(netlog.Conn) { ... }) // on every state change, not per byte
log.Clear()          // drops the ended ones; open ones stay
```

- **Where.** The middleware sits outside the guard, so a dial the guard
  refuses is logged too (state `refused`, code
  `netx.not_user_initiated`), and it sees the target as the protocol
  client asked for it. It is a wrapper on the dialer, not a `Trace`,
  because it must see every dial whatever its context carries and must
  follow the connection after the dial.
- **What.** One `Conn` per dial: an ID; the user action that caused it
  (`UserOp` from the context: kind and operation or connection ID, empty
  for a refused dial); the network and target `host:port`; the local and
  remote addresses once open; when it started, connected and ended; bytes
  read and written; and the state: `connecting`, `open`, `closed`,
  `failed` (with the dial error's code and message) or `refused` (any
  `permission_denied` dial error). Through a proxy it records the
  dial the protocol client asked for, `Via` names the proxy
  (`socks5h://host:port`, `http://host:port`, no credentials) and the remote address is the
  proxy's ([SOCKS5 proxy](#socks5-proxy)). `NetworkService`'s DTO carries
  it as `via` ([Proxy indicator](#proxy-indicator)).
- **Bytes and close.** A connection that dials successfully is returned
  wrapped: `Read`/`Write` add to atomic counters (bytes on the wire, TLS
  records included), and the first `Close` marks it `closed`. The wrapper
  keeps `CloseWrite`/`CloseRead` when the connection has them, so raw TCP
  half-close still works, and has `Unwrap() net.Conn`. Code must not type
  assert the connection a `Dialer` returns to `*net.TCPConn`.
- **Bounds.** At most 500 connections, in memory only; when full, the
  oldest one that has ended goes first, so every open connection stays
  listed. `Active` counts every connecting or open one.
- **Idle.** Nothing dials on its own (PRV-05), so an idle app has no
  connections. HTTP keep-alive connections stay open in the transport's
  pool for up to 90 seconds after a request (`DefaultIdleConnTimeout`),
  and show as open until then, or until the proxy in effect changes
  ([Pools and proxy changes](#pools-and-proxy-changes)).
- **Privacy.** The log holds `host:port` and IP addresses only: no URL,
  path, query, header or payload ([privacy.md](privacy.md#connection-log)).

`NetworkService` (`internal/app/services/network`) lists (`List`) and
clears (`Clear`) it and sends `network:activity` events
([events.md](events.md#other-events)). Listening sockets and the
connections local servers accept are not in the log: they are inbound and
loopback by default ([Listening](#listening)).

`TestDialerRefusesNonUserDials` (`internal/app`) checks that a refused
dial and an operation's dial both appear, the second with its operation.

## Errors

`WrapDialError(err, network, address)` maps dial errors to `errs` errors;
the net package error stays the cause, so `errors.Is` / `errors.As` still
reach `*net.OpError`, `*net.DNSError`, `context.Canceled` and
`context.DeadlineExceeded`. Details carry `network` and `address`.

| Cause | Kind | Code | Hint |
|---|---|---|---|
| `context.Canceled` | `cancelled` | `netx.dial_cancelled` | |
| deadline, `Timeout`, DNS timeout | `timeout` | `netx.dial_timeout` | Check that the host is reachable, or increase the connect timeout. |
| `*net.DNSError` not found | `network` | `netx.dns_not_found` | Check the host name. |
| other `*net.DNSError` | `network` | `netx.dns_failed` | Check your network connection and DNS settings. |
| connection refused | `network` | `netx.connection_refused` | Is the server running on that host and port? |
| host or network unreachable | `network` | `netx.unreachable` | Check the address and your network connection. |
| connection reset or aborted | `network` | `netx.connection_reset` | see below |
| anything else | `network` | `netx.dial_failed` | |
| guard refusal | `permission_denied` | `netx.dial_denied` | from the guard |
| no user operation in `ctx` (`RequireUserOp`) | `permission_denied` | `netx.not_user_initiated` | asks to report it |
| route returned no dialer | `internal` | `netx.no_route` | |
| the proxy in effect is not a valid URL, or manual without a URL ([Proxy settings](#proxy-settings)) | `invalid_argument` | `netx.proxy_invalid_url` | fix the URL or the variable, or choose No proxy |
| a `ProxyURL` of an unknown scheme (only one built by hand; every parsed scheme is dialed) | `unsupported` | `netx.proxy_unsupported` | use a SOCKS or HTTP proxy, or choose No proxy |

An error that already contains an `*errs.Error` (a guard's refusal, an inner
dialer's mapped error) is returned as that error, unchanged.

### SOCKS5 errors

SOCKS-13. Errors of the SOCKS5 dialer carry the `network`, `address`
(target) and `via` details; reply errors also `socksReply` (the code, in
decimal) and have a `*netx.SOCKSReplyError` cause for `errors.As`. `P` is
the proxy's `host:port`, `T` the target's.

| Cause | Kind | Code | Message | Hint |
|---|---|---|---|---|
| invalid `SOCKS5Config` | `invalid_argument` | `netx.socks_invalid_config` | says which field | |
| target not `host:port`, bad port, name over 255 bytes | `invalid_argument` | `netx.socks_invalid_address` | | |
| network neither TCP nor UDP | `unsupported` | `netx.socks_unsupported_network` | | |
| UDP ASSOCIATE refused (reply 7) | `network` | `netx.socks_command_not_supported` | The SOCKS proxy at P does not support UDP ASSOCIATE. | This proxy does not relay UDP (ssh -D never does). Use a SOCKS5 proxy with UDP ASSOCIATE enabled, or send without the proxy. |
| the relay named cannot be dialed | `network` (`timeout`) | `netx.socks_udp_relay_unreachable` | Could not reach the UDP relay R of the SOCKS proxy at P. | The proxy named a relay address PostSonde cannot reach. Check the proxy's external address settings. |
| the control connection ended while open | `network` | `netx.socks_udp_association_closed` | The SOCKS proxy at P ended the UDP association. | … Start the UDP request again. |
| a fragment or malformed datagram from the relay (dropped, not fatal: `netx.IsDroppedDatagram`) | `protocol` | `netx.socks_udp_fragment_dropped`, `netx.socks_udp_malformed` | | |
| cannot connect to the proxy | `network` (`timeout` if that connect timed out) | `netx.socks_proxy_unreachable` | Could not connect to the SOCKS proxy at P. | Check the proxy address and port, and that the proxy is running. |
| proxy silent past `Timeout` | `timeout` | `netx.socks_timeout` | The SOCKS proxy at P did not answer in time (step). | Check that the proxy is a working SOCKS5 proxy, or increase the timeout. |
| not SOCKS5, malformed reply, hung up mid-handshake, a method not offered | `protocol` | `netx.socks_protocol_error` | | Check that the proxy speaks SOCKS5 on this port (not HTTP or SOCKS4). |
| proxy wants credentials, none set | `network` | `netx.socks_auth_required` | The SOCKS proxy at P requires a username and password. | Set the proxy username and password. |
| no offered method accepted (0xFF) | `network` | `netx.socks_auth_unsupported` | The SOCKS proxy at P accepts none of the authentication methods PostSonde supports. | PostSonde supports no authentication and username/password. Check the proxy's authentication settings. |
| RFC 1929 status not 0 | `network` | `netx.socks_auth_failed` | The SOCKS proxy at P rejected the username or password. | Check the proxy username and password. |
| reply 1, general failure | `network` | `netx.socks_general_failure` | The SOCKS proxy at P could not connect to T (general failure). | The proxy could not connect for a reason of its own. Check that the target is up, and look at the proxy's logs. |
| reply 2, not allowed by ruleset | `network` | `netx.socks_not_allowed` | The SOCKS proxy at P does not allow connections to T. | The proxy's rules block this target. Check the proxy's configuration, or connect without the proxy. |
| reply 3, network unreachable | `network` | `netx.socks_network_unreachable` | The SOCKS proxy at P cannot reach the network of T. | The proxy has no route to that network. Check the address. |
| reply 4, host unreachable | `network` | `netx.socks_host_unreachable` | The SOCKS proxy at P cannot reach the host T. | Check the host name, and that the host is reachable from the proxy. |
| reply 5, connection refused | `network` | `netx.socks_connection_refused` | The connection to T was refused by the destination (through the SOCKS proxy at P). | Is the server running on that host and port? |
| reply 6, TTL expired | `network` | `netx.socks_ttl_expired` | The connection to T through the SOCKS proxy at P expired on the way (TTL expired). | The target is too many hops away from the proxy, or there is a routing loop. |
| reply 7, command not supported | `network` | `netx.socks_command_not_supported` | The SOCKS proxy at P does not support the CONNECT command. | Use a proxy that supports the SOCKS5 CONNECT command. |
| reply 8, address type not supported | `network` | `netx.socks_address_type_not_supported` | The SOCKS proxy at P does not support this kind of address. | For a host name: turn off remote DNS (socks5 instead of socks5h). For an IP address: try a host name or the other IP version. |
| any other reply | `network` | `netx.socks_unknown_reply` | The SOCKS proxy at P did not connect to T (reply code N). | Look at the proxy's logs. |
| `ctx` cancelled or past its deadline, local DNS failure, guard refusal | as in the table above | `netx.dial_cancelled`, `netx.dial_timeout`, `netx.dns_*`, `netx.dial_denied`, ... | | |

`netx.socks_proxy_unreachable` keeps the dial error as its cause, so
`netx.Classify` still finds the refused or unreachable connection to the
proxy. The codes are exported as `netx.CodeSOCKS*`, the fixed hints as
`netx.HintSOCKS*`.

### SOCKS4 errors

SOCKS-06. The SOCKS4 dialer's errors carry `network`, `address` and
`via`; reply errors also `socksReply` and a `*netx.SOCKSReplyError`
cause. It shares the SOCKS5 codes for the rest: `netx.socks_invalid_config`
(an address that is not `host:port`, a user ID over 255 bytes or with a
NUL), `netx.socks_invalid_address`, `netx.socks_unsupported_network`,
`netx.socks_proxy_unreachable`, `netx.socks_timeout` and
`netx.socks_protocol_error` (hint: check that the proxy speaks SOCKS4 on
this port), and `netx.socks_unknown_reply` for a code other than 90 to 93.

| Cause | Kind | Code | Message | Hint |
|---|---|---|---|---|
| an IPv6 target | `unsupported` | `netx.socks4_ipv6_unsupported` | A SOCKS4 proxy cannot connect to the IPv6 address in T. | SOCKS4 carries IPv4 addresses only. Use a host name with socks4a://, or a SOCKS5 proxy. |
| reply 91 | `network` | `netx.socks4_rejected` | The SOCKS4 proxy at P rejected the connection to T, or could not make it. | The proxy refused the request, or could not connect to the target. … |
| reply 92 | `network` | `netx.socks4_identd_unreachable` | The SOCKS4 proxy at P could not check the user ID: identd on this computer did not answer. | Use a proxy that does not require identd. |
| reply 93 | `network` | `netx.socks4_identd_mismatch` | The SOCKS4 proxy at P refused the user ID: identd reported another user. | Check the user ID in the proxy URL (socks4://user@host). |

The proxy connection test counts 92 and 93 as the `auth` step and 91 and
an IPv6 target as `target` ([Testing the proxy](#testing-the-proxy)).

### HTTP proxy errors

NET-02. Errors of the HTTP proxy dialer carry the `network`, `address`
(target) and `via` details; answers of the proxy also `httpStatus`, and a
407 `proxyAuthenticate` (the schemes offered, e.g. `Basic, NTLM`). `P` is
the proxy's `host:port`, `T` the target's. A 407 to an absolute-form
request (`httpclient`) gives the same errors as a 407 to a CONNECT.

| Cause | Kind | Code | Message | Hint |
|---|---|---|---|---|
| invalid `HTTPProxyConfig` | `invalid_argument` | `netx.http_proxy_invalid_config` | says which field | |
| target not `host:port`, bad port, a host that cannot go on a request line | `invalid_argument` | `netx.http_proxy_invalid_address` | | |
| network not TCP | `unsupported` | `netx.http_proxy_unsupported_network` | | |
| cannot connect to the proxy | `network` (`timeout` if that connect timed out) | `netx.http_proxy_unreachable` | Could not connect to the HTTP proxy at P. | Check the proxy address and port, and that the proxy is running. |
| proxy silent past `Timeout` | `timeout` | `netx.http_proxy_timeout` | The HTTP proxy at P did not answer in time (TLS handshake / CONNECT). | Check that the proxy is a working HTTP proxy, or increase the timeout. |
| not HTTP, answer head over 64 KiB, hung up mid-exchange | `protocol` | `netx.http_proxy_protocol_error` | | Check that the proxy speaks HTTP on this port (not SOCKS), and whether it is an http:// or https:// proxy. |
| 407, no credentials set | `network` | `netx.http_proxy_auth_required` | The HTTP proxy at P requires a user name and password. | Put the user name into the proxy URL and set the password under Settings, Network, Proxy, or add both to ALL_PROXY. |
| 407 to the credentials sent | `network` | `netx.http_proxy_auth_failed` | The HTTP proxy at P rejected the user name or password. | Check the proxy user name and password under Settings, Network, Proxy (or in ALL_PROXY). |
| 407 offering no Basic (NTLM, Negotiate, Digest only) | `network` | `netx.http_proxy_auth_unsupported` | … asks for an authentication PostSonde does not support (NTLM, Negotiate). | PostSonde supports Basic proxy authentication only. … |
| 403 to a CONNECT | `network` | `netx.http_proxy_forbidden` | The HTTP proxy at P does not allow connections to T (403 Forbidden). | The proxy's rules block this target or port. … |
| another non-2xx answer to a CONNECT (502, 503, 504, …) | `network` | `netx.http_proxy_connect_failed` | The HTTP proxy at P could not connect to T (502 Bad Gateway). | The proxy could not connect to the target. … |
| an https:// proxy's certificate or TLS handshake | `tls` | `netx.tls_*` ([below](#connection-and-tls-failures)) | The certificate of the HTTPS proxy at P is not signed by a trusted authority. (…) | for an unknown CA: add the proxy's CA in Settings, Network, Custom CA certificates; a plain proxy on that port: use an http:// proxy URL |
| a CA bundle that cannot be read | as `tlsconf` reports it | `tlsconf.*` | | |
| `ctx` cancelled or past its deadline, guard refusal | as in the table above | `netx.dial_cancelled`, `netx.dial_timeout`, `netx.dial_denied`, ... | | |

The codes are exported as `netx.CodeHTTPProxy*`, the fixed hints as
`netx.HintHTTPProxy*`. The status text of a proxy's answer is shown cut to
64 printable characters.

### Connection and TLS failures

DBG-09: a DNS, connection or TLS problem gets the same code, message and
hint whatever the protocol. The classification lives here, in
`failure.go`, and every protocol client uses it:

```go
f := netx.Classify(err)        // FailureDNSNotFound, FailureReset, FailureTLSExpired, … or FailureNone
e := netx.ConnError(err, addr) // *errs.Error with kind, netx.* code, message and hint; nil for FailureNone
netx.IsReset(err)              // ECONNRESET, ECONNABORTED, EPIPE (and the Winsock numbers)
```

`Classify` looks through the whole error tree (`*url.Error`,
`*net.OpError`, `*tls.CertificateVerificationError`), so it works on what
`net/http`, `crypto/tls` and raw connections return. It leaves timeouts and
cancellations to the caller, which knows which deadline expired, and
returns `FailureNone` for anything else (the caller keeps its own codes,
such as `httpclient.malformed_response`). `ConnError` never re-maps an
error that already contains an `*errs.Error`. `WrapDialError` uses both for
the dial rows above.

| Failure | Kind | Code | Message (`addr` is host:port) | Hint |
|---|---|---|---|---|
| `FailureReset` | `network` | `netx.connection_reset` | The server at addr reset the connection. | The server closed the connection abruptly. Check that it speaks this protocol on this port, and look at its logs. |
| `FailureTLSUnknownAuthority` (also the macOS verifier's "certificate is not trusted") | `tls` | `netx.tls_unknown_authority` | The certificate of addr is not signed by a trusted authority. | If you trust this server, add the CA certificate that signed it in Settings, Network, Custom CA certificates, or turn off certificate verification. |
| `FailureTLSHostnameMismatch` | `tls` | `netx.tls_hostname_mismatch` | The certificate of addr is not valid for this host name. | Check the host name: the certificate is issued for other names. |
| `FailureTLSExpired` (expired or not yet valid) | `tls` | `netx.tls_certificate_expired` | The certificate of addr has expired or is not valid yet. | Check the server's certificate, and this computer's date and time. |
| `FailureTLSInvalid` (other verification failures) | `tls` | `netx.tls_certificate_invalid` | The certificate of addr could not be verified. | Check the server's certificate, or turn off certificate verification. |
| `FailureTLSNotTLS` (`tls.RecordHeaderError`, `http.ErrSchemeMismatch`) | `tls` | `netx.tls_server_not_tls` | The server at addr did not answer with TLS. | The server does not use TLS on this port. Use a plain (non-TLS) URL, or check the port. |
| `FailureTLSHandshake` (other alerts the server sent, versions, ciphers) | `tls` | `netx.tls_handshake_failed` | The secure connection (TLS) to addr failed. | Check that the server supports TLS 1.2 or newer on this port. |
| `FailureTLSClientCertRequired` (alert `certificate required`, TLS 1.3) | `tls` | `netx.tls_client_cert_required` | The server at addr requires a client certificate. | The server requires a client certificate (mTLS). Add one for this host in Settings, Network, Client certificates. |
| `FailureTLSClientCertRejected` (alerts `bad certificate`, `unsupported certificate`, `revoked certificate`, `expired certificate`, `unknown certificate`, `unknown certificate authority`, `access denied`) | `tls` | `netx.tls_client_cert_rejected` | The server at addr did not accept the client certificate, or the lack of one. | Add one for this host in Settings, Network, Client certificates; if one is set, check its CA, validity and client-auth usage. |

`netx.RemoteAlert(err)` returns the text of an alert the server sent
(`crypto/tls` reports it as a `*net.OpError` with Op `remote error` around
an unexported type; some HTTP/2 paths only keep the text). Servers
rarely say clearly why they refuse a certificate. A TLS 1.2 server
answers a missing or refused one with a plain `handshake failure`. With
TLS 1.3 the client's side of the handshake is complete before the server
checks the certificate, so the server's `certificate required` alert
races with the client's first write and read: the client may see the
alert, a broken pipe or reset, an EOF, or net/http's "client conn could
not be established" (HTTP/2) or "server closed idle connection"
(HTTP/1.1). `httpclient` and `wsclient` therefore note in their
`GetClientCertificate` hook whether the server asked for a certificate and
whether one was sent, and a failure before any response on such a
connection goes through `netx.ClassifyClientCert(err, sent)`:
`tls_client_cert_required` when none was sent (TLS 1.2 alerts give
`tls_client_cert_rejected`), `tls_client_cert_rejected` when one was;
timeouts and other alerts are classified as usual (`netx.FailureError`
builds the error). gRPC does the same with its own codes. Tests pin each
outcome down with servers that ask for a certificate and then reset or
close the connection without an alert (`TestClientCertMissing`,
`TestClientCertificateMissing`, `netx.TestClassifyClientCert`).

Protocol clients may reword a message for their context (the HTTP client
adds the stage, gRPC names its server name setting) or pick a more precise
hint, but keep the code. Where each one uses it:

| Client | Where | Notes |
|---|---|---|
| `httpclient` | `mapError` | Adds the `stage` detail; own hints for a plain server, verification and name mismatch ([http-client.md](http-client.md#errors)). |
| `wsclient` | `mapConnectError` | Handshake failures; a plain server gets "Use a ws:// URL" ([websocket.md](websocket.md)). |
| `wsframe` | `wrapIO` | A reset of an open WebSocket connection. |
| `tcpclient` | `handshakeError`, `readError`, `writeError` | TLS over TCP handshake failures (a plain server gets "Turn off TLS for this request"), TLS alerts while open (TLS 1.3's refused client certificate), resets while open, with read/write-specific hints ([tcp.md](tcp.md#tls)). |
| `grpc/transport` | `tlsErr`, `transportErr` | TLS classes with gRPC's hints; resets on TLS calls ([grpc-transport.md](grpc-transport.md)). |

The app services pass these errors through unchanged, and the UI shows the
message and hint under a title chosen by kind (`errorTitle`: "Network
error", "TLS error", "Timed out"). `TestFailureMessages` in `netx`
provokes every class for real, in process: an NXDOMAIN answer from an
injected resolver for a reserved `.invalid` name (`netxtest`), a closed
port, a server that closes with `SO_LINGER 0`, a dial blocked past its
timeout, and certificates generated with `tlstest` (another CA, expired,
another name) plus a plaintext server for TLS. The HTTP, WebSocket, gRPC
and TCP clients have the same kind of test for their own messages.

`netx/netxtest` (test support only) provides `NXDomainResolver` and
`FailingResolver` for `Options.Resolver`, so DNS tests never depend on the
network or the machine's resolver; `RecordingResolver`, which answers
from a map and records every name it is asked for (localhost, which the
hosts file of Windows leaves out, gets 127.0.0.1 unrecorded); `SOCKS5Server`, an
in-process SOCKS5 proxy (no authentication or username/password) that
records each CONNECT, name or IP, and relays it to loopback servers, never
resolving names through DNS (it has a `Hosts` map); and `HTTPProxyServer`,
the same for HTTP proxies ([HTTP proxy](#http-proxy)). They listen through
`netx/listen`.

## How the planned pieces plug in

- **SOCKS5**: done ([SOCKS5 proxy](#socks5-proxy)), with proxy URLs, the
  global setting, `ALL_PROXY` and the route ([Proxy settings](#proxy-settings)).
  Scopes (SOCKS-08), the bypass list (SOCKS-09) and scope credentials
  kept locally (SOCKS-10) are done ([Proxy scopes](#proxy-scopes),
  [Bypass list](#bypass-list)). UDP ASSOCIATE (SOCKS-07) is done
  ([udp.md](udp.md#socks5-udp-associate)).
  "Test connection" (SOCKS-11), the proxy hop in the timeline (SOCKS-12),
  SOCKS4/4a (SOCKS-06, [SOCKS4 proxy](#socks4-proxy)), the proxy
  indicator (NET-06), every protocol through the proxy and the pools
  closed on a proxy change (SOCKS-05, [Every protocol through the
  proxy](#every-protocol-through-the-proxy)) are done.
- **HTTP proxy (NET-02)**: done ([HTTP proxy](#http-proxy)): CONNECT for
  every protocol, absolute form for the HTTP client's http:// requests.
  "Test connection" and the proxy hop in the timeline are done.
  Scopes, the bypass list and scope credentials apply to HTTP
  proxies as to SOCKS, and so do the OS proxy settings ([System
  proxy](#system-proxy)). A
  per-request proxy would need a pool per proxy rather than pools closed
  on a change ([Proxy scopes](#proxy-scopes)).
- **Route**: `ProxyRouter` reads the proxy of each dial and its bypass
  list from its source function, which gets `ctx`: the active
  environment's or project's scope (SOCKS-08), else the app's setting,
  and the bypass list next to `IsLoopbackTarget` (SOCKS-09).
- **TLS**: not in the dialer. Protocol clients wrap the connection with
  `tls.Client` (or let `http.Transport` do it), so TLS runs end to end
  through a tunnel. The trusted CAs come from one `tlsconf.Trust` shared by
  every client ([Custom CA certificates](#custom-ca-certificates)), and so
  do the per-host client certificates ([Client
  certificates](#client-certificates)); TLS details for the timeline may
  come later, and will take a connection from a `Dialer`, not dial by
  themselves.
- **UDP (SOCKS-07)**: done. `"udp"` dials return a `netx.DatagramConn`:
  `Direct` connected or, with `netx.UnconnectedUDP`, unconnected; SOCKS5
  through UDP ASSOCIATE; SOCKS4 and HTTP proxies refuse it, never sending
  direct ([udp.md](udp.md)).

## Custom CA certificates

NET-04: PEM files whose certificates every TLS client trusts on top of the
system's certificate authorities, for corporate and local development CAs.
They are app-wide: the settings file keeps their absolute paths in
`network.caBundles` ([settings.md](settings.md)), added and removed on the
settings screen (Network, TLS, Custom CA certificates; "Add file…" shows
the native file dialog). There is no per-request or per-folder list; a
gRPC request's own CA settings (GRPC-23) are separate.

- **Always added, never replacing.** The pool is `x509.SystemCertPool()`
  plus every `CERTIFICATE` block of each file, so public servers keep
  working. On macOS and Windows Go asks the platform verifier first and
  falls back to its own verification with the added CAs.
- **One trust for every client.** `internal/app/services.go` builds one
  `tlsconf.NewTrust(func() []string { … settings … })` and passes
  `trust.Roots` to HTTP sends and GraphQL requests and schema fetches
  (`httpservice.WithTrust`), WebSocket connects (`websocket.WithTrust`),
  gRPC calls (`grpc.WithTrust`) and TLS over TCP (`tcp.WithTrust`).
  Verification turned off (a request's
  `verifyTls: false`, the global switch) skips the bundles.
- **Read at use, cached.** `Trust.Roots` stats the files on every
  connection and rebuilds the pool only when the list, or a file's size
  or modification time, changes; otherwise it returns the same
  `*x509.CertPool`, so services key their cached clients on the pointer
  and reuse connections. A new pool drops the old clients (idle
  connections closed). No bundles: `nil`, the system roots as before.
- **Checked when added.** `tlsconf.InspectBundle` refuses a file that is
  not PEM or has no certificate (`tlsconf.ca_no_certificates`, hint: use
  PEM), one that does not parse (`tlsconf.ca_invalid`), cannot be read
  (`tlsconf.file_unreadable`) or is over 1 MiB (`tlsconf.file_too_large`);
  nothing is saved then. Warnings do not stop it and are shown on the
  bundle's row: an expired CA (`tlsconf.ca_expired`), one not valid yet
  (`tlsconf.ca_not_yet_valid`), a certificate that is not a CA
  (`tlsconf.ca_not_a_ca`, trusted as is), a private key in the file
  (`tlsconf.ca_bundle_has_key`).
- **A bundle that breaks later** (moved, deleted, edited into something
  else) is listed with its error on the settings screen and fails every
  TLS connection that would use it, at once, with the same codes, detail
  `field` `network.caBundles[i]` and the hint "Fix the file, or remove it
  …". Plain `http://`, `ws://` and h2c connections do not use the bundles
  and are not stopped (an http request redirected to https then trusts
  the system roots only).
- **Unknown CA hint.** `netx.tls_unknown_authority` (and the
  httpclient/gRPC hints for it) points at Settings, Network, Custom CA
  certificates.

- **The local servers' CA.** The CA that signs the certificates of
  PostSonde's own TLS servers (SRV-05, `<data>/servers/tls/ca.pem`,
  [servers.md](servers.md#tls)) is not trusted by default. The server
  editors offer "Trust in PostSonde", which adds that file here
  (`SettingsService.TrustCABundle`, checked like a chosen file); it can be
  removed here like any other bundle.

Tests generate the CA and server certificates at run time
(`tlsconf/tlstest`) and check each protocol end to end: a server signed by
the custom CA is refused, accepted once the bundle is configured, and
refused again after it is removed (`TestSendCustomCA`,
`TestConnectCustomCA`, `TestInvokeCustomCA`; `tlsconf.TestTrustRoots`
for the cache).

## Client certificates

AUTH-30, NET-05: per-host client certificates for servers that require
mTLS. The settings keep a list in `network.clientCerts`
([settings.md](settings.md#client-certificates)), each entry a host
pattern and either a PEM certificate and key or a PKCS#12 (`.p12`, `.pfx`)
file, by absolute path. They are app-wide, like the CA bundles, and apply
to every protocol: HTTP sends, GraphQL requests, subscriptions and schema
fetches, SSE streams, WebSocket connects, gRPC calls and TLS over TCP.

- **Host patterns.** `tlsconf.ParseHostPattern`: `api.example.com`,
  `*.example.com`, `localhost:8443`, `[::1]:8443`, `*`. `*` matches any run
  of characters, dots included (`*.example.com` matches `a.b.example.com`
  but not `example.com`); names compare without case and trailing dot.
  Without a port (or with `:*`) every port matches. A scheme, path, user
  info or spaces are refused (`tlsconf.host_pattern_invalid`).
- **Which one.** `tlsconf.MatchClientCert`: the most specific matching
  pattern wins (more literal host characters, then a pattern with a port),
  the first in the list on a tie. The connection's host and port are the
  URL's (443 by default for https and wss; a gRPC address without a port
  is port 443), for every hop: a redirect to another host gets that host's
  certificate or none.
- **Formats.** PEM: a certificate file (intermediates after the leaf, and
  optionally the key) and a key file; PKCS#1, SEC 1 and PKCS#8 keys, and
  encrypted PKCS#8 (`ENCRYPTED PRIVATE KEY`) with the passphrase. PKCS#12
  through `internal/core/pkcs12` ([pkcs12.md](pkcs12.md)): every export
  profile it decodes (Windows AES and 3DES, macOS Keychain, OpenSSL
  default and legacy), with the passphrase. The chain in the file is sent
  after the leaf. Old OpenSSL `Proc-Type: 4,ENCRYPTED` keys are refused
  with a hint to convert them to PKCS#8.
- **Passphrases are never in settings.json.** They go to the app's local
  secret storage, `internal/core/appsecrets` (`<data>/secrets/app.json`,
  `0600`, key `clientCert.<id>.passphrase`, [privacy.md](privacy.md)), and
  never come back to the UI (the DTO only says `hasPassphrase`). The
  settings entry has an `id` that keys it.
- **Checked when added or edited.** `SettingsService.AddClientCert` and
  `UpdateClientCert` run `tlsconf.LoadClientCert`: the files must decode,
  the passphrase must open them (`pkcs12.wrong_password`), the key must
  be the certificate's (`tlsconf.client_key_mismatch`); nothing is saved
  otherwise. An expired or not yet valid certificate, or one not issued
  for client authentication, is saved with a warning shown on its row
  (`tlsconf.client_cert_expired`, `tlsconf.client_cert_not_yet_valid`,
  `tlsconf.client_cert_not_for_client_auth`): the server decides. A
  second entry with the same pattern is refused
  (`settings.client_cert_duplicate`).
- **One selector for every client.** `internal/app/services.go` builds one
  `tlsconf.NewClientCerts(entries, passphrase)` and hands a
  `httpclient.ClientCertFunc` (host:port to certificate) to the HTTP,
  WebSocket, gRPC and TCP services (`WithClientCerts`). `ClientCerts.For`
  reads the list per connection and caches each loaded certificate until
  its entry, passphrase or a file's size or modification time changes, so
  the same `*tls.Certificate` comes back and pools keyed on it are reused.
- **Per-host pools.** `httpclient` routes every https hop through a
  connection pool per certificate (`certRouter`), so connections made
  with one certificate are never reused for a host that needs another (or
  none); `wsclient` sets it on the handshake; gRPC keeps one transport
  client per certificate. The certificate is always sent when the server
  asks, even if its issuer is not among the CAs the server lists.
- **A certificate that breaks later** (moved file, wrong passphrase, the
  secret file unreadable) fails every connection to a matching host at
  once, before dialing, with the load error's code, detail `host` (the
  pattern) and `clientCert` (the ID) and the hint "Fix or remove the
  client certificate for this host in Settings, Network, Client
  certificates." It is listed with that error on the settings screen.
  Plain `http://`, `ws://`, h2c and TCP without TLS never ask.
- **Timeline.** When the server asks for a certificate the timeline gets a
  `client_cert` event: the certificate's common name and the pattern that
  chose it (`"api client" (*.example.com)`), or "requested by the server;
  none is set for this host". Nothing about the key or passphrase is
  logged or shown.
- **Errors.** Without a certificate the server's refusal is
  `netx.tls_client_cert_required` (TLS 1.3) or
  `netx.tls_client_cert_rejected` (TLS 1.2, or a refused certificate), with
  a hint pointing at Settings, Network, Client certificates
  ([Connection and TLS failures](#connection-and-tls-failures)).

Tests run in-process servers with `tls.RequireAndVerifyClientCert` that
trust the PKCS#12 fixtures' CAs (`tlstest.FixtureClientCAs`), and use the
committed fixtures of `internal/core/pkcs12/testdata` (PEM files, Windows,
macOS and OpenSSL `.p12` exports, encrypted PKCS#8 keys): HTTP/1.1 and
HTTP/2 (`httpclient.TestPerHostClientCertificates`,
`TestClientCertMissing`), WebSocket (`wsclient.TestClientCertificate`),
gRPC (`transport.TestPerHostClientCertificate`), TLS over TCP
(`tcpclient.TestTLSClientCertificate`, `tcp.TestTLSWithClientCertificate`,
with generated `tlstest` certificates) and the app services end
to end (`TestSendClientCertificates`, `TestConnectClientCertificates`,
`TestInvokeClientCertificates`), each succeeding with PEM and PKCS#12 and
failing with the coded error without a certificate, on TLS 1.3 and 1.2.
`tlsconf.TestParseHostPattern`, `TestMatchClientCert`, `TestLoadClientCert`
and `TestClientCerts` cover patterns, formats, errors and the cache.

## Listening

Every listening socket PostSonde opens (the OAuth redirect listener, mock
servers, WebSocket, TCP and UDP servers) comes from
`internal/core/netx/listen`. It binds to loopback unless the user opted in
to LAN access (SRV-02, PRV-07).

```go
ln, info, err := listen.Listen(ctx, listen.Spec{})                              // 127.0.0.1:<free port>
pc, info, err := listen.ListenPacket(ctx, listen.Spec{Host: "::1", Port: 5353}) // UDP
pcs, info, err := listen.ListenPackets(ctx, listen.Spec{DualStack: true})     // UDP on 127.0.0.1 and ::1
t, err := listen.ParseHost("tcp", host)                                         // validate as the user types
```

| `Spec` field | Meaning |
|---|---|
| `Network` | `tcp` (default), `tcp4`, `tcp6` for `Listen`; `udp` (default), `udp4`, `udp6` for `ListenPacket`. The 4/6 forms restrict `localhost` to that family. |
| `Host` | `""` or `localhost` (any case): loopback. Otherwise an IP literal (brackets and IPv6 zones accepted). |
| `Port` | `0` lets the OS pick; read it from `Info.Port()`. |
| `AllowLAN` | Required for anything that is not loopback. |
| `DualStack` | For `""`/`localhost` on `tcp`: bind `127.0.0.1` and `::1` on the same port and accept on both. `ListenPackets` does the same for `udp` and returns one `PacketConn` per family. |

### Rules

- **Loopback by default.** `""` and `localhost` bind `127.0.0.1`, or `::1`
  when IPv4 loopback is unavailable. Any `127.0.0.0/8` address and `::1` are
  loopback too. IPv4-mapped addresses (`::ffff:127.0.0.1`) are unmapped and
  bound as IPv4, so the bound address is the one the user sees.
- **No host names.** Only `localhost` and IP literals are accepted
  (`listen.invalid_host`, even with `AllowLAN`). Listen addresses never go
  through DNS, so a name cannot resolve to a LAN or public address behind
  the user's back.
- **LAN needs an opt-in.** Any other address (private, link-local, public)
  and the unspecified addresses `0.0.0.0` and `::` fail before a socket is
  created, with kind `permission_denied`, code `listen.lan_not_allowed` and
  details `network`, `address` and `scope`. With `AllowLAN` they bind and
  `Info.Warning` (`{code, message}`, JSON-tagged) is set for the UI to show
  next to the server:

  | Address | `Info.Scope` | `Warning.Code` |
  |---|---|---|
  | `127.0.0.0/8`, `::1` | `loopback` | none |
  | one interface address | `lan` | `listen.lan_exposed` |
  | `0.0.0.0` or `::` | `all_interfaces` | `listen.all_interfaces_exposed` (stronger message) |

- **One family per socket.** An address binds with `tcp4`/`tcp6`
  (`udp4`/`udp6`): `0.0.0.0` is every IPv4 interface and `::` every IPv6
  interface, never both through one dual-stack socket.
- **DualStack.** One `net.Listener` has one address, so `DualStack` returns
  a listener that accepts from both (`Addr` is the IPv4 one, `Info.Addrs`
  lists both). The first family picks the port and the other joins on it.
  A family the machine does not have is skipped. With `Port: 0`, a port
  taken in the other family is retried with a new one (5 attempts); with an
  explicit port it fails with `listen.address_in_use`. It is ignored for IP
  literals and 4/6 networks, and `ListenPacket` does not support it
  (`unsupported`, `listen.dual_stack_unsupported`): one `PacketConn` has
  one address, so `ListenPackets` returns one per family instead (the UDP
  server reads from each and replies from the one a datagram came in on).

### Errors

| Cause | Kind | Code | Hint |
|---|---|---|---|
| host is not an IP or `localhost`, or of the wrong family | `invalid_argument` | `listen.invalid_host` | Use 127.0.0.1 or localhost ... |
| network other than tcp/udp (4/6) | `invalid_argument` | `listen.invalid_network` | |
| port outside 0–65535 | `invalid_argument` | `listen.invalid_port` | |
| non-loopback without `AllowLAN` | `permission_denied` | `listen.lan_not_allowed` | Allow LAN access for this server ... |
| `DualStack` on `ListenPacket` | `unsupported` | `listen.dual_stack_unsupported` | |
| port in use | `network` | `listen.address_in_use` | Another program is using this port ... |
| address not on this machine, family unsupported | `network` | `listen.address_unavailable` | Check that this IP address belongs ... |
| OS refused (EACCES, EPERM) | `permission_denied` | `listen.bind_denied` | privileged-port hint below 1024 |
| `ctx` cancelled or past its deadline | `cancelled` / `timeout` | `listen.cancelled` / `listen.timeout` | |
| anything else | `network` | `listen.failed` | |

The net package error stays the cause; details carry `network` and
`address`.

### Rule for servers

**Open listening sockets only through `listen.Listen`,
`listen.ListenPacket` or `listen.ListenPackets`.** Outside `internal/core/netx/listen`, non-test Go
code must not use `net.Listen`, `net.ListenTCP`, `net.ListenUDP`,
`net.ListenPacket`, `net.ListenIP`, `net.ListenMulticastUDP`,
`net.ListenConfig`, `tls.Listen`, `http.ListenAndServe(TLS)` or the
`http.Server` methods `ListenAndServe(TLS)`. Serve on the listener you got:
`srv.Serve(ln)`, `tls.NewListener(ln, cfg)` (how the local servers serve
TLS, SRV-05: the servers Manager wraps the listener `listen.Listen`
returned). `TestNoDirectListens` in
`internal/core/netx/listen/rule_test.go` enforces it; test files are exempt
(`httptest` binds loopback). Unix domain sockets are not covered. To look by
hand:

```sh
git grep -nE '\bnet\.Listen|tls\.Listen\(|ListenAndServe' -- '*.go' ':!*_test.go' ':!internal/core/netx/listen/'
```
