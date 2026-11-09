package tlsconf

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// CA bundles (NET-04): PEM files the user trusts app-wide, on top of the
// system's certificate authorities, for every TLS client (HTTP, GraphQL,
// WebSocket, gRPC). The settings keep their absolute paths; Trust reads
// them when a connection needs them and caches the pool until the list or
// a file changes. See docs/network.md#custom-ca-certificates.

// Warning codes of InspectBundle.
const (
	CodeCAExpired  = "tlsconf.ca_expired"        // a CA certificate past its NotAfter
	CodeCANotYet   = "tlsconf.ca_not_yet_valid"  // a CA certificate before its NotBefore
	CodeCANotForCA = "tlsconf.ca_not_a_ca"       // a certificate that is not a CA (a server's own, self-signed)
	CodeCAHasKey   = "tlsconf.ca_bundle_has_key" // the file also holds a private key
)

// Hints of the bundle warnings and errors.
const (
	HintCAExpired = "Servers whose certificates chain to an expired CA are rejected. Get a current copy of the CA certificate."
	HintCANotCA   = "It is trusted as it is: only a server presenting this very certificate is accepted, not certificates it signed."
	HintCAHasKey  = "PostSonde ignores the key, but a CA bundle should not hold one. Keep private keys out of shared files."
	HintBundle    = "Fix the file, or remove it from Custom CA certificates in Settings, Network."
)

// BundleCert describes one certificate of a CA bundle, for the settings
// screen.
type BundleCert struct {
	Subject   string
	Issuer    string
	NotBefore time.Time
	NotAfter  time.Time
	IsCA      bool
	// SHA256 is the fingerprint of the DER certificate, lower-case hex.
	SHA256 string
}

// Bundle is what InspectBundle found in a CA bundle file.
type Bundle struct {
	// Path is the file, absolute and cleaned.
	Path  string
	Certs []BundleCert
	// Warnings, in certificate order: expired or not yet valid CAs,
	// certificates that are not CAs, a private key in the file.
	Warnings []Warning
}

// InspectBundle reads the CA bundle at path, which must be absolute, and
// describes its certificates. Errors are those of Build for a CA file
// (codes CodeInvalidPath, CodeFileUnreadable, CodeFileTooLarge,
// CodeCANoCertificates, CodeCAInvalid), with the detail field "caBundle".
// An expired CA is a warning, not an error: the file may hold current CAs
// too.
func InspectBundle(path string) (*Bundle, error) {
	return inspectBundle(path, time.Now())
}

func inspectBundle(path string, now time.Time) (*Bundle, error) {
	const field = "caBundle"
	data, p, err := readFile(field, path, "")
	if err != nil {
		return nil, err
	}
	certs, err := parseCAs(field, p, data)
	if err != nil {
		return nil, err
	}
	b := &Bundle{Path: filepath.Clean(p)}
	for i, c := range certs {
		sum := sha256.Sum256(c.Raw)
		b.Certs = append(b.Certs, BundleCert{
			Subject:   c.Subject.String(),
			Issuer:    c.Issuer.String(),
			NotBefore: c.NotBefore,
			NotAfter:  c.NotAfter,
			IsCA:      c.IsCA,
			SHA256:    hex.EncodeToString(sum[:]),
		})
		name := certName(c, i)
		switch {
		case now.After(c.NotAfter):
			b.Warnings = append(b.Warnings, Warning{Code: CodeCAExpired, Message: sentence(name) + " expired on " + c.NotAfter.UTC().Format(time.DateOnly) + ".", Hint: HintCAExpired})
		case now.Before(c.NotBefore):
			b.Warnings = append(b.Warnings, Warning{Code: CodeCANotYet, Message: sentence(name) + " is not valid until " + c.NotBefore.UTC().Format(time.DateOnly) + ".", Hint: HintExpired})
		}
		if !c.IsCA {
			b.Warnings = append(b.Warnings, Warning{Code: CodeCANotForCA, Message: sentence(name) + " is not a CA certificate.", Hint: HintCANotCA})
		}
	}
	for _, blk := range pemBlocks(data) {
		if isKeyBlock(blk) {
			b.Warnings = append(b.Warnings, Warning{Code: CodeCAHasKey, Message: "The file also contains a private key.", Hint: HintCAHasKey})
			break
		}
	}
	return b, nil
}

// certName names certificate i of a bundle in a message: its common name
// when it has one.
func certName(c *x509.Certificate, i int) string {
	if cn := strings.TrimSpace(c.Subject.CommonName); cn != "" {
		return "the certificate " + strconv.Quote(cn)
	}
	return "certificate " + strconv.Itoa(i+1)
}

// Trust is the app-wide trust configuration: the system roots plus the CA
// bundles files returns (absolute paths). It is safe for concurrent use.
type Trust struct {
	files func() []string

	mu   sync.Mutex
	key  string
	pool *x509.CertPool
	// system loads the system roots; x509.SystemCertPool outside tests.
	system func() (*x509.CertPool, error)
}

// NewTrust returns a Trust over the bundle list files returns (read on
// every Roots call, so a settings change applies to the next connection).
// A nil files means no bundles.
func NewTrust(files func() []string) *Trust {
	return &Trust{files: files, system: x509.SystemCertPool}
}

// Roots returns the certificate authorities to verify servers against:
// nil (the system's, as crypto/tls uses them by default) when no bundle is
// configured, otherwise the system's plus every certificate of the
// bundles. The pool is cached and only rebuilt when the list, or a file's
// size or modification time, changes, so callers may key their clients
// on the pointer.
//
// A bundle that can no longer be used (deleted, no longer PEM) is an
// error with the code of Build for a CA file, the detail field
// "network.caBundles[i]" and HintBundle. A nil Trust has no bundles.
func (t *Trust) Roots() (*x509.CertPool, error) {
	if t == nil || t.files == nil {
		return nil, nil
	}
	paths := t.files()
	if len(paths) == 0 {
		return nil, nil
	}
	var key strings.Builder
	for i, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, bundleErr(fileErr(err, bundleField(i), p))
		}
		key.WriteString(p + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + "\x00")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pool != nil && t.key == key.String() {
		return t.pool, nil
	}
	var certs []*x509.Certificate
	for i, p := range paths {
		field := bundleField(i)
		data, path, err := readFile(field, p, "")
		if err != nil {
			return nil, bundleErr(err)
		}
		c, err := parseCAs(field, path, data)
		if err != nil {
			return nil, bundleErr(err)
		}
		certs = append(certs, c...)
	}
	pool, err := t.system()
	if err != nil {
		return nil, errs.Wrap(err, errs.Unsupported, "The system's CA certificates could not be loaded, so the custom CA certificates cannot be added to them.").
			WithCode(CodeSystemRoots).WithHint(HintBundle).WithDetail("field", "network.caBundles")
	}
	for _, c := range certs {
		pool.AddCert(c)
	}
	t.pool, t.key = pool, key.String()
	return pool, nil
}

func bundleField(i int) string { return "network.caBundles[" + strconv.Itoa(i) + "]" }

// bundleErr adds the settings hint to an error about a configured bundle.
func bundleErr(err error) error {
	if e, ok := err.(*errs.Error); ok {
		return e.WithHint(HintBundle)
	}
	return err
}
