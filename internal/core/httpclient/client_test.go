package httpclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

// newTLSServer starts an httptest TLS server that does not log handshake
// errors, with HTTP/2 when h2 is set.
func newTLSServer(t *testing.T, h http.Handler, h2 bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = h2
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func trusting(srv *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

func headerValues(hs []Header, name string) []string {
	var out []string
	for _, h := range hs {
		if h.Name == name {
			out = append(out, h.Value)
		}
	}
	return out
}

func TestResponseCapture(t *testing.T) {
	t.Parallel()

	payload := []byte("\x00\x01 binary \xff\xfe")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["X-B"] = []string{"2"}
		w.Header()["X-A"] = []string{"1", "1b"}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusCreated)
		w.Write(payload)
	}))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})

	resp, err := c.Send(t.Context(), get(srv.URL+"/x?y=1#fragment"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Proto != "HTTP/1.1" || resp.StatusCode != http.StatusCreated || resp.Status != "201 Created" {
		t.Errorf("status = %s %d %q, want HTTP/1.1 201 \"201 Created\"", resp.Proto, resp.StatusCode, resp.Status)
	}
	if !bytes.Equal(resp.Body, payload) || resp.BodyTruncated {
		t.Errorf("body = %q (truncated %v), want %q", resp.Body, resp.BodyTruncated, payload)
	}
	if n := int64(len(payload)); resp.Size != n || resp.ContentLength != n {
		t.Errorf("Size, ContentLength = %d, %d, want %d", resp.Size, resp.ContentLength, n)
	}
	if want := srv.URL + "/x?y=1"; resp.URL != want {
		t.Errorf("URL = %q, want %q", resp.URL, want)
	}
	if !testenv.Measured(resp.Duration) {
		t.Errorf("Duration = %v, want > 0", resp.Duration)
	}
	var xs []Header
	for _, h := range resp.Headers {
		if strings.HasPrefix(h.Name, "X-") {
			xs = append(xs, h)
		}
	}
	if want := []Header{{"X-A", "1", true}, {"X-A", "1b", true}, {"X-B", "2", true}}; !slices.Equal(xs, want) {
		t.Errorf("X- headers = %v, want %v", xs, want)
	}
	if !slices.IsSortedFunc(resp.Headers, func(a, b Header) int { return strings.Compare(a.Name, b.Name) }) {
		t.Errorf("headers not sorted: %v", resp.Headers)
	}
	if len(resp.Trailers) != 0 {
		t.Errorf("Trailers = %v, want none", resp.Trailers)
	}
}

// TestResponseAsReceived pins what net/http keeps of a response head: the
// reason phrase as sent, header names canonicalized and merged.
func TestResponseAsReceived(t *testing.T) {
	t.Parallel()
	srv := startRawServer(t, "HTTP/1.1 299 Fine Thanks\r\nx-lower: 1\r\nX-Dup: a\r\nx-dup: b\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	c := newTestClient(t, TransportOptions{})

	resp, err := c.Send(t.Context(), get("http://"+srv.addr+"/"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.StatusCode != 299 || resp.Status != "299 Fine Thanks" {
		t.Errorf("status = %d %q, want 299 \"299 Fine Thanks\"", resp.StatusCode, resp.Status)
	}
	if got := headerValues(resp.Headers, "X-Lower"); !slices.Equal(got, []string{"1"}) {
		t.Errorf("X-Lower = %v, want [1]", got)
	}
	if got := headerValues(resp.Headers, "X-Dup"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("X-Dup = %v, want [a b]", got)
	}
	if len(resp.Body) != 0 || resp.Body == nil {
		t.Errorf("Body = %#v, want empty and not nil", resp.Body)
	}
}

func TestResponseTrailers(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Trailer", "X-Checksum")
		io.WriteString(w, "data")
		w.Header().Set("X-Checksum", "abc")
	}))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})

	resp, err := c.Send(t.Context(), get(srv.URL))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if want := []Header{{"X-Checksum", "abc", true}}; !slices.Equal(resp.Trailers, want) {
		t.Errorf("Trailers = %v, want %v", resp.Trailers, want)
	}
	if resp.ContentLength != -1 || string(resp.Body) != "data" {
		t.Errorf("ContentLength, Body = %d, %q, want -1, \"data\"", resp.ContentLength, resp.Body)
	}
}

