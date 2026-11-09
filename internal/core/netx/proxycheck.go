package netx

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// The proxy connection test (SOCKS-11): ProxyRouter.Test connects to a
// target the user chooses through the proxy in effect, and says how the
// hop went (ProxyHop) or at which step it failed. See
// docs/network.md#testing-the-proxy.

// Steps of a proxy connection test (ProxyTest.Step): where it failed.
const (
	// ProxyStepSetup: the proxy setting cannot be used (no proxy, an
	// invalid URL, a kind PostSonde cannot dial), or the dial was refused
	// before anything was connected.
	ProxyStepSetup = "setup"
	// ProxyStepDNS: the target's host name could not be resolved here
	// (socks5 and other local-DNS modes).
	ProxyStepDNS = "dns"
	// ProxyStepConnect: the connection to the proxy itself.
	ProxyStepConnect = "connect"
	// ProxyStepTLS: the TLS handshake with an https:// proxy.
	ProxyStepTLS = "tls"
	// ProxyStepHandshake: the proxy protocol: the proxy did not answer in
	// time, or not as a proxy of its kind.
	ProxyStepHandshake = "handshake"
	// ProxyStepAuth: the proxy wants credentials, refused them, or offers
	// no method PostSonde supports.
	ProxyStepAuth = "auth"
	// ProxyStepTarget: the proxy could not, or would not, connect to the
	// target.
	ProxyStepTarget = "target"
	// ProxyStepTimeout: the test ran out of time.
	ProxyStepTimeout = "timeout"
	// ProxyStepCancelled: the test was cancelled.
	ProxyStepCancelled = "cancelled"
)

// Codes of the proxy connection test's own errors.
const (
	// CodeProxyNone: no proxy is in effect, so there is nothing to test
	// (kind invalid_argument).
	CodeProxyNone = "netx.proxy_none"
	// CodeProxyTestTarget: the test's target is not host:port (kind
	// invalid_argument).
	CodeProxyTestTarget = "netx.proxy_test_target"
)

// ProxyTest is the result of a proxy connection test.
type ProxyTest struct {
	// Proxy is the proxy tested, without its password; zero when none is
	// in effect or the setting could not be read.
	Proxy ProxyURL
	// Target is the host:port connected to through it.
	Target string
	// Hop is how the dial through the proxy went, as far as it got; nil
	// when it never reached the proxy dialer (Step setup).
	Hop *ProxyHop
	// Duration is the time of the whole test.
	Duration time.Duration
	// Err is nil when the proxy connected to the target. Otherwise it is
	// the dial's error (an *errs.Error with the proxy dialer's code) and
	// Step says where it failed.
	Err  error
	Step string
	// Bypassed is true when the target would not go through the proxy
	// otherwise: it is loopback or on the bypass list (SOCKS-09). The test
	// connects through the proxy all the same.
	Bypassed bool
}

// OK reports whether the proxy connected to the target.
func (t ProxyTest) OK() bool { return t.Err == nil }

