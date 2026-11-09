package wsclient

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// rawResponse returns a handler that answers the upgrade with the bytes
// build returns, then closes the connection.
func rawResponse(t *testing.T, build func(accept string) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nc, _ := hijack(t, w)
		defer nc.Close()
		_, _ = nc.Write([]byte(build(wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")))))
	}
}

func TestHandshakeFailures(t *testing.T) {
	t.Parallel()
	const ok = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
	cases := []struct {
		name  string
		resp  func(accept string) string
		proto []string
		code  string
		kind  errs.Kind
	}{
		{"wrong accept", func(string) string {
			return ok + "Sec-WebSocket-Accept: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
		}, nil, CodeInvalidAccept, errs.Protocol},
		{"missing accept", func(string) string { return ok + "\r\n" }, nil, CodeInvalidAccept, errs.Protocol},
		{"missing upgrade", func(a string) string {
			return "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + a + "\r\n\r\n"
		}, nil, CodeInvalidUpgrade, errs.Protocol},
		{"missing connection", func(a string) string {
			return "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: " + a + "\r\n\r\n"
		}, nil, CodeInvalidUpgrade, errs.Protocol},
		{"unrequested subprotocol", func(a string) string {
			return ok + "Sec-WebSocket-Accept: " + a + "\r\nSec-WebSocket-Protocol: chat\r\n\r\n"
		}, []string{"graphql-ws"}, CodeUnrequestedSubprotocol, errs.Protocol},
		{"subprotocol without request", func(a string) string {
			return ok + "Sec-WebSocket-Accept: " + a + "\r\nSec-WebSocket-Protocol: chat\r\n\r\n"
		}, nil, CodeUnrequestedSubprotocol, errs.Protocol},
		{"two subprotocols", func(a string) string {
			return ok + "Sec-WebSocket-Accept: " + a + "\r\nSec-WebSocket-Protocol: a, b\r\n\r\n"
		}, []string{"a", "b"}, CodeUnrequestedSubprotocol, errs.Protocol},
		{"unrequested extension", func(a string) string {
			return ok + "Sec-WebSocket-Accept: " + a + "\r\nSec-WebSocket-Extensions: permessage-deflate\r\n\r\n"
		}, nil, CodeUnrequestedExtension, errs.Protocol},
		{"not http", func(string) string { return "SSH-2.0-OpenSSH_9.6\r\n\r\n" }, nil, CodeMalformedResponse, errs.Protocol},
		{"folded header", func(string) string { return ok + " folded\r\n\r\n" }, nil, CodeMalformedResponse, errs.Protocol},
		{"closed without response", func(string) string { return "" }, nil, CodeConnectionClosed, errs.Network},
		{"cut response", func(string) string { return "HTTP/1.1 101 Switching" }, nil, CodeConnectionClosed, errs.Network},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newServer(t, false, rawResponse(t, tc.resp))
			o, st := options(srv, "/")
			o.Subprotocols = tc.proto
			c, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			err = c.Connect(context.Background())
			if errCode(err) != tc.code || errs.KindOf(err) != tc.kind {
				t.Fatalf("err = %v (%s, %s)", err, errCode(err), errs.KindOf(err))
			}
			if got := st.names(); got != "connecting,closed" {
				t.Fatalf("states = %s", got)
			}
			if c.Err() != err || c.CloseInfo().Code != wsframe.CloseAbnormalClosure || c.Handshake() == nil {
				t.Fatalf("after failure: Err %v, CloseInfo %+v", c.Err(), c.CloseInfo())
			}
		})
	}
}

func TestUpgradeRejected(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "denied=1; Path=/")
		w.Header().Set("X-Reason", "no token")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(strings.Repeat("forbidden!", 10)))
	})
	jar := cookies.New(cookies.Options{})
	o, _ := options(srv, "/ws")
	o.Jar = jar
	o.MaxResponseBody = 50
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Connect(context.Background())
	if errCode(err) != CodeUpgradeRejected || errs.KindOf(err) != errs.Protocol {
		t.Fatalf("err = %v", err)
	}
	resp := c.Handshake().Response
	if resp.StatusCode != 403 || resp.Status != "403 Forbidden" || resp.Proto != "HTTP/1.1" ||
		string(resp.Body) != strings.Repeat("forbidden!", 5) || !resp.BodyTruncated ||
		!slices.Equal(resp.Header("x-reason"), []string{"no token"}) ||
		!strings.HasPrefix(resp.RawHeader, "HTTP/1.1 403 Forbidden\r\n") || !strings.HasSuffix(resp.RawHeader, "\r\n\r\n") {
		t.Fatalf("response = %+v", resp)
	}
	u, _ := url.Parse(wsURL(srv, "/"))
	if jar.Header(u) != "denied=1" {
		t.Fatalf("jar = %q", jar.Header(u))
	}
}

