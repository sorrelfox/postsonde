// Package servertls gives the local servers their TLS certificates
// (SRV-05): the user's own certificate files, or a certificate generated
// at every start by PostSonde's local certificate authority.
//
// The local CA is created once per data directory, the first time a
// server uses a generated certificate, and kept in it:
//
//	<data>/servers/tls/ca.pem       the CA certificate (0644), to trust in clients
//	<data>/servers/tls/ca-key.pem   its private key (0600, in a 0700 directory)
//
// It is never installed anywhere: the user trusts it where they test
// (PostSonde's own CA bundles setting, curl --cacert, the OS or a
// browser; docs/servers.md#tls). Its name constraints limit what it can
// sign to localhost and loopback, private, link-local and shared
// (100.64.0.0/10) addresses, so trusting it cannot make a public site's
// certificate valid even if its key leaks. A CA that expires within 30
// days is replaced. Leaf certificates are made in memory at every start
// and never written.
//
// Standard library only. See docs/servers.md#tls.
package servertls

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// Files of the local CA, relative to the data directory.
const (
	Dir        = "servers/tls"
	CAFileName = "ca.pem"
	KeyName    = "ca-key.pem"
)

// Lifetimes.
const (
	// CAValidity is how long a new local CA is valid.
	CAValidity = 10 * 365 * 24 * time.Hour
	// CARenewBefore: a CA that expires sooner is replaced.
	CARenewBefore = 30 * 24 * time.Hour
	// LeafValidity is how long a generated server certificate is valid,
	// under the 398 days clients accept.
	LeafValidity = 365 * 24 * time.Hour
)

// Error codes.
const (
	// CodeCAInvalid: the stored CA cannot be read or does not parse.
	CodeCAInvalid = "servertls.ca_invalid"
	// CodeCAFailed: the CA or a certificate could not be created or saved.
	CodeCAFailed = "servertls.ca_failed"
)

const (
	dirPerm  fs.FileMode = 0o700
	keyPerm  fs.FileMode = 0o600
	certPerm fs.FileMode = 0o644
)

// permitted are the address ranges the local CA may sign.
var permitted = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

