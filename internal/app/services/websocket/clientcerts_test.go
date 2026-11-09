package websocket

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestConnectClientCertificates (AUTH-30): a wss server that requires
// mTLS closes the connect with the client certificate code until a
// certificate is set for its host, then accepts PEM files.
func TestConnectClientCertificates(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "wss CA")
	peer := make(chan string, 4)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer <- r.TLS.PeerCertificates[0].Subject.CommonName
		nc, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = nc.Close() }()
		_, _ = nc.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n"))
		conn := wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleServer, Reader: brw.Reader})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	srv.TLS = tlstest.MTLSConfig(ca.Issue(t, tlstest.CertOptions{}), tlstest.FixtureClientCAs(t), 0, 0)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	url := "wss" + strings.TrimPrefix(srv.URL, "https") + "/"
	bundle := tlstest.WriteFile(t, "ca.pem", ca.CertPEM)
	trust := tlsconf.NewTrust(func() []string { return []string{bundle} })

	var mu sync.Mutex
	var entries []tlsconf.ClientCertEntry
	certs := tlsconf.NewClientCerts(func() []tlsconf.ClientCertEntry { mu.Lock(); defer mu.Unlock(); return entries }, nil)
	pick := func(addr string) (*httpclient.ClientCert, error) {
		c, err := certs.ForAddress(addr)
		if err != nil || c == nil {
			return nil, err
		}
		return &httpclient.ClientCert{Certificate: c.Certificate, Description: c.Describe()}, nil
	}
	s, ev := newService(t, nil, WithTrust(trust.Roots), WithClientCerts(pick))
	ctx := t.Context()

	connect := func() (string, *StateEventDTO) {
		t.Helper()
		conn, err := s.Connect(ctx, ConnectInputDTO{URL: url})
		if err != nil {
			t.Fatal(err)
		}
		ev.waitFor(t, "open or closed", func(e *events) bool {
			st := e.statesOf(conn.ConnID)
			return st == "connecting,open" || strings.HasSuffix(st, "closed")
		})
		return conn.ConnID, ev.state(conn.ConnID, "closed")
	}

	if _, closed := connect(); closed == nil || closed.Error == nil || closed.Error.Code != netx.CodeTLSClientCertRequired {
		t.Fatalf("without a certificate: closed = %+v", closed)
	}
	mu.Lock()
	entries = []tlsconf.ClientCertEntry{{ID: "cc_1", Host: "127.0.0.1", CertFile: tlstest.Fixture(t, "ec.crt"), KeyFile: tlstest.Fixture(t, "ec.key")}}
	mu.Unlock()
	id, closed := connect()
	if closed != nil {
		t.Fatalf("with a certificate: closed = %+v", closed)
	}
	// The refused handshake never reached the handler.
	if got := <-peer; got != "PostSonde Test Client ec" {
		t.Errorf("server saw %q", got)
	}
	if err := s.Close(ctx, CloseInputDTO{ConnID: id, Code: 1000}); err != nil {
		t.Fatal(err)
	}
}