// TestNoDecompression checks that a gzip body arrives compressed, as sent,
// and that the request asked for no encoding.
func TestNoDecompression(t *testing.T) {
	t.Parallel()

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	io.WriteString(zw, strings.Repeat("hello ", 100))
	zw.Close()
	var acceptEncoding atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding.Store(fmt.Sprint(r.Header["Accept-Encoding"]))
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(gz.Bytes())
	}))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})

	resp, err := c.Send(t.Context(), get(srv.URL))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !bytes.Equal(resp.Body, gz.Bytes()) {
		t.Errorf("body = %d bytes, want the %d gzip bytes as sent", len(resp.Body), gz.Len())
	}
	if got := headerValues(resp.Headers, "Content-Encoding"); !slices.Equal(got, []string{"gzip"}) {
		t.Errorf("Content-Encoding = %v, want [gzip]", got)
	}
	if got := acceptEncoding.Load(); got != "[]" {
		t.Errorf("server saw Accept-Encoding %v, want none", got)
	}
}

func TestBodyCap(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "0123456789")
	}))
	t.Cleanup(srv.Close) // after the parallel subtests
	c := newTestClient(t, TransportOptions{})

	tests := []struct {
		name      string
		max       int64
		want      string
		truncated bool
		size      int64
	}{
		{"default", 0, "0123456789", false, 10},
		{"larger than the body", 20, "0123456789", false, 10},
		{"exactly the body", 10, "0123456789", false, 10},
		{"smaller than the body", 4, "0123", true, 5},
		{"one byte", 1, "0", true, 2},
		{"no limit", -1, "0123456789", false, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := get(srv.URL)
			req.Settings.MaxBodyBytes = tt.max
			resp, err := c.Send(t.Context(), req)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if string(resp.Body) != tt.want || resp.BodyTruncated != tt.truncated || resp.Size != tt.size {
				t.Errorf("body, truncated, size = %q, %v, %d, want %q, %v, %d",
					resp.Body, resp.BodyTruncated, resp.Size, tt.want, tt.truncated, tt.size)
			}
		})
	}
}

func TestRedirects(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/r/{n}", func(w http.ResponseWriter, r *http.Request) {
		if n, _ := strconv.Atoi(r.PathValue("n")); n > 0 {
			http.Redirect(w, r, "/r/"+strconv.Itoa(n-1), http.StatusFound)
			return
		}
		fmt.Fprintf(w, "%s referer=%q", r.Method, r.Header.Get("Referer"))
	})
	mux.HandleFunc("/307", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/echo", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s", r.Method, b)
	})
	mux.HandleFunc("/q", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery == "" {
			w.Header().Set("Location", "?page=2")
			w.WriteHeader(http.StatusFound)
			return
		}
		fmt.Fprintf(w, "query %s", r.URL.RawQuery)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := newTestClient(t, TransportOptions{})

	follow := func(max int) Settings { return Settings{FollowRedirects: true, MaxRedirects: max} }
	post := func(path string) Request {
		return Request{Method: http.MethodPost, URL: path, Body: RawBody{Data: []byte("abc")}, Settings: follow(DefaultMaxRedirects)}
	}
	tests := []struct {
		name     string
		req      Request
		status   int
		body     string // checked when not ""
		location string // checked when not ""
		url      string
		code     string // an error code instead of a response
	}{
		{"not followed", Request{Method: "GET", URL: "/r/2"}, http.StatusFound, "", "/r/1", "/r/2", ""},
		{"followed with the default limit", get("/r/3"), 200, `GET referer=""`, "", "/r/0", ""},
		{"at the limit", Request{Method: "GET", URL: "/r/3", Settings: follow(3)}, 200, `GET referer=""`, "", "/r/0", ""},
		{"over the limit", Request{Method: "GET", URL: "/r/3", Settings: follow(2)}, 0, "", "", "", CodeTooManyRedirects},
		{"307 keeps the method and body", post("/307"), 200, "POST abc", "", "/echo", ""},
		{"302 turns POST into GET", post("/r/1"), 200, `GET referer=""`, "", "/r/0", ""},
		{"query-only location", get("/q"), 200, "query page=2", "", "/q?page=2", ""},
		{"Referer row kept", get("/r/1", on("Referer", "https://from.example/")), 200, `GET referer="https://from.example/"`, "", "/r/0", ""},
		{"lowercase referer row kept", get("/r/1", on("referer", "https://from.example/")), 200, `GET referer="https://from.example/"`, "", "/r/0", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := tt.req
			req.URL = srv.URL + req.URL
			resp, err := c.Send(t.Context(), req)
			if tt.code != "" {
				e := wantErr(t, err, errs.Protocol, tt.code)
				if e.Hint == "" {
					t.Error("no hint")
				}
				return
			}
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if resp.StatusCode != tt.status {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.status)
			}
			if tt.body != "" && string(resp.Body) != tt.body {
				t.Errorf("body = %q, want %q", resp.Body, tt.body)
			}
			if tt.location != "" {
				if got := headerValues(resp.Headers, "Location"); !slices.Equal(got, []string{tt.location}) {
					t.Errorf("Location = %v, want %q", got, tt.location)
				}
			}
			if want := srv.URL + tt.url; resp.URL != want {
				t.Errorf("URL = %q, want %q", resp.URL, want)
			}
		})
	}
}

