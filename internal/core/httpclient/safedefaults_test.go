package httpclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// The tests in this file lock in the safe network defaults of PRV-11:
// TLS verification on, redirects capped, and Authorization never sent to
// another host. Changing one of them must be a deliberate decision that
// updates these tests and docs/privacy.md.

// TestSafeDefaultTLSVerification: a client built with zero options
// verifies certificates, so a server with an untrusted certificate fails.
func TestSafeDefaultTLSVerification(t *testing.T) {
	t.Parallel()

	tr := NewTransport(netx.NewDirect(netx.Options{}), TransportOptions{})
	if tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Errorf("zero TransportOptions: InsecureSkipVerify %v, MinVersion %x; want verification on, TLS 1.2+",
			tr.TLSClientConfig.InsecureSkipVerify, tr.TLSClientConfig.MinVersion)
	}

	var hits atomic.Int32
	srv := newTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }), false)
	c := newTestClient(t, TransportOptions{}) // system roots: the test CA is not among them
	_, err := c.Send(t.Context(), get(srv.URL))
	wantErr(t, err, errs.TLS, CodeTLSUnknownAuthority)
	if n := hits.Load(); n != 0 {
		t.Errorf("the untrusted server got %d requests, want 0", n)
	}
}

// TestSafeDefaultRedirectCap: the editor's defaults follow redirects but at
// most DefaultMaxRedirects (10); a redirect loop fails after 10 hops.
// MaxRedirects 0 follows none (HTTP-22), never "unlimited": there is no
// setting without a cap.
func TestSafeDefaultRedirectCap(t *testing.T) {
	t.Parallel()

	if DefaultMaxRedirects != 10 {
		t.Errorf("DefaultMaxRedirects = %d, want 10", DefaultMaxRedirects)
	}
	if d := DefaultSettings(); !d.FollowRedirects || d.MaxRedirects != DefaultMaxRedirects {
		t.Errorf("DefaultSettings = %+v, want redirects followed up to %d", d, DefaultMaxRedirects)
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, TransportOptions{})

	for name, s := range map[string]Settings{
		"DefaultSettings":  DefaultSettings(),
		"MaxRedirects 100": {FollowRedirects: true, MaxRedirects: 100},
	} {
		want := int32(s.MaxRedirects)
		hits.Store(0)
		_, err := c.Send(t.Context(), Request{Method: http.MethodGet, URL: srv.URL + "/loop", Settings: s})
		e := wantErr(t, err, errs.Protocol, CodeTooManyRedirects)
		if e.Details["maxRedirects"] != strconv.Itoa(int(want)) {
			t.Errorf("%s: maxRedirects detail %q, want %d", name, e.Details["maxRedirects"], want)
		}
		// The first request and want redirects followed.
		if n := hits.Load(); n != want+1 {
			t.Errorf("%s: the server got %d requests, want %d", name, n, want+1)
		}
	}

	// The zero Settings, and MaxRedirects 0 with FollowRedirects, send
	// exactly one request.
	for name, s := range map[string]Settings{
		"zero Settings":  {},
		"MaxRedirects 0": {FollowRedirects: true},
	} {
		hits.Store(0)
		resp, err := c.Send(t.Context(), Request{Method: http.MethodGet, URL: srv.URL + "/loop", Settings: s})
		if err != nil || resp.StatusCode != http.StatusFound || hits.Load() != 1 {
			t.Errorf("%s: %v, %v, %d requests; want the 302 after one request", name, resp, err, hits.Load())
		}
	}
}

// TestSafeDefaultAuthorizationNotForwarded: credentials go only to the
// origin the request was sent to. A redirect to a subdomain, a parent
// domain, another port or another host drops them, for the rest of the
// redirects (net/http keeps them dropped even back at the origin); a
// redirect within the origin keeps them. TestSameOrigin covers a scheme
// change.
func TestSafeDefaultAuthorizationNotForwarded(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s auth=%q proxy=%q other=%q", r.Host, r.Header.Get("Authorization"),
			r.Header.Get("Proxy-Authorization"), r.Header.Get("X-Other"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// Every host name and port reaches the test server, so the redirects
	// can change host and port freely.
	toServer := netx.DialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return netx.NewDirect(netx.Options{}).DialContext(ctx, network, srv.Listener.Addr().String())
	})
	c := New(toServer, TransportOptions{})
	t.Cleanup(c.CloseIdleConnections)

	const kept = `auth="Bearer secret" proxy="Basic cHJveHk=" other="kept"`
	const dropped = `auth="" proxy="" other="kept"`
	tests := []struct {
		name, to, want string
	}{
		{"same origin", "http://api.test/end", "api.test " + kept},
		{"same origin, host in another case, default port spelled out", "http://API.test:80/end", "API.test:80 " + kept},
		{"another host", "http://evil.test/end", "evil.test " + dropped},
		{"a subdomain", "http://sub.api.test/end", "sub.api.test " + dropped},
		{"a parent domain", "http://test/end", "test " + dropped},
		{"another port", "http://api.test:8080/end", "api.test:8080 " + dropped},
		{"back to the origin after another host", "http://evil.test/start?to=" + url.QueryEscape("http://api.test/end"), "api.test " + dropped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := get("http://api.test/start?to="+url.QueryEscape(tt.to),
				on("authorization", "Bearer secret"), on("PROXY-AUTHORIZATION", "Basic cHJveHk="), on("X-Other", "kept"))
			resp, err := c.Send(t.Context(), req)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if string(resp.Body) != tt.want {
				t.Errorf("after the redirect the server saw %s, want %s", resp.Body, tt.want)
			}
		})
	}
}

func TestSameOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want bool
	}{
		{"http://a.test/x", "http://a.test/y?q", true},
		{"http://a.test/", "HTTP://A.TEST:80/", true},
		{"https://a.test/", "https://a.test:443/", true},
		{"http://a.test/", "https://a.test/", false},
		{"https://a.test/", "http://a.test:443/", false},
		{"http://a.test/", "http://a.test:8080/", false},
		{"http://a.test/", "http://b.a.test/", false},
		{"http://[::1]:80/", "http://[::1]/", true},
	}
	for _, tt := range tests {
		a, _ := url.Parse(tt.a)
		b, _ := url.Parse(tt.b)
		if got := sameOrigin(a, b); got != tt.want {
			t.Errorf("sameOrigin(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
