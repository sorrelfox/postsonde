package netx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestFailureMessages makes every failure class happen for real, in
// process, and checks the kind, code, message and hint the user sees
// (DBG-09). DNS uses a reserved .invalid name and an injected resolver, so
// the test does not depend on the network.
func TestFailureMessages(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "test CA")
	other := tlstest.NewCA(t, "other CA")
	valid := ca.Issue(t, tlstest.CertOptions{})
	expired := ca.Issue(t, tlstest.CertOptions{NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour)})

	type attempt struct {
		opts       Options
		address    string
		serverName string         // TLS handshake with this name when set
		roots      *x509.CertPool // for the handshake
		exchange   bool           // write, then read, after connecting
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T) attempt
		kind    errs.Kind
		code    string
		message string // with ADDR for the address
		hint    string
		failure Failure
	}{
		{
			name: "dns not found",
			setup: func(*testing.T) attempt {
				return attempt{opts: Options{Resolver: nxdomainResolver()}, address: "nothing-here.invalid:443"}
			},
			kind: errs.Network, code: CodeDNSNotFound, failure: FailureDNSNotFound,
			message: "The host name in ADDR was not found.", hint: HintDNSNotFound,
		},
		{
			name: "dns lookup failed",
			setup: func(*testing.T) attempt {
				return attempt{opts: Options{Resolver: failingResolver()}, address: "api.example.invalid:443"}
			},
			kind: errs.Network, code: CodeDNSFailed, failure: FailureDNSFailed,
			message: "The host name in ADDR could not be looked up.", hint: HintDNSFailed,
		},
		{
			name:  "connection refused",
			setup: func(t *testing.T) attempt { return attempt{address: closedPort(t)} },
			kind:  errs.Network, code: CodeConnectionRefused, failure: FailureRefused,
			message: "The connection to ADDR was refused.", hint: HintConnectionRefused,
		},
		{
			name:  "connection reset",
			setup: func(t *testing.T) attempt { return attempt{address: resetServer(t), exchange: true} },
			kind:  errs.Network, code: CodeConnectionReset, failure: FailureReset,
			message: "The server at ADDR reset the connection.", hint: HintConnectionReset,
		},
		{
			name: "dial timeout",
			setup: func(t *testing.T) attempt {
				return attempt{opts: Options{Timeout: 50 * time.Millisecond, ControlContext: blockingControl(nil)}, address: mustServe(t)}
			},
			kind: errs.Timeout, code: CodeTimeout,
			message: "Connecting to ADDR timed out.", hint: HintTimeout,
		},
		{
			name: "tls unknown authority",
			setup: func(t *testing.T) attempt {
				return attempt{address: tlsServer(t, valid), serverName: "localhost", roots: other.Pool()}
			},
			kind: errs.TLS, code: CodeTLSUnknownAuthority, failure: FailureTLSUnknownAuthority,
			message: "The certificate of ADDR is not signed by a trusted authority.", hint: HintTLSUnknownAuthority,
		},
		{
			name: "tls expired certificate",
			setup: func(t *testing.T) attempt {
				return attempt{address: tlsServer(t, expired), serverName: "localhost", roots: ca.Pool()}
			},
			kind: errs.TLS, code: CodeTLSCertificateExpired, failure: FailureTLSExpired,
			message: "The certificate of ADDR has expired or is not valid yet.", hint: HintTLSExpired,
		},
		{
			name: "tls hostname mismatch",
			setup: func(t *testing.T) attempt {
				return attempt{address: tlsServer(t, valid), serverName: "api.example.invalid", roots: ca.Pool()}
			},
			kind: errs.TLS, code: CodeTLSHostnameMismatch, failure: FailureTLSHostnameMismatch,
			message: "The certificate of ADDR is not valid for this host name.", hint: HintTLSHostnameMismatch,
		},
		{
			name: "tls to a plaintext server",
			setup: func(t *testing.T) attempt {
				return attempt{address: plainServer(t), serverName: "localhost", roots: ca.Pool()}
			},
			kind: errs.TLS, code: CodeTLSServerNotTLS, failure: FailureTLSNotTLS,
			message: "The server at ADDR did not answer with TLS.", hint: HintTLSServerNotTLS,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := tt.setup(t)
			raw := connect(t, a.opts, a.address, a.serverName, a.roots, a.exchange)
			if raw == nil {
				t.Fatal("no error")
			}
			if got := Classify(raw); got != tt.failure {
				t.Errorf("Classify = %d, want %d (%v)", got, tt.failure, raw)
			}
			err := WrapDialError(raw, "tcp", a.address)
			e := wantErr(t, err, tt.kind, tt.code)
			if want := strings.ReplaceAll(tt.message, "ADDR", a.address); e.Message != want {
				t.Errorf("message = %q, want %q", e.Message, want)
			}
			if e.Hint != tt.hint {
				t.Errorf("hint = %q, want %q", e.Hint, tt.hint)
			}
			if !errors.Is(err, raw) {
				t.Errorf("the cause is lost: %v", err)
			}
		})
	}
}