// Test connects to target (host:port) through the proxy in effect and
// closes the connection at once: the connection to the proxy, the TLS
// handshake of an https:// proxy, the proxy's greeting and authentication
// and its connect to the target (SOCKS-11). Unlike Route it never goes
// direct: a loopback target or one on the bypass list goes through the
// proxy too (Bypassed says so), and no proxy is an error
// (CodeProxyNone). The dial goes through the proxy's dialer wrapped in
// mws (the app passes the connection log and the user-action guard), with
// ctx, so its trace and user operation apply.
func (r *ProxyRouter) Test(ctx context.Context, target string, mws ...Middleware) ProxyTest {
	now := r.opts.Now
	if now == nil {
		now = time.Now
	}
	start := now()
	res := ProxyTest{Target: target}
	finish := func(err error, step string) ProxyTest {
		res.Duration = now().Sub(start)
		res.Err, res.Step = err, step
		return res
	}
	if err := checkTestTarget(target); err != nil {
		return finish(err, ProxyStepSetup)
	}
	var c ProxyConfig
	if r.current != nil {
		var err error
		if c, err = r.current(ctx); err != nil {
			return finish(routeError(err, "tcp", target), ProxyStepSetup)
		}
	}
	p := c.Proxy
	if p.IsZero() {
		// Only http:// requests go through a proxy (a system HTTP proxy
		// without an HTTPS one): test that.
		p = c.ForHTTP()
	}
	if p.IsZero() {
		return finish(errs.New(errs.InvalidArgument, "No proxy is in effect: connections go directly, so there is no proxy to test.").
			WithCode(CodeProxyNone).
			WithHint("Choose Manual and enter a proxy URL, or start PostSonde with ALL_PROXY set."), ProxyStepSetup)
	}
	res.Proxy = p.WithoutPassword()
	res.Bypassed = c.bypassed(target)
	d, err := r.dialer(p)
	if err != nil {
		return finish(routeError(err, "tcp", target).WithDetail("via", p.String()), ProxyStepSetup)
	}
	var hop *ProxyHop
	ctx = WithTrace(ctx, &Trace{DialDone: func(i DialDoneInfo) {
		if i.Proxy != nil {
			h := *i.Proxy
			hop = &h
		}
	}})
	conn, err := Chain(d, mws...).DialContext(ctx, "tcp", target)
	res.Hop = hop
	if err != nil {
		return finish(err, ProxyFailureStep(err))
	}
	_ = conn.Close()
	return finish(nil, "")
}

// checkTestTarget checks that target is host:port with a port from 1 to
// 65535.
func checkTestTarget(target string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(target))
	n, perr := strconv.Atoi(port)
	if err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
		return errs.New(errs.InvalidArgument, "Enter the target to connect to as host:port, for example example.com:443.").
			WithCode(CodeProxyTestTarget).WithDetail("field", "target")
	}
	return nil
}

// ProxyFailureStep says at which step of a dial through a proxy err
// happened (one of the ProxyStep* names), from its code and kind.
func ProxyFailureStep(err error) string {
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		return ProxyStepConnect
	}
	switch e.Code {
	case CodeSOCKSProxyUnreachable, CodeHTTPProxyUnreachable:
		return ProxyStepConnect
	case CodeSOCKSAuthRequired, CodeSOCKSAuthUnsupported, CodeSOCKSAuthFailed, CodeSOCKS4IdentdUnreachable, CodeSOCKS4IdentdMismatch,
		CodeHTTPProxyAuthRequired, CodeHTTPProxyAuthFailed, CodeHTTPProxyAuthUnsupported:
		return ProxyStepAuth
	case CodeSOCKSTimeout, CodeHTTPProxyTimeout, CodeSOCKSProtocolError, CodeHTTPProxyProtocolError:
		return ProxyStepHandshake
	case CodeDNSNotFound, CodeDNSFailed:
		return ProxyStepDNS
	case CodeSOCKSInvalidAddress, CodeHTTPProxyInvalidAddress, CodeSOCKSUnsupportedNetwork, CodeHTTPProxyUnsupportedNetwork,
		CodeHTTPProxyForbidden, CodeHTTPProxyConnectFailed, CodeSOCKSUnknownReply, CodeSOCKS4Rejected, CodeSOCKS4IPv6:
		return ProxyStepTarget
	}
	if _, ok := errors.AsType[*SOCKSReplyError](err); ok {
		return ProxyStepTarget
	}
	switch e.Kind {
	case errs.Cancelled:
		return ProxyStepCancelled
	case errs.Timeout:
		return ProxyStepTimeout
	case errs.TLS:
		return ProxyStepTLS
	case errs.PermissionDenied, errs.InvalidArgument, errs.Unsupported, errs.Internal:
		return ProxyStepSetup
	}
	return ProxyStepConnect
}
