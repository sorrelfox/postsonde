package servertls

import (
	"context"
	"net"
	"net/netip"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
)

// CodeUnreachableAddrs is the warning of a generated certificate that
// leaves out some of the server's addresses (public ones the local CA
// may not sign).
const CodeUnreachableAddrs = "servertls.addresses_left_out"

// PassphraseFunc returns the passphrase stored for a certificate file
// (SecretFile), "" for none.
type PassphraseFunc func(file string) (string, error)

// Provider is the servers.CertificateSource of the app: generated
// certificates from CA, the user's files opened with the passphrases
// Passphrase returns.
type Provider struct {
	CA         *Authority
	Passphrase PassphraseFunc
	// Interfaces lists this computer's addresses, for a server that
	// listens on all of them; nil uses net.InterfaceAddrs.
	Interfaces func() []netip.Addr
	// CheckFiles, when not nil, may refuse the user's certificate files
	// before they are read: the app limits them to the project folder and
	// the allowed directories (PRV-10, fileaccess.not_allowed).
	CheckFiles func(t servers.TLS) error
}

// SecretFile is the file of t a passphrase belongs to: the PKCS#12 file,
// or the PEM key (the certificate file when it holds the key).
func SecretFile(t servers.TLS) string {
	switch {
	case t.PFXFile != "":
		return t.PFXFile
	case t.KeyFile != "":
		return t.KeyFile
	}
	return t.CertFile
}

// Source returns p as a servers.CertificateSource.
func (p *Provider) Source() servers.CertificateSource { return p.Certificate }

// Certificate returns the certificate spec presents (spec.TLS is set).
func (p *Provider) Certificate(_ context.Context, spec servers.Spec) (*servers.ServerCertificate, error) {
	t := spec.TLS
	if t == nil {
		return nil, errs.New(errs.Internal, "The server does not serve TLS.").WithCode(servers.CodeTLSInvalid)
	}
	if t.Certificate == servers.CertGenerated {
		addrs, all := p.hostAddrs(spec.Host)
		cert, skipped, err := p.CA.Issue(addrs)
		if err != nil {
			return nil, err
		}
		out := &servers.ServerCertificate{Certificate: cert, Generated: true}
		// On all interfaces, public addresses (a global IPv6 one) are left
		// out without a word: the server is meant for the others.
		if len(skipped) > 0 && !all {
			out.Warnings = append(out.Warnings, tlsconf.Warning{
				Code:    CodeUnreachableAddrs,
				Message: "The generated certificate does not cover " + describeAddrs(skipped) + ": the local CA signs only localhost and private addresses.",
				Hint:    "Clients connecting to those addresses reject it; use your own certificate for them.",
			})
		}
		return out, nil
	}
	if p.CheckFiles != nil {
		if err := p.CheckFiles(*t); err != nil {
			return nil, err
		}
	}
	pass := ""
	if p.Passphrase != nil {
		var err error
		if pass, err = p.Passphrase(SecretFile(*t)); err != nil {
			return nil, errs.Wrap(err, errs.KindOf(err), "The passphrase of the server certificate could not be read.").
				WithCode(tlsconf.CodePassphraseFailed)
		}
	}
	c, err := tlsconf.LoadServerCert(t.CertFile, t.KeyFile, t.PFXFile, pass)
	if err != nil {
		return nil, err
	}
	return &servers.ServerCertificate{Certificate: c.Certificate, Warnings: c.Warnings}, nil
}

// hostAddrs lists the addresses a server bound to host is reached at,
// besides localhost: the address itself, or every interface address for
// an unspecified one (all is then true).
func (p *Provider) hostAddrs(host string) (addrs []netip.Addr, all bool) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return nil, false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil, false
	}
	if !ip.IsUnspecified() {
		return []netip.Addr{ip}, false
	}
	if p.Interfaces != nil {
		return p.Interfaces(), true
	}
	return interfaceAddrs(), true
}

func interfaceAddrs() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip, ok := netip.AddrFromSlice(n.IP); ok {
			out = append(out, ip.Unmap())
		}
	}
	return out
}
