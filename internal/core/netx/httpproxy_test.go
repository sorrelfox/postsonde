package netx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// proxyTarget is a host name only the test proxies know.
const httpProxyTarget = "api.example.test"

// proxyPassword is the password of the test proxies; tests check it never
// shows in an error, a detail or a trace event.
const proxyPassword = "pr0xy-s3cret"

// testProxyCA returns a CA and a TLS config for an https:// test proxy on
// 127.0.0.1 signed by it.
func testProxyCA(t *testing.T) (*tlstest.CA, *tls.Config) {
	t.Helper()
	ca := tlstest.NewCA(t, "proxy CA")
	cert := ca.Issue(t, tlstest.CertOptions{CommonName: "proxy"})
	return ca, &tls.Config{Certificates: []tls.Certificate{cert.TLS}, MinVersion: tls.VersionTLS12}
}

// basic is the Proxy-Authorization value of user and password.
func basic(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}

// checkNoSecret fails when err shows the proxy password or the encoded
// credentials anywhere: message, hint, details or causes.
func checkNoSecret(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	all := err.Error() + " " + strings.Join(errs.Chain(err), " ")
	if e, ok := errors.AsType[*errs.Error](err); ok {
		all += " " + e.Hint
		for k, v := range e.Details {
			all += " " + k + "=" + v
		}
	}
	for _, s := range []string{proxyPassword, base64.StdEncoding.EncodeToString([]byte("alice:" + proxyPassword))} {
		if strings.Contains(all, s) {
			t.Errorf("the error shows a credential: %q", all)
		}
	}
}

// TestHTTPProxyConnect: NET-02 CONNECT tunnels through an http:// and an
// https:// proxy, with and without Basic credentials; the target's name
// goes to the proxy unresolved, the tunnel carries bytes both ways, and
// the trace and the connection report the proxy.
func TestHTTPProxyConnect(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	_, port, _ := net.SplitHostPort(target)
	ca, serverTLS := testProxyCA(t)
	tests := []struct {
		name       string
		tls        bool
		user, pass string
	}{
		{"http, no auth", false, "", ""},
		{"http, basic auth", false, "alice", proxyPassword},
		{"https, no auth", true, "", ""},
		{"https, basic auth", true, "alice", proxyPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := &netxtest.HTTPProxyServer{Username: tt.user, Password: tt.pass, Hosts: map[string]string{httpProxyTarget: "127.0.0.1"}}
			if tt.tls {
				srv.TLS = serverTLS
			}
			addr := srv.Start(t)
			p, err := NewHTTPProxy(HTTPProxyConfig{Address: addr, TLS: tt.tls, Username: tt.user, Password: tt.pass,
				RootCAs: func() (*x509.CertPool, error) { return ca.Pool(), nil }}, nil)
			if err != nil {
				t.Fatal(err)
			}
			via := "http://" + addr
			if tt.tls {
				via = "https://" + addr
			}
			if p.Via() != via {
				t.Errorf("Via = %q, want %q", p.Via(), via)
			}
			rec := &recorder{}
			dest := net.JoinHostPort(httpProxyTarget, port)
			c, err := p.DialContext(WithTrace(t.Context(), rec.trace()), "tcp", dest)
			if err != nil {
				t.Fatalf("DialContext = %v", err)
			}
			if ViaOf(c) != via {
				t.Errorf("ViaOf = %q, want %q", ViaOf(c), via)
			}
			if c.RemoteAddr().String() != addr {
				t.Errorf("RemoteAddr = %v, want the proxy's %s", c.RemoteAddr(), addr)
			}
			roundTrip(t, c, "through the tunnel")

			reqs := srv.Requests()
			if len(reqs) != 1 || reqs[0].Method != http.MethodConnect || reqs[0].Target != dest || reqs[0].Host != dest {
				t.Fatalf("proxy requests = %+v, want one CONNECT %s", reqs, dest)
			}
			wantAuth := ""
			if tt.user != "" {
				wantAuth = basic(tt.user, tt.pass)
			}
			if reqs[0].Authorization != wantAuth {
				t.Errorf("Proxy-Authorization = %q, want %q", reqs[0].Authorization, wantAuth)
			}
			if ua := reqs[0].Header.Get("User-Agent"); ua != "" {
				t.Errorf("CONNECT sent User-Agent %q, want none", ua)
			}

			starts, dones := rec.events()
			if len(starts) != 2 || starts[0] != (DialStartInfo{Network: "tcp", Address: dest, Via: via}) || starts[1].Address != addr || starts[1].Via != "" {
				t.Errorf("DialStart events = %+v", starts)
			}
			if len(dones) != 2 || dones[1].Address != dest || dones[1].Via != via || dones[1].Err != nil || dones[1].RemoteAddr.String() != addr {
				t.Errorf("DialDone events = %+v", dones)
			}
		})
	}
}

