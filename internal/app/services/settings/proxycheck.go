package settings

import (
	"context"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/opid"
)

// The proxy connection test (SOCKS-11): "Test connection" on the settings
// screen connects to a host:port the user chooses through the proxy in
// effect and shows how it went. The dial is the user's action
// (netx.WithUserOp, kind UserOpProxyTest), goes through the connection
// log, and never falls back to a direct connection.
// See docs/network.md#testing-the-proxy.

// UserOpProxyTest is the user operation kind of a proxy connection test.
const UserOpProxyTest = "settings.proxy_test"

// ProxyTestTimeout bounds one proxy connection test.
const ProxyTestTimeout = 20 * time.Second

// ProxyTester runs a proxy connection test to target (host:port) through
// the proxy in effect; internal/app passes netx.ProxyRouter.Test with the
// connection log and the user-action guard.
type ProxyTester func(ctx context.Context, target string) netx.ProxyTest

// WithProxyTester sets how TestProxy connects.
func WithProxyTester(test ProxyTester) Option {
	return func(s *SettingsService) { s.testProxy = test }
}

// ProxyTestInputDTO is what to test.
type ProxyTestInputDTO struct {
	// Target is the host:port to connect to through the proxy, e.g.
	// "example.com:443".
	Target string `json:"target"`
}

// ProxyTestDTO is the result of a proxy connection test.
type ProxyTestDTO struct {
	// OK is set when the proxy connected to the target.
	OK bool `json:"ok"`
	// Proxy is the proxy tested, without its password; "" when none is in
	// effect.
	Proxy string `json:"proxy"`
	// Target is the host:port connected to.
	Target string `json:"target"`
	// DurationMs is the time of the whole test.
	DurationMs float64 `json:"durationMs"`
	// Step is where the test failed: "setup", "dns", "connect", "tls",
	// "handshake", "auth", "target", "timeout" or "cancelled"; "" on
	// success.
	Step string `json:"step"`
	// Hop is how the dial through the proxy went, as far as it got; null
	// when it never reached the proxy.
	Hop *ProxyHopDTO `json:"hop"`
	// Error is why it failed, null on success.
	Error *apperr.ErrorDTO `json:"error"`
	// Bypassed is true when connections to the target would not use the
	// proxy (loopback, or on the bypass list, SOCKS-09); the test went
	// through the proxy anyway.
	Bypassed bool `json:"bypassed"`
}

// ProxyHopDTO is how a dial went through a proxy (netx.ProxyHop,
// SOCKS-12). It holds no credential.
type ProxyHopDTO struct {
	// Scheme is socks5, socks5h, http or https; Version the protocol
	// spoken with the proxy ("SOCKS5", "HTTP/1.1").
	Scheme  string `json:"scheme"`
	Version string `json:"version"`
	// Proxy is the proxy's host:port, ProxyAddr the address connected to
	// ("" when the connection to it failed).
	Proxy     string `json:"proxy"`
	ProxyAddr string `json:"proxyAddr"`
	// Auth is "none", "username/password" or "basic"; "" when not chosen.
	Auth string `json:"auth"`
	// RemoteDNS is set when the proxy resolves the target's host name.
	RemoteDNS bool `json:"remoteDns"`
	// Reply is the proxy's answer to the connect request (the SOCKS reply
	// code or the CONNECT's HTTP status), null when none came; ReplyText
	// its meaning.
	Reply     *int   `json:"reply"`
	ReplyText string `json:"replyText"`
	// LookupMs, ConnectMs, TLSMs and HandshakeMs are the local lookup of
	// the target, the connection to the proxy, the TLS handshake with an
	// https:// proxy and the proxy protocol; 0 for a step that did not
	// happen.
	LookupMs    float64 `json:"lookupMs"`
	ConnectMs   float64 `json:"connectMs"`
	TLSMs       float64 `json:"tlsMs"`
	HandshakeMs float64 `json:"handshakeMs"`
}

// TestProxy connects to in.Target through the proxy in effect and closes
// the connection (SOCKS-11). A failed test is a result with OK false,
// Step and Error, not an error; errors are only for a call that could not
// run (cancelled, no tester).
func (s *SettingsService) TestProxy(ctx context.Context, in ProxyTestInputDTO) (ProxyTestDTO, error) {
	if err := ctx.Err(); err != nil {
		return ProxyTestDTO{}, errs.Wrap(err, errs.KindOf(err), "The proxy test was interrupted.")
	}
	if s.testProxy == nil {
		return ProxyTestDTO{}, errs.New(errs.Unsupported, "The proxy connection test is not available.")
	}
	target := strings.TrimSpace(in.Target)
	ctx = netx.WithUserOp(ctx, netx.UserOp{Kind: UserOpProxyTest, ID: opid.New()})
	ctx, cancel := context.WithTimeout(ctx, ProxyTestTimeout)
	defer cancel()
	res := s.testProxy(ctx, target)
	dto := ProxyTestDTO{
		OK:         res.OK(),
		Proxy:      res.Proxy.String(),
		Target:     target,
		DurationMs: float64(res.Duration.Microseconds()) / 1000,
		Step:       res.Step,
		Hop:        proxyHopDTO(res.Hop),
		Bypassed:   res.Bypassed,
	}
	if res.Err != nil {
		e := apperr.ToDTO(res.Err)
		dto.Error = &e
		s.logger.Info("proxy test failed", "proxy", dto.Proxy, "target", target, "step", res.Step, "code", e.Code)
	} else {
		s.logger.Info("proxy test succeeded", "proxy", dto.Proxy, "target", target, "ms", dto.DurationMs)
	}
	return dto, nil
}

// proxyHopDTO converts a proxy hop; nil for nil.
func proxyHopDTO(h *netx.ProxyHop) *ProxyHopDTO {
	if h == nil {
		return nil
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	d := &ProxyHopDTO{
		Scheme: h.Scheme, Version: h.Version, Proxy: h.Proxy, ProxyAddr: h.ProxyAddr,
		Auth: h.Auth, RemoteDNS: h.RemoteDNS, ReplyText: h.ReplyText,
		LookupMs: ms(h.Lookup), ConnectMs: ms(h.Connect), TLSMs: ms(h.TLS), HandshakeMs: ms(h.Handshake),
	}
	if h.Replied {
		reply := h.Reply
		d.Reply = &reply
	}
	return d
}
