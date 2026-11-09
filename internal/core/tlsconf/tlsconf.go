// Package tlsconf turns the TLS settings a user can give a connection
// (certificate verification, a server name override, custom CA
// certificates, a client certificate for mTLS, the minimum TLS version)
// into a *tls.Config, with clear, coded errors for everything that can be
// wrong with the input: unreadable files, PEM without certificates,
// encrypted or mismatched private keys.
//
// Options is plain data (strings, paths, flags) so it can travel in DTOs
// and be stored; Build reads the files and validates. The gRPC transport
// (GRPC-23) uses it; the app-wide CA bundles (NET-04, bundles.go) and the
// per-host client certificates (AUTH-30, clientcerts.go) share its
// loading and validation.
//
// Standard library only. See docs/http-client.md#tls-options.
package tlsconf

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/pkcs12"
)

// MaxFileSize limits one certificate or key file.
const MaxFileSize = 1 << 20

// Minimum TLS versions for Options.MinVersion.
const (
	Version12 = "1.2"
	Version13 = "1.3"
)

// Error codes of Build. Every error has the detail "field" naming the
// option ("caFiles[1]", "clientKeyPem", ...); file errors also have "path".
const (
	CodeInvalidOption        = "tlsconf.invalid_option"     // a value that is not allowed (server name, min version, both a file and PEM)
	CodeInvalidPath          = "tlsconf.invalid_path"       // an empty path, or a relative one without a base directory
	CodeFileUnreadable       = "tlsconf.file_unreadable"    // kind not_found, permission_denied or io
	CodeFileTooLarge         = "tlsconf.file_too_large"     // over MaxFileSize
	CodeCANoCertificates     = "tlsconf.ca_no_certificates" // a CA source without a PEM certificate
	CodeCAInvalid            = "tlsconf.ca_invalid"         // a CA certificate that does not parse
	CodeCARequired           = "tlsconf.ca_required"        // ReplaceSystemRoots with no CA given
	CodeSystemRoots          = "tlsconf.system_roots_unavailable"
	CodeClientCertMissing    = "tlsconf.client_cert_missing"  // a key without a certificate
	CodeClientKeyMissing     = "tlsconf.client_key_missing"   // a certificate without a key
	CodeClientCertInvalid    = "tlsconf.client_cert_invalid"  // no PEM certificate, or one that does not parse
	CodeClientKeyInvalid     = "tlsconf.client_key_invalid"   // no PEM private key, or one that does not parse
	CodeClientKeyEncrypted   = "tlsconf.client_key_encrypted" // kind unsupported
	CodeClientKeyMismatch    = "tlsconf.client_key_mismatch"  // the key is not the certificate's
	CodeClientCertExpired    = "tlsconf.client_cert_expired"  // warning
	CodeClientCertNotYet     = "tlsconf.client_cert_not_yet_valid"
	CodeClientCertUsage      = "tlsconf.client_cert_not_for_client_auth" // warning
	CodeVerificationDisabled = "tlsconf.verification_disabled"           // warning
)

// Hints.
const (
	HintPEM         = "Use a PEM file (text starting with -----BEGIN CERTIFICATE-----). DER and PKCS#12 (.p12, .pfx) files are not supported yet."
	HintKeyPEM      = "Use a PEM private key (-----BEGIN PRIVATE KEY-----, RSA PRIVATE KEY or EC PRIVATE KEY)."
	HintEncrypted   = "Password-protected keys are not supported yet. Decrypt the key first, e.g. openssl pkcs8 -in key.pem -out key-plain.pem, and keep the result private."
	HintMismatch    = "Pick the private key that was created together with this certificate."
	HintCARequired  = "Add a CA certificate, or trust the system certificates too."
	HintMinVersion  = "Use 1.2 or 1.3."
	HintServerName  = "Enter a host name only, without scheme or port."
	HintExpired     = "Renew the client certificate, or check this computer's date and time."
	HintUsage       = "The server may reject a certificate that is not issued for client authentication."
	HintNoVerify    = "Certificate verification is off: the connection is encrypted, but the server is not authenticated."
	HintRelative    = "Use an absolute path."
	HintCheckPath   = "Check that the file exists and can be read."
	HintOneSource   = "Give either a file or pasted PEM, not both."
	HintSystemRoots = "Trust only the custom CA certificates instead."
	// HintPassphrase and HintLegacyEncrypted are for per-host client
	// certificates, which take a passphrase (AUTH-30).
	HintPassphrase      = "Enter the passphrase of the private key."
	HintLegacyEncrypted = "This old OpenSSL key encryption (Proc-Type: 4,ENCRYPTED) is not supported. Convert the key to PKCS#8, e.g. openssl pkcs8 -topk8 -in key.pem -out key-pkcs8.pem, and use that file."
)