func TestUpgradeRedirect(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	o, _ := options(srv, "/")
	c, _ := New(o)
	err := c.Connect(context.Background())
	if errCode(err) != CodeUpgradeRejected || !strings.Contains(err.Error(), "/elsewhere") {
		t.Fatalf("err = %v", err)
	}
}

func TestSubprotocolSelected(t *testing.T) {
	t.Parallel()
	offered := make(chan string, 1)
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		offered <- r.Header.Get("Sec-WebSocket-Protocol")
		sc, nc := upgrade(t, w, r, "Sec-WebSocket-Protocol: graphql-transport-ws\r\n", nil)
		echo(sc, nc, make(chan error, 1))
	})
	o, _ := options(srv, "/")
	o.Subprotocols = []string{"graphql-ws", "graphql-transport-ws"}
	c := connect(t, o)
	if got := <-offered; got != "graphql-ws, graphql-transport-ws" {
		t.Fatalf("offered %q", got)
	}
	if c.Subprotocol() != "graphql-transport-ws" {
		t.Fatalf("Subprotocol = %q", c.Subprotocol())
	}
}

func TestCookies(t *testing.T) {
	t.Parallel()
	gotCookie := make(chan string, 1)
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		gotCookie <- strings.Join(r.Header.Values("Cookie"), " | ")
		sc, nc := upgrade(t, w, r, "Set-Cookie: sid=abc; Path=/; HttpOnly\r\n", nil)
		echo(sc, nc, make(chan error, 1))
	})
	store, err := cookies.Open(t.TempDir(), cookies.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jar := store.Jar()
	u, _ := url.Parse(wsURL(srv, "/"))
	jar.SetCookieLines(u, []string{"pre=1; Path=/", "row=jar; Path=/"})

	o, _ := options(srv, "/chat")
	o.Jar = jar
	o.Headers = []httpclient.Header{{Name: "Cookie", Value: "row=typed;", Enabled: true}}
	c := connect(t, o)
	// The row wins over the jar cookie of the same name.
	if got := <-gotCookie; got != "row=typed; pre=1" {
		t.Fatalf("Cookie sent = %q", got)
	}
	hs := c.Handshake()
	if hs.RequestCookie != "row=typed; pre=1" {
		t.Fatalf("RequestCookie = %q", hs.RequestCookie)
	}
	if !slices.ContainsFunc(jar.All(), func(ck cookies.Cookie) bool { return ck.Name == "sid" && ck.Value == "abc" && ck.HTTPOnly }) {
		t.Fatalf("jar = %+v", jar.All())
	}
	var names []string
	for _, e := range hs.Events {
		names = append(names, e.Name)
		if strings.Contains(e.Detail, "abc") {
			t.Fatalf("event %s shows a cookie value: %q", e.Name, e.Detail)
		}
	}
	if !slices.Contains(names, httpclient.EventCookiesSent) || !slices.Contains(names, httpclient.EventCookiesStored) {
		t.Fatalf("events = %v", names)
	}
}

