package httpclient

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// mtlsServer starts an HTTPS server that requires a client certificate
// from the fixture CAs (tls.RequireAndVerifyClientCert) and answers with
// the common name it got. It returns the server and a pool trusting it.
func mtlsServer(t *testing.T, h2 bool, maxVersion uint16) (*httptest.Server, *tlstest.CA) {
	t.Helper()
	ca := tlstest.NewCA(t, "mTLS server CA")
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	srv.EnableHTTP2 = h2
	srv.TLS = tlstest.MTLSConfig(ca.Issue(t, tlstest.CertOptions{}), tlstest.FixtureClientCAs(t), 0, maxVersion)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, ca
}

// fixtureCerts returns a ClientCertFunc over per-host entries, as the
// app wires tlsconf.ClientCerts, counting the lookups.
func fixtureCerts(entries []tlsconf.ClientCertEntry, pass string, calls *atomic.Int32) ClientCertFunc {
	cc := tlsconf.NewClientCerts(func() []tlsconf.ClientCertEntry { return entries }, func(string) (string, error) { return pass, nil })
	return func(addr string) (*ClientCert, error) {
		calls.Add(1)
		c, err := cc.ForAddress(addr)
		if err != nil || c == nil {
			return nil, err
		}
		return &ClientCert{Certificate: c.Certificate, Description: c.Describe()}, nil
	}
}

// TestPerHostClientCertificates: a server that requires mTLS accepts the
// per-host certificate as PEM files and as a PKCS#12 file, over HTTP/1.1
// and HTTP/2; the timeline names the certificate sent.
func TestPerHostClientCertificates(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		entry tlsconf.ClientCertEntry
		pass  string
		h2    bool
	}{
		{"pem", tlsconf.ClientCertEntry{CertFile: tlstest.Fixture(t, "rsa.crt"), KeyFile: tlstest.Fixture(t, "rsa.key")}, "", false},
		{"pem h2", tlsconf.ClientCertEntry{CertFile: tlstest.Fixture(t, "ec.crt"), KeyFile: tlstest.Fixture(t, "ec.key")}, "", true},
		{"pkcs12", tlsconf.ClientCertEntry{PFXFile: tlstest.Fixture(t, "windows-aes256-sha256.p12")}, tlstest.FixturePassword, false},
		{"pkcs12 h2", tlsconf.ClientCertEntry{PFXFile: tlstest.Fixture(t, "macos-keychain.p12")}, tlstest.FixturePassword, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, ca := mtlsServer(t, tt.h2, 0)
			host := strings.TrimPrefix(srv.URL, "https://")
			tt.entry.ID, tt.entry.Host = "cc_1", host
			var calls atomic.Int32
			c := newTestClient(t, TransportOptions{RootCAs: ca.Pool(), ClientCerts: fixtureCerts([]tlsconf.ClientCertEntry{tt.entry}, tt.pass, &calls)})

			for range 2 {
				resp, err := c.Send(t.Context(), get(srv.URL+"/"))
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != 200 || !strings.HasPrefix(string(resp.Body), "PostSonde Test Client") {
					t.Fatalf("response %d %q", resp.StatusCode, resp.Body)
				}
			}
			resp, _ := c.Send(t.Context(), get(srv.URL+"/"))
			_ = resp
			if calls.Load() != 3 {
				t.Errorf("lookups = %d, want one per send", calls.Load())
			}
		})
	}
}