// TestRedirectSensitiveHeaders checks that credentials typed in any
// spelling are not forwarded to another host (PRV-11).
func TestRedirectSensitiveHeaders(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	port := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
	mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/end", http.StatusFound) })
	mux.HandleFunc("/other", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+port+"/end", http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%q %q %q", r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.Header.Get("X-Other"))
	})
	c := newTestClient(t, TransportOptions{})

	tests := []struct{ path, want string }{
		{"/same", `"Bearer secret" "a=1" "kept"`},
		{"/other", `"" "" "kept"`},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			req := get(srv.URL+tt.path, on("authorization", "Bearer secret"), on("COOKIE", "a=1"), on("x-other", "kept"))
			resp, err := c.Send(t.Context(), req)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if string(resp.Body) != tt.want {
				t.Errorf("headers after the redirect = %s, want %s", resp.Body, tt.want)
			}
		})
	}
}

func TestNoCookieJar(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "1", Path: "/"})
		http.Redirect(w, r, "/get", http.StatusFound)
	})
	mux.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "cookie="+r.Header.Get("Cookie"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})

	for _, path := range []string{"/set", "/get"} {
		resp, err := c.Send(t.Context(), get(srv.URL+path))
		if err != nil {
			t.Fatalf("Send %s: %v", path, err)
		}
		if string(resp.Body) != "cookie=" {
			t.Errorf("%s: %s, want no cookie sent", path, resp.Body)
		}
	}
}

func TestHTTP2(t *testing.T) {
	t.Parallel()

	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s ua=%v ae=%v x=%q body=%q", r.Proto, r.Method, r.Header["User-Agent"],
			r.Header["Accept-Encoding"], r.Header.Get("X-Mixed-Case"), b)
	}), true)
	c := newTestClient(t, TransportOptions{RootCAs: trusting(srv)})

	var dials atomic.Int32
	ctx := netx.WithTrace(t.Context(), &netx.Trace{DialDone: func(netx.DialDoneInfo) { dials.Add(1) }})
	req := Request{Method: "PATCH", URL: srv.URL + "/h2", Headers: []Header{on("X-MiXeD-CaSe", "v")}, Body: RawBody{Data: []byte("b")}}
	resp, err := c.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Proto != "HTTP/2.0" || resp.StatusCode != 200 || resp.Status != "200 OK" {
		t.Errorf("status = %s %d %q, want HTTP/2.0 200", resp.Proto, resp.StatusCode, resp.Status)
	}
	if want := `HTTP/2.0 PATCH ua=[] ae=[] x="v" body="b"`; string(resp.Body) != want {
		t.Errorf("server saw %s, want %s", resp.Body, want)
	}
	if n := dials.Load(); n != 1 {
		t.Errorf("%d dials through the dialer, want 1", n)
	}
}

