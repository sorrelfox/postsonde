package httpclient

import (
	"context"
	"fmt"
	"io"
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

// Tests of the redirect options (HTTP-22) and the TLS verification switch
// (HTTP-23). The safe defaults they may not change are in
// safedefaults_test.go.

// TestRedirectFollowAndLimit: FollowRedirects false and MaxRedirects 0
// return the first 3xx; a limit of n follows n redirects and fails on the
// next one.
func TestRedirectFollowAndLimit(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n > 0 {
			http.Redirect(w, r, "/?n="+strconv.Itoa(n-1), http.StatusFound)
			return
		}
		io.WriteString(w, "end")
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, TransportOptions{})

	tests := []struct {
		name     string
		settings Settings
		status   int    // 0: an error is expected
		hits     int32  // requests the server saw
		url      string // the response's URL query
	}{
		{"not followed", Settings{FollowRedirects: false, MaxRedirects: 10}, http.StatusFound, 1, "n=3"},
		{"max 0 follows none", Settings{FollowRedirects: true, MaxRedirects: 0}, http.StatusFound, 1, "n=3"},
		{"max 1 fails on the second", Settings{FollowRedirects: true, MaxRedirects: 1}, 0, 2, ""},
		{"max 3 reaches the end", Settings{FollowRedirects: true, MaxRedirects: 3}, http.StatusOK, 4, "n=0"},
	}
	for _, tt := range tests {
		hits.Store(0)
		resp, err := c.Send(t.Context(), Request{Method: http.MethodGet, URL: srv.URL + "/?n=3", Settings: tt.settings})
		if tt.status == 0 {
			e := wantErr(t, err, errs.Protocol, CodeTooManyRedirects)
			if e.Details["maxRedirects"] != strconv.Itoa(tt.settings.MaxRedirects) {
				t.Errorf("%s: maxRedirects detail %q", tt.name, e.Details["maxRedirects"])
			}
		} else {
			if err != nil {
				t.Fatalf("%s: Send: %v", tt.name, err)
			}
			if resp.StatusCode != tt.status || resp.URL != srv.URL+"/?"+tt.url {
				t.Errorf("%s: status %d at %s, want %d at ?%s", tt.name, resp.StatusCode, resp.URL, tt.status, tt.url)
			}
		}
		if n := hits.Load(); n != tt.hits {
			t.Errorf("%s: the server got %d requests, want %d", tt.name, n, tt.hits)
		}
	}
}

// TestRedirectKeepMethod: by default 301, 302 and 303 turn a POST into a
// GET without body or Content-Type; with KeepMethod the POST, its body and
// its Content-Type reach the target, over several hops. 307 keeps them
// either way.
func TestRedirectKeepMethod(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/to/{code}", func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(r.PathValue("code"))
		// Two hops: through /via to /echo.
		http.Redirect(w, r, "/via/"+strconv.Itoa(code), code)
	})
	mux.HandleFunc("/via/{code}", func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(r.PathValue("code"))
		http.Redirect(w, r, "/echo", code)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %q type=%q", r.Method, b, r.Header.Get("Content-Type"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := newTestClient(t, TransportOptions{})

	const kept = `POST "{\"a\":1}" type="application/json"`
	const changed = `GET "" type=""`
	tests := []struct {
		code int
		keep bool
		want string
	}{
		{301, false, changed},
		{302, false, changed},
		{303, false, changed},
		{307, false, kept},
		{301, true, kept},
		{302, true, kept},
		{303, true, kept},
		{307, true, kept},
		{308, true, kept},
	}
	for _, tt := range tests {
		s := DefaultSettings()
		s.KeepMethod = tt.keep
		req := Request{Method: http.MethodPost, URL: srv.URL + "/to/" + strconv.Itoa(tt.code),
			Body: RawBody{Data: []byte(`{"a":1}`), Type: "application/json"}, Settings: s}
		resp, err := c.Send(t.Context(), req)
		if err != nil {
			t.Fatalf("%d keep=%v: Send: %v", tt.code, tt.keep, err)
		}
		if string(resp.Body) != tt.want {
			t.Errorf("%d keep=%v: the target saw %s, want %s", tt.code, tt.keep, resp.Body, tt.want)
		}
		if len(resp.Redirects) != 2 {
			t.Fatalf("%d keep=%v: %d hops recorded, want 2", tt.code, tt.keep, len(resp.Redirects))
		}
		// The timeline records the method each hop really sent.
		wantMethod := "GET"
		if tt.keep || tt.code >= 307 {
			wantMethod = "POST"
		}
		if hop := resp.Redirects[1].Request; hop == nil || hop.Method != wantMethod || resp.Request.Method != wantMethod {
			t.Errorf("%d keep=%v: recorded methods %+v, final %q; want %s", tt.code, tt.keep, hop, resp.Request.Method, wantMethod)
		}
	}

	// A typed Content-Type row, in its spelling, is restored too, and a
	// GET stays a GET.
	s := DefaultSettings()
	s.KeepMethod = true
	resp, err := c.Send(t.Context(), Request{Method: "PUT", URL: srv.URL + "/to/302",
		Headers: []Header{on("content-type", "text/plain")}, Body: RawBody{Data: []byte("x")}, Settings: s})
	if err != nil || string(resp.Body) != `PUT "x" type="text/plain"` {
		t.Errorf("PUT with a typed Content-Type: %v, %v", resp, err)
	}
	resp, err = c.Send(t.Context(), Request{Method: http.MethodGet, URL: srv.URL + "/to/301", Settings: s})
	if err != nil || string(resp.Body) != `GET "" type=""` {
		t.Errorf("GET: %v, %v", resp, err)
	}
}