// TestHTTPProxyForward: DialForward connects to the proxy itself, and an
// http.Transport whose Proxy is ForwardURL writes absolute-form requests
// with the credentials, as httpclient does.
func TestHTTPProxyForward(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "origin saw "+r.Method+" "+r.RequestURI+" host="+r.Host+" auth="+r.Header.Get("Proxy-Authorization"))
	}))
	t.Cleanup(origin.Close)
	_, port, _ := net.SplitHostPort(origin.Listener.Addr().String())
	ca, serverTLS := testProxyCA(t)
	for _, useTLS := range []bool{false, true} {
		t.Run("tls="+map[bool]string{false: "no", true: "yes"}[useTLS], func(t *testing.T) {
			t.Parallel()
			srv := &netxtest.HTTPProxyServer{Username: "alice", Password: proxyPassword, Hosts: map[string]string{httpProxyTarget: "127.0.0.1"}}
			if useTLS {
				srv.TLS = serverTLS
			}
			addr := srv.Start(t)
			p, err := NewHTTPProxy(HTTPProxyConfig{Address: addr, TLS: useTLS, Username: "alice", Password: proxyPassword,
				RootCAs: func() (*x509.CertPool, error) { return ca.Pool(), nil }}, nil)
			if err != nil {
				t.Fatal(err)
			}
			dest := net.JoinHostPort(httpProxyTarget, port)
			if u := p.ForwardURL(dest); u.Host != dest || u.Scheme != "http" || u.User.String() != url.UserPassword("alice", proxyPassword).String() {
				t.Errorf("ForwardURL = %v", u.Redacted())
			}
			if p.AuthorizationHeader() != basic("alice", proxyPassword) {
				t.Errorf("AuthorizationHeader = %q", p.AuthorizationHeader())
			}
			tr := &http.Transport{
				Proxy:       func(r *http.Request) (*url.URL, error) { return p.ForwardURL(r.URL.Host), nil },
				DialContext: p.DialForward,
			}
			t.Cleanup(tr.CloseIdleConnections)
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+dest+"/items?id=7", nil)
			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip = %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if want := "origin saw GET /items?id=7 host=" + dest + " auth="; string(body) != want {
				t.Errorf("body = %q, want %q", body, want)
			}
			reqs := srv.Requests()
			if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Target != "http://"+dest+"/items?id=7" || reqs[0].Authorization != basic("alice", proxyPassword) {
				t.Errorf("proxy requests = %+v, want one absolute-form GET with credentials", reqs)
			}
		})
	}
}