// Options are the TLS settings of a connection. The zero value verifies
// the server against the system's certificate authorities with TLS 1.2 or
// newer and sends no client certificate.
//
// Paths may be relative to the base directory given to Build. Private key
// material is never serialized: ClientKeyPEM has no JSON name (see
// docs/grpc-transport.md#persisting-tls-settings).
type Options struct {
	// InsecureSkipVerify turns off verification of the server certificate
	// (chain, host name, expiry). Off by default.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
	// ServerName overrides the name sent in SNI and checked against the
	// server certificate; empty uses the host being connected to.
	ServerName string `json:"serverName,omitempty"`

	// CAFiles and CAPEM add trusted CA certificates (PEM, any number of
	// certificates per source).
	CAFiles []string `json:"caFiles,omitempty"`
	CAPEM   string   `json:"caPem,omitempty"`
	// ReplaceSystemRoots trusts only the CAs above. By default they are
	// trusted in addition to the system's.
	ReplaceSystemRoots bool `json:"replaceSystemRoots,omitempty"`

	// The client certificate for mTLS, as a file or pasted PEM, and its
	// private key. The certificate source may hold intermediates after the
	// leaf, and may hold the key too (then the key may be left empty).
	ClientCertFile string `json:"clientCertFile,omitempty"`
	ClientCertPEM  string `json:"clientCertPem,omitempty"`
	ClientKeyFile  string `json:"clientKeyFile,omitempty"`
	ClientKeyPEM   string `json:"-"`

	// MinVersion is "" (TLS 1.2), "1.2" or "1.3".
	MinVersion string `json:"minVersion,omitempty"`
}

// HasClientCert reports whether a client certificate is configured.
func (o Options) HasClientCert() bool {
	return o.ClientCertFile != "" || o.ClientCertPEM != "" || o.ClientKeyFile != "" || o.ClientKeyPEM != ""
}

// Warning is a problem that does not stop Build: the connection may still
// work, but the user should know.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Result is what Build produces.
type Result struct {
	// Config is ready for http.Transport.TLSClientConfig or tls.Client.
	// NextProtos is left for the protocol client to set.
	Config *tls.Config
	// ClientCert is the leaf client certificate, nil without mTLS.
	ClientCert *x509.Certificate
	// CACerts are the custom CA certificates, in order.
	CACerts []*x509.Certificate
	// Warnings, in a stable order.
	Warnings []Warning
}

// Build validates o, reads its files (relative paths are resolved against
// baseDir; with an empty baseDir they are an error) and returns the TLS
// configuration. Errors are *errs.Error with the codes above.
func Build(o Options, baseDir string) (*Result, error) {
	return build(o, baseDir, time.Now())
}

func build(o Options, baseDir string, now time.Time) (*Result, error) {
	res := &Result{Config: &tls.Config{}}
	cfg := res.Config

	switch o.MinVersion {
	case "", Version12:
		cfg.MinVersion = tls.VersionTLS12
	case Version13:
		cfg.MinVersion = tls.VersionTLS13
	default:
		return nil, invalid("minVersion", "The minimum TLS version "+strconv.Quote(o.MinVersion)+" is not supported.", HintMinVersion)
	}
	if err := validServerName(o.ServerName); err != nil {
		return nil, err
	}
	cfg.ServerName = o.ServerName
	cfg.InsecureSkipVerify = o.InsecureSkipVerify
	if o.InsecureSkipVerify {
		res.Warnings = append(res.Warnings, Warning{Code: CodeVerificationDisabled, Message: "The server certificate is not verified.", Hint: HintNoVerify})
	}

	if err := buildRoots(o, baseDir, res); err != nil {
		return nil, err
	}
	if o.HasClientCert() {
		cert, err := loadClientCert(o, baseDir, nil)
		if err != nil {
			return nil, err
		}
		res.ClientCert = cert.Leaf
		res.Warnings = append(res.Warnings, clientCertWarnings(cert.Leaf, now)...)
		// Always send the configured certificate when the server asks,
		// even if its issuer is not in the server's list of acceptable
		// CAs: the server's verdict is a clearer error than sending none.
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &cert, nil
		}
	}
	return res, nil
}

