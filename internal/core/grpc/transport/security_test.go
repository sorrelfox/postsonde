package transport_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// secureClient returns a Client built with NewWithTLS, failing the test on
// a build error.
func secureClient(t *testing.T, sec tlsconf.Options, baseDir string) (*transport.Client, *tlsconf.Result) {
	t.Helper()
	c, res, err := transport.NewWithTLS(netx.NewDirect(netx.Options{}), transport.Options{}, sec, baseDir)
	if err != nil {
		t.Fatalf("NewWithTLS: %v", err)
	}
	t.Cleanup(c.CloseIdleConnections)
	return c, res
}

// exercise makes a unary, a server streaming and a bidi call.
func exercise(t *testing.T, c *transport.Client, o transport.CallOptions) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	o.Method = grpctest.MethodUnary
	res, err := c.Unary(ctx, o, []byte("hello"))
	if err != nil || string(res.Message) != "hello" {
		t.Fatalf("unary: %v, %+v", err, res)
	}

	o.Method = grpctest.MethodServerStream
	s, err := c.Invoke(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send([]byte("m")); err != nil {
		t.Fatal(err)
	}
	s.CloseSend()
	n := 0
	for {
		_, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("server stream: %v", err)
		}
		n++
	}
	if n != 3 {
		t.Fatalf("server stream: %d messages", n)
	}

	o.Method = grpctest.MethodBidi
	s, err = c.Invoke(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, m := range []string{"a", "b"} {
		if err := s.Send([]byte(m)); err != nil {
			t.Fatal(err)
		}
		got, err := s.Recv()
		if err != nil || string(got) != m {
			t.Fatalf("bidi: %q, %v", got, err)
		}
	}
	s.CloseSend()
	if _, err := s.Recv(); err != io.EOF {
		t.Fatalf("bidi end: %v", err)
	}
}

func unaryErr(t *testing.T, c *transport.Client, srv *grpctest.Server, tlsOn bool) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.Unary(ctx, transport.CallOptions{Address: srv.Addr, TLS: tlsOn, Method: grpctest.MethodUnary}, []byte("x"))
	return err
}

// wantTLSCode checks the status, error code and the tls kind.
func wantTLSCode(t *testing.T, err error, code string) *errs.Error {
	t.Helper()
	wantStatus(t, err, transport.Unavailable, code)
	e, _ := errors.AsType[*errs.Error](err)
	if e.Kind != errs.TLS || e.Hint == "" {
		t.Fatalf("kind %v, hint %q; want tls with a hint (%v)", e.Kind, e.Hint, err)
	}
	return e
}

