package netx

import (
	"context"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// Defaults used by NewDirect for zero Options fields.
const (
	// DefaultTimeout bounds DNS lookup and connect of one dial.
	DefaultTimeout = 30 * time.Second
	// DefaultKeepAlive is the TCP keep-alive period.
	DefaultKeepAlive = 30 * time.Second
)

// Options configure a Direct dialer. The zero value gives the defaults.
type Options struct {
	// Timeout bounds one dial, DNS lookup included. Zero means
	// DefaultTimeout; a negative value means no limit besides ctx.
	Timeout time.Duration
	// KeepAlive is the TCP keep-alive period. Zero means DefaultKeepAlive;
	// a negative value turns keep-alives off.
	KeepAlive time.Duration
	// FallbackDelay is how long an IPv6 attempt runs before an IPv4
	// fallback starts (Happy Eyeballs, RFC 6555). Zero means net.Dialer's
	// default (300 ms); a negative value turns the fallback off.
	FallbackDelay time.Duration
	// LocalAddr is the local address to dial from; nil lets the OS pick.
	LocalAddr net.Addr
	// Resolver resolves host names; nil means net.DefaultResolver.
	Resolver *net.Resolver
	// Guard checks every resolved address right before the connect, with
	// DialCheck.Resolved set, and can refuse it. Nil allows everything.
	Guard Guard
	// ControlContext is called after Guard, on the raw socket before the
	// connect, as net.Dialer.ControlContext. For socket options and tests.
	ControlContext func(ctx context.Context, network, address string, c syscall.RawConn) error
	// Now is the clock for DialDoneInfo.Duration; nil means time.Now,
	// whose readings are monotonic. Tests inject a fake.
	Now func() time.Time
}

// Direct dials targets itself, without a proxy. It wraps net.Dialer, maps
// errors with WrapDialError and reports every dial to the context's Trace.
// It is safe for concurrent use.
type Direct struct {
	dialer net.Dialer
	now    func() time.Time
}

var _ Dialer = (*Direct)(nil)

// NewDirect returns a Direct dialer configured by o.
func NewDirect(o Options) *Direct {
	d := &Direct{
		dialer: net.Dialer{
			Timeout:       positiveOr(o.Timeout, DefaultTimeout),
			KeepAlive:     o.KeepAlive,
			FallbackDelay: o.FallbackDelay,
			LocalAddr:     o.LocalAddr,
			Resolver:      o.Resolver,
		},
		now: o.Now,
	}
	if o.KeepAlive == 0 {
		d.dialer.KeepAlive = DefaultKeepAlive
	}
	if d.now == nil {
		d.now = time.Now
	}
	if o.Guard != nil || o.ControlContext != nil {
		d.dialer.ControlContext = control(o.Guard, o.ControlContext)
	}
	return d
}

// positiveOr maps zero to def and a negative value to zero ("none").
func positiveOr(v, def time.Duration) time.Duration {
	switch {
	case v == 0:
		return def
	case v < 0:
		return 0
	}
	return v
}

// control runs the guard on the resolved address, then the caller's hook.
func control(g Guard, next func(context.Context, string, string, syscall.RawConn) error) func(context.Context, string, string, syscall.RawConn) error {
	return func(ctx context.Context, network, address string, c syscall.RawConn) error {
		if g != nil {
			resolved, _ := netip.ParseAddrPort(address) // zero for unix sockets
			if err := check(ctx, g, DialCheck{Network: network, Address: address, Resolved: resolved}); err != nil {
				return err
			}
		}
		if next != nil {
			return next(ctx, network, address, c)
		}
		return nil
	}
}

// DialContext connects to address on the named network. A ctx that is
// already done fails at once, without trace events.
func (d *Direct) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, WrapDialError(err, network, address)
	}
	tr := ContextTrace(ctx)
	tr.dialStart(DialStartInfo{Network: network, Address: address})
	start := d.now()
	conn, err := d.dialer.DialContext(ctx, network, address)
	if err == nil && IsUDP(network) {
		// A UDP socket (UDP-01): buffers large enough for any datagram,
		// and unconnected when ctx asks for it, once the target is
		// resolved and checked. Connected, it reports an ICMP "port
		// unreachable" on every system.
		setUDPBuffers(conn)
		if IsUnconnectedUDP(ctx) {
			conn, err = unconnect(ctx, conn, network, address)
		} else {
			reportPortUnreachable(conn)
		}
	}
	info := DialDoneInfo{Network: network, Address: address, Duration: d.now().Sub(start)}
	if err != nil {
		err = WrapDialError(err, network, address)
		info.Err = err
		tr.dialDone(info)
		return nil, err
	}
	info.RemoteAddr = conn.RemoteAddr()
	info.LocalAddr = conn.LocalAddr()
	tr.dialDone(info)
	return conn, nil
}
