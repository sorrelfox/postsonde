package servertls_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/reflection"
	grpctransport "github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sse"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/echo"
	"github.com/sorrelfox/postsonde/internal/core/servers/grpcmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/servertls"
	"github.com/sorrelfox/postsonde/internal/core/servers/sseserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/tcpserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/webhook"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// End-to-end tests of local TLS (SRV-05): every server type served over
// TLS with a generated certificate and with the user's own, reached by
// net/http and by PostSonde's own clients trusting the local CA through
// the CA bundles setting (tlsconf.Trust over ca.pem).

type events struct {
	mu   sync.Mutex
	list []servers.Event
}

func (e *events) add(ev servers.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

// waitFor polls until an event matches and returns it.
func (e *events) waitFor(t *testing.T, what string, match func(servers.Event) bool) servers.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.mu.Lock()
		i := slices.IndexFunc(e.list, match)
		var ev servers.Event
		if i >= 0 {
			ev = e.list[i]
		}
		all := slices.Clone(e.list)
		e.mu.Unlock()
		if i >= 0 {
			return ev
		}
		if time.Now().After(deadline) {
			var sums []string
			for _, ev := range all {
				sums = append(sums, string(ev.Kind)+": "+ev.Summary)
			}
			t.Fatalf("no %s event in:\n%s", what, strings.Join(sums, "\n"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type fixture struct {
	m        *servers.Manager
	rec      *events
	ca       *servertls.Authority
	caPath   string
	passFile map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{rec: &events{}, ca: servertls.NewAuthority(t.TempDir()), passFile: map[string]string{}}
	p := &servertls.Provider{CA: f.ca, Passphrase: func(file string) (string, error) { return f.passFile[file], nil }}
	f.m = servers.NewManager(servers.Options{
		Types: []servers.Type{
			echo.New(), mockhttp.New(mockhttp.Options{}), wsserver.New(wsserver.Options{}), tcpserver.New(tcpserver.Options{}), webhook.New(webhook.Options{}),
			sseserver.New(sseserver.Options{}), grpcmock.New(grpcmock.Options{}),
		},
		Certificates: p.Source(),
		OnTraffic:    f.rec.add,
		StopTimeout:  time.Second,
		Logger:       slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { f.m.StopAll(context.Background()) })
	ca, err := f.ca.CA()
	if err != nil {
		t.Fatal(err)
	}
	f.caPath = ca.Path
	return f
}

func (f *fixture) start(t *testing.T, spec servers.Spec) servers.Server {
	t.Helper()
	s, err := f.m.Add(spec)
	if err != nil {
		t.Fatal(err)
	}
	s, err = f.m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// roots trusts the local CA the way the app does: ca.pem added to the CA
// bundles setting, on top of the system's certificates.
func (f *fixture) roots(t *testing.T) *x509.CertPool {
	t.Helper()
	pool, err := tlsconf.NewTrust(func() []string { return []string{f.caPath} }).Roots()
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func addr(s servers.Server) string { return net.JoinHostPort("localhost", strconv.Itoa(s.Port())) }

var generated = &servers.TLS{Certificate: servers.CertGenerated}

// TestHTTPSWithGeneratedCertificate: the echo, mock and webhook servers
// answer HTTPS with a certificate of the local CA, which net/http and the
// app's HTTP client accept once ca.pem is trusted; HTTP/1.1 is chosen.
func TestHTTPSWithGeneratedCertificate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	transport := httpclient.NewTransport(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{RootCAs: f.roots(t)})
	t.Cleanup(transport.CloseIdleConnections)
	app := &http.Client{Transport: transport}
	plain := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: f.roots(t)}}}

	for _, tt := range []struct {
		typ, method, path string
	}{
		{echo.TypeName, http.MethodPost, "/anything"},
		{mockhttp.TypeName, http.MethodGet, "/hello"},
		{webhook.TypeName, http.MethodPost, "/hook"},
	} {
		s := f.start(t, servers.Spec{Type: tt.typ, TLS: generated})
		if s.Cert == nil || s.Cert.Subject.CommonName != "localhost" {
			t.Fatalf("%s: Cert = %v", tt.typ, s.Cert)
		}
		f.rec.waitFor(t, "started", func(ev servers.Event) bool {
			return ev.ServerID == s.ID && ev.Kind == servers.KindStarted && strings.Contains(ev.Summary, "over TLS (generated certificate \"localhost\"")
		})
		for name, c := range map[string]*http.Client{"app": app, "net/http": plain} {
			req, err := http.NewRequest(tt.method, "https://"+addr(s)+tt.path, strings.NewReader("hi"))
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.Do(req)
			if err != nil {
				t.Fatalf("%s via %s: %v", tt.typ, name, err)
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			// The app's client offers h2 and http/1.1; the server takes
			// HTTP/1.1.
			if res.StatusCode != http.StatusOK || res.TLS == nil || res.Proto != "HTTP/1.1" || (name == "app" && res.TLS.NegotiatedProtocol != "http/1.1") {
				t.Errorf("%s via %s: %d %s alpn %q", tt.typ, name, res.StatusCode, res.Proto, res.TLS.NegotiatedProtocol)
			}
		}
		f.rec.waitFor(t, "request", func(ev servers.Event) bool {
			return ev.ServerID == s.ID && ev.Kind == servers.KindRequest && ev.HTTP.Target == tt.path
		})
		if _, err := f.m.Stop(t.Context(), s.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := f.m.Get(s.ID); got.Cert != nil {
			t.Errorf("%s: Cert kept after stop", tt.typ)
		}
	}
}

// TestWSSWithGeneratedCertificate: the WebSocket server serves wss; the
// app's WebSocket client connects with the local CA trusted and gets its
// message echoed.
func TestWSSWithGeneratedCertificate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	s := f.start(t, servers.Spec{Type: wsserver.TypeName, TLS: generated, Config: wsserver.Config{Path: "/ws", Echo: true}})

	got := make(chan string, 1)
	c, err := wsclient.New(wsclient.Options{
		URL: "wss://" + addr(s) + "/ws", Dialer: netx.NewDirect(netx.Options{}), RootCAs: f.roots(t),
		OnMessage: func(m wsclient.Message) { got <- string(m.Data) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Abort)
	if err := c.SendText("ping over tls"); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m != "ping over tls" {
			t.Errorf("echo = %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no echo")
	}

	// Without the CA the client refuses the server, and the server logs
	// the failed handshake on its connection.
	c2, err := wsclient.New(wsclient.Options{URL: "wss://" + addr(s) + "/ws", Dialer: netx.NewDirect(netx.Options{}), RootCAs: x509.NewCertPool()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c2.Connect(t.Context()); err == nil {
		t.Fatal("a client without the CA connected")
	}
	ev := f.rec.waitFor(t, "handshake error", func(ev servers.Event) bool {
		return ev.Kind == servers.KindError && strings.HasPrefix(ev.Summary, "TLS handshake failed")
	})
	if !strings.Contains(ev.Summary, "does not trust") || ev.Peer == "" || ev.ConnID == 0 {
		t.Errorf("handshake event = %+v", ev)
	}
}

// TestSSEOverTLS: the SSE server serves its stream over HTTPS with a
// generated certificate; a client trusting the local CA gets a pushed
// event.
func TestSSEOverTLS(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	s := f.start(t, servers.Spec{Type: sseserver.TypeName, TLS: generated})
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: f.roots(t)}}}
	res, err := c.Get("https://" + addr(s) + sseserver.DefaultPath)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %v", res.StatusCode, res.Header)
	}
	inst, err := f.m.Running(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	hub := inst.(sseserver.Hub)
	for deadline := time.Now().Add(5 * time.Second); len(hub.Subscribers()) == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no subscriber")
		}
	}
	if _, err := hub.Send("", sseserver.Message{Data: "over tls"}); err != nil {
		t.Fatal(err)
	}
	for ev, err := range sse.Events(res.Body, sse.Options{}) {
		if err != nil || ev.Data != "over tls" {
			t.Errorf("event = %+v, %v", ev, err)
		}
		break
	}
}

// TestTCPOverTLS: the TCP server serves TLS; tls.Dial reaches it, and a
// failed handshake is logged as such.
func TestTCPOverTLS(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	s := f.start(t, servers.Spec{Type: tcpserver.TypeName, TLS: generated})
	conn, err := tls.Dial("tcp", addr(s), &tls.Config{RootCAs: f.roots(t), ServerName: "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("secret hello")); err != nil {
		t.Fatal(err)
	}
	f.rec.waitFor(t, "message", func(ev servers.Event) bool {
		return ev.Kind == servers.KindMessage && bytes.Equal(ev.Content, []byte("secret hello"))
	})
	conn.Close()

	if _, err := tls.Dial("tcp", addr(s), &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "localhost"}); err == nil {
		t.Fatal("untrusted dial succeeded")
	}
	f.rec.waitFor(t, "handshake error", func(ev servers.Event) bool {
		return ev.Kind == servers.KindError && strings.Contains(ev.Summary, "TLS handshake failed") && strings.Contains(ev.Summary, "does not trust")
	})
}

// TestGRPCMockOverTLS: the gRPC mock serves h2 over TLS (ALPN h2) with the
// local CA's certificate: PostSonde's gRPC transport trusting ca.pem gets
// the canned response over gRPC and gRPC-Web, and lists the services by
// reflection; without the CA the call fails the handshake.
func TestGRPCMockOverTLS(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	proto := filepath.Join(t.TempDir(), "hello.proto")
	src := "syntax = \"proto3\";\npackage hello.v1;\nmessage Req { string name = 1; }\nmessage Reply { string text = 1; }\nservice Greeter { rpc Hello(Req) returns (Reply); }\n"
	if err := os.WriteFile(proto, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.start(t, servers.Spec{Type: grpcmock.TypeName, TLS: generated, Config: grpcmock.Config{
		Files: []string{proto}, Reflection: true,
		Methods: []grpcmock.Method{{Service: "hello.v1.Greeter", Method: "Hello", Messages: []string{`{"text": "hi over TLS"}`}}},
	}})
	client := grpctransport.New(netx.NewDirect(netx.Options{}), grpctransport.Options{Transport: httpclient.TransportOptions{RootCAs: f.roots(t)}})
	t.Cleanup(client.CloseIdleConnections)
	// Reply{text: "hi over TLS"} is field 1, length 11.
	want := append([]byte{0x0a, 11}, "hi over TLS"...)
	for _, p := range []grpctransport.Protocol{grpctransport.ProtocolGRPC, grpctransport.ProtocolGRPCWeb} {
		res, err := client.Unary(t.Context(), grpctransport.CallOptions{Address: addr(s), TLS: true, Protocol: p, Method: "/hello.v1.Greeter/Hello"}, nil)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if !bytes.Equal(res.Message, want) {
			t.Errorf("%s: message = %q", p, res.Message)
		}
	}
	snap, err := reflection.NewClient(client).Fetch(t.Context(), reflection.FetchOptions{Call: grpctransport.CallOptions{Address: addr(s), TLS: true}})
	if err != nil || !slices.Equal(snap.Services, []string{"hello.v1.Greeter"}) {
		t.Errorf("reflection: %+v, %v", snap, err)
	}
	f.rec.waitFor(t, "call over h2", func(ev servers.Event) bool {
		return ev.Kind == servers.KindRequest && ev.GRPC != nil && ev.HTTP.Proto == "HTTP/2.0"
	})

	untrusted := grpctransport.New(netx.NewDirect(netx.Options{}), grpctransport.Options{Transport: httpclient.TransportOptions{RootCAs: x509.NewCertPool()}})
	t.Cleanup(untrusted.CloseIdleConnections)
	if _, err := untrusted.Unary(t.Context(), grpctransport.CallOptions{Address: addr(s), TLS: true, Method: "/hello.v1.Greeter/Hello"}, nil); err == nil {
		t.Error("untrusted call succeeded")
	}
}

// TestUserCertificates: the user's PEM certificate and key, and a PKCS#12
// file opened with its stored passphrase, are what the server presents.
func TestUserCertificates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ca := tlstest.NewCA(t, "User CA")
	leaf := ca.Issue(t, tlstest.CertOptions{CommonName: "my-dev-server"})
	certFile := tlstest.WriteFile(t, "cert.pem", leaf.CertPEM)
	keyFile := tlstest.WriteFile(t, "key.pem", leaf.KeyPEM)

	s := f.start(t, servers.Spec{Type: echo.TypeName, TLS: &servers.TLS{Certificate: servers.CertFiles, CertFile: certFile, KeyFile: keyFile}})
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca.Pool()}}}
	res, err := c.Get("https://" + addr(s) + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.TLS.PeerCertificates[0].Subject.CommonName != "my-dev-server" || s.Cert.Subject.CommonName != "my-dev-server" {
		t.Errorf("served %q, Cert %q", res.TLS.PeerCertificates[0].Subject.CommonName, s.Cert.Subject.CommonName)
	}

	// A PKCS#12 export (a client certificate, so it warns) with the
	// passphrase from the secret storage. It is not issued for localhost:
	// the client checks the certificate it expects instead.
	pfx := tlstest.Fixture(t, "windows-aes256-sha256.p12")
	f.passFile[pfx] = tlstest.FixturePassword
	s2 := f.start(t, servers.Spec{Type: wsserver.TypeName, TLS: &servers.TLS{Certificate: servers.CertFiles, PFXFile: pfx}})
	f.rec.waitFor(t, "warning", func(ev servers.Event) bool {
		return ev.ServerID == s2.ID && ev.Kind == servers.KindStarted && strings.Contains(ev.Summary, "not issued for server authentication")
	})
	conn, err := tls.Dial("tcp", addr(s2), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if !conn.ConnectionState().PeerCertificates[0].Equal(s2.Cert) {
		t.Error("the PKCS#12 certificate was not served")
	}
	conn.Close()

	// A wrong passphrase: the server does not start and nothing listens.
	f.passFile[pfx] = "wrong"
	bad, err := f.m.Add(servers.Spec{Type: echo.TypeName, TLS: &servers.TLS{Certificate: servers.CertFiles, PFXFile: pfx}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.m.Start(t.Context(), bad.ID)
	if e, ok := errors.AsType[*errs.Error](err); !ok || !strings.HasPrefix(e.Code, "pkcs12.") || got.State != servers.StateFailed || len(got.Addrs) != 0 {
		t.Errorf("wrong passphrase: %v, %s", err, got.State)
	}

	// A missing file fails the start with the path.
	missing, err := f.m.Add(servers.Spec{Type: echo.TypeName, TLS: &servers.TLS{Certificate: servers.CertFiles, CertFile: certFile + ".gone", KeyFile: keyFile}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.m.Start(t.Context(), missing.ID)
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != tlsconf.CodeFileUnreadable || e.Details["path"] != certFile+".gone" {
		t.Errorf("missing file: %#v", err)
	}
}

// TestTLSChangesOnlyWhenStopped: TLS is part of the address: it cannot
// change while the server runs.
func TestTLSChangesOnlyWhenStopped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	s := f.start(t, servers.Spec{Type: echo.TypeName})
	spec := s.Spec
	spec.TLS = generated
	if _, err := f.m.Redefine(s.ID, spec); err == nil {
		t.Fatal("TLS changed while running")
	}
	if _, err := f.m.Stop(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Redefine(s.ID, spec); err != nil {
		t.Fatal(err)
	}
	s, err := f.m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Cert == nil {
		t.Error("no certificate after turning TLS on")
	}
}

// TestNoCertificateSource: without a source a TLS server fails to start.
func TestNoCertificateSource(t *testing.T) {
	t.Parallel()
	m := servers.NewManager(servers.Options{Types: []servers.Type{echo.New()}, Logger: slog.New(slog.DiscardHandler)})
	s, err := m.Add(servers.Spec{Type: echo.TypeName, TLS: generated})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Start(t.Context(), s.ID)
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != servers.CodeTLSUnavailable {
		t.Errorf("err = %v", err)
	}
}