func validServerName(name string) error {
	if name == "" {
		return nil
	}
	bad := strings.ContainsAny(name, " \t\r\n/@?#[]\\")
	if _, _, err := net.SplitHostPort(name); err == nil {
		bad = true
	}
	if bad {
		return invalid("serverName", "The server name "+strconv.Quote(name)+" is not a host name.", HintServerName)
	}
	return nil
}

func buildRoots(o Options, baseDir string, res *Result) error {
	for i, p := range o.CAFiles {
		field := "caFiles[" + strconv.Itoa(i) + "]"
		data, path, err := readFile(field, p, baseDir)
		if err != nil {
			return err
		}
		certs, err := parseCAs(field, path, data)
		if err != nil {
			return err
		}
		res.CACerts = append(res.CACerts, certs...)
	}
	if o.CAPEM != "" {
		certs, err := parseCAs("caPem", "", []byte(o.CAPEM))
		if err != nil {
			return err
		}
		res.CACerts = append(res.CACerts, certs...)
	}
	if len(res.CACerts) == 0 {
		if o.ReplaceSystemRoots {
			return errs.New(errs.InvalidArgument, "Only custom CA certificates should be trusted, but none are given.").
				WithCode(CodeCARequired).WithHint(HintCARequired).WithDetail("field", "replaceSystemRoots")
		}
		return nil // nil RootCAs: the system's
	}
	pool := x509.NewCertPool()
	if !o.ReplaceSystemRoots {
		sys, err := x509.SystemCertPool()
		if err != nil {
			return errs.Wrap(err, errs.Unsupported, "The system's CA certificates could not be loaded.").
				WithCode(CodeSystemRoots).WithHint(HintSystemRoots).WithDetail("field", "replaceSystemRoots")
		}
		pool = sys
	}
	for _, c := range res.CACerts {
		pool.AddCert(c)
	}
	res.Config.RootCAs = pool
	return nil
}

// parseCAs returns every CERTIFICATE block of data; other blocks are
// ignored.
func parseCAs(field, path string, data []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for _, b := range pemBlocks(data) {
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, withPath(errs.Wrap(err, errs.Parse, "CA certificate "+strconv.Itoa(len(certs)+1)+" in "+sourceName(field, path)+" is not valid.").
				WithCode(CodeCAInvalid).WithDetail("field", field), path)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, withPath(errs.New(errs.Parse, sentence(sourceName(field, path))+" contains no PEM certificate.").
			WithCode(CodeCANoCertificates).WithHint(HintPEM).WithDetail("field", field), path)
	}
	return certs, nil
}