// TestClientCertTimeline: the handshake that sends the certificate adds a
// client_cert event with the description, and nothing secret.
func TestClientCertTimeline(t *testing.T) {
	t.Parallel()
	srv, ca := mtlsServer(t, false, 0)
	entry := tlsconf.ClientCertEntry{ID: "cc_1", Host: "127.0.0.1", PFXFile: tlstest.Fixture(t, "openssl-default.p12")}
	var calls atomic.Int32
	c := newTestClient(t, TransportOptions{RootCAs: ca.Pool(), ClientCerts: fixtureCerts([]tlsconf.ClientCertEntry{entry}, tlstest.FixturePassword, &calls)})
	resp, err := c.Send(t.Context(), get(srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	var detail string
	for _, e := range resp.Events {
		if e.Name == EventClientCert {
			detail = e.Detail
		}
	}
	if !strings.Contains(detail, "PostSonde Test Client rsa") || !strings.Contains(detail, "127.0.0.1") || strings.Contains(detail, tlstest.FixturePassword) {
		t.Errorf("client_cert detail = %q (events %+v)", detail, resp.Events)
	}
}

// TestClientCertMissing: without a matching entry the server's refusal is
// a clear, coded error on TLS 1.3 and 1.2; a matching entry that cannot
// be loaded fails before connecting.
func TestClientCertMissing(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		max  uint16
		h2   bool
		code string
	}{
		{"tls13", 0, false, netx.CodeTLSClientCertRequired},
		{"tls13 h2", 0, true, netx.CodeTLSClientCertRequired},
		{"tls12", tls.VersionTLS12, false, netx.CodeTLSClientCertRejected},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, ca := mtlsServer(t, tt.h2, tt.max)
			other := tlsconf.ClientCertEntry{ID: "cc_1", Host: "other.example.com", PFXFile: tlstest.Fixture(t, "openssl-default.p12")}
			var calls atomic.Int32
			c := newTestClient(t, TransportOptions{RootCAs: ca.Pool(), ClientCerts: fixtureCerts([]tlsconf.ClientCertEntry{other}, tlstest.FixturePassword, &calls)})
			_, err := c.Send(t.Context(), get(srv.URL+"/"))
			e := wantErr(t, err, errs.TLS, tt.code)
			if !strings.Contains(e.Hint, "Client certificates") {
				t.Errorf("hint = %q", e.Hint)
			}
		})
	}

	// TLS 1.3: the server's alert races with the client's first write
	// and read, so the client may only see the connection drop. A server
	// that asks for a certificate and then resets or closes without an
	// alert pins down those outcomes.
	for _, drop := range []string{"reset", "close"} {
		for _, h2 := range []bool{false, true} {
			name := "tls13 " + drop
			if h2 {
				name += " h2"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				addr, ca := droppingServer(t, drop == "reset")
				var calls atomic.Int32
				c := newTestClient(t, TransportOptions{RootCAs: ca.Pool(), ClientCerts: fixtureCerts(nil, "", &calls)})
				r := get("https://" + addr + "/")
				if !h2 {
					r.Settings.HTTPVersion = HTTPVersion1
				}
				_, err := c.Send(t.Context(), r)
				wantErr(t, err, errs.TLS, netx.CodeTLSClientCertRequired)
			})
		}
	}

	t.Run("unusable", func(t *testing.T) {
		t.Parallel()
		srv, ca := mtlsServer(t, false, 0)
		entry := tlsconf.ClientCertEntry{ID: "cc_1", Host: "127.0.0.1", PFXFile: tlstest.Fixture(t, "openssl-default.p12")}
		var calls atomic.Int32
		c := newTestClient(t, TransportOptions{RootCAs: ca.Pool(), ClientCerts: fixtureCerts([]tlsconf.ClientCertEntry{entry}, "wrong", &calls)})
		_, err := c.Send(t.Context(), get(srv.URL+"/"))
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != "pkcs12.wrong_password" || e.Hint != tlsconf.HintClientCertFix {
			t.Fatalf("err = %v", err)
		}
	})
}

// droppingServer accepts TLS 1.3 connections, asks for a client
// certificate (ALPN h2 and http/1.1), completes the handshake and then
// drops the connection without an alert: reset (SO_LINGER 0) or a plain
// close.
func droppingServer(t *testing.T, reset bool) (string, *tlstest.CA) {
	t.Helper()
	ca := tlstest.NewCA(t, "dropping server CA")
	cfg := &tls.Config{
		Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{}).TLS},
		ClientAuth:   tls.RequestClientCert,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				tc := tls.Server(conn, cfg)
				_ = tc.Handshake()
				if reset {
					conn.(*net.TCPConn).SetLinger(0)
				}
				conn.Close()
			}()
		}
	}()
	return ln.Addr().String(), ca
}

// TestClientCertsOnlyForHTTPS: plain http never asks for a certificate.
func TestClientCertsOnlyForHTTPS(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
	t.Cleanup(srv.Close)
	var calls atomic.Int32
	c := newTestClient(t, TransportOptions{ClientCerts: func(string) (*ClientCert, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	}})
	if _, err := c.Send(t.Context(), get(srv.URL+"/")); err != nil || calls.Load() != 0 {
		t.Fatalf("err %v, calls %d", err, calls.Load())
	}
}

func TestClientCertForURL(t *testing.T) {
	t.Parallel()
	var got []string
	f := func(addr string) (*ClientCert, error) { got = append(got, addr); return nil, nil }
	for _, u := range []string{"https://a.example", "wss://b.example:8443/x", "http://c.example", "ws://d", "://bad", "https://[::1]/"} {
		if _, err := ClientCertForURL(f, u); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(got, ",") != "a.example:443,b.example:8443,[::1]:443" {
		t.Errorf("addresses = %v", got)
	}
	if c, err := ClientCertForURL(nil, "https://x"); c != nil || err != nil {
		t.Errorf("nil func = %v, %v", c, err)
	}
}
