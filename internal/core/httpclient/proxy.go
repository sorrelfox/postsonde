package httpclient

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// maxProxyPools bounds the forward-proxy connection pools of one
// transport; past it the idle ones are closed and the pools rebuilt on
// demand (a proxy changed over and over).
const maxProxyPools = 8

// EventProxy is the timeline event of a hop sent to an HTTP proxy in
// absolute form (NET-02); its detail names the proxy, never its
// credentials. A hop tunnelled through a proxy (CONNECT, SOCKS) shows the
// proxy in its dial_start and dial_done details instead.
const EventProxy = "proxy"

// forwardRouter sends each http:// hop that the dialer's proxy route
// sends to an HTTP proxy (netx.ForwardProxier) through a connection pool
// of that proxy, whose transport writes the request in absolute form
// (NET-02); every other hop goes through base. Proxy-Authorization comes
// from the proxy's URL, never from the request, and is left out of the
// recorded request.
type forwardRouter struct {
	lookup       func(ctx context.Context, address string) *netx.HTTPProxy
	base         http.RoundTripper
	newTransport func(*netx.HTTPProxy) *http.Transport

	mu    sync.Mutex
	pools map[*netx.HTTPProxy]*http.Transport
}

func (r *forwardRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	// A Host header other than the URL's goes end to end through a
	// tunnel: in absolute form net/http would put it into the URL the
	// proxy connects to.
	if !strings.EqualFold(req.URL.Scheme, "http") {
		return r.base.RoundTrip(req)
	}
	addr := hostPort(req.URL.Hostname(), req.URL.Port(), "80")
	var p *netx.HTTPProxy
	if req.Host == "" || req.Host == req.URL.Host {
		p = r.lookup(req.Context(), addr)
	}
	if p == nil {
		// Not to an HTTP proxy in absolute form: the route may still
		// have a proxy for http:// requests of its own (the system's
		// SOCKS or web proxy, NET-01), which the mark lets it pick, for
		// every connection of this pool alike.
		plain := req.WithContext(netx.PlainHTTPContext(req.Context()))
		resp, err := r.base.RoundTrip(plain)
		if resp != nil {
			resp.Request = req
		}
		return resp, err
	}
	if st, ok := req.Context().Value(stateKey{}).(*sendState); ok && st.rec != nil {
		st.rec.forwardProxy(p.Via(), absoluteTarget(req.URL), p.AuthorizationHeader())
	}
	send := req
	if req.URL.Opaque != "" {
		// net/http writes an Opaque path as it is, also to a proxy: put
		// the scheme and host in front ("//host/path" is written as
		// "http://host/path"). The URL's Host is unchanged, so the pool
		// and ForwardURL still see the target.
		send = req.Clone(req.Context())
		send.URL.Opaque = "//" + req.URL.Host + req.URL.Opaque
	}
	resp, err := r.pool(p).RoundTrip(send)
	if resp != nil {
		resp.Request = req
	}
	if err == nil && resp.StatusCode == http.StatusProxyAuthRequired {
		// The proxy's answer, not the server's: an error like the 407
		// of a CONNECT.
		_ = resp.Body.Close()
		return nil, p.AuthError(resp.Header, "tcp", addr)
	}
	return resp, err
}

// absoluteTarget is the request-target net/http writes in absolute form.
func absoluteTarget(u *url.URL) string {
	return u.Scheme + "://" + u.Host + u.RequestURI()
}

func (r *forwardRouter) pool(p *netx.HTTPProxy) *http.Transport {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tr, ok := r.pools[p]; ok {
		return tr
	}
	if r.pools == nil || len(r.pools) >= maxProxyPools {
		for _, tr := range r.pools {
			tr.CloseIdleConnections()
		}
		r.pools = map[*netx.HTTPProxy]*http.Transport{}
	}
	tr := r.newTransport(p)
	r.pools[p] = tr
	return tr
}

func (r *forwardRouter) closeIdle() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tr := range r.pools {
		tr.CloseIdleConnections()
	}
}

// newForwardTransport returns the transport of the absolute-form requests
// to proxy p: HTTP/1.1 only, Proxy is p.ForwardURL of the request's
// host:port, and every dial goes through d (the guard, the connection log
// and the route see the target) with netx.ForwardContext, so the route
// connects it to p without CONNECT.
func newForwardTransport(d netx.Dialer, o TransportOptions, p *netx.HTTPProxy) *http.Transport {
	tr := NewTransport(netx.DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		return d.DialContext(netx.ForwardContext(ctx, p), network, address)
	}), o)
	tr.Proxy = func(req *http.Request) (*url.URL, error) {
		return p.ForwardURL(hostPort(req.URL.Hostname(), req.URL.Port(), "80")), nil
	}
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	tr.Protocols = &protocols
	return tr
}
