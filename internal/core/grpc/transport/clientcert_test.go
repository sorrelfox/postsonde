package transport_test

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// TestPerHostClientCertificate: a per-host client certificate (AUTH-30),
// given as TransportOptions.ClientCertificate the way the app's gRPC
// service does, is accepted by a server requiring mTLS, from PEM files
// and PKCS#12 files; without it the call fails with the client
// certificate code.
func TestPerHostClientCertificate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		entry tlsconf.ClientCertEntry
		pass  string
		max   uint16
	}{
		{"pem", tlsconf.ClientCertEntry{CertFile: tlstest.Fixture(t, "rsa.crt"), KeyFile: tlstest.Fixture(t, "rsa.key")}, "", 0},
		{"pkcs12", tlsconf.ClientCertEntry{PFXFile: tlstest.Fixture(t, "windows-3des-sha1.p12")}, tlstest.FixturePassword, 0},
		{"pkcs12 tls12", tlsconf.ClientCertEntry{PFXFile: tlstest.Fixture(t, "ed25519-pbes2-3des.p12")}, tlstest.FixturePassword, tls.VersionTLS12},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := grpctest.NewServerWith(t, grpctest.ServerOptions{Security: grpctest.MTLS, ClientCAs: tlstest.FixtureClientCAs(t), MaxVersion: tt.max})
			tt.entry.ID, tt.entry.Host = "cc_1", "127.0.0.1"
			cert, err := tlsconf.LoadClientCert(tt.entry, tt.pass)
			if err != nil {
				t.Fatal(err)
			}
			c := transport.New(netx.NewDirect(netx.Options{}), transport.Options{Transport: httpclient.TransportOptions{
				RootCAs:           srv.RootCAs,
				ClientCertificate: &httpclient.ClientCert{Certificate: cert.Certificate, Description: cert.Describe()},
			}})
			t.Cleanup(c.CloseIdleConnections)
			exercise(t, c, transport.CallOptions{Address: srv.Addr, TLS: true})
			if peer := srv.PeerCertificates(); len(peer) == 0 || peer[0].Subject.CommonName != cert.Leaf.Subject.CommonName {
				t.Errorf("server saw %v", peer)
			}

			// The same server without a certificate.
			bare := transport.New(netx.NewDirect(netx.Options{}), transport.Options{Transport: httpclient.TransportOptions{RootCAs: srv.RootCAs}})
			t.Cleanup(bare.CloseIdleConnections)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err = bare.Unary(ctx, transport.CallOptions{Address: srv.Addr, TLS: true, Method: grpctest.MethodUnary}, []byte("x"))
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != transport.CodeTLSClientCertRequired || e.Hint != netx.HintTLSClientCertRequired {
				t.Fatalf("without a certificate: %v", err)
			}
		})
	}
}