// loadClientCert reads the client certificate and key of o. passphrase
// decrypts an encrypted PKCS#8 key ("ENCRYPTED PRIVATE KEY"); nil means
// encrypted keys are not supported (Build), "" that none was given.
func loadClientCert(o Options, baseDir string, passphrase *string) (tls.Certificate, error) {
	var none tls.Certificate
	if o.ClientCertFile != "" && o.ClientCertPEM != "" {
		return none, invalid("clientCertPem", "The client certificate is given both as a file and as PEM.", HintOneSource)
	}
	if o.ClientKeyFile != "" && o.ClientKeyPEM != "" {
		return none, invalid("clientKeyPem", "The client key is given both as a file and as PEM.", HintOneSource)
	}

	certField, certPath, certData := "clientCertPem", "", []byte(o.ClientCertPEM)
	if o.ClientCertFile != "" {
		var err error
		certField = "clientCertFile"
		if certData, certPath, err = readFile(certField, o.ClientCertFile, baseDir); err != nil {
			return none, err
		}
	}
	keyField, keyPath, keyData := "clientKeyPem", "", []byte(o.ClientKeyPEM)
	if o.ClientKeyFile != "" {
		var err error
		keyField = "clientKeyFile"
		if keyData, keyPath, err = readFile(keyField, o.ClientKeyFile, baseDir); err != nil {
			return none, err
		}
	}
	if len(certData) == 0 {
		return none, errs.New(errs.InvalidArgument, "A client key is given without its certificate.").
			WithCode(CodeClientCertMissing).WithDetail("field", "clientCertFile")
	}

	var chain [][]byte
	var certKey *pem.Block
	for _, b := range pemBlocks(certData) {
		switch {
		case b.Type == "CERTIFICATE":
			chain = append(chain, b.Bytes)
		case isKeyBlock(b) && certKey == nil:
			certKey = b
		}
	}
	certSrc := sourceName(certField, certPath)
	if len(chain) == 0 {
		return none, withPath(errs.New(errs.Parse, sentence(certSrc)+" contains no PEM certificate.").
			WithCode(CodeClientCertInvalid).WithHint(HintPEM).WithDetail("field", certField), certPath)
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return none, withPath(errs.Wrap(err, errs.Parse, "The client certificate in "+certSrc+" is not valid.").
			WithCode(CodeClientCertInvalid).WithHint(HintPEM).WithDetail("field", certField), certPath)
	}
	for _, der := range chain[1:] {
		if _, err := x509.ParseCertificate(der); err != nil {
			return none, withPath(errs.Wrap(err, errs.Parse, "An intermediate certificate in "+certSrc+" is not valid.").
				WithCode(CodeClientCertInvalid).WithDetail("field", certField), certPath)
		}
	}

	var keyBlock *pem.Block
	keySrc := sourceName(keyField, keyPath)
	if len(keyData) > 0 {
		for _, b := range pemBlocks(keyData) {
			if isKeyBlock(b) {
				keyBlock = b
				break
			}
		}
		if keyBlock == nil {
			return none, withPath(errs.New(errs.Parse, sentence(keySrc)+" contains no PEM private key.").
				WithCode(CodeClientKeyInvalid).WithHint(HintKeyPEM).WithDetail("field", keyField), keyPath)
		}
	} else if certKey != nil {
		keyBlock, keyField, keyPath, keySrc = certKey, certField, certPath, certSrc
	} else {
		return none, withPath(errs.New(errs.InvalidArgument, "The client certificate has no private key.").
			WithCode(CodeClientKeyMissing).WithHint("Choose the private key file of the client certificate.").WithDetail("field", "clientKeyFile"), certPath)
	}

	var key crypto.Signer
	switch {
	case keyBlock.Type == "ENCRYPTED PRIVATE KEY" && passphrase != nil && *passphrase != "":
		k, err := pkcs12.DecryptPKCS8(keyBlock.Bytes, *passphrase)
		if err != nil {
			e, _ := errors.AsType[*errs.Error](err)
			switch {
			case e == nil:
				e = errs.Wrap(err, errs.Parse, "The private key in "+keySrc+" could not be decrypted.")
			case e.Code == pkcs12.CodeWrongPassword:
				e = errs.Wrap(err, e.Kind, "The passphrase of the private key in "+keySrc+" is wrong, or the key is damaged.").
					WithCode(e.Code).WithHint("Enter the passphrase that protects the key.")
			}
			return none, withPath(e.WithDetail("field", keyField), keyPath)
		}
		signer, ok := k.(crypto.Signer)
		if !ok {
			return none, withPath(errs.New(errs.Parse, "The private key in "+keySrc+" is of an unsupported type.").
				WithCode(CodeClientKeyInvalid).WithHint(HintKeyPEM).WithDetail("field", keyField), keyPath)
		}
		key = signer
	case keyBlock.Type == "ENCRYPTED PRIVATE KEY" || strings.Contains(keyBlock.Headers["Proc-Type"], "ENCRYPTED"):
		hint := HintEncrypted
		if passphrase != nil {
			hint = HintPassphrase
			if keyBlock.Type != "ENCRYPTED PRIVATE KEY" {
				hint = HintLegacyEncrypted
			}
		}
		return none, withPath(errs.New(errs.Unsupported, "The private key in "+keySrc+" is password-protected.").
			WithCode(CodeClientKeyEncrypted).WithHint(hint).WithDetail("field", keyField), keyPath)
	default:
		k, err := parseKey(keyBlock.Bytes)
		if err != nil {
			return none, withPath(errs.Wrap(err, errs.Parse, "The private key in "+keySrc+" is not valid.").
				WithCode(CodeClientKeyInvalid).WithHint(HintKeyPEM).WithDetail("field", keyField), keyPath)
		}
		key = k
	}
	if !samePublicKey(leaf.PublicKey, key.Public()) {
		return none, withPath(errs.New(errs.InvalidArgument, "The private key does not belong to the client certificate.").
			WithCode(CodeClientKeyMismatch).WithHint(HintMismatch).WithDetail("field", keyField), keyPath)
	}
	return tls.Certificate{Certificate: chain, PrivateKey: key, Leaf: leaf}, nil
}

