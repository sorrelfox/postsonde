package wsclient

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestNetworkFailureMessages provokes resets and TLS failures for real
// while connecting and checks the shared code, message and hint (DBG-09).
// DNS and refused connections come from netx (TestDialError).
func TestNetworkFailureMessages(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "test CA")
	other := tlstest.NewCA(t, "other CA")
	valid := ca.Issue(t, tlstest.CertOptions{})
	elsewhere := ca.Issue(t, tlstest.CertOptions{DNSNames: []string{"api.example.invalid"}})
	expired := ca.Issue(t, tlstest.CertOptions{NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour)})

	tests := []struct {
		name    string
		url     func(t *testing.T) string
		roots   *x509.CertPool
		kind    errs.Kind
		code    string
		message string // HOST is the dialled host:port
		hint    string
	}{
		{"reset during the upgrade", func(t *testing.T) string { return "ws://" + resettingServer(t) + "/" }, nil,
			errs.Network, CodeConnectionReset, "The server at HOST reset the connection.", netx.HintConnectionReset},
		{"tls unknown authority", func(t *testing.T) string { return tlsServer(t, valid) }, other.Pool(),
			errs.TLS, CodeTLSUnknownAuthority, "The certificate of HOST is not signed by a trusted authority.", netx.HintTLSUnknownAuthority},
		{"tls expired certificate", func(t *testing.T) string { return tlsServer(t, expired) }, ca.Pool(),
			errs.TLS, CodeTLSCertificateExpired, "The certificate of HOST has expired or is not valid yet.", netx.HintTLSExpired},
		{"tls hostname mismatch", func(t *testing.T) string { return tlsServer(t, elsewhere) }, ca.Pool(),
			errs.TLS, CodeTLSHostnameMismatch, "The certificate of HOST is not valid for this host name.", netx.HintTLSHostnameMismatch},
		{"tls to a plain server", func(t *testing.T) string {
			srv := newServer(t, false, func(http.ResponseWriter, *http.Request) {})
			return "wss" + strings.TrimPrefix(srv.URL, "http") + "/"
		}, ca.Pool(), errs.TLS, CodeTLSServerNotTLS, "The server at HOST did not answer with TLS.", HintServerNotTLS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u := tt.url(t)
			c, err := New(Options{URL: u, Dialer: netx.NewDirect(netx.Options{}), RootCAs: tt.roots})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			err = c.Connect(ctx)
			e, ok := errors.AsType[*errs.Error](err)
			if !ok || e.Kind != tt.kind || e.Code != tt.code {
				t.Fatalf("err = %v (%s), want %s %s", err, errCode(err), tt.kind, tt.code)
			}
			host := strings.TrimSuffix(strings.SplitN(u, "://", 2)[1], "/")
			if want := strings.ReplaceAll(tt.message, "HOST", host); e.Message != want {
				t.Errorf("message = %q, want %q", e.Message, want)
			}
			if e.Hint != tt.hint {
				t.Errorf("hint = %q, want %q", e.Hint, tt.hint)
			}
		})
	}
}

// TestResetWhileOpen checks that a reset of an open connection ends it
// with the shared reset code and hint.
func TestResetWhileOpen(t *testing.T) {
	t.Parallel()
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		_, nc := upgrade(t, w, r, "", nil)
		nc.(*net.TCPConn).SetLinger(0)
		nc.Close()
	})
	o, _ := options(srv, "/")
	c := connect(t, o)
	waitDone(t, c)
	e, ok := errors.AsType[*errs.Error](c.Err())
	if !ok || e.Code != netx.CodeConnectionReset || e.Kind != errs.Network || e.Hint != netx.HintConnectionReset {
		t.Fatalf("Err = %v (%s)", c.Err(), errCode(c.Err()))
	}
}

// resettingServer reads the upgrade request, then resets the connection
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
				br := bufio.NewReader(conn)
				for {
					line, err := br.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
				}
				conn.(*net.TCPConn).SetLinger(0)
				conn.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

// tlsServer starts an HTTPS server with cert and returns its wss:// URL.
func tlsServer(t *testing.T, cert *tlstest.Cert) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert.TLS}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return "wss" + strings.TrimPrefix(srv.URL, "https") + "/"
}
