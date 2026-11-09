// Package tlstest generates certificate authorities and certificates for
// tests at run time, so no private key is ever committed: a CA signs
// server certificates (localhost, 127.0.0.1 and ::1 by default) and client
// certificates for mTLS. Keys are ECDSA P-256 and encoded as PKCS#8.
//
// It is test support only; nothing outside tests imports it.
package tlstest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// CA is a certificate authority.
type CA struct {
	// Cert is the parsed CA certificate; CertPEM its PEM encoding.
	Cert    *x509.Certificate
	CertPEM []byte
	key     crypto.Signer
}

// Cert is a certificate issued by a CA, with its private key.
type Cert struct {
	Cert *x509.Certificate
	// CertPEM holds the leaf certificate; KeyPEM the PKCS#8 private key.
	CertPEM []byte
	KeyPEM  []byte
	// TLS is the pair ready for tls.Config.Certificates.
	TLS tls.Certificate
}

// CertOptions describe a certificate to issue. The zero value is a server
// certificate for localhost, 127.0.0.1 and ::1, valid from an hour ago for
// a day.
type CertOptions struct {
	// CommonName defaults to "server" or "client".
	CommonName string
	// Client issues a client certificate (ExtKeyUsageClientAuth, no names
	// unless given) instead of a server certificate.
	Client bool
	// DNSNames and IPs replace the default names of a server certificate.
	DNSNames []string
	IPs      []net.IP
	// NotBefore and NotAfter replace the default validity.
	NotBefore, NotAfter time.Time
}

var serial atomic.Int64

// NewCA creates a self-signed CA named name, valid from an hour ago for a
// day.
func NewCA(tb testing.TB, name string) *CA {
	tb.Helper()
	return NewCAValid(tb, name, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
}

// NewCAValid creates a self-signed CA named name valid from notBefore to
// notAfter (an expired CA, one not valid yet).
func NewCAValid(tb testing.TB, name string, notBefore, notAfter time.Time) *CA {
	tb.Helper()
	key := newKey(tb)
	tmpl := &x509.Certificate{
		SerialNumber:          nextSerial(),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"PostSonde tests"}},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		tb.Fatalf("tlstest: create CA: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		tb.Fatalf("tlstest: parse CA: %v", err)
	}
	return &CA{Cert: cert, CertPEM: pemBlock("CERTIFICATE", der), key: key}
}

// Pool returns a pool holding only the CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

// Issue creates a certificate signed by the CA.
func (ca *CA) Issue(tb testing.TB, o CertOptions) *Cert {
	tb.Helper()
	return issue(tb, o, ca.Cert, ca.key)
}

// SelfSigned creates a certificate signed by its own key, not a CA (a
// self-signed server certificate, expired with o.NotAfter in the past).
func SelfSigned(tb testing.TB, o CertOptions) *Cert {
	tb.Helper()
	return issue(tb, o, nil, nil)
}

// issue creates the certificate o signed by parent with parentKey, or by
// itself when parent is nil.
func issue(tb testing.TB, o CertOptions, parent *x509.Certificate, parentKey crypto.Signer) *Cert {
	tb.Helper()
	key := newKey(tb)
	tmpl := &x509.Certificate{
		SerialNumber: nextSerial(),
		NotBefore:    o.NotBefore,
		NotAfter:     o.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		DNSNames:     o.DNSNames,
		IPAddresses:  o.IPs,
	}
	if tmpl.NotBefore.IsZero() {
		tmpl.NotBefore = time.Now().Add(-time.Hour)
	}
	if tmpl.NotAfter.IsZero() {
		tmpl.NotAfter = time.Now().Add(24 * time.Hour)
	}
	cn := o.CommonName
	if o.Client {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		if cn == "" {
			cn = "client"
		}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		if cn == "" {
			cn = "server"
		}
		if o.DNSNames == nil && o.IPs == nil {
			tmpl.DNSNames = []string{"localhost"}
			tmpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
		}
	}
	tmpl.Subject = pkix.Name{CommonName: cn}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, key.Public(), parentKey)
	if err != nil {
		tb.Fatalf("tlstest: issue %s: %v", cn, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		tb.Fatalf("tlstest: parse %s: %v", cn, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		tb.Fatalf("tlstest: marshal key: %v", err)
	}
	return &Cert{
		Cert:    cert,
		CertPEM: pemBlock("CERTIFICATE", der),
		KeyPEM:  pemBlock("PRIVATE KEY", keyDER),
		TLS:     tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert},
	}
}

// WriteFile writes data to name in a fresh temporary directory of tb (0600)
// and returns the path.
func WriteFile(tb testing.TB, name string, data []byte) string {
	tb.Helper()
	p := filepath.Join(tb.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		tb.Fatalf("tlstest: %v", err)
	}
	return p
}

func newKey(tb testing.TB) *ecdsa.PrivateKey {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatalf("tlstest: generate key: %v", err)
	}
	return key
}

func nextSerial() *big.Int {
	return big.NewInt(time.Now().UnixNano() + serial.Add(1))
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