// TestHTTPProxyLeftoverBytes: bytes the proxy sends right after its
// CONNECT answer (a server that speaks first) are read first.
func TestHTTPProxyLeftoverBytes(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	srv := &netxtest.HTTPProxyServer{AfterConnect: []byte("banner\n")}
	addr := srv.Start(t)
	p, err := NewHTTPProxy(HTTPProxyConfig{Address: addr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.DialContext(t.Context(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("banner\n"))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "banner\n" {
		t.Fatalf("first bytes = %q, %v", buf, err)
	}
	if ViaOf(c) != "http://"+addr {
		t.Errorf("ViaOf = %q", ViaOf(c))
	}
	roundTrip(t, c, "after the banner")
}

// rawServer serves every connection in a fixed way: with an answer, it
// reads what the client sends first, writes answer, half-closes and
// drains the rest (no reset); without one it closes at once
// (closeAfter) or holds the connection open until the test ends.
func rawServer(t *testing.T, answer []byte, closeAfter bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		ln.Close()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				switch {
				case answer != nil:
					_ = c.SetDeadline(time.Now().Add(5 * time.Second))
					buf := make([]byte, 4096)
					if _, err := c.Read(buf); err != nil {
						return
					}
					_, _ = c.Write(answer)
					_ = c.(*net.TCPConn).CloseWrite()
					_, _ = io.Copy(io.Discard, c)
				case closeAfter:
				default:
					<-done
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// TestHTTPProxyErrors: each failure has its code, kind and hint, the dial
// details and the proxy, and never the credentials.
func TestHTTPProxyErrors(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	_, serverTLS := testProxyCA(t)
	start := func(t *testing.T, s *netxtest.HTTPProxyServer) string {
		s.Hosts = map[string]string{httpProxyTarget: "127.0.0.1"}
		return s.Start(t)
	}
	tests := []struct {
		name    string
		proxy   func(t *testing.T) string
		cfg     HTTPProxyConfig
		network string
		address string
		kind    errs.Kind
		code    string
		msg     string
		hint    string
		details map[string]string
	}{
		{name: "auth required", proxy: func(t *testing.T) string {
			return start(t, &netxtest.HTTPProxyServer{Username: "alice", Password: proxyPassword})
		},
			kind: errs.Network, code: CodeHTTPProxyAuthRequired, msg: "requires a user name and password", hint: HintHTTPProxyAuthRequired,
			details: map[string]string{"httpStatus": "407", "proxyAuthenticate": "Basic"}},
		{name: "wrong password", proxy: func(t *testing.T) string {
			return start(t, &netxtest.HTTPProxyServer{Username: "alice", Password: "other"})
		},
			cfg:  HTTPProxyConfig{Username: "alice", Password: proxyPassword},
			kind: errs.Network, code: CodeHTTPProxyAuthFailed, msg: "rejected the user name or password", hint: HintHTTPProxyAuthFailed,
			details: map[string]string{"httpStatus": "407"}},
		{name: "unsupported scheme", proxy: func(t *testing.T) string {
			return start(t, &netxtest.HTTPProxyServer{Username: "alice", Password: "other", Challenge: `NTLM, Negotiate`})
		},
			cfg:  HTTPProxyConfig{Username: "alice", Password: proxyPassword},
			kind: errs.Network, code: CodeHTTPProxyAuthUnsupported, msg: "(NTLM, Negotiate)", hint: HintHTTPProxyAuthUnsupported,
			details: map[string]string{"proxyAuthenticate": "NTLM, Negotiate"}},
		{name: "forbidden", proxy: func(t *testing.T) string { return start(t, &netxtest.HTTPProxyServer{ConnectStatus: 403}) },
			kind: errs.Network, code: CodeHTTPProxyForbidden, msg: "does not allow connections to", hint: HintHTTPProxyForbidden,
			details: map[string]string{"httpStatus": "403"}},
		{name: "unknown target", proxy: func(t *testing.T) string { return start(t, &netxtest.HTTPProxyServer{}) },
			address: "nowhere.example.test:80",
			kind:    errs.Network, code: CodeHTTPProxyConnectFailed, msg: "could not connect to nowhere.example.test:80 (502 Bad Gateway)", hint: HintHTTPProxyConnectFailed,
			details: map[string]string{"httpStatus": "502"}},
		{name: "proxy not running", proxy: closedPort,
			kind: errs.Network, code: CodeHTTPProxyUnreachable, msg: "Could not connect to the HTTP proxy", hint: HintHTTPProxyUnreachable},
		{name: "not an HTTP proxy", proxy: func(t *testing.T) string { return rawServer(t, []byte("SSH-2.0-OpenSSH_9.6\r\n"), true) },
			kind: errs.Protocol, code: CodeHTTPProxyProtocolError, hint: HintHTTPProxyProtocol},
		{name: "proxy hangs up", proxy: func(t *testing.T) string { return rawServer(t, nil, true) },
			kind: errs.Protocol, code: CodeHTTPProxyProtocolError, msg: "closed the connection during the CONNECT"},
		{name: "silent proxy", proxy: func(t *testing.T) string { return rawServer(t, nil, false) }, cfg: HTTPProxyConfig{Timeout: 100 * time.Millisecond},
			kind: errs.Timeout, code: CodeHTTPProxyTimeout, msg: "did not answer in time (CONNECT)", hint: HintHTTPProxyTimeout},
		{name: "https proxy with an unknown CA", proxy: func(t *testing.T) string { return start(t, &netxtest.HTTPProxyServer{TLS: serverTLS}) },
			cfg:  HTTPProxyConfig{TLS: true, Username: "alice", Password: proxyPassword},
			kind: errs.TLS, code: CodeTLSUnknownAuthority, msg: "The certificate of the HTTPS proxy at", hint: HintHTTPProxyUnknownCA},
		{name: "https proxy URL for a plain proxy", proxy: func(t *testing.T) string {
			return rawServer(t, []byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"), true)
		},
			cfg:  HTTPProxyConfig{TLS: true},
			kind: errs.TLS, code: CodeTLSServerNotTLS, msg: "did not answer with TLS", hint: HintHTTPProxyNotTLS},
		{name: "CA bundles that cannot be read", proxy: func(t *testing.T) string { return start(t, &netxtest.HTTPProxyServer{TLS: serverTLS}) },
			cfg: HTTPProxyConfig{TLS: true, RootCAs: func() (*x509.CertPool, error) {
				return nil, errs.New(errs.IO, "The CA bundle cannot be read.").WithCode("tlsconf.file_unreadable")
			}},
			kind: errs.IO, code: "tlsconf.file_unreadable"},
		{name: "UDP", proxy: func(t *testing.T) string { return start(t, &netxtest.HTTPProxyServer{}) }, network: "udp",
			kind: errs.Unsupported, code: CodeHTTPProxyUnsupportedNetwork},
		{name: "address without a port", proxy: func(t *testing.T) string { return start(t, &netxtest.HTTPProxyServer{}) }, address: "api.example.test",
			kind: errs.InvalidArgument, code: CodeHTTPProxyInvalidAddress},
		{name: "host that would break the request line", proxy: func(t *testing.T) string { return start(t, &netxtest.HTTPProxyServer{}) },
			address: "a b\r\nX-Evil: 1:80",
			kind:    errs.InvalidArgument, code: CodeHTTPProxyInvalidAddress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.cfg
			cfg.Address = tt.proxy(t)
			p, err := NewHTTPProxy(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			network, address := tt.network, tt.address
			if network == "" {
				network = "tcp"
			}
			if address == "" {
				address = target
			}
			c, err := p.DialContext(t.Context(), network, address)
			if err == nil {
				c.Close()
				t.Fatal("DialContext succeeded")
			}
			e := wantErr(t, err, tt.kind, tt.code)
			if !strings.Contains(e.Message, tt.msg) {
				t.Errorf("message %q does not contain %q", e.Message, tt.msg)
			}
			if tt.hint != "" && e.Hint != tt.hint {
				t.Errorf("hint = %q, want %q", e.Hint, tt.hint)
			}
			if e.Details["network"] != network || e.Details["address"] != address || e.Details["via"] != p.Via() {
				t.Errorf("details = %v", e.Details)
			}
			for k, v := range tt.details {
				if e.Details[k] != v {
					t.Errorf("detail %s = %q, want %q", k, e.Details[k], v)
				}
			}
			checkNoSecret(t, err)
		})
	}
}

// TestHTTPProxyCancel: cancelling ctx while the proxy does not answer
// ends the dial at once with netx.dial_cancelled.
func TestHTTPProxyCancel(t *testing.T) {
	t.Parallel()
	p, err := NewHTTPProxy(HTTPProxyConfig{Address: rawServer(t, nil, false), Timeout: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = p.DialContext(ctx, "tcp", "api.example.test:443")
	e := wantErr(t, err, errs.Cancelled, CodeCancelled)
	if e.Details["via"] != p.Via() {
		t.Errorf("details = %v", e.Details)
	}
	if _, err := p.DialContext(ctx, "tcp", "api.example.test:443"); !errorsIsCode(err, CodeCancelled) {
		t.Errorf("a done ctx: err = %v", err)
	}
}

func TestNewHTTPProxyInvalid(t *testing.T) {
	t.Parallel()
	for _, cfg := range []HTTPProxyConfig{
		{Address: "proxy.lan"},
		{Address: ":3128"},
		{Address: "proxy.lan:3128", Password: "pw"},
		{Address: "proxy.lan:3128", Username: "a:b"},
		{Address: "proxy.lan:3128", Username: "a\nb"},
	} {
		_, err := NewHTTPProxy(cfg, nil)
		if !errorsIsCode(err, CodeHTTPProxyInvalidConfig) || errs.KindOf(err) != errs.InvalidArgument {
			t.Errorf("%+v: err = %v", cfg, err)
		}
	}
}

func TestChallengeSchemes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{`Basic realm="proxy"`}, []string{"Basic"}},
		{[]string{`Basic realm="a, b", charset="UTF-8"`, "NTLM"}, []string{"Basic", "NTLM"}},
		{[]string{`Negotiate, NTLM, Digest realm="x", qop="auth", nonce="abc=="`}, []string{"Negotiate", "NTLM", "Digest"}},
		{[]string{`Bearer abc==`}, []string{"Bearer"}},
		{[]string{`, ,realm="x"`}, nil},
	}
	for _, tt := range tests {
		if got := challengeSchemes(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("challengeSchemes(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestProxyRouterHTTP: an http:// proxy URL routes dials through CONNECT,
// ForwardProxy names it for absolute-form requests (not for loopback
// targets or other kinds of proxy), and a dial marked with ForwardContext
// goes to the proxy without CONNECT.
func TestProxyRouterHTTP(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello from "+r.Host)
	}))
	t.Cleanup(origin.Close)
	_, port, _ := net.SplitHostPort(origin.Listener.Addr().String())
	srv := &netxtest.HTTPProxyServer{Hosts: map[string]string{httpProxyTarget: "127.0.0.1"}}
	addr := srv.Start(t)
	current, _ := ParseProxyURL("http://" + addr)
	r := NewProxyRouter(nil, func(context.Context) (ProxyURL, error) { return current, nil }, ProxyOptions{})
	dest := net.JoinHostPort(httpProxyTarget, port)

	d, err := r.Route(t.Context(), "tcp", dest)
	hp, ok := d.(*HTTPProxy)
	if err != nil || !ok || hp.Via() != "http://"+addr {
		t.Fatalf("Route = %T %v, %v; want the HTTP proxy", d, d, err)
	}
	if f := r.ForwardProxy(t.Context(), dest); f != hp {
		t.Errorf("ForwardProxy = %v, want the route's dialer", f)
	}
	for _, a := range []string{"localhost:80", "127.0.0.1:" + port} {
		if f := r.ForwardProxy(t.Context(), a); f != nil {
			t.Errorf("ForwardProxy(%s) = %v, want nil (loopback)", a, f)
		}
	}

	// A marked dial goes to the proxy itself: an absolute-form request
	// written on it reaches the origin.
	c, err := Routed(r.Route).DialContext(ForwardContext(t.Context(), hp), "tcp", dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(c, "GET http://"+dest+"/ HTTP/1.1\r\nHost: "+dest+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(c)
	c.Close()
	if !strings.Contains(string(got), "hello from "+dest) {
		t.Errorf("answer = %q", got)
	}
	if reqs := srv.Requests(); len(reqs) != 1 || reqs[0].Method != http.MethodGet {
		t.Errorf("proxy requests = %+v, want one GET and no CONNECT", reqs)
	}
	// A marked dial to a loopback target still goes direct.
	if d, err := r.Route(ForwardContext(t.Context(), hp), "tcp", "localhost:80"); err != nil || d != r.direct {
		t.Errorf("loopback with ForwardContext: Route = %v, %v", d, err)
	}

	// Other proxies, no proxy and a broken source: no forward proxy.
	for _, tt := range []struct {
		p   ProxyURL
		err error
	}{
		{ProxyURL{Scheme: ProxySOCKS5H, Host: "127.0.0.1", Port: 1080}, nil},
		{ProxyURL{}, nil},
		{ProxyURL{}, errs.New(errs.InvalidArgument, "bad").WithCode(CodeProxyInvalidURL)},
	} {
		r := NewProxyRouter(nil, func(context.Context) (ProxyURL, error) { return tt.p, tt.err }, ProxyOptions{})
		if f := r.ForwardProxy(t.Context(), dest); f != nil {
			t.Errorf("%+v, %v: ForwardProxy = %v, want nil", tt.p, tt.err, f)
		}
	}
	if f := NewProxyRouter(nil, nil, ProxyOptions{}).ForwardProxy(t.Context(), dest); f != nil {
		t.Errorf("no source: ForwardProxy = %v", f)
	}
	fp := NewForwardProxier(Routed(r.Route), r.ForwardProxy)
	if fp.ForwardProxy(t.Context(), dest) != hp {
		t.Error("NewForwardProxier does not use its lookup")
	}
}
