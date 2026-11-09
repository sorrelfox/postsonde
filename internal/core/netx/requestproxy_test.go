package netx

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
)

// TestProxyRouterRequestProxy: a request's own proxy (WithRequestProxy)
// replaces the proxy in effect for the dials of its ctx: it is used
// although the target is on the bypass list, the zero ProxyURL connects
// directly although a proxy is in effect, loopback targets stay direct,
// and the proxy in effect is neither read nor changed by such a dial, so
// the OnChange hooks do not run.
func TestProxyRouterRequestProxy(t *testing.T) {
	t.Parallel()
	global := &netxtest.SOCKS5Server{Hosts: map[string]string{}}
	globalURL, _ := ParseProxyURL("socks5h://" + global.Start(t))
	own := &netxtest.SOCKS5Server{Username: "bob", Password: "s3cret", Hosts: map[string]string{}}
	ownURL, err := ParseProxyURL("socks5h://bob:s3cret@" + own.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	bypass, err := ParseBypass([]string{"bypassed.test"})
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingDialer{next: NewDirect(Options{})}
	var reads atomic.Int32
	r := NewProxyConfigRouter(counting, func(context.Context) (ProxyConfig, error) {
		reads.Add(1)
		return ProxyConfig{Proxy: globalURL, Bypass: bypass}, nil
	}, ProxyOptions{})
	var changes atomic.Int32
	r.OnChange(func() { changes.Add(1) })
	r.Refresh(t.Context())

	inherit := t.Context()
	custom := WithRequestProxy(t.Context(), ownURL)
	direct := WithRequestProxy(t.Context(), ProxyURL{})

	if d, err := r.Route(inherit, "tcp", "api.example.test:443"); err != nil || d.(*SOCKS5).Via() != globalURL.String() {
		t.Errorf("inherit: Route = %v, %v; want the proxy in effect", d, err)
	}
	if d, err := r.Route(inherit, "tcp", "bypassed.test:443"); err != nil || d != Dialer(counting) {
		t.Errorf("inherit, bypassed: Route = %v, %v; want direct", d, err)
	}
	readsBefore := reads.Load()
	for _, target := range []string{"api.example.test:443", "bypassed.test:443"} {
		d, err := r.Route(custom, "tcp", target)
		if err != nil {
			t.Fatalf("custom: %v", err)
		}
		if s, ok := d.(*SOCKS5); !ok || s.Via() != "socks5h://"+ownURL.Address() {
			t.Errorf("custom %s: Route = %v; want the request's proxy", target, d)
		}
	}
	d1, _ := r.Route(custom, "tcp", "a.test:1")
	d2, _ := r.Route(custom, "tcp", "b.test:1")
	if d1 != d2 {
		t.Error("the request's proxy got a new dialer per dial")
	}
	for _, target := range []string{"localhost:80", "127.0.0.1:80"} {
		if d, err := r.Route(custom, "tcp", target); err != nil || d != Dialer(counting) {
			t.Errorf("custom, loopback %s: Route = %v, %v; want direct", target, d, err)
		}
	}
	if d, err := r.Route(direct, "tcp", "api.example.test:443"); err != nil || d != Dialer(counting) {
		t.Errorf("none: Route = %v, %v; want direct", d, err)
	}
	if reads.Load() != readsBefore {
		t.Error("a dial with its own proxy read the proxy in effect")
	}
	// Alternating with dials that inherit changes nothing either.
	_, _ = r.Route(inherit, "tcp", "api.example.test:443")
	_, _ = r.Route(custom, "tcp", "api.example.test:443")
	_, _ = r.Route(inherit, "tcp", "api.example.test:443")
	if n := changes.Load(); n != 0 {
		t.Errorf("OnChange ran %d times for requests' own proxies", n)
	}

	// End to end: a dial through the request's proxy reaches it, with
	// its credentials, and the proxy in effect sees nothing.
	target := echoServer(t)
	own.Hosts["echo.test"] = "127.0.0.1"
	_, port, _ := net.SplitHostPort(target)
	c, err := Routed(r.Route).DialContext(custom, "tcp", net.JoinHostPort("echo.test", port))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c, "own proxy")
	if len(own.Requests()) != 1 || !own.Requests()[0].Name || len(global.Requests()) != 0 {
		t.Errorf("own proxy got %v, the proxy in effect %v", own.Requests(), global.Requests())
	}

	// An HTTP request's own proxy is the forward proxy of http:// URLs;
	// a SOCKS one or none is not.
	httpURL, _ := ParseProxyURL("http://127.0.0.1:3128")
	if p := r.ForwardProxy(WithRequestProxy(t.Context(), httpURL), "api.example.test:80"); p == nil || p.Via() != "http://127.0.0.1:3128" {
		t.Errorf("ForwardProxy with an HTTP request proxy = %v", p)
	}
	if p := r.ForwardProxy(WithRequestProxy(t.Context(), httpURL), "localhost:80"); p != nil {
		t.Errorf("ForwardProxy to loopback = %v", p)
	}
	if p := r.ForwardProxy(custom, "api.example.test:80"); p != nil {
		t.Errorf("ForwardProxy with a SOCKS request proxy = %v", p)
	}
	if p := r.ForwardProxy(direct, "api.example.test:80"); p != nil {
		t.Errorf("ForwardProxy with no proxy = %v", p)
	}
}

// TestRequestProxyDialer: a dialer over RequestProxyDialer routes every
// dial, and answers ForwardProxy, with its proxy, whatever the ctx.
func TestRequestProxyDialer(t *testing.T) {
	t.Parallel()
	own := &netxtest.SOCKS5Server{Hosts: map[string]string{}}
	ownURL, _ := ParseProxyURL("socks5h://" + own.Start(t))
	httpURL, _ := ParseProxyURL("http://127.0.0.1:3128")
	direct := NewDirect(Options{})
	r := NewProxyConfigRouter(direct, func(context.Context) (ProxyConfig, error) { return ProxyConfig{}, nil }, ProxyOptions{})
	app := NewForwardProxier(Routed(r.Route), r.ForwardProxy)

	d := RequestProxyDialer(app, ownURL)
	if _, ok := d.(ForwardProxier); !ok {
		t.Fatal("the request proxy dialer of a ForwardProxier is not one")
	}
	target := echoServer(t)
	own.Hosts["echo.test"] = "127.0.0.1"
	_, port, _ := net.SplitHostPort(target)
	c, err := d.DialContext(t.Context(), "tcp", net.JoinHostPort("echo.test", port))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c, "via the request proxy dialer")
	if len(own.Requests()) != 1 {
		t.Errorf("own proxy got %v", own.Requests())
	}
	if p := RequestProxyDialer(app, httpURL).(ForwardProxier).ForwardProxy(t.Context(), "api.example.test:80"); p == nil {
		t.Error("ForwardProxy did not see the request's HTTP proxy")
	}
	if _, ok := RequestProxyDialer(direct, ownURL).(ForwardProxier); ok {
		t.Error("a plain dialer became a ForwardProxier")
	}
}
