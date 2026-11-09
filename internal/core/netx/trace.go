package netx

import (
	"context"
	"net"
	"time"
)

// Trace receives dial events, for the request timeline and the network
// activity log. Attach it to a context with WithTrace; every Dialer passes
// ctx down, so the Dialer that opens the connection reports it whatever
// middleware sits in between. Nil hooks are skipped.
//
// Each dialer that opens a connection reports it: Direct reports the TCP
// (or UDP, unix) connection it makes. With a proxy, Direct reports the
// connection to the proxy, and the proxy dialer (SOCKS5, HTTPProxy) reports the dial
// of the target through it, with Via set: its DialStart comes first and its
// DialDone last, around Direct's events for the proxy. That DialDone
// carries the hop through the proxy (DialDoneInfo.Proxy): the proxy's
// protocol, authentication, DNS mode, reply and timings.
//
// Hooks are called synchronously on the dialing goroutine, possibly from
// several goroutines at once (parallel requests, Happy Eyeballs). They must
// be cheap, must not block and must be safe for concurrent use. They must
// not keep or use the connection.
//
// A context that already carries a Trace (from WithTrace or an outer
// caller) keeps it: the hooks of both are called, the newer first.
//
// For HTTP, net/http/httptrace hooks (DNSStart, ConnectStart, ...) still
// fire too, since Direct hands ctx to net.Dialer.
type Trace struct {
	// DialStart is called when a dial starts, before any DNS lookup.
	DialStart func(DialStartInfo)
	// DialDone is called when the dial ends, with the connection's
	// addresses on success or the error.
	DialDone func(DialDoneInfo)
}

// DialStartInfo describes a dial that is starting.
type DialStartInfo struct {
	// Network and Address are as passed to DialContext; Address may be a
	// host name that is not resolved yet.
	Network string
	Address string
	// Via names the proxy the dial goes through ("socks5h://proxy:1080",
	// never with credentials); "" for a direct dial.
	Via string
}

// DialDoneInfo describes a dial that has ended.
type DialDoneInfo struct {
	// Network and Address are as passed to DialContext.
	Network string
	Address string
	// Via names the proxy the dial went through; "" for a direct dial.
	Via string
	// RemoteAddr and LocalAddr are the connection's addresses; nil when the
	// dial failed. Through a proxy, RemoteAddr is the proxy's.
	RemoteAddr net.Addr
	LocalAddr  net.Addr
	// Duration is the time from the start of the dial to its end (DNS
	// lookup and connect), from a monotonic clock.
	Duration time.Duration
	// Err is the error DialContext returns, or nil.
	Err error
	// Proxy is the hop through the proxy (SOCKS-12), reported by the proxy
	// dialers on success and on failure; nil for a direct dial. Hooks must
	// not modify it; copy it to keep it.
	Proxy *ProxyHop
}

type traceKey struct{}

// WithTrace returns a copy of ctx that carries t. If ctx already carries a
// Trace, both are called, t's hooks first. WithTrace(ctx, nil) returns ctx.
func WithTrace(ctx context.Context, t *Trace) context.Context {
	if t == nil {
		return ctx
	}
	if old := ContextTrace(ctx); old != nil {
		t = &Trace{
			DialStart: join(t.DialStart, old.DialStart),
			DialDone:  join(t.DialDone, old.DialDone),
		}
	}
	return context.WithValue(ctx, traceKey{}, t)
}

// ContextTrace returns the Trace carried by ctx, or nil.
func ContextTrace(ctx context.Context) *Trace {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	return t
}

func join[T any](a, b func(T)) func(T) {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return func(v T) {
		a(v)
		b(v)
	}
}

func (t *Trace) dialStart(info DialStartInfo) {
	if t != nil && t.DialStart != nil {
		t.DialStart(info)
	}
}

func (t *Trace) dialDone(info DialDoneInfo) {
	if t != nil && t.DialDone != nil {
		t.DialDone(info)
	}
}
