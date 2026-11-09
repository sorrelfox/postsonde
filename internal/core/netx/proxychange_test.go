package netx

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// TestProxyRouterOnChange (SOCKS-05): the hooks run when the proxy in
// effect changes (another proxy, a password, none, a failure), as Route,
// ForwardProxy or Refresh read it; not on the first reading, not while it
// stays the same.
func TestProxyRouterOnChange(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	current := ProxyURL{}
	var failure error
	r := NewProxyRouter(DialerFunc(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no dials in this test")
	}), func(context.Context) (ProxyURL, error) {
		mu.Lock()
		defer mu.Unlock()
		return current, failure
	}, ProxyOptions{})
	var calls atomic.Int32
	r.OnChange(func() { calls.Add(1) })
	r.OnChange(nil)
	set := func(u string, err error) {
		mu.Lock()
		defer mu.Unlock()
		current, failure = ProxyURL{}, err
		if u != "" {
			current, _ = ParseProxyURL(u)
		}
	}
	want := func(n int32, what string) {
		t.Helper()
		if got := calls.Load(); got != n {
			t.Errorf("%s: %d calls, want %d", what, got, n)
		}
	}

	r.Refresh(t.Context())
	want(0, "first reading")
	r.Refresh(t.Context())
	want(0, "unchanged")
	set("socks5h://proxy.lan:1080", nil)
	r.Refresh(t.Context())
	want(1, "a proxy set")
	_, _ = r.Route(t.Context(), "tcp", "api.example.test:443")
	want(1, "a dial with the same proxy")
	set("socks5h://alice:pw@proxy.lan:1080", nil)
	_, _ = r.Route(t.Context(), "tcp", "api.example.test:443")
	want(2, "a new password, seen by a dial")
	set("http://proxy.lan:3128", nil)
	r.ForwardProxy(t.Context(), "api.example.test:80")
	want(3, "an HTTP proxy, seen by ForwardProxy")
	set("", errors.New("settings unreadable"))
	r.Refresh(t.Context())
	want(4, "a failure")
	r.Refresh(t.Context())
	want(4, "the same failure")
	set("", nil)
	r.Refresh(t.Context())
	want(5, "no proxy")

	// Concurrent readings call the hooks once per change.
	set("socks5h://other.lan:1080", nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { r.Refresh(t.Context()) })
	}
	wg.Wait()
	want(6, "concurrent readings")
}
