package tlstest

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The committed PKCS#12 fixtures of internal/core/pkcs12/testdata (see its
// gen.sh): client certificates for RSA, ECDSA and Ed25519 keys issued by
// "PostSonde Test Intermediate CA" under "PostSonde Test Root CA", valid
// for a hundred years, as PEM files (rsa.crt, rsa.key, ...) and PKCS#12
// files, most with FixturePassword. They let mTLS tests use real exports
// (Windows, macOS, OpenSSL) instead of generated certificates.

// FixturePassword is the password of the PKCS#12 fixtures and encrypted
// PKCS#8 keys.
const FixturePassword = "correct horse"

// Fixture returns the absolute path of a file in the PKCS#12 fixtures.
func Fixture(tb testing.TB, name string) string {
	tb.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		tb.Fatal("tlstest: cannot locate the fixtures")
	}
	p := filepath.Join(filepath.Dir(file), "..", "..", "pkcs12", "testdata", name)
	if _, err := os.Stat(p); err != nil {
		tb.Fatalf("tlstest: fixture %s: %v", name, err)
	}
	return p
}

// FixtureClientCAs returns a pool with the fixtures' root and intermediate
// CA, for a server that verifies fixture client certificates (a PEM
// certificate file holds the leaf only, without the intermediate).
func FixtureClientCAs(tb testing.TB) *x509.CertPool {
	tb.Helper()
	pool := x509.NewCertPool()
	for _, name := range []string{"root.crt", "inter.crt"} {
		data, err := os.ReadFile(Fixture(tb, name))
		if err != nil {
			tb.Fatalf("tlstest: %v", err)
		}
		if !pool.AppendCertsFromPEM(data) {
			tb.Fatalf("tlstest: %s holds no certificate", name)
		}
	}
	return pool
}

// FixtureLeaf returns the client certificate of a fixture PEM file
// ("rsa.crt").
func FixtureLeaf(tb testing.TB, name string) *x509.Certificate {
	tb.Helper()
	data, err := os.ReadFile(Fixture(tb, name))
	if err != nil {
		tb.Fatalf("tlstest: %v", err)
	}
	b, _ := pem.Decode(data)
	if b == nil {
		tb.Fatalf("tlstest: %s is not PEM", name)
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		tb.Fatalf("tlstest: %v", err)
	}
	return c
}

// MTLSConfig returns a server TLS config with server that requires and
// verifies a client certificate against clientCAs, with TLS versions from
// minVersion to maxVersion (0: crypto/tls's defaults).
func MTLSConfig(server *Cert, clientCAs *x509.CertPool, minVersion, maxVersion uint16) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{server.TLS},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   minVersion,
		MaxVersion:   maxVersion,
	}
}