func TestSecurityModes(t *testing.T) {
	t.Parallel()
	t.Run("h2c", func(t *testing.T) {
		t.Parallel()
		srv := grpctest.NewServerWith(t, grpctest.ServerOptions{Security: grpctest.H2C})
		c, _ := secureClient(t, tlsconf.Options{}, "")
		exercise(t, c, transport.CallOptions{Address: srv.Addr})
		if srv.LastRequest().ProtoMajor != 2 {
			t.Fatalf("proto = %s", srv.LastRequest().Proto)
		}
	})
	t.Run("tls custom CA added to system roots", func(t *testing.T) {
		t.Parallel()
		srv := grpctest.NewServerWith(t, grpctest.ServerOptions{Security: grpctest.TLS})
		c, res := secureClient(t, tlsconf.Options{CAPEM: string(srv.CA.CertPEM)}, "")
		if len(res.Warnings) != 0 {
			t.Fatalf("warnings = %+v", res.Warnings)
		}
		exercise(t, c, transport.CallOptions{Address: srv.Addr, TLS: true})
	})
	t.Run("tls custom CA only, from a file", func(t *testing.T) {
		t.Parallel()
		srv := grpctest.NewServerWith(t, grpctest.ServerOptions{Security: grpctest.TLS})
		path := tlstest.WriteFile(t, "ca.pem", srv.CA.CertPEM)
		c, _ := secureClient(t, tlsconf.Options{CAFiles: []string{filepath.Base(path)}, ReplaceSystemRoots: true}, filepath.Dir(path))
		exercise(t, c, transport.CallOptions{Address: srv.Addr, TLS: true})
	})
	for _, maxVersion := range []uint16{tls.VersionTLS12, 0} {
		name := "mtls TLS 1.3"
		if maxVersion != 0 {
			name = "mtls TLS 1.2"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := grpctest.NewServerWith(t, grpctest.ServerOptions{Security: grpctest.MTLS, MaxVersion: maxVersion})
			cert := tlstest.WriteFile(t, "client.crt", srv.ClientCert.CertPEM)
			key := tlstest.WriteFile(t, "client.key", srv.ClientCert.KeyPEM)
			c, res := secureClient(t, tlsconf.Options{
				CAPEM:          string(srv.CA.CertPEM),
				ClientCertFile: cert,
				ClientKeyFile:  key,
			}, "")
			if res.ClientCert == nil {
				t.Fatal("no client certificate in the result")
			}
			exercise(t, c, transport.CallOptions{Address: srv.Addr, TLS: true})
			if pc := srv.PeerCertificates(); len(pc) == 0 || !pc[0].Equal(srv.ClientCert.Cert) {
				t.Fatalf("server saw client certificates %v", pc)
			}
		})
	}
	t.Run("one client, plaintext and TLS calls", func(t *testing.T) {
		t.Parallel()
		h2c := grpctest.NewServer(t, false)
		tlsSrv := grpctest.NewServer(t, true)
		c, _ := secureClient(t, tlsconf.Options{CAPEM: string(tlsSrv.CA.CertPEM)}, "")
		exercise(t, c, transport.CallOptions{Address: h2c.Addr})
		exercise(t, c, transport.CallOptions{Address: tlsSrv.Addr, TLS: true})
	})
}

func TestServerVerificationFailures(t *testing.T) {
	t.Parallel()
	srv := grpctest.NewServerWith(t, grpctest.ServerOptions{Security: grpctest.TLS})
	other := tlstest.NewCA(t, "other CA")

	t.Run("no CA", func(t *testing.T) {
		c, _ := secureClient(t, tlsconf.Options{}, "")
		wantTLSCode(t, unaryErr(t, c, srv, true), transport.CodeTLSUnknownAuthority)
	})
	t.Run("wrong CA only", func(t *testing.T) {
		c, _ := secureClient(t, tlsconf.Options{CAPEM: string(other.CertPEM), ReplaceSystemRoots: true}, "")
		wantTLSCode(t, unaryErr(t, c, srv, true), transport.CodeTLSUnknownAuthority)
	})
	t.Run("wrong server name", func(t *testing.T) {
		c, _ := secureClient(t, tlsconf.Options{CAPEM: string(srv.CA.CertPEM), ReplaceSystemRoots: true, ServerName: "wrong.example"}, "")
		wantTLSCode(t, unaryErr(t, c, srv, true), transport.CodeTLSHostnameMismatch)
	})
	t.Run("verification off", func(t *testing.T) {
		c, res := secureClient(t, tlsconf.Options{InsecureSkipVerify: true, ServerName: "wrong.example"}, "")
		if len(res.Warnings) != 1 || res.Warnings[0].Code != tlsconf.CodeVerificationDisabled {
			t.Fatalf("warnings = %+v", res.Warnings)
		}
		if err := unaryErr(t, c, srv, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		ca := tlstest.NewCA(t, "CA")
		expired := ca.Issue(t, tlstest.CertOptions{NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour)})
		old := grpctest.NewServerWith(t, grpctest.ServerOptions{Security: grpctest.TLS, CA: ca, ServerCert: expired})
		c, _ := secureClient(t, tlsconf.Options{CAPEM: string(ca.CertPEM), ReplaceSystemRoots: true}, "")
		wantTLSCode(t, unaryErr(t, c, old, true), transport.CodeTLSCertificateExpired)
	})
}