// connect dials address, then optionally does a TLS handshake or a write
// and a read, and returns the first error, unmapped (the *errs.Error from
// Direct is unwrapped to its cause).
func connect(t *testing.T, o Options, address, serverName string, roots *x509.CertPool, exchange bool) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := NewDirect(o).DialContext(ctx, "tcp", address)
	if err != nil {
		return errors.Unwrap(err)
	}
	defer conn.Close()
	if serverName != "" {
		tc := tls.Client(conn, &tls.Config{ServerName: serverName, RootCAs: roots, MinVersion: tls.VersionTLS12})
		return tc.HandshakeContext(ctx)
	}
	if exchange {
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write([]byte("hello")); err != nil {
			return err
		}
		_, err := io.ReadAll(conn)
		return err
	}
	return nil
}

// resetServer accepts connections, waits for the client's first bytes and
// resets the connection (SO_LINGER 0 makes Close send an RST).
func resetServer(t *testing.T) string {
	t.Helper()
	return acceptLoop(t, func(c net.Conn) {
		c.Read(make([]byte, 16))
		c.(*net.TCPConn).SetLinger(0)
		c.Close()
	})
}

// tlsServer serves TLS with cert and closes each connection after the
// handshake.
func tlsServer(t *testing.T, cert *tlstest.Cert) string {
	t.Helper()
	cfg := &tls.Config{Certificates: []tls.Certificate{cert.TLS}, MinVersion: tls.VersionTLS12}
	return acceptLoop(t, func(c net.Conn) {
		defer c.Close()
		tc := tls.Server(c, cfg)
		tc.SetDeadline(time.Now().Add(10 * time.Second))
		tc.Handshake()
	})
}

// plainServer answers anything with a plain HTTP response.
func plainServer(t *testing.T) string {
	t.Helper()
	return acceptLoop(t, func(c net.Conn) {
		defer c.Close()
		c.SetDeadline(time.Now().Add(10 * time.Second))
		c.Read(make([]byte, 1024))
		io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	})
}

func acceptLoop(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	return ln.Addr().String()
}