func TestRawRequest(t *testing.T) {
	t.Parallel()
	raw := make(chan string, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		buf := make([]byte, 4096)
		var got []byte
		for !bytes.Contains(got, []byte("\r\n\r\n")) {
			n, err := nc.Read(buf)
			if err != nil {
				break
			}
			got = append(got, buf[:n]...)
		}
		raw <- string(got)
		_, _ = nc.Write([]byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"))
	}()

	o := Options{
		URL:    "WS://user:p%40ss@" + ln.Addr().String() + "/a%2Fb/{x}?q=1&q=2&s= é",
		Dialer: netx.NewDirect(netx.Options{}),
		Headers: []httpclient.Header{
			{Name: "x-lower", Value: " one ", Enabled: true},
			{Name: "Skipped", Value: "no", Enabled: false},
			{Name: "X-Dup", Value: "a", Enabled: true},
			{Name: "X-Dup", Value: "b", Enabled: true},
			{Name: "Origin", Value: "http://example.com", Enabled: true},
		},
		Subprotocols: []string{"v1"},
		Rand:         bytes.NewReader(make([]byte, 64)),
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if c.URL() != "ws://"+ln.Addr().String()+"/a%2Fb/{x}?q=1&q=2&s=%20%C3%A9" {
		t.Fatalf("URL = %q", c.URL())
	}
	if err := c.Connect(context.Background()); errCode(err) != CodeUpgradeRejected {
		t.Fatalf("err = %v", err)
	}
	want := "GET /a%2Fb/{x}?q=1&q=2&s=%20%C3%A9 HTTP/1.1\r\n" +
		"Host: " + ln.Addr().String() + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Protocol: v1\r\n" +
		"Authorization: Basic dXNlcjpwQHNz\r\n" +
		"x-lower: one\r\n" +
		"X-Dup: a\r\n" +
		"X-Dup: b\r\n" +
		"Origin: http://example.com\r\n" +
		"\r\n"
	if got := <-raw; got != want {
		t.Fatalf("wire:\n%s\nwant:\n%s", got, want)
	}
	if hs := c.Handshake(); hs.RawRequest != want || len(hs.RequestHeaders) != 11 || hs.Response.StatusCode != 400 {
		t.Fatalf("handshake = %+v", hs)
	}
}

func TestAllowHandshakeHeaders(t *testing.T) {
	t.Parallel()
	got := make(chan http.Header, 1)
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header
		sc, nc := upgrade(t, w, r, "", nil)
		echo(sc, nc, make(chan error, 1))
	})
	o, _ := options(srv, "/")
	o.Headers = []httpclient.Header{
		{Name: "Sec-WebSocket-Version", Value: "8", Enabled: true},
		{Name: "Sec-WebSocket-Key", Value: "dGhlIHNhbXBsZSBub25jZQ==", Enabled: true},
		{Name: "Host", Value: "example.test", Enabled: true},
	}
	if _, err := New(o); errCode(err) != CodeReservedHeader {
		t.Fatalf("without the option: %v", err)
	}
	o.AllowHandshakeHeaders = true
	connect(t, o)
	h := <-got
	if h.Values("Sec-WebSocket-Version")[0] != "8" || len(h.Values("Sec-WebSocket-Version")) != 1 ||
		h.Get("Sec-WebSocket-Key") != "dGhlIHNhbXBsZSBub25jZQ==" {
		t.Fatalf("headers = %v", h)
	}
}