func TestTLSErrors(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	tlsSrv := newTLSServer(t, ok, false)
	plain := httptest.NewServer(ok)
	t.Cleanup(plain.Close)
	port := strconv.Itoa(tlsSrv.Listener.Addr().(*net.TCPAddr).Port)

	tests := []struct {
		name  string
		roots *x509.CertPool
		url   string
		code  string
	}{
		{"unknown authority", x509.NewCertPool(), tlsSrv.URL, CodeTLSUnknownAuthority},
		// The test certificate is for 127.0.0.1, ::1 and example.com.
		{"hostname mismatch", trusting(tlsSrv), "https://localhost:" + port + "/", CodeTLSHostnameMismatch},
		{"plain HTTP server", trusting(tlsSrv), "https://" + plain.Listener.Addr().String() + "/", CodeTLSServerSentHTTP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(t, TransportOptions{RootCAs: tt.roots})
			_, err := c.Send(t.Context(), get(tt.url))
			e := wantErr(t, err, errs.TLS, tt.code)
			if e.Hint == "" || e.Details["stage"] != "connecting" {
				t.Errorf("hint %q, stage %q, want a hint and stage connecting", e.Hint, e.Details["stage"])
			}
		})
	}
}

func TestInsecureSkipVerify(t *testing.T) {
	t.Parallel()
	srv := newTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), false)
	c := newTestClient(t, TransportOptions{RootCAs: x509.NewCertPool(), InsecureSkipVerify: true})
	if _, err := c.Send(t.Context(), get(srv.URL)); err != nil {
		t.Errorf("Send with verification off: %v", err)
	}
}

func TestMalformedResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		kind     errs.Kind
		code     string
		stage    string
	}{
		{"not HTTP", "SSH-2.0-OpenSSH_9.0\r\n\r\n", errs.Protocol, CodeMalformedResponse, "waiting"},
		{"bad status code", "HTTP/1.1 abc OK\r\n\r\n", errs.Protocol, CodeMalformedResponse, "waiting"},
		{"closed without a response", "", errs.Network, CodeConnectionClosed, "waiting"},
		{"short body", "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nabc", errs.Network, CodeBodyIncomplete, "reading_body"},
		{"bad chunked body", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n", errs.Protocol, CodeMalformedBody, "reading_body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := startRawServer(t, tt.response)
			c := newTestClient(t, TransportOptions{})
			_, err := c.Send(t.Context(), get("http://"+srv.addr+"/"))
			e := wantErr(t, err, tt.kind, tt.code)
			if e.Details["stage"] != tt.stage {
				t.Errorf("stage = %q, want %q", e.Details["stage"], tt.stage)
			}
		})
	}
}

func TestTimeouts(t *testing.T) {
	t.Parallel()

	partial := func(w http.ResponseWriter) {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "abc")
	}
	tests := []struct {
		name     string
		write    func(http.ResponseWriter)
		settings Settings
		code     string
		stage    string
	}{
		{"request timeout while waiting", nil, Settings{Timeout: 100 * time.Millisecond}, CodeTimeout, "waiting"},
		{"response header timeout", nil, Settings{ResponseHeaderTimeout: 100 * time.Millisecond}, CodeResponseHeaderTimeout, "waiting"},
		{"request timeout while reading the body", partial, Settings{Timeout: 300 * time.Millisecond}, CodeTimeout, "reading_body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := blockingServer(t, tt.write)
			c := newTestClient(t, TransportOptions{})
			start := time.Now()
			_, err := c.Send(t.Context(), Request{Method: "GET", URL: srv.URL, Settings: tt.settings})
			e := wantErr(t, err, errs.Timeout, tt.code)
			if e.Details["stage"] != tt.stage || e.Hint == "" {
				t.Errorf("stage %q, hint %q, want stage %q and a hint", e.Details["stage"], e.Hint, tt.stage)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("took %v", d)
			}
		})
	}
}

// TestHeaderTimeoutAfterHeaders checks that a response header timeout does
// not cut a slow body once the headers are in.
func TestHeaderTimeoutAfterHeaders(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "a")
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		io.WriteString(w, "b")
	}))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})
	resp, err := c.Send(t.Context(), Request{Method: "GET", URL: srv.URL, Settings: Settings{ResponseHeaderTimeout: 50 * time.Millisecond}})
	if err != nil || string(resp.Body) != "ab" {
		t.Fatalf("Send = %v, want body \"ab\"", err)
	}
}

func TestGuardDenies(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	deny := func(_ context.Context, c netx.DialCheck) error { return netx.Deny(c, "Blocked by this test.") }
	c := New(netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(deny)), TransportOptions{})
	defer c.CloseIdleConnections()

	_, err := c.Send(t.Context(), get(srv.URL))
	e := wantErr(t, err, errs.PermissionDenied, netx.CodeDialDenied)
	if e.Hint != "Blocked by this test." {
		t.Errorf("hint = %q", e.Hint)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the server got %d requests, want 0", n)
	}
}