func clientCertWarnings(leaf *x509.Certificate, now time.Time) []Warning {
	var w []Warning
	switch {
	case now.After(leaf.NotAfter):
		w = append(w, Warning{Code: CodeClientCertExpired, Message: "The client certificate expired on " + leaf.NotAfter.UTC().Format(time.DateOnly) + ".", Hint: HintExpired})
	case now.Before(leaf.NotBefore):
		w = append(w, Warning{Code: CodeClientCertNotYet, Message: "The client certificate is not valid until " + leaf.NotBefore.UTC().Format(time.DateOnly) + ".", Hint: HintExpired})
	}
	if len(leaf.ExtKeyUsage) > 0 {
		ok := false
		for _, u := range leaf.ExtKeyUsage {
			if u == x509.ExtKeyUsageClientAuth || u == x509.ExtKeyUsageAny {
				ok = true
			}
		}
		if !ok {
			w = append(w, Warning{Code: CodeClientCertUsage, Message: "The client certificate is not issued for client authentication.", Hint: HintUsage})
		}
	}
	return w
}

func isKeyBlock(b *pem.Block) bool {
	return b.Type == "PRIVATE KEY" || b.Type == "ENCRYPTED PRIVATE KEY" || strings.HasSuffix(b.Type, " PRIVATE KEY")
}

// parseKey accepts PKCS#8, PKCS#1 (RSA) and SEC 1 (EC) keys whatever the
// block type says, as OpenSSL does.
func parseKey(der []byte) (crypto.Signer, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		switch k := k.(type) {
		case *rsa.PrivateKey, *ecdsa.PrivateKey, ed25519.PrivateKey:
			return k.(crypto.Signer), nil
		}
		return nil, errors.New("unsupported private key type")
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	return nil, errors.New("not a PKCS#8, PKCS#1 or EC private key")
}

func samePublicKey(a, b crypto.PublicKey) bool {
	eq, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && eq.Equal(b)
}

// pemBlocks decodes every PEM block of data, skipping text around them.
func pemBlocks(data []byte) []*pem.Block {
	var out []*pem.Block
	for {
		b, rest := pem.Decode(data)
		if b == nil {
			return out
		}
		out = append(out, b)
		data = rest
	}
}

// readFile reads a certificate or key file, resolving a relative path
// against baseDir. It returns the resolved path too.
func readFile(field, p, baseDir string) ([]byte, string, error) {
	if strings.TrimSpace(p) == "" {
		return nil, "", errs.New(errs.InvalidArgument, "A file path is empty.").
			WithCode(CodeInvalidPath).WithDetail("field", field)
	}
	if !filepath.IsAbs(p) {
		if baseDir == "" {
			return nil, "", errs.New(errs.InvalidArgument, "The path "+strconv.Quote(p)+" is relative.").
				WithCode(CodeInvalidPath).WithHint(HintRelative).WithDetail("field", field).WithDetail("path", p)
		}
		p = filepath.Join(baseDir, p)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, p, fileErr(err, field, p)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, p, fileErr(err, field, p)
	}
	if len(data) > MaxFileSize {
		return nil, p, errs.New(errs.InvalidArgument, "The file "+p+" is larger than 1 MiB; it is not a certificate or key.").
			WithCode(CodeFileTooLarge).WithDetail("field", field).WithDetail("path", p)
	}
	return data, p, nil
}

func fileErr(err error, field, p string) *errs.Error {
	kind := errs.KindOf(err)
	switch kind {
	case errs.NotFound, errs.PermissionDenied:
	default:
		kind = errs.IO
	}
	return errs.Wrap(err, kind, "The file "+p+" could not be read.").
		WithCode(CodeFileUnreadable).WithHint(HintCheckPath).WithDetail("field", field).WithDetail("path", p)
}

func invalid(field, msg, hint string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidOption).WithHint(hint).WithDetail("field", field)
}

func sourceName(field, path string) string {
	if path != "" {
		return "the file " + path
	}
	return "the pasted PEM (" + field + ")"
}

// sentence capitalizes the first letter of s.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func withPath(e *errs.Error, path string) *errs.Error {
	if path != "" {
		e = e.WithDetail("path", path)
	}
	return e
}
