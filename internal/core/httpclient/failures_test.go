package httpclient

import (
	"bufio"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestNetworkFailureMessages sends to a server that fails in each way DBG-09
// names, for real and in process, and checks the message and hint the
// response panel shows. HOST in a message is the URL's host:port.
func TestNetworkFailureMessages(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "test CA")
	other := tlstest.NewCA(t, "other CA")
	valid := ca.Issue(t, tlstest.CertOptions{})
	elsewhere := ca.Issue(t, tlstest.CertOptions{DNSNames: []string{"api.example.invalid"}})
	expired := ca.Issue(t, tlstest.CertOptions{NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour)})

	type target struct {
		url      string
		dial     netx.Options
		tr       TransportOptions
		settings Settings
	}
	tests := []struct {
		name    string
		target  func(t *testing.T) target
		kind    errs.Kind
		code    string
		message string
		hint    string
	}{
		{
			name: "dns not found",
			target: func(*testing.T) target {
				return target{url: "http://nothing-here.invalid/", dial: netx.Options{Resolver: netxtest.NXDomainResolver()}}
			},
			kind: errs.Network, code: netx.CodeDNSNotFound,
			message: "The host name in nothing-here.invalid:80 was not found.", hint: netx.HintDNSNotFound,
		},
		{
			name: "dns lookup failed",
			target: func(*testing.T) target {
				return target{url: "https://api.example.invalid/", dial: netx.Options{Resolver: netxtest.FailingResolver()}}
			},
			kind: errs.Network, code: netx.CodeDNSFailed,
			message: "The host name in api.example.invalid:443 could not be looked up.", hint: netx.HintDNSFailed,
		},
		{
			name:   "connection refused",
			target: func(t *testing.T) target { return target{url: "http://" + closedAddr(t) + "/"} },
			kind:   errs.Network, code: netx.CodeConnectionRefused,
			message: "The connection to HOST was refused.", hint: netx.HintConnectionRefused,
		},
		{
			name:   "connection reset",
			target: func(t *testing.T) target { return target{url: "http://" + resettingServer(t) + "/"} },
			kind:   errs.Network, code: CodeConnectionReset,
			message: "The server at HOST reset the connection while waiting for the response.", hint: netx.HintConnectionReset,
		},
		{
			name: "tls unknown authority",
			target: func(t *testing.T) target {
				return target{url: tlsServerWith(t, valid), tr: TransportOptions{RootCAs: other.Pool()}}
			},
			kind: errs.TLS, code: CodeTLSUnknownAuthority,
			message: "The certificate of HOST is not signed by a trusted authority.", hint: HintTLSUnknownAuthority,
		},
		{
			name: "tls expired certificate",
			target: func(t *testing.T) target {
				return target{url: tlsServerWith(t, expired), tr: TransportOptions{RootCAs: ca.Pool()}}
			},
			kind: errs.TLS, code: CodeTLSCertificateExpired,
			message: "The certificate of HOST has expired or is not valid yet.", hint: HintTLSExpired,
		},
		{
			name: "tls hostname mismatch",
			target: func(t *testing.T) target {
				// The URL has 127.0.0.1; the certificate names another host.
				return target{url: tlsServerWith(t, elsewhere), tr: TransportOptions{RootCAs: ca.Pool()}}
			},
			kind: errs.TLS, code: CodeTLSHostnameMismatch,
			message: "The certificate of HOST is not valid for this host name.", hint: HintTLSHostnameMismatch,
		},
		{
			name: "request timeout",
			target: func(t *testing.T) target {
				srv, _ := blockingServer(t, nil)
				return target{url: srv.URL + "/", settings: Settings{Timeout: 100 * time.Millisecond}}
			},
			kind: errs.Timeout, code: CodeTimeout,
			message: "The request to HOST timed out while waiting for the response.", hint: HintTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tg := tt.target(t)
			c := New(netx.NewDirect(tg.dial), tg.tr)
			t.Cleanup(c.CloseIdleConnections)
			req := get(tg.url)
			if tg.settings != (Settings{}) {
				req.Settings = tg.settings
			}
			_, err := c.Send(t.Context(), req)
			e := wantErr(t, err, tt.kind, tt.code)
			host := strings.TrimSuffix(strings.SplitN(tg.url, "://", 2)[1], "/")
			if want := strings.ReplaceAll(tt.message, "HOST", host); e.Message != want {
				t.Errorf("message = %q, want %q", e.Message, want)
			}
			if e.Hint != tt.hint || e.Hint == "" {
				t.Errorf("hint = %q, want %q", e.Hint, tt.hint)
			}
		})
	}
}

func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// resettingServer reads each request, then resets the connection
// (SO_LINGER 0 makes Close send an RST).
func resettingServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				readRawRequest(bufio.NewReader(conn))
				conn.(*net.TCPConn).SetLinger(0)
				conn.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

// tlsServerWith starts an HTTPS server with cert and returns its URL (by IP).
func tlsServerWith(t *testing.T, cert *tlstest.Cert) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert.TLS}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}
