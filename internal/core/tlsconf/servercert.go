package tlsconf

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Server certificates of the local servers (SRV-05): a local server can
// serve TLS with a certificate the user provides, as a PEM certificate and
// key or a PKCS#12 file, read like a client certificate. The passphrase
// comes from the app's local secret storage; the files are only read. See
// docs/servers.md#tls.

// Warning codes of a server certificate.
const (
	CodeServerCertExpired = "tlsconf.server_cert_expired"
	CodeServerCertNotYet  = "tlsconf.server_cert_not_yet_valid"
	CodeServerCertUsage   = "tlsconf.server_cert_not_for_server_auth"
	// CodeServerCertSource: neither or both PEM and PKCS#12, a key with
	// PKCS#12, or a relative path.
	CodeServerCertSource = "tlsconf.server_cert_source"
)

// HintServerCertFile is the hint of a server certificate without a usable
// source.
const HintServerCertFile = "Choose a PEM certificate and its key, or one PKCS#12 (.p12, .pfx) file."

// ServerCert is a loaded server certificate.
type ServerCert struct {
	// Certificate is ready for tls.Config: the leaf, the chain, the key.
	Certificate *tls.Certificate
	// Leaf is the server certificate.
	Leaf *x509.Certificate
	// Warnings: expired or not yet valid, not issued for server
	// authentication. The server serves it anyway: testing how a client
	// handles a bad certificate is a reason to have one.
	Warnings []Warning
}

// CheckServerCertFiles checks the shape of a server certificate source
// without reading files: a PEM certificate (with KeyFile unless the
// certificate file holds the key) or a PKCS#12 file, with absolute paths.
// Errors are invalid_argument with a detail "field" (certFile, keyFile or
// pfxFile).
func CheckServerCertFiles(certFile, keyFile, pfxFile string) error {
	source := func(msg, field string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeServerCertSource).WithHint(HintServerCertFile).WithDetail("field", field)
	}
	switch {
	case pfxFile != "" && (certFile != "" || keyFile != ""):
		return source("A server certificate is either a PKCS#12 file or a PEM certificate and key, not both.", "pfxFile")
	case pfxFile == "" && certFile == "":
		if keyFile != "" {
			return source("A private key is given without its certificate.", "certFile")
		}
		return source("No certificate file is given.", "certFile")
	}
	for _, f := range []entryFile{{"certFile", certFile}, {"keyFile", keyFile}, {"pfxFile", pfxFile}} {
		if f.path != "" && (strings.TrimSpace(f.path) != f.path || !filepath.IsAbs(f.path)) {
			return errs.New(errs.InvalidArgument, "The path "+strconv.Quote(f.path)+" is not absolute.").
				WithCode(CodeServerCertSource).WithHint(HintRelative).WithDetail("field", f.field).WithDetail("path", f.path)
		}
	}
	return nil
}

// LoadServerCert reads and checks a server certificate: a PEM certificate
// file (with any intermediates after it) and its PEM key, or a PKCS#12
// file. passphrase opens the PKCS#12 file or an encrypted PKCS#8 key. The
// key must belong to the certificate. An expired certificate is only a
// warning. Errors are *errs.Error with the codes of Build (tlsconf.*) and
// pkcs12.Decode (pkcs12.*), and the details field and path.
func LoadServerCert(certFile, keyFile, pfxFile, passphrase string) (*ServerCert, error) {
	return loadServerCert(certFile, keyFile, pfxFile, passphrase, time.Now())
}

func loadServerCert(certFile, keyFile, pfxFile, passphrase string, now time.Time) (*ServerCert, error) {
	if err := CheckServerCertFiles(certFile, keyFile, pfxFile); err != nil {
		return nil, err
	}
	var cert tls.Certificate
	var err error
	if pfxFile != "" {
		cert, err = loadPKCS12(pfxFile, passphrase)
	} else {
		cert, err = loadClientCert(Options{ClientCertFile: certFile, ClientKeyFile: keyFile}, "", &passphrase)
		err = entryField(err)
	}
	if err != nil {
		return nil, serverWording(err)
	}
	return &ServerCert{Certificate: &cert, Leaf: cert.Leaf, Warnings: serverCertWarnings(cert.Leaf, now)}, nil
}

// serverWording rewords an error of the client certificate loader for a
// server certificate.
func serverWording(err error) error {
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		return err
	}
	r := strings.NewReplacer("client certificate", "server certificate", "client key", "private key", "Client certificate", "Server certificate")
	out := *e
	out.Message = r.Replace(e.Message)
	out.Hint = r.Replace(e.Hint)
	return &out
}

func serverCertWarnings(leaf *x509.Certificate, now time.Time) []Warning {
	var w []Warning
	const hint = "Clients that check certificates reject it. Renew it, or use the generated certificate."
	switch {
	case now.After(leaf.NotAfter):
		w = append(w, Warning{Code: CodeServerCertExpired, Message: "The server certificate expired on " + leaf.NotAfter.UTC().Format(time.DateOnly) + ".", Hint: hint})
	case now.Before(leaf.NotBefore):
		w = append(w, Warning{Code: CodeServerCertNotYet, Message: "The server certificate is not valid until " + leaf.NotBefore.UTC().Format(time.DateOnly) + ".", Hint: hint})
	}
	if len(leaf.ExtKeyUsage) > 0 {
		ok := false
		for _, u := range leaf.ExtKeyUsage {
			if u == x509.ExtKeyUsageServerAuth || u == x509.ExtKeyUsageAny {
				ok = true
			}
		}
		if !ok {
			w = append(w, Warning{Code: CodeServerCertUsage, Message: "The certificate is not issued for server authentication.", Hint: "Most clients reject it for a TLS server."})
		}
	}
	return w
}