// TestRedirectForwardAuthorization: with ForwardAuthorization the
// Authorization row reaches another origin, also after coming back to the
// origin; Proxy-Authorization stays dropped.
func TestRedirectForwardAuthorization(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s auth=%q proxy=%q", r.Host, r.Header.Get("Authorization"), r.Header.Get("Proxy-Authorization"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	toServer := netx.DialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return netx.NewDirect(netx.Options{}).DialContext(ctx, network, srv.Listener.Addr().String())
	})
	c := New(toServer, TransportOptions{})
	t.Cleanup(c.CloseIdleConnections)

	tests := []struct {
		name, to string
		forward  bool
		want     string
	}{
		{"another host, stripped", "http://evil.test/end", false, `evil.test auth="" proxy=""`},
		{"another host, forwarded", "http://evil.test/end", true, `evil.test auth="Bearer secret" proxy=""`},
		{"a subdomain, forwarded", "http://sub.api.test/end", true, `sub.api.test auth="Bearer secret" proxy=""`},
		{"back to the origin, forwarded", "http://evil.test/start?to=" + url.QueryEscape("http://api.test/end"), true, `api.test auth="Bearer secret" proxy=""`},
		{"same origin, forwarded", "http://api.test/end", true, `api.test auth="Bearer secret" proxy="Basic cHJveHk="`},
	}
	for _, tt := range tests {
		s := DefaultSettings()
		s.ForwardAuthorization = tt.forward
		req := Request{Method: http.MethodGet, URL: "http://api.test/start?to=" + url.QueryEscape(tt.to), Settings: s,
			Headers: []Header{on("authorization", "Bearer secret"), on("Proxy-Authorization", "Basic cHJveHk=")}}
		resp, err := c.Send(t.Context(), req)
		if err != nil {
			t.Fatalf("%s: Send: %v", tt.name, err)
		}
		if string(resp.Body) != tt.want {
			t.Errorf("%s: the target saw %s, want %s", tt.name, resp.Body, tt.want)
		}
	}
}

// TestTLSVerificationSwitch: InsecureSkipVerify accepts a certificate the
// system roots do not trust; without it the same server is refused.
func TestTLSVerificationSwitch(t *testing.T) {
	t.Parallel()

	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }), true)

	_, err := newTestClient(t, TransportOptions{}).Send(t.Context(), get(srv.URL))
	wantErr(t, err, errs.TLS, CodeTLSUnknownAuthority)

	resp, err := newTestClient(t, TransportOptions{InsecureSkipVerify: true}).Send(t.Context(), get(srv.URL))
	if err != nil || string(resp.Body) != "ok" {
		t.Fatalf("with verification off: %v, %v", resp, err)
	}
}