// TestClassify covers classes that are hard to provoke in process.
func TestClassify(t *testing.T) {
	t.Parallel()
	opErr := func(err error) error {
		return &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", err)}
	}
	tests := []struct {
		name string
		err  error
		want Failure
	}{
		{"nil", nil, FailureNone},
		{"plain", errors.New("odd"), FailureNone},
		{"eof", io.EOF, FailureNone},
		{"timeout", opErr(os.ErrDeadlineExceeded), FailureNone},
		{"dns timeout", &net.DNSError{Err: "i/o timeout", IsTimeout: true}, FailureNone},
		{"reset", opErr(syscall.ECONNRESET), FailureReset},
		{"broken pipe", opErr(syscall.EPIPE), FailureReset},
		{"aborted", opErr(syscall.ECONNABORTED), FailureReset},
		{"unreachable", opErr(unreachableErrnos[0]), FailureUnreachable},
		{"scheme mismatch", http.ErrSchemeMismatch, FailureTLSNotTLS},
		{"other certificate problem", &tls.CertificateVerificationError{Err: x509.CertificateInvalidError{Reason: x509.NotAuthorizedToSign}}, FailureTLSInvalid},
		{"macOS untrusted", &tls.CertificateVerificationError{Err: errors.New("x509: “x” certificate is not trusted")}, FailureTLSUnknownAuthority},
		{"platform verifier", &tls.CertificateVerificationError{Err: errors.New("x509: certificate is not standards compliant")}, FailureTLSInvalid},
		{"alert", &net.OpError{Op: "remote error", Err: tls.AlertError(40)}, FailureTLSHandshake},
		{"other alert", &net.OpError{Op: "remote error", Err: tls.AlertError(70)}, FailureTLSHandshake},
		{"certificate required", &net.OpError{Op: "remote error", Err: tls.AlertError(116)}, FailureTLSClientCertRequired},
		{"bad certificate", &net.OpError{Op: "remote error", Err: tls.AlertError(42)}, FailureTLSClientCertRejected},
		{"unknown ca", &net.OpError{Op: "remote error", Err: tls.AlertError(48)}, FailureTLSClientCertRejected},
		{"flattened alert", errors.New("http2: client conn could not be established: remote error: tls: certificate required"), FailureTLSClientCertRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%v) = %d, want %d", tt.err, got, tt.want)
			}
			e := ConnError(tt.err, "h:1")
			switch {
			case tt.want == FailureNone && e != nil:
				t.Errorf("ConnError = %v, want nil", e)
			case tt.want != FailureNone && (e == nil || tt.want.IsTLS() != (e.Kind == errs.TLS)):
				t.Errorf("ConnError = %v, IsTLS = %v", e, tt.want.IsTLS())
			}
		})
	}
	if ConnError(Deny(DialCheck{Address: "h:1"}, ""), "h:1") != nil {
		t.Error("ConnError re-mapped an *errs.Error")
	}
	if ConnError(io.EOF, "h:1") != nil {
		t.Error("ConnError mapped io.EOF")
	}
}

// TestClassifyClientCert: once the server asked for a client certificate,
// its refusal is recognised however the TLS 1.3 alert raced with the
// client's first write and read.
func TestClassifyClientCert(t *testing.T) {
	t.Parallel()
	pipe := &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)}
	tests := []struct {
		name string
		err  error
		sent bool
		want Failure
	}{
		{"nil", nil, false, FailureNone},
		{"alert", &net.OpError{Op: "remote error", Err: tls.AlertError(116)}, false, FailureTLSClientCertRequired},
		{"tls12 handshake failure", &net.OpError{Op: "remote error", Err: tls.AlertError(40)}, false, FailureTLSClientCertRejected},
		{"bad certificate, sent", &net.OpError{Op: "remote error", Err: tls.AlertError(42)}, true, FailureTLSClientCertRejected},
		{"other alert", &net.OpError{Op: "remote error", Err: tls.AlertError(70)}, false, FailureNone},
		{"broken pipe", pipe, false, FailureTLSClientCertRequired},
		{"broken pipe, sent", pipe, true, FailureTLSClientCertRejected},
		{"reset", &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, false, FailureTLSClientCertRequired},
		{"eof", io.EOF, false, FailureTLSClientCertRequired},
		{"h2", errors.New("http2: client conn could not be established"), false, FailureTLSClientCertRequired},
		{"h1 idle", errors.New("http: server closed idle connection"), false, FailureTLSClientCertRequired},
		{"timeout", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, false, FailureNone},
		{"other", errors.New("odd"), false, FailureNone},
	}
	for _, tt := range tests {
		if got := ClassifyClientCert(tt.err, tt.sent); got != tt.want {
			t.Errorf("%s: ClassifyClientCert = %d, want %d", tt.name, got, tt.want)
		}
	}
	if e := FailureError(FailureTLSClientCertRequired, io.EOF, "h:1"); e == nil || e.Code != CodeTLSClientCertRequired || e.Kind != errs.TLS {
		t.Errorf("FailureError = %v", e)
	}
	if FailureError(FailureNone, io.EOF, "h:1") != nil {
		t.Error("FailureError(FailureNone) is not nil")
	}
}
