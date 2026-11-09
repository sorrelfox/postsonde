// Package netx opens every network connection PostSonde makes.
//
// Protocol clients (HTTP, GraphQL, gRPC, WebSocket, raw TCP, OAuth calls)
// never call net.Dial, use a net.Dialer or rely on http.DefaultTransport.
// They take a Dialer and open connections with DialContext, so proxies,
// the privacy guard and the connection timeline apply to every protocol
// the same way. TestNoDirectDials enforces this; see docs/network.md.
//
// A Dialer is built from small pieces:
//
//   - Direct dials the target itself, wrapping net.Dialer with PostSonde's
//     defaults (NewDirect).
//   - SOCKS5 tunnels through a SOCKS5 proxy (RFC 1928 CONNECT, RFC 1929
//     username/password), reaching the proxy through a forward Dialer,
//     normally Direct (NewSOCKS5). RemoteDNS (socks5h) sends host names to
//     the proxy unresolved. A "udp" dial goes through the proxy's UDP
//     ASSOCIATE relay (SOCKS-07, socks5udp.go).
//   - A "udp" dial returns a DatagramConn (udp.go): Direct connects the
//     socket, or with UnconnectedUDP receives from any sender.
//   - SOCKS4 tunnels through a legacy SOCKS4 proxy, or a SOCKS4a one with
//     RemoteDNS (NewSOCKS4): IPv4 or host names, a user ID, no password.
//   - HTTPProxy tunnels through an HTTP or HTTPS proxy with CONNECT and
//     Basic credentials (NewHTTPProxy), and connects plain HTTP requests
//     to it for the absolute form (DialForward, ForwardContext,
//     ForwardProxier; httpclient writes the requests).
//   - A Route picks the Dialer for each target, e.g. direct for a bypass
//     list and a proxy dialer for everything else (Routed). ProxyRouter is
//     the app's: loopback targets and "no proxy" go direct, everything
//     else through the proxy in effect, given as a ProxyURL
//     (ParseProxyURL: socks5h://user:pass@host:port, http://host:3128;
//     ProxyFromEnv reads
//     ALL_PROXY). It never falls back to direct for a proxy it cannot use.
//   - A Middleware wraps a Dialer (Chain). Proxy dialers take the Dialer
//     they use to reach the proxy itself.
//   - A Guard sees every dial before it happens and may refuse it with a
//     permission_denied error (Guarded, Options.Guard). RequireUserOp is
//     the app's: it refuses dials whose context was not marked by a user
//     action (WithUserOp), so PostSonde connects only when asked to.
//   - A Trace attached to the context reports dial events with the target,
//     addresses, duration and error (WithTrace).
//
// Wiring them up for an HTTP client:
//
//	d := netx.Chain(netx.Routed(route), netx.Guarded(policy))
//	tr := &http.Transport{DialContext: d.DialContext}
//
// TLS is not part of the dialer: protocol clients layer it on top of the
// returned net.Conn, so TLS through a proxy tunnel is end to end.
//
// Errors returned by DialContext are *errs.Error values with kind network,
// timeout, cancelled or permission_denied and a netx.* code; the net
// package error stays in the chain for errors.Is and errors.As.
//
// The package imports only the standard library, like all of internal/core.
package netx
