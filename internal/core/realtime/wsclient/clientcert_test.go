package wsclient

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// mtlsWSServer starts a wss server that requires a client certificate
// from the fixture CAs and echoes messages; peer receives the common name
// of each certificate it got.
func mtlsWSServer(t *testing.T, maxVersion uint16) (*httptest.Server, *tlstest.CA, chan string) {
	t.Helper()
	ca := tlstest.NewCA(t, "wss server CA")
	peer := make(chan string, 4)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer <- r.TLS.PeerCertificates[0].Subject.CommonName
		sc, nc := upgrade(t, w, r, "", nil)
		echo(sc, nc, make(chan error, 1))
	}))
	srv.TLS = tlstest.MTLSConfig(ca.Issue(t, tlstest.CertOptions{}), tlstest.FixtureClientCAs(t), 0, maxVersion)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, ca, peer
}

func loadFixture(t *testing.T, e tlsconf.ClientCertEntry, pass string) *httpclient.ClientCert {
	t.Helper()
	e.ID, e.Host = "cc_1", "127.0.0.1"
	c, err := tlsconf.LoadClientCert(e, pass)
	if err != nil {
		t.Fatal(err)
	}
	return &httpclient.ClientCert{Certificate: c.Certificate, Description: c.Describe()}
}

// TestClientCertificate: a wss server requiring mTLS accepts PEM and
// PKCS#12 client certificates; the handshake timeline names it.
func TestClientCertificate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		entry tlsconf.ClientCertEntry
		pass  string
		cn    string
	}{
		{"pem", tlsconf.ClientCertEntry{CertFile: tlstest.Fixture(t, "rsa.crt"), KeyFile: tlstest.Fixture(t, "rsa.key")}, "", "PostSonde Test Client rsa"},
		{"pkcs12", tlsconf.ClientCertEntry{PFXFile: tlstest.Fixture(t, "ec-aes128.p12")}, tlstest.FixturePassword, "PostSonde Test Client ec"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, ca, peer := mtlsWSServer(t, 0)
			cert := loadFixture(t, tt.entry, tt.pass)
			c := connect(t, Options{URL: wsURL(srv, "/"), Dialer: netx.NewDirect(netx.Options{}), RootCAs: ca.Pool(), ClientCertificate: cert})
			if got := <-peer; got != tt.cn {
				t.Errorf("server saw %q", got)
			}
			found := false
			for _, e := range c.Handshake().Events {
				found = found || (e.Name == httpclient.EventClientCert && strings.Contains(e.Detail, tt.cn))
			}
			if !found {
				t.Errorf("no client_cert event: %+v", c.Handshake().Events)
			}
		})
	}
}

// TestClientCertificateMissing: without a certificate the connect fails
// with the netx client certificate codes.
func TestClientCertificateMissing(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		max  uint16
		code []string
	}{
		{"tls13", 0, []string{netx.CodeTLSClientCertRequired}},
		// A TLS 1.2 server answers with "handshake failure" after asking.
		{"tls12", tls.VersionTLS12, []string{netx.CodeTLSClientCertRejected}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, ca, _ := mtlsWSServer(t, tt.max)
			c, err := New(Options{URL: wsURL(srv, "/"), Dialer: netx.NewDirect(netx.Options{}), RootCAs: ca.Pool()})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = c.Connect(ctx)
			code := errCode(err)
			ok := false
			for _, want := range tt.code {
				ok = ok || code == want
			}
			if !ok {
				t.Fatalf("Connect = %v (code %q), want one of %v", err, code, tt.code)
			}
		})
	}

	// TLS 1.3: the alert races with the request write and the first read;
	// a server that asks for a certificate and then drops the connection
	// without an alert pins down those outcomes.
	for _, reset := range []bool{true, false} {
		t.Run(map[bool]string{true: "tls13 reset", false: "tls13 close"}[reset], func(t *testing.T) {
			t.Parallel()
			ca := tlstest.NewCA(t, "dropping server CA")
			cfg := &tls.Config{
				Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{}).TLS},
				ClientAuth:   tls.RequestClientCert,
				MinVersion:   tls.VersionTLS13,
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
					_ = tls.Server(conn, cfg).Handshake()
					if reset {
						conn.(*net.TCPConn).SetLinger(0)
					}
					conn.Close()
				}
			}()
			c, err := New(Options{URL: "wss://" + ln.Addr().String() + "/", Dialer: netx.NewDirect(netx.Options{}), RootCAs: ca.Pool()})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = c.Connect(ctx)
			if code := errCode(err); code != netx.CodeTLSClientCertRequired {
				t.Fatalf("Connect = %v (code %q), want %s", err, code, netx.CodeTLSClientCertRequired)
			}
		})
	}
}