func TestServerNameOverride(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "CA")
	cert := ca.Issue(t, tlstest.CertOptions{DNSNames: []string{"grpc.internal"}})
	srv := grpctest.NewServerWith(t, grpctest.ServerOptions{Security: grpctest.TLS, CA: ca, ServerCert: cert})

	c, _ := secureClient(t, tlsconf.Options{CAPEM: string(ca.CertPEM), ReplaceSystemRoots: true}, "")
	wantTLSCode(t, unaryErr(t, c, srv, true), transport.CodeTLSHostnameMismatch)

	c, _ = secureClient(t, tlsconf.Options{CAPEM: string(ca.CertPEM), ReplaceSystemRoots: true, ServerName: "grpc.internal"}, "")
	exercise(t, c, transport.CallOptions{Address: srv.Addr, TLS: true})
	if got := srv.LastRequest().TLS.ServerName; got != "grpc.internal" {
		t.Fatalf("SNI = %q", got)
	}
	// :authority stays the address.
	if got := srv.LastRequest().Host; got != srv.Addr {
		t.Fatalf(":authority = %q", got)
	}
}

func TestClientCertificateFailures(t *testing.T) {
	t.Parallel()
	for _, maxVersion := range []uint16{tls.VersionTLS12, 0} {
		name := "TLS 1.3"
		if maxVersion != 0 {
			name = "TLS 1.2"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := grpctest.NewServerWith(t, grpctest.ServerOptions{Security: grpctest.MTLS, MaxVersion: maxVersion})
			base := tlsconf.Options{CAPEM: string(srv.CA.CertPEM)}

			// No client certificate.
			c, _ := secureClient(t, base, "")
			wantTLSCode(t, unaryErr(t, c, srv, true), transport.CodeTLSClientCertRequired)

			// A certificate from a CA the server does not trust.
			stranger := tlstest.NewCA(t, "stranger").Issue(t, tlstest.CertOptions{Client: true})
			o := base
			o.ClientCertPEM, o.ClientKeyPEM = string(stranger.CertPEM), string(stranger.KeyPEM)
			c, _ = secureClient(t, o, "")
			wantTLSCode(t, unaryErr(t, c, srv, true), transport.CodeTLSClientCertRejected)

			// An expired certificate from the right CA: a warning, then
			// rejected.
			expired := srv.ClientCA.Issue(t, tlstest.CertOptions{Client: true, NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-time.Hour)})
			o.ClientCertPEM, o.ClientKeyPEM = string(expired.CertPEM), string(expired.KeyPEM)
			c, res := secureClient(t, o, "")
			if len(res.Warnings) != 1 || res.Warnings[0].Code != tlsconf.CodeClientCertExpired {
				t.Fatalf("warnings = %+v", res.Warnings)
			}
			wantTLSCode(t, unaryErr(t, c, srv, true), transport.CodeTLSClientCertRejected)
		})
	}
}

func TestSecurityModeMismatch(t *testing.T) {
	t.Parallel()
	h2c := grpctest.NewServer(t, false)
	tlsSrv := grpctest.NewServer(t, true)
	c, _ := secureClient(t, tlsconf.Options{CAPEM: string(tlsSrv.CA.CertPEM)}, "")

	// TLS to a plaintext server.
	wantTLSCode(t, unaryErr(t, c, h2c, true), transport.CodeTLSServerNotTLS)

	// Plaintext to a TLS server: the server drops the connection.
	err := unaryErr(t, c, tlsSrv, false)
	e := wantStatusCode(t, err, transport.Unavailable)
	if e.Hint != transport.HintPlaintextClosed && e.Code != transport.CodeNotHTTP2 {
		t.Fatalf("plaintext to TLS: %#v", e)
	}
}

func wantStatusCode(t *testing.T, err error, code transport.Code) *errs.Error {
	t.Helper()
	if st := transport.StatusFromError(err); st.Code != code {
		t.Fatalf("status = %+v (%v), want %v", st, err, code)
	}
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		t.Fatalf("%T is not an *errs.Error", err)
	}
	return e
}

func TestNewWithTLSInvalid(t *testing.T) {
	t.Parallel()
	_, _, err := transport.NewWithTLS(netx.NewDirect(netx.Options{}), transport.Options{}, tlsconf.Options{CAPEM: "not pem"}, "")
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != tlsconf.CodeCANoCertificates {
		t.Fatalf("err = %v", err)
	}
}
