package websocket

import (
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestConnectCustomCA (NET-04): a wss server whose certificate is signed
// by a private CA is refused until the CA's bundle is trusted.
func TestConnectCustomCA(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "Dev CA")
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{}).TLS}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	url := "wss" + strings.TrimPrefix(srv.URL, "https") + "/"
	bundle := tlstest.WriteFile(t, "dev-ca.pem", ca.CertPEM)

	var mu sync.Mutex
	var bundles []string
	setBundles := func(b ...string) { mu.Lock(); bundles = b; mu.Unlock() }
	trust := tlsconf.NewTrust(func() []string { mu.Lock(); defer mu.Unlock(); return bundles })
	s, ev := newService(t, nil, WithTrust(trust.Roots))
	ctx := t.Context()

	connect := func() (id string, closed *StateEventDTO) {
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

	if _, closed := connect(); closed == nil || closed.Error == nil || closed.Error.Code != netx.CodeTLSUnknownAuthority {
		t.Fatalf("without the bundle: closed = %+v, want %s", closed, netx.CodeTLSUnknownAuthority)
	}
	setBundles(bundle)
	id, closed := connect()
	if closed != nil {
		t.Fatalf("with the bundle: closed = %+v", closed)
	}
	if err := s.Close(ctx, CloseInputDTO{ConnID: id, Code: 1000}); err != nil {
		t.Fatal(err)
	}

	// A bundle that cannot be used fails a wss connect at once, but not
	// a ws one.
	setBundles(filepath.Join(t.TempDir(), "gone.pem"))
	_, err := s.Connect(ctx, ConnectInputDTO{URL: url})
	checkCode(t, err, errs.NotFound, tlsconf.CodeFileUnreadable)
	if _, err := s.Connect(ctx, ConnectInputDTO{URL: "ws" + strings.TrimPrefix(url, "wss")}); err != nil {
		t.Fatalf("ws connect with a broken bundle: %v", err)
	}
}
