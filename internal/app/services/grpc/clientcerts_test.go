package grpc

import (
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestInvokeClientCertificates (AUTH-30): a TLS call to a server that
// requires mTLS fails with the client certificate code until a
// certificate is set for its address, then succeeds with a PKCS#12 file.
func TestInvokeClientCertificates(t *testing.T) {
	t.Parallel()
	srv := newCallServerWith(t, grpctest.ServerOptions{Security: grpctest.MTLS, ClientCAs: tlstest.FixtureClientCAs(t)})
	bundle := tlstest.WriteFile(t, "ca.pem", srv.CA.CertPEM)
	trust := tlsconf.NewTrust(func() []string { return []string{bundle} })

	var entries []tlsconf.ClientCertEntry
	certs := tlsconf.NewClientCerts(func() []tlsconf.ClientCertEntry { return entries },
		func(string) (string, error) { return tlstest.FixturePassword, nil })
	var asked []string
	pick := func(addr string) (*httpclient.ClientCert, error) {
		asked = append(asked, addr)
		c, err := certs.ForAddress(addr)
		if err != nil || c == nil {
			return nil, err
		}
		return &httpclient.ClientCert{Certificate: c.Certificate, Description: c.Describe()}, nil
	}
	s, rec := newCallService(t, WithTrust(trust.Roots), WithClientCerts(pick))

	call := func() string {
		t.Helper()
		in := input(srv.Addr, "Echo")
		in.TLS = true
		started, err := s.Invoke(t.Context(), in)
		if err != nil {
			var e *errs.Error
			if errors.As(err, &e) {
				return e.Code
			}
			t.Fatalf("Invoke: %v", err)
		}
		_, done := rec.wait(t, started.OpID)
		if done.Status == operation.StatusOK {
			return ""
		}
		return done.Error.Code
	}

	if code := call(); code != transport.CodeTLSClientCertRequired {
		t.Fatalf("without a certificate: %q", code)
	}
	if len(asked) != 1 || asked[0] != srv.Addr {
		t.Errorf("asked for %v, want %s", asked, srv.Addr)
	}
	entries = []tlsconf.ClientCertEntry{{ID: "cc_1", Host: "127.0.0.1", PFXFile: tlstest.Fixture(t, "openssl-legacy.p12")}}
	if code := call(); code != "" {
		t.Fatalf("with a certificate: %q", code)
	}
	if peer := srv.PeerCertificates(); len(peer) == 0 || peer[0].Subject.CommonName != "PostSonde Test Client rsa" {
		t.Errorf("server saw %v", peer)
	}
	// Plaintext calls do not ask.
	asked = nil
	plain := newCallServer(t, false)
	if _, done := invoke(t, s, rec, input(plain.Addr, "Echo")); done.Status != operation.StatusOK || len(asked) != 0 {
		t.Fatalf("h2c call: %+v, asked %v", done, asked)
	}
}

func TestCallAddress(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"api.example.com":      "api.example.com:443",
		"api.example.com:8443": "api.example.com:8443",
		"[::1]":                "[::1]:443",
		"[::1]:50051":          "[::1]:50051",
	} {
		if got := callAddress(in); got != want {
			t.Errorf("callAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
