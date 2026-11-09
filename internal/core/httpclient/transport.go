package httpclient

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Defaults used by NewTransport for zero TransportOptions fields.
const (
	// DefaultMaxIdleConns is the idle connection limit over all hosts.
	DefaultMaxIdleConns = 100
	// DefaultMaxIdleConnsPerHost is the idle connection limit per host.
	// Higher than Go's 2, since runners send many requests to one host.
	DefaultMaxIdleConnsPerHost = 10
	// DefaultIdleConnTimeout closes connections idle for this long.
	DefaultIdleConnTimeout = 90 * time.Second
	// DefaultTLSHandshakeTimeout bounds one TLS handshake.
	DefaultTLSHandshakeTimeout = 10 * time.Second
	// DefaultExpectContinueTimeout is how long a request with
	// "Expect: 100-continue" waits for the server before sending the body.
	DefaultExpectContinueTimeout = 1 * time.Second
)

// TransportOptions configure the connections of a Client. The zero value
// gives the defaults: certificates verified against the system roots, TLS
// 1.2 or newer.
type TransportOptions struct {
	// RootCAs are the certificate authorities trusted for https URLs; nil
	// means the system's (NET-04 adds custom CAs through this).
	RootCAs *x509.CertPool
	// InsecureSkipVerify turns off certificate verification (HTTP-23).
	// Off by default; the UI shows a warning when it is on.
	InsecureSkipVerify bool
	// TLSConfig, when non-nil, replaces the TLS settings built from RootCAs
	// and InsecureSkipVerify (which are then ignored): a clone is used, so
	// client certificates, a server name override and custom roots from
	// tlsconf.Build apply. A zero MinVersion is raised to TLS 1.2.
	TLSConfig *tls.Config
	// MaxIdleConns and MaxIdleConnsPerHost limit kept-alive idle
	// connections. Zero means the defaults above; other values are as for
	// http.Transport.
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	// IdleConnTimeout closes idle connections after this long. Zero means
	// DefaultIdleConnTimeout; a negative value keeps them open.
	IdleConnTimeout time.Duration
	// TLSHandshakeTimeout bounds a TLS handshake. Zero means
	// DefaultTLSHandshakeTimeout; a negative value means no limit.
	TLSHandshakeTimeout time.Duration

	// ClientCertificate is the client certificate (mTLS) every TLS
	// connection of the transport sends when the server asks, also with
	// TLSConfig. Nil sends none (or TLSConfig's).
	ClientCertificate *ClientCert
	// ClientCerts picks the client certificate per host (AUTH-30): a
	// Client routes each https hop, redirects included, to a pool of
	// connections that send the certificate it returns for the hop's
	// host:port, so connections with different certificates are never
	// shared. NewTransport ignores it; ClientCertificate applies when it
	// returns nil.
	ClientCerts ClientCertFunc
}

// ClientCert is a client certificate for mTLS.
type ClientCert struct {
	// Certificate is sent as it is: the leaf, the chain, the key.
	Certificate *tls.Certificate
	// Description names it in the timeline (event client_cert), e.g. its
	// subject and the host pattern that chose it. Never key material.
	Description string
}

// ClientCertFunc returns the client certificate for a connection to addr
// ("host:port", the port always present), or nil for none. An error fails
// the request before it connects; it should be an *errs.Error, which is
// returned as it is. The same *tls.Certificate should come back while the
// certificate is unchanged: each one gets its own connection pool.
type ClientCertFunc func(addr string) (*ClientCert, error)

// NewTransport returns an http.Transport that opens every connection
// through d and changes nothing about the requests it sends:
//
//   - DialContext is d's; DialTLSContext is unset, so TLS runs on top of
//     the dialed connection (end to end through a proxy tunnel).
//   - Proxy is nil: no proxy from HTTP_PROXY and friends. Proxies are
//     dialers (docs/network.md).
//   - DisableCompression is true: no "Accept-Encoding: gzip" is added and
//     bodies are never decompressed behind the user's back.
//   - ForceAttemptHTTP2 offers HTTP/2 through ALPN on TLS connections (a
//     custom DialContext would otherwise turn it off). Plain http:// stays
//     HTTP/1.1.
//   - TLS 1.2 is the minimum; certificates are verified unless
//     InsecureSkipVerify is set. TLSConfig replaces these settings.
//
// It panics if d is nil.
func NewTransport(d netx.Dialer, o TransportOptions) *http.Transport {
	if d == nil {
		panic("httpclient: NewTransport needs a netx.Dialer")
	}
	tc := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		RootCAs:            o.RootCAs,
		InsecureSkipVerify: o.InsecureSkipVerify,
	}
	if o.TLSConfig != nil {
		tc = o.TLSConfig.Clone()
		if tc.MinVersion == 0 {
			tc.MinVersion = tls.VersionTLS12
		}
	}
	switch c := o.ClientCertificate; {
	case c != nil && c.Certificate != nil:
		tc.GetClientCertificate = sendClientCert(c)
	case o.ClientCerts != nil && o.TLSConfig == nil:
		// The pool of hosts without a certificate notes that the server
		// asked for one, for the timeline and the error.
		tc.GetClientCertificate = noClientCert
	}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		TLSClientConfig:       tc,
		TLSHandshakeTimeout:   positiveOr(o.TLSHandshakeTimeout, DefaultTLSHandshakeTimeout),
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          intOr(o.MaxIdleConns, DefaultMaxIdleConns),
		MaxIdleConnsPerHost:   intOr(o.MaxIdleConnsPerHost, DefaultMaxIdleConnsPerHost),
		IdleConnTimeout:       positiveOr(o.IdleConnTimeout, DefaultIdleConnTimeout),
		ExpectContinueTimeout: DefaultExpectContinueTimeout,
	}
}

// EventClientCert is the timeline event of a server asking for a client
// certificate: its detail is ClientCert.Description of the one sent, or
// says that none is set for the host (with ClientCerts).
const EventClientCert = "client_cert"

// sendClientCert returns a GetClientCertificate that always sends c, even
// when its issuer is not among the CAs the server lists (the server's
// verdict is a clearer error than sending none), and records it in the
// timeline of the send whose context the handshake runs in.
func sendClientCert(c *ClientCert) func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
		if st, ok := cri.Context().Value(stateKey{}).(*sendState); ok && st.rec != nil {
			st.certRequested.Store(true)
			st.certSent.Store(true)
			st.rec.record(EventClientCert, c.Description, nil)
		}
		return c.Certificate, nil
	}
}

// noClientCert sends no certificate and records that the server asked.
func noClientCert(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	if st, ok := cri.Context().Value(stateKey{}).(*sendState); ok && st.rec != nil {
		st.certRequested.Store(true)
		st.rec.record(EventClientCert, "requested by the server; none is set for this host", nil)
	}
	return &tls.Certificate{}, nil
}

// positiveOr maps zero to def and a negative value to zero ("none").
func positiveOr(v, def time.Duration) time.Duration {
	switch {
	case v == 0:
		return def
	case v < 0:
		return 0
	}
	return v
}

func intOr(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
