package servers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
)

// Local TLS (SRV-05): a server with Spec.TLS serves TLS on its socket. The
// Manager wraps the listener it opened in a TLS listener before the type
// starts, so every type serves TLS the same way: the HTTP types become
// HTTPS, the WebSocket server wss, the TCP server TLS over TCP. The
// certificate comes from Options.Certificates at every start: one the
// user provides (PEM or PKCS#12 files) or one generated for this computer
// by the local CA (servertls). See docs/servers.md#tls.

// Kinds of TLS.Certificate.
const (
	// CertGenerated: a certificate for localhost and the server's
	// addresses, issued at every start by PostSonde's local CA.
	CertGenerated = "generated"
	// CertFiles: the user's certificate, from CertFile and KeyFile (PEM)
	// or PFXFile (PKCS#12).
	CertFiles = "files"
)

// Error codes of local TLS.
const (
	// CodeTLSInvalid: an unknown kind of certificate.
	CodeTLSInvalid = "servers.tls_invalid"
	// CodeTLSUnavailable: the Manager has no Options.Certificates.
	CodeTLSUnavailable = "servers.tls_unavailable"
	// CodeTLSNotSupported: a kind of server that cannot serve TLS (UDP).
	CodeTLSNotSupported = "servers.tls_not_supported"
)

func tlsNotSupported(label string) *errs.Error {
	return errs.New(errs.InvalidArgument, label+" servers cannot serve TLS.").
		WithCode(CodeTLSNotSupported).
		WithHint("Turn TLS off for this server.").
		WithDetail("field", "tls")
}

// TLS is how a server serves TLS. Paths are absolute; a passphrase is
// never part of it (the certificate source keeps it in the app's local
// secret storage).
type TLS struct {
	// Certificate is CertGenerated or CertFiles.
	Certificate string
	// CertFile and KeyFile (PEM; KeyFile "" when the certificate file
	// holds the key), or PFXFile (PKCS#12), for CertFiles.
	CertFile string
	KeyFile  string
	PFXFile  string
}

// ServerCertificate is the certificate a TLS server presents.
type ServerCertificate struct {
	// Certificate is the leaf, its chain and the key.
	Certificate *tls.Certificate
	// Generated is set for a certificate of the local CA.
	Generated bool
	// Warnings are problems a client may reject it for (expired, not for
	// server authentication); the server serves it anyway.
	Warnings []tlsconf.Warning
}

// CertificateSource returns the certificate the server spec presents
// (spec.TLS is not nil). Errors are shown to the user as the reason the
// server did not start.
type CertificateSource func(ctx context.Context, spec Spec) (*ServerCertificate, error)

// ALPNType is a Type that names the application protocols it speaks over
// TLS (ALPN), such as "http/1.1". A type without it offers none.
type ALPNType interface {
	ALPN() []string
}

// checkTLS normalizes t: paths trimmed, those of another kind dropped.
func checkTLS(t *TLS) (*TLS, error) {
	if t == nil {
		return nil, nil
	}
	out := &TLS{Certificate: strings.TrimSpace(t.Certificate)}
	switch out.Certificate {
	case CertGenerated:
	case CertFiles:
		out.CertFile, out.KeyFile, out.PFXFile = strings.TrimSpace(t.CertFile), strings.TrimSpace(t.KeyFile), strings.TrimSpace(t.PFXFile)
		if err := tlsconf.CheckServerCertFiles(out.CertFile, out.KeyFile, out.PFXFile); err != nil {
			return nil, err
		}
	default:
		msg := "\"" + t.Certificate + "\" is not a kind of TLS certificate PostSonde knows."
		if out.Certificate == "" {
			msg = "The TLS settings do not say which certificate to use."
		}
		return nil, errs.New(errs.InvalidArgument, msg).
			WithCode(CodeTLSInvalid).WithHint("Choose the generated certificate or your own certificate files.").WithDetail("field", "tls.certificate")
	}
	return out, nil
}

func sameTLS(a, b *TLS) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// tlsConfig returns the server configuration for cert: TLS 1.2 and later,
// with the type's application protocols.
func tlsConfig(typ Type, cert *tls.Certificate) *tls.Config {
	c := &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12}
	if a, ok := typ.(ALPNType); ok {
		c.NextProtos = a.ALPN()
	}
	return c
}

// describeCert is the certificate part of the started event: "over TLS
// (generated certificate "localhost", valid until 2027-09-19)".
func describeCert(c *ServerCertificate) string {
	text := "over TLS"
	if leaf := leafOf(c); leaf != nil {
		name := leaf.Subject.CommonName
		if name == "" {
			name = leaf.Subject.String()
		}
		kind := "certificate"
		if c.Generated {
			kind = "generated certificate"
		}
		text += " (" + kind + " \"" + name + "\", valid until " + leaf.NotAfter.UTC().Format(time.DateOnly) + ")"
	}
	for _, w := range c.Warnings {
		text += ". Warning: " + w.Message
	}
	return text
}

func leafOf(c *ServerCertificate) *x509.Certificate {
	if c == nil || c.Certificate == nil {
		return nil
	}
	if c.Certificate.Leaf != nil {
		return c.Certificate.Leaf
	}
	if len(c.Certificate.Certificate) == 0 {
		return nil
	}
	leaf, _ := x509.ParseCertificate(c.Certificate.Certificate[0])
	return leaf
}

// HandshakeSummary describes a failed TLS handshake for the traffic log,
// saying what the usual causes mean.
func HandshakeSummary(msg string) string {
	text := "TLS handshake failed: " + msg
	switch {
	case strings.Contains(msg, "bad certificate"), strings.Contains(msg, "unknown certificate authority"), strings.Contains(msg, "certificate unknown"):
		text += " (the client does not trust the server's certificate)"
	case strings.Contains(msg, "protocol version"):
		text += " (the client and the server have no TLS version in common; the server takes TLS 1.2 and 1.3)"
	case strings.Contains(msg, "no application protocol"):
		text += " (the client asked for another protocol than HTTP/1.1, such as h2 only)"
	}
	return text
}

func tlsUnavailable() *errs.Error {
	return errs.New(errs.Unsupported, "This server cannot serve TLS here.").WithCode(CodeTLSUnavailable)
}
