package netx

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestProxyRouterSplitHTTP (NET-01): with a proxy per scheme, as the OS
// settings give them, http:// requests use the HTTP one (ForwardProxy for
// an HTTP proxy, a PlainHTTPContext dial for a SOCKS one or none) and every
// other dial the other; the bypass list applies to both; the connection
// test falls back to the HTTP proxy when it is the only one.
func TestProxyRouterSplitHTTP(t *testing.T) {
	t.Parallel()
	web, _ := ParseProxyURL("http://web.proxy.test:3128")
	secure, _ := ParseProxyURL("http://secure.proxy.test:3129")
	socks, _ := ParseProxyURL("socks5h://socks.proxy.test:1080")
	counting := &countingDialer{next: NewDirect(Options{})}
	var cfg atomic.Pointer[ProxyConfig]
	set := func(c ProxyConfig) { cfg.Store(&c) }
	r := NewProxyConfigRouter(counting, func(context.Context) (ProxyConfig, error) { return *cfg.Load(), nil }, ProxyOptions{})
	var changes atomic.Int32
	r.OnChange(func() { changes.Add(1) })
	plain := PlainHTTPContext(t.Context())
	via := func(ctx context.Context, addr string) string {
		t.Helper()
		d, err := r.Route(ctx, "tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if d == Dialer(counting) {
			return "direct"
		}
		return d.(interface{ Via() string }).Via()
	}

	set(ProxyConfig{Proxy: secure, SplitHTTP: true, HTTP: web, Bypass: mustBypass(t, "*.corp")})
	if got := via(t.Context(), "api.example.test:443"); got != secure.String() {
		t.Errorf("tunnel via %s, want the secure proxy", got)
	}
	if p := r.ForwardProxy(t.Context(), "api.example.test:80"); p == nil || p.Via() != web.String() {
		t.Errorf("ForwardProxy = %v, want the web proxy", p)
	}
	if p := r.ForwardProxy(t.Context(), "git.corp:80"); p != nil {
		t.Error("a bypassed http:// target goes to the web proxy")
	}
	if got := via(plain, "git.corp:80"); got != "direct" {
		t.Errorf("bypassed plain http via %s", got)
	}

	// A SOCKS proxy for http:// requests: not in absolute form, the
	// marked dial tunnels through it.
	set(ProxyConfig{Proxy: secure, SplitHTTP: true, HTTP: socks})
	if p := r.ForwardProxy(t.Context(), "api.example.test:80"); p != nil {
		t.Errorf("ForwardProxy = %v with a SOCKS proxy for http://", p.Via())
	}
	if got := via(plain, "api.example.test:80"); got != socks.String() {
		t.Errorf("plain http via %s, want SOCKS", got)
	}
	if got := via(t.Context(), "api.example.test:80"); got != secure.String() {
		t.Errorf("an unmarked dial via %s, want the secure proxy", got)
	}

	// Only a web proxy: http:// requests through it, the rest direct.
	set(ProxyConfig{SplitHTTP: true, HTTP: web})
	if got := via(t.Context(), "api.example.test:443"); got != "direct" {
		t.Errorf("https with only a web proxy via %s", got)
	}
	if p := r.ForwardProxy(t.Context(), "api.example.test:80"); p == nil {
		t.Error("ForwardProxy = nil with a web proxy")
	}
	// A cancelled test: it names the proxy without connecting anywhere.
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if res := r.Test(cancelled, "example.test:1"); res.Proxy != web {
		t.Errorf("Test used %v, want the web proxy", res.Proxy)
	}

	// Without SplitHTTP, http:// requests use the one proxy as before.
	set(ProxyConfig{Proxy: socks})
	if got := via(plain, "api.example.test:80"); got != socks.String() {
		t.Errorf("one proxy: plain http via %s", got)
	}
	if n := changes.Load(); n != 3 {
		t.Errorf("%d changes, want 3", n)
	}
}