// Permitted reports whether the local CA may sign a certificate for ip.
func Permitted(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range permitted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Authority is the local CA of a data directory. It is safe for concurrent
// use.
type Authority struct {
	dir string // "" keeps the CA in memory
	now func() time.Time

	mu      sync.Mutex
	cert    *x509.Certificate
	certPEM []byte
	key     crypto.Signer
}

// NewAuthority returns the local CA of the data directory dataDir; ""
// makes one in memory for the session (tests, no usable data directory).
// Nothing is read or created until it is used.
func NewAuthority(dataDir string) *Authority {
	a := &Authority{now: time.Now}
	if dataDir != "" {
		a.dir = filepath.Join(dataDir, filepath.FromSlash(Dir))
	}
	return a
}

// CA describes the local CA.
type CA struct {
	// Path is the CA certificate file, "" when kept in memory.
	Path string
	// Cert is the CA certificate and PEM its PEM encoding.
	Cert *x509.Certificate
	PEM  []byte
}

// SHA256 is the certificate's fingerprint, lower-case hex.
func (c CA) SHA256() string {
	sum := sha256.Sum256(c.Cert.Raw)
	return hex.EncodeToString(sum[:])
}

// CA returns the local CA, reading it from the data directory or creating
// it there the first time.
func (a *Authority) CA() (CA, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.loadLocked(); err != nil {
		return CA{}, err
	}
	return CA{Path: a.certPath(), Cert: a.cert, PEM: a.certPEM}, nil
}

func (a *Authority) certPath() string {
	if a.dir == "" {
		return ""
	}
	return filepath.Join(a.dir, CAFileName)
}

func (a *Authority) keyPath() string { return filepath.Join(a.dir, KeyName) }

// loadLocked makes sure a holds a usable CA: the one in memory, the one
// in the data directory, or a new one.
func (a *Authority) loadLocked() error {
	now := a.now()
	if a.cert != nil && now.Add(CARenewBefore).Before(a.cert.NotAfter) {
		return nil
	}
	if a.dir != "" && a.cert == nil {
		cert, certPEM, key, err := a.read()
		switch {
		case err != nil:
			return err
		case cert != nil && now.Add(CARenewBefore).Before(cert.NotAfter):
			a.cert, a.certPEM, a.key = cert, certPEM, key
			return nil
		}
	}
	return a.createLocked(now)
}

// read returns the stored CA; nil when either file is missing.
func (a *Authority) read() (*x509.Certificate, []byte, crypto.Signer, error) {
	certPEM, err := os.ReadFile(a.certPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, a.invalid(err, "The local CA certificate "+a.certPath()+" could not be read.")
	}
	keyPEM, err := os.ReadFile(a.keyPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, a.invalid(err, "The local CA key "+a.keyPath()+" could not be read.")
	}
	cb, _ := pem.Decode(certPEM)
	if cb == nil || cb.Type != "CERTIFICATE" {
		return nil, nil, nil, a.invalid(nil, "The local CA certificate "+a.certPath()+" is not a PEM certificate.")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil || !cert.IsCA {
		return nil, nil, nil, a.invalid(err, "The local CA certificate "+a.certPath()+" is not valid.")
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, nil, a.invalid(nil, "The local CA key "+a.keyPath()+" is not a PEM private key.")
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, nil, a.invalid(err, "The local CA key "+a.keyPath()+" is not valid.")
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return nil, nil, nil, a.invalid(nil, "The local CA key "+a.keyPath()+" is of an unsupported type.")
	}
	if eq, ok := cert.PublicKey.(interface{ Equal(crypto.PublicKey) bool }); !ok || !eq.Equal(signer.Public()) {
		return nil, nil, nil, a.invalid(nil, "The local CA key "+a.keyPath()+" does not belong to its certificate.")
	}
	return cert, certPEM, signer, nil
}

func (a *Authority) invalid(err error, msg string) error {
	return errs.Wrap(err, errs.Parse, msg).WithCode(CodeCAInvalid).
		WithHint("Delete the folder "+a.dir+" to create a new local CA; clients that trusted the old one must trust the new one.").
		WithDetail("path", a.dir)
}

func (a *Authority) failed(err error, msg string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).WithCode(CodeCAFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", a.dir)
}

// createLocked makes a new CA and stores it.
func (a *Authority) createLocked(now time.Time) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return a.failed(err, "The local CA could not be created.")
	}
	var ranges []*net.IPNet
	for _, p := range permitted {
		ranges = append(ranges, &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())})
	}
	host, _ := os.Hostname()
	name := "PostSonde local CA"
	if host != "" {
		name += " (" + host + ")"
	}
	tmpl := &x509.Certificate{
		SerialNumber:                newSerial(),
		Subject:                     pkix.Name{CommonName: name, Organization: []string{"PostSonde local development CA"}},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(CAValidity),
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid:       true,
		IsCA:                        true,
		MaxPathLenZero:              true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{"localhost"},
		PermittedIPRanges:           ranges,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return a.failed(err, "The local CA could not be created.")
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return a.failed(err, "The local CA could not be created.")
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if a.dir != "" {
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return a.failed(err, "The local CA could not be created.")
		}
		if err := os.MkdirAll(a.dir, dirPerm); err != nil {
			return a.failed(err, "The local CA could not be saved.")
		}
		if err := atomicfile.WriteFile(a.keyPath(), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), keyPerm); err != nil {
			return a.failed(err, "The local CA could not be saved.")
		}
		if err := atomicfile.WriteFile(a.certPath(), certPEM, certPerm); err != nil {
			return a.failed(err, "The local CA could not be saved.")
		}
	}
	a.cert, a.certPEM, a.key = cert, certPEM, key
	return nil
}

// Issue returns a new server certificate from the local CA for localhost,
// 127.0.0.1 and ::1 and the addresses in ips the CA may sign (Permitted);
// skipped lists the others.
func (a *Authority) Issue(ips []netip.Addr) (cert *tls.Certificate, skipped []netip.Addr, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.loadLocked(); err != nil {
		return nil, nil, err
	}
	now := a.now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, a.failed(err, "The server certificate could not be created.")
	}
	names := []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()}
	for _, ip := range ips {
		ip = ip.Unmap()
		switch {
		case !ip.IsValid() || ip.IsUnspecified() || containsAddr(names, ip):
		case !Permitted(ip):
			skipped = append(skipped, ip)
		default:
			names = append(names, ip)
		}
	}
	netIPs := make([]net.IP, 0, len(names))
	for _, ip := range names {
		netIPs = append(netIPs, net.IP(ip.AsSlice()))
	}
	notAfter := now.Add(LeafValidity)
	if notAfter.After(a.cert.NotAfter) {
		notAfter = a.cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: "localhost", Organization: []string{"PostSonde local server"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  netIPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, key.Public(), a.key)
	if err != nil {
		return nil, nil, a.failed(err, "The server certificate could not be created.")
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, a.failed(err, "The server certificate could not be created.")
	}
	return &tls.Certificate{Certificate: [][]byte{der, a.cert.Raw}, PrivateKey: key, Leaf: leaf}, skipped, nil
}

func containsAddr(list []netip.Addr, ip netip.Addr) bool {
	for _, x := range list {
		if x == ip {
			return true
		}
	}
	return false
}

func newSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil || n.Sign() == 0 {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

// describeAddrs lists addresses for a warning.
func describeAddrs(ips []netip.Addr) string {
	parts := make([]string, len(ips))
	for i, ip := range ips {
		parts[i] = ip.String()
	}
	return strings.Join(parts, ", ")
}
