package netx

import (
	"context"
	"net"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Dialer opens a connection to address on the named network ("tcp",
// "tcp4", "tcp6", "udp", "unix", ...). Its method set matches net.Dialer,
// so a Dialer plugs into http.Transport.DialContext and the dial options of
// gRPC and WebSocket clients as it is.
//
// The address is passed on as the caller gave it. A host name is resolved
// only by the dialer that finally connects to it: Direct resolves locally,
// while a SOCKS5 dialer in remote-DNS mode (socks5h) sends the name to the
// proxy unresolved. Middleware must not resolve addresses itself.
//
// TLS is not the dialer's job. Protocol clients wrap the returned
// connection in TLS themselves, so TLS through a proxy tunnel is end to end.
//
// Implementations must be safe for concurrent use, honour ctx cancellation
// and deadlines, pass ctx on to the Dialer they wrap (the Trace travels in
// it) and return *errs.Error values (WrapDialError does the mapping).
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Every net.Dialer is a Dialer; that is the compatibility promise above.
var _ Dialer = (*net.Dialer)(nil)

// DialerFunc adapts a function to the Dialer interface.
type DialerFunc func(ctx context.Context, network, address string) (net.Conn, error)

// DialContext calls f(ctx, network, address).
func (f DialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

// Middleware wraps a Dialer: it may inspect or refuse a dial, rewrite where
// it goes, or tunnel it (a proxy dialer uses next to reach the proxy).
type Middleware func(next Dialer) Dialer

// Chain wraps d in mws. The first middleware is the outermost: it sees each
// dial first and hands it on to the second, and so on down to d.
// Chain(d, a, b) is a(b(d)). Nil middlewares are skipped.
func Chain(d Dialer, mws ...Middleware) Dialer {
	for i := len(mws) - 1; i >= 0; i-- {
		if mws[i] != nil {
			d = mws[i](d)
		}
	}
	return d
}

// Route picks the Dialer for one dial, e.g. Direct for hosts on the bypass
// list (localhost by default) and a proxy dialer for everything else. The
// proxy settings can come from ctx, so they may differ per request. An
// error (an invalid proxy setting) fails the dial and should be an
// *errs.Error.
type Route func(ctx context.Context, network, address string) (Dialer, error)

// Routed returns a Dialer that asks r for a Dialer on every dial and dials
// through it with the same ctx, network and address.
func Routed(r Route) Dialer {
	return DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		d, err := r(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, errs.New(errs.Internal, "No connection method was chosen for "+address+".").
				WithCode(CodeNoRoute).
				WithDetail("network", network).
				WithDetail("address", address)
		}
		return d.DialContext(ctx, network, address)
	})
}
