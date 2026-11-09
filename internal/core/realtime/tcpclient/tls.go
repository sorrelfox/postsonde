package tcpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"net"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// HintServerNotTLS replaces netx's hint when the server of a TLS
// connection does not answer with TLS.
const HintServerNotTLS = "The server does not use TLS on this port. Turn off TLS for this request, or check the port."

// handshake runs the TLS client handshake over raw, bounded by ctx, and
// returns the TLS connection. raw is closed when it fails.
func (c *Conn) handshake(ctx context.Context, raw net.Conn, host string) (net.Conn, error) {
	t := c.o.TLS
	cfg := &tls.Config{
		ServerName:         t.ServerName,
		RootCAs:            t.RootCAs,
		InsecureSkipVerify: t.InsecureSkipVerify,
		MinVersion:         tls.VersionTLS12,
	}
	if cfg.ServerName == "" {
		// crypto/tls sends no SNI for an IP address, and checks the
		// certificate's IP addresses instead.
		cfg.ServerName = host
	}
	// A client certificate is always sent when the server asks, as the
	// HTTP and WebSocket clients do. The callback runs on this goroutine,
	// before the read goroutine starts.
	cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		c.certRequested = true
		if cc := t.ClientCertificate; cc != nil && cc.Certificate != nil {
			c.certSent = true
			return cc.Certificate, nil
		}
		return &tls.Certificate{}, nil
	}
	tc := tls.Client(raw, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, c.handshakeError(ctx, err)
	}
	cs := tc.ConnectionState()
	c.tls = httpclient.NewTLSInfo(&cs)
	return tc, nil
}

// handshakeError maps a failed TLS handshake: the netx TLS codes (a
// refused client certificate, an untrusted server, a server without TLS,
// netx.tls_handshake_failed for anything else), or netx.dial_timeout and
// netx.dial_cancelled when ctx ended.
func (c *Conn) handshakeError(ctx context.Context, err error) error {
	addr := c.o.Address
	if ctx.Err() != nil || isNetTimeout(err) {
		return netx.WrapDialError(err, "tcp", addr)
	}
	f := netx.FailureNone
	if c.certRequested {
		f = netx.ClassifyClientCert(err, c.certSent)
	}
	if f == netx.FailureNone {
		f = netx.Classify(err)
	}
	if f == netx.FailureNone {
		f = netx.FailureTLSHandshake
	}
	e := netx.FailureError(f, err, addr).WithDetail("network", "tcp").WithDetail("address", addr)
	if f == netx.FailureTLSNotTLS {
		e = e.WithHint(HintServerNotTLS)
	}
	return e
}

func isNetTimeout(err error) bool {
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}