// TestTraceHooks shows the hook for the timeline: a netx.Trace and an
// httptrace.ClientTrace in the context see the dial and the exchange, and
// the second request reuses the connection.
func TestTraceHooks(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	c := newTestClient(t, TransportOptions{})

	var mu sync.Mutex
	var dials []netx.DialDoneInfo
	var reused []bool
	var wrote, firstByte int
	ctx := netx.WithTrace(t.Context(), &netx.Trace{DialDone: func(i netx.DialDoneInfo) {
		mu.Lock()
		defer mu.Unlock()
		dials = append(dials, i)
	}})
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(i httptrace.GotConnInfo) {
			mu.Lock()
			defer mu.Unlock()
			reused = append(reused, i.Reused)
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			mu.Lock()
			defer mu.Unlock()
			wrote++
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			defer mu.Unlock()
			firstByte++
		},
	})
	for range 2 {
		if _, err := c.Send(ctx, get(srv.URL)); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(dials) != 1 || dials[0].Err != nil || dials[0].RemoteAddr.String() != srv.Listener.Addr().String() {
		t.Errorf("dials = %+v, want one to %s", dials, srv.Listener.Addr())
	}
	if !slices.Equal(reused, []bool{false, true}) {
		t.Errorf("GotConn Reused = %v, want [false true]", reused)
	}
	if wrote != 2 || firstByte != 2 {
		t.Errorf("WroteRequest, GotFirstResponseByte = %d, %d, want 2, 2", wrote, firstByte)
	}
}

func TestNewTransport(t *testing.T) {
	t.Parallel()

	tr := NewTransport(netx.NewDirect(netx.Options{}), TransportOptions{})
	switch {
	case tr.Proxy != nil:
		t.Error("Proxy is set; proxies are dialers, never from the environment")
	case tr.DialContext == nil, tr.DialTLSContext != nil, tr.Dial != nil, tr.DialTLS != nil:
		t.Error("connections must come from the dialer's DialContext only")
	case !tr.DisableCompression:
		t.Error("DisableCompression is false")
	case !tr.ForceAttemptHTTP2:
		t.Error("ForceAttemptHTTP2 is false")
	case tr.TLSClientConfig.MinVersion != tls.VersionTLS12, tr.TLSClientConfig.InsecureSkipVerify, tr.TLSClientConfig.RootCAs != nil:
		t.Errorf("TLS config = %+v, want TLS 1.2+, verified against system roots", tr.TLSClientConfig)
	case tr.MaxIdleConns != DefaultMaxIdleConns, tr.MaxIdleConnsPerHost != DefaultMaxIdleConnsPerHost,
		tr.IdleConnTimeout != DefaultIdleConnTimeout, tr.TLSHandshakeTimeout != DefaultTLSHandshakeTimeout:
		t.Error("pool and timeout defaults not applied")
	}

	tr = NewTransport(netx.NewDirect(netx.Options{}), TransportOptions{InsecureSkipVerify: true, TLSHandshakeTimeout: -1, MaxIdleConnsPerHost: 3})
	if !tr.TLSClientConfig.InsecureSkipVerify || tr.TLSHandshakeTimeout != 0 || tr.MaxIdleConnsPerHost != 3 {
		t.Error("options not applied")
	}

	// TLSConfig replaces RootCAs and InsecureSkipVerify, is cloned, and
	// gets the TLS 1.2 floor when it has no minimum.
	custom := &tls.Config{ServerName: "override"}
	tr = NewTransport(netx.NewDirect(netx.Options{}), TransportOptions{TLSConfig: custom, InsecureSkipVerify: true, RootCAs: x509.NewCertPool()})
	if c := tr.TLSClientConfig; c == custom || c.ServerName != "override" || c.InsecureSkipVerify || c.RootCAs != nil || c.MinVersion != tls.VersionTLS12 || custom.MinVersion != 0 {
		t.Errorf("TLSConfig not applied as a clone: %+v", c)
	}

	defer func() {
		if recover() == nil {
			t.Error("New(nil) did not panic")
		}
	}()
	New(nil, TransportOptions{})
}