func TestOptionsValidation(t *testing.T) {
	t.Parallel()
	d := netx.NewDirect(netx.Options{})
	cases := []struct {
		name string
		o    Options
		code string
	}{
		{"no scheme", Options{URL: "localhost:8080"}, CodeInvalidURL},
		{"http scheme", Options{URL: "http://localhost/"}, CodeSchemeUnsupported},
		{"fragment", Options{URL: "ws://localhost/#x"}, CodeInvalidURL},
		{"no host", Options{URL: "ws:///path"}, CodeInvalidURL},
		{"bad port", Options{URL: "ws://localhost:99999/"}, CodeInvalidURL},
		{"empty port", Options{URL: "ws://localhost:/"}, CodeInvalidURL},
		{"space in host", Options{URL: "ws://local host/"}, CodeInvalidURL},
		{"bad header name", Options{URL: "ws://h/", Headers: []httpclient.Header{{Name: "Bad Name", Enabled: true}}}, CodeInvalidHeader},
		{"control in value", Options{URL: "ws://h/", Headers: []httpclient.Header{{Name: "X", Value: "a\r\nb", Enabled: true}}}, CodeInvalidHeader},
		{"two hosts", Options{URL: "ws://h/", Headers: []httpclient.Header{{Name: "Host", Value: "a", Enabled: true}, {Name: "host", Value: "b", Enabled: true}}}, CodeInvalidHeader},
		{"upgrade row", Options{URL: "ws://h/", Headers: []httpclient.Header{{Name: "upgrade", Value: "x", Enabled: true}}}, CodeReservedHeader},
		{"protocol row", Options{URL: "ws://h/", Headers: []httpclient.Header{{Name: "Sec-WebSocket-Protocol", Value: "x", Enabled: true}}}, CodeReservedHeader},
		{"bad subprotocol", Options{URL: "ws://h/", Subprotocols: []string{"a b"}}, CodeInvalidSubprotocol},
		{"duplicate subprotocol", Options{URL: "ws://h/", Subprotocols: []string{"a", "a"}}, CodeInvalidSubprotocol},
	}
	for _, tc := range cases {
		tc.o.Dialer = d
		if _, err := New(tc.o); errCode(err) != tc.code {
			t.Errorf("%s: err = %v, want %s", tc.name, err, tc.code)
		}
	}
	// Disabled rows are not validated; these are valid.
	for _, o := range []Options{
		{URL: "wss://[::1]:8443/x", Headers: []httpclient.Header{{Name: "Upgrade", Value: "x"}}},
		{URL: "Ws://example.com?x"},
		{URL: "ws://example.com"},
	} {
		o.Dialer = d
		c, err := New(o)
		if err != nil {
			t.Errorf("%s: %v", o.URL, err)
			continue
		}
		if c.t.requestURI[0] != '/' {
			t.Errorf("%s: request URI %q", o.URL, c.t.requestURI)
		}
	}
	c, _ := New(Options{URL: "wss://[::1]/x", Dialer: d})
	if c.t.dialAddr != "[::1]:443" || c.t.hostHeader != "[::1]" || c.t.serverName != "::1" {
		t.Fatalf("target = %+v", c.t)
	}
}

func TestTLSFailures(t *testing.T) {
	t.Parallel()
	t.Run("untrusted certificate", func(t *testing.T) {
		t.Parallel()
		srv := newServer(t, true, func(http.ResponseWriter, *http.Request) {})
		o, _ := options(srv, "/")
		o.RootCAs = nil
		c, _ := New(o)
		if err := c.Connect(context.Background()); errCode(err) != CodeTLSUnknownAuthority || errs.KindOf(err) != errs.TLS {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("skip verify", func(t *testing.T) {
		t.Parallel()
		srv := newServer(t, true, func(w http.ResponseWriter, r *http.Request) {
			sc, nc := upgrade(t, w, r, "", nil)
			echo(sc, nc, make(chan error, 1))
		})
		o, _ := options(srv, "/")
		o.RootCAs, o.InsecureSkipVerify = nil, true
		connect(t, o)
	})
	t.Run("plain server", func(t *testing.T) {
		t.Parallel()
		srv := newServer(t, false, func(http.ResponseWriter, *http.Request) {})
		o, _ := options(srv, "/")
		o.URL = "wss" + strings.TrimPrefix(srv.URL, "http") + "/"
		c, _ := New(o)
		if err := c.Connect(context.Background()); errs.KindOf(err) != errs.TLS {
			t.Fatalf("err = %v (%s)", err, errCode(err))
		}
	})
}

func TestHandshakeTimeoutAndCancel(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan net.Conn, 10)
	t.Cleanup(func() {
		ln.Close()
		for len(held) > 0 {
			(<-held).Close()
		}
	})
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			held <- nc // accepts, never answers
		}
	}()
	url := "ws://" + ln.Addr().String() + "/"

	c, _ := New(Options{URL: url, Dialer: netx.NewDirect(netx.Options{}), HandshakeTimeout: 100 * time.Millisecond})
	start := time.Now()
	if err := c.Connect(context.Background()); errCode(err) != CodeTimeout || errs.KindOf(err) != errs.Timeout {
		t.Fatalf("timeout: err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the handshake timeout was not honoured")
	}

	c, _ = New(Options{URL: url, Dialer: netx.NewDirect(netx.Options{})})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if err := c.Connect(ctx); errCode(err) != CodeCancelled || errs.KindOf(err) != errs.Cancelled {
		t.Fatalf("cancel: err = %v", err)
	}
}

func TestDialError(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c, _ := New(Options{URL: "ws://" + addr + "/", Dialer: netx.NewDirect(netx.Options{})})
	if err := c.Connect(context.Background()); errCode(err) != "netx.connection_refused" {
		t.Fatalf("err = %v (%s)", err, errCode(err))
	}
}
