package netx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// HTTPProxyConfig configures an HTTP proxy dialer (NET-02).
type HTTPProxyConfig struct {
	// Address is the proxy's host:port.
	Address string
	// TLS is set for an https:// proxy: the connection to the proxy itself
	// is TLS, verified against the system roots plus RootCAs, before any
	// CONNECT or request is sent through it.
	TLS bool
	// ServerName is the name the proxy's certificate must be valid for;
	// "" means the host of Address.
	ServerName string
	// Username and Password are sent as Basic credentials
	// (Proxy-Authorization) with every CONNECT and, through
	// ForwardURL, every absolute-form request. The user name cannot
	// contain a colon; a Password needs a Username.
	Username string
	Password string
	// Timeout bounds the TLS handshake with an https:// proxy and the
	// CONNECT exchange, after the connection to the proxy is open; the
	// forward dialer bounds that connection itself. Zero means
	// DefaultTimeout; a negative value means no limit besides ctx.
	Timeout time.Duration
	// RootCAs returns the certificate authorities an https:// proxy's
	// certificate is verified against (tlsconf.Trust.Roots in the app:
	// the system's plus the custom CA bundles); nil, or a nil pool, means
	// the system's. An error fails the dial.
	RootCAs func() (*x509.CertPool, error)
	// Now is the clock for DialDoneInfo.Duration; nil means time.Now.
	Now func() time.Time
}

// HTTPProxy dials through an HTTP or HTTPS proxy (NET-02). DialContext
// opens a CONNECT tunnel to the target, for TLS and every other protocol;
// DialForward opens a plain connection to the proxy for HTTP requests in
// absolute form (httpclient uses it for http:// URLs, see ForwardURL).
// Credentials go as Basic Proxy-Authorization. It reaches the proxy
// through the forward Dialer it was built with (normally Direct), below
// the guard and the connection log, like SOCKS5 (docs/network.md). It is
// safe for concurrent use.
type HTTPProxy struct {
	cfg     HTTPProxyConfig
	forward Dialer
	via     string
	auth    string
	timeout time.Duration
	now     func() time.Time
}

var _ Dialer = (*HTTPProxy)(nil)

// Codes of the HTTP proxy dialer's errors. See docs/network.md.
const (
	CodeHTTPProxyInvalidConfig      = "netx.http_proxy_invalid_config"
	CodeHTTPProxyInvalidAddress     = "netx.http_proxy_invalid_address"
	CodeHTTPProxyUnsupportedNetwork = "netx.http_proxy_unsupported_network"
	CodeHTTPProxyUnreachable        = "netx.http_proxy_unreachable"
	CodeHTTPProxyTimeout            = "netx.http_proxy_timeout"
	CodeHTTPProxyProtocolError      = "netx.http_proxy_protocol_error"
	// CodeHTTPProxyAuthRequired: the proxy answered 407 and PostSonde
	// had no credentials to send.
	CodeHTTPProxyAuthRequired = "netx.http_proxy_auth_required"
	// CodeHTTPProxyAuthFailed: the proxy answered 407 to the credentials
	// sent.
	CodeHTTPProxyAuthFailed = "netx.http_proxy_auth_failed"
	// CodeHTTPProxyAuthUnsupported: the proxy answered 407 and offers
	// none of the schemes PostSonde supports (Basic).
	CodeHTTPProxyAuthUnsupported = "netx.http_proxy_auth_unsupported"
	// CodeHTTPProxyForbidden: the proxy answered the CONNECT with 403.
	CodeHTTPProxyForbidden = "netx.http_proxy_forbidden"
	// CodeHTTPProxyConnectFailed: the proxy answered the CONNECT with
	// another status than 2xx, 403 or 407 (502, 503, 504...).
	CodeHTTPProxyConnectFailed = "netx.http_proxy_connect_failed"
)

// Hints of the HTTP proxy dialer's errors that do not depend on the dial.
const (
	HintHTTPProxyUnreachable     = "Check the proxy address and port, and that the proxy is running."
	HintHTTPProxyTimeout         = "Check that the proxy is a working HTTP proxy, or increase the timeout."
	HintHTTPProxyProtocol        = "Check that the proxy speaks HTTP on this port (not SOCKS), and whether it is an http:// or https:// proxy."
	HintHTTPProxyAuthRequired    = "Put the user name into the proxy URL (http://user@host:port) and set the proxy password under Settings, Network, Proxy, or add both to the ALL_PROXY variable."
	HintHTTPProxyAuthFailed      = "Check the proxy user name and password under Settings, Network, Proxy (or in ALL_PROXY)."
	HintHTTPProxyAuthUnsupported = "PostSonde supports Basic proxy authentication only. Check the proxy's authentication settings, or use another proxy."
	HintHTTPProxyForbidden       = "The proxy's rules block this target or port. Check the proxy's configuration, or connect without the proxy."
	HintHTTPProxyConnectFailed   = "The proxy could not connect to the target. Check that the target is up and reachable from the proxy, and look at the proxy's logs."
	HintHTTPProxyUnknownCA       = "If you trust this proxy, add the CA certificate that signed its certificate in Settings, Network, Custom CA certificates."
	HintHTTPProxyNotTLS          = "The proxy does not use TLS on this port. Use an http:// proxy URL, or check the port."
)

// maxHTTPProxyHead bounds the response head of a CONNECT.
const maxHTTPProxyHead = 64 << 10

// NewHTTPProxy returns a dialer that goes through the HTTP proxy in cfg,
// reaching the proxy through forward (normally Direct; nil means
// NewDirect(Options{})). An invalid cfg is an invalid_argument error with
// the code netx.http_proxy_invalid_config.
func NewHTTPProxy(cfg HTTPProxyConfig, forward Dialer) (*HTTPProxy, error) {
	invalid := func(msg string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeHTTPProxyInvalidConfig)
	}
	host, port, err := net.SplitHostPort(cfg.Address)
	if err != nil || host == "" || port == "" {
		return nil, invalid("The HTTP proxy address must be host:port.")
	}
	switch {
	case cfg.Password != "" && cfg.Username == "":
		return nil, invalid("An HTTP proxy password needs a user name.")
	case strings.Contains(cfg.Username, ":"):
		return nil, invalid("An HTTP proxy user name cannot contain a colon (Basic authentication).")
	case strings.ContainsFunc(cfg.Username+cfg.Password, isControl):
		return nil, invalid("The HTTP proxy user name or password contains a control character.")
	}
	if forward == nil {
		forward = NewDirect(Options{})
	}
	if cfg.ServerName == "" {
		cfg.ServerName = host
	}
	scheme := ProxyHTTP
	if cfg.TLS {
		scheme = ProxyHTTPS
	}
	p := &HTTPProxy{
		cfg:     cfg,
		forward: forward,
		via:     scheme + "://" + cfg.Address,
		timeout: positiveOr(cfg.Timeout, DefaultTimeout),
		now:     cfg.Now,
	}
	if cfg.Username != "" {
		p.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(cfg.Username+":"+cfg.Password))
	}
	if p.now == nil {
		p.now = time.Now
	}
	return p, nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// Via names the proxy as "http://host:port" or "https://host:port",
// without credentials. Trace events, errors (detail "via") and the
// connections it returns (ViaOf) carry it.
func (p *HTTPProxy) Via() string { return p.via }

// AuthorizationHeader returns the Proxy-Authorization value sent to the
// proxy ("Basic …"), "" without credentials. It is a secret: callers use
// it only to recognise the header, never to show or log it.
func (p *HTTPProxy) AuthorizationHeader() string { return p.auth }

// ForwardURL returns what http.Transport.Proxy should return for an
// absolute-form request to address (host:port) through p: an http:// URL
// whose host is address itself, so the transport dials the target's
// address (the guard and the connection log see the target; a dial whose
// ctx carries ForwardContext(ctx, p) is connected to the proxy by
// ProxyRouter), and whose user info holds p's credentials, which the
// transport sends as Proxy-Authorization. The URL holds the password: it
// must never be shown or logged.
func (p *HTTPProxy) ForwardURL(address string) *url.URL {
	u := &url.URL{Scheme: "http", Host: address}
	if p.cfg.Username != "" {
		u.User = url.UserPassword(p.cfg.Username, p.cfg.Password)
	}
	return u
}

// DialContext connects to address through a CONNECT tunnel. Only "tcp",
// "tcp4" and "tcp6" are supported; host names go to the proxy
// unresolved. The returned connection's RemoteAddr is the proxy's; ViaOf
// reports the proxy.
func (p *HTTPProxy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return p.dial(ctx, network, address, true)
}

// DialForward connects to the proxy itself, for HTTP requests to address
// written in absolute form (no CONNECT). The trace and the returned
// connection report the dial as DialContext does.
func (p *HTTPProxy) DialForward(ctx context.Context, network, address string) (net.Conn, error) {
	return p.dial(ctx, network, address, false)
}

func (p *HTTPProxy) dial(ctx context.Context, network, address string, tunnel bool) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, p.finish(WrapDialError(err, network, address))
	}
	tr := ContextTrace(ctx)
	tr.dialStart(DialStartInfo{Network: network, Address: address, Via: p.via})
	start := p.now()
	hop := &ProxyHop{
		Scheme:    ProxyHTTP,
		Version:   "HTTP/1.1",
		Proxy:     p.cfg.Address,
		Target:    address,
		RemoteDNS: true,
		Forward:   !tunnel,
	}
	if p.cfg.TLS {
		hop.Scheme = ProxyHTTPS
	}
	conn, err := p.open(ctx, network, address, tunnel, hop)
	info := DialDoneInfo{Network: network, Address: address, Via: p.via, Proxy: hop}
	if err != nil {
		err = p.finish(err)
		info.Err = err
		info.Duration = p.now().Sub(start)
		tr.dialDone(info)
		return nil, err
	}
	info.Duration = p.now().Sub(start)
	info.RemoteAddr = conn.RemoteAddr()
	info.LocalAddr = conn.LocalAddr()
	tr.dialDone(info)
	return proxied(conn, p.via), nil
}

// finish adds the via detail to a dial error.
func (p *HTTPProxy) finish(err error) error {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.WithDetail("via", p.via)
	}
	return err
}

func (p *HTTPProxy) open(ctx context.Context, network, address string, tunnel bool, hop *ProxyHop) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, withAddr(errs.New(errs.Unsupported, "An HTTP proxy cannot carry "+network+" connections.").
			WithCode(CodeHTTPProxyUnsupportedNetwork), network, address)
	}
	target, terr := connectTarget(ctx, address)
	if terr != nil {
		return nil, withAddr(terr, network, address)
	}
	var roots *x509.CertPool
	if p.cfg.TLS && p.cfg.RootCAs != nil {
		var err error
		if roots, err = p.cfg.RootCAs(); err != nil {
			if e, ok := errors.AsType[*errs.Error](err); ok {
				return nil, withAddr(e, network, address)
			}
			return nil, withAddr(errs.Wrap(err, errs.TLS, "The CA certificates for the HTTPS proxy at "+p.cfg.Address+" could not be read."), network, address)
		}
	}
	at := p.now()
	conn, err := p.forward.DialContext(ctx, "tcp", p.cfg.Address)
	hop.Connect = p.now().Sub(at)
	if err != nil {
		return nil, p.proxyDialError(err, network, address)
	}
	hop.ProxyAddr = conn.RemoteAddr().String()
	hop.Auth = AuthNone
	if p.auth != "" {
		hop.Auth = AuthBasic // sent with the CONNECT, or with each absolute-form request
	}
	h := &httpProxyHandshake{p: p, ctx: ctx, network: network, address: address, hop: hop}
	c, err := h.run(conn, roots, tunnel, target)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// connectTarget checks address and returns it as the CONNECT
// request-target: host:port with a numeric port, an IPv6 address in
// brackets and without a zone.
func connectTarget(ctx context.Context, address string) (string, *errs.Error) {
	invalid := func(msg string) (string, *errs.Error) {
		return "", errs.New(errs.InvalidArgument, msg).WithCode(CodeHTTPProxyInvalidAddress)
	}
	host, portStr, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return invalid(address + " is not a host:port address.")
	}
	port, err := net.DefaultResolver.LookupPort(ctx, "tcp", portStr)
	if err != nil || port < 1 || port > 65535 {
		return invalid("The port in " + address + " is not valid.")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.WithZone("").Unmap().String()
	} else if len(host) > 255 || strings.ContainsFunc(host, func(r rune) bool {
		return r <= ' ' || r == 0x7f || strings.ContainsRune("/?#@[]\\\"", r)
	}) {
		return invalid("The host name in " + address + " cannot be sent to an HTTP proxy.")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// proxyDialError maps a failure to reach the proxy. Cancellations and
// refusals (a guard) pass through; anything else names the proxy.
func (p *HTTPProxy) proxyDialError(err error, network, address string) error {
	kind := errs.KindOf(err)
	if kind == errs.Cancelled || kind == errs.PermissionDenied {
		return err
	}
	if kind != errs.Timeout {
		kind = errs.Network
	}
	return withAddr(errs.Wrap(err, kind, "Could not connect to the HTTP proxy at "+p.cfg.Address+".").
		WithCode(CodeHTTPProxyUnreachable).
		WithHint(HintHTTPProxyUnreachable), network, address)
}

// httpProxyHandshake is one TLS handshake with the proxy and CONNECT
// exchange, for error mapping.
type httpProxyHandshake struct {
	p       *HTTPProxy
	ctx     context.Context
	network string
	address string
	step    string
	hop     *ProxyHop
}

// run does the TLS handshake with an https:// proxy and, for a tunnel,
// the CONNECT exchange, bounded by the proxy's timeout and ctx. It
// returns the connection to use: conn, a TLS client over it, and either
// one with the bytes the proxy sent after its CONNECT answer in front.
func (h *httpProxyHandshake) run(conn net.Conn, roots *x509.CertPool, tunnel bool, target string) (c net.Conn, err error) {
	if h.p.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(h.p.timeout))
	}
	stop := context.AfterFunc(h.ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer func() {
		if !stop() && err == nil {
			// ctx ended just as the handshake finished: the deadline may
			// already be in the past, so the connection is unusable.
			err = WrapDialError(h.ctx.Err(), h.network, h.address)
		}
		if err == nil {
			_ = conn.SetDeadline(time.Time{})
		}
	}()
	c = conn
	if h.p.cfg.TLS {
		h.step = "TLS handshake"
		at := h.p.now()
		defer func() { h.hop.TLS = h.p.now().Sub(at) - h.hop.Handshake }()
		tc := tls.Client(conn, &tls.Config{
			ServerName: h.p.cfg.ServerName,
			RootCAs:    roots,
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
		})
		if err := tc.Handshake(); err != nil {
			return nil, h.tlsError(err)
		}
		c = tc
	}
	if !tunnel {
		return c, nil
	}
	at := h.p.now()
	defer func() { h.hop.Handshake = h.p.now().Sub(at) }()
	return h.connect(c, target)
}

// connect sends CONNECT target and reads the answer's head. Bytes the
// proxy sent after it (the target speaking first) are kept.
func (h *httpProxyHandshake) connect(conn net.Conn, target string) (net.Conn, error) {
	h.step = "CONNECT"
	var b strings.Builder
	b.WriteString("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n")
	if h.p.auth != "" {
		b.WriteString("Proxy-Authorization: " + h.p.auth + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return nil, h.ioError(err)
	}
	lr := &io.LimitedReader{R: conn, N: maxHTTPProxyHead}
	br := bufio.NewReader(lr)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		if lr.N <= 0 {
			return nil, h.protocolError("The HTTP proxy at "+h.p.cfg.Address+" sent a CONNECT answer longer than 64 KiB.", err)
		}
		return nil, h.ioError(err)
	}
	h.hop.Replied, h.hop.Reply, h.hop.ReplyText = true, resp.StatusCode, shortStatus(resp.StatusCode, resp.Status)
	if resp.StatusCode/100 != 2 {
		return nil, h.p.statusError(resp.StatusCode, resp.Status, resp.Header, h.network, h.address)
	}
	if n := br.Buffered(); n > 0 {
		rest, _ := br.Peek(n)
		return &prefixConn{Conn: conn, prefix: bytes.Clone(rest)}, nil
	}
	return conn, nil
}

// tlsError maps a failed TLS handshake with an https:// proxy.
func (h *httpProxyHandshake) tlsError(err error) error {
	if cerr := h.ctx.Err(); cerr != nil {
		return WrapDialError(cerr, h.network, h.address)
	}
	addr := h.p.cfg.Address
	switch f := Classify(err); {
	case errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err):
		return h.ioError(err)
	case f == FailureTLSNotTLS:
		return h.fail(errs.TLS, CodeTLSServerNotTLS, "The proxy at "+addr+" did not answer with TLS.", HintHTTPProxyNotTLS, err)
	case f == FailureReset, errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return h.protocolError("The HTTPS proxy at "+addr+" closed the connection during the TLS handshake.", err)
	case f.IsTLS():
		e := ConnError(err, "the HTTPS proxy at "+addr)
		if e == nil {
			break
		}
		if f == FailureTLSUnknownAuthority {
			e = e.WithHint(HintHTTPProxyUnknownCA)
		}
		return withAddr(e, h.network, h.address)
	}
	return h.fail(errs.TLS, CodeTLSHandshakeFailed, "The secure connection (TLS) to the HTTPS proxy at "+addr+" failed.", HintTLSHandshake, err)
}

// ioError maps a read or write failure during the exchange: ctx ended,
// the proxy took too long, or it closed or broke the connection.
func (h *httpProxyHandshake) ioError(err error) error {
	if cerr := h.ctx.Err(); cerr != nil {
		return WrapDialError(cerr, h.network, h.address)
	}
	addr := h.p.cfg.Address
	if errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err) {
		return h.fail(errs.Timeout, CodeHTTPProxyTimeout,
			"The HTTP proxy at "+addr+" did not answer in time ("+h.step+").", HintHTTPProxyTimeout, err)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || IsReset(err) {
		return h.protocolError("The HTTP proxy at "+addr+" closed the connection during the "+h.step+".", err)
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return h.fail(errs.Network, CodeHTTPProxyProtocolError,
			"The connection to the HTTP proxy at "+addr+" failed during the "+h.step+".", HintHTTPProxyUnreachable, err)
	}
	// A malformed status line or header: not an HTTP proxy.
	return h.protocolError("The proxy at "+addr+" did not answer the CONNECT as an HTTP proxy.", err)
}

func (h *httpProxyHandshake) protocolError(msg string, cause error) error {
	return h.fail(errs.Protocol, CodeHTTPProxyProtocolError, msg, HintHTTPProxyProtocol, cause)
}

func (h *httpProxyHandshake) fail(kind errs.Kind, code, msg, hint string, cause error) error {
	e := errs.Wrap(cause, kind, msg).WithCode(code)
	if hint != "" {
		e = e.WithHint(hint)
	}
	return withAddr(e, h.network, h.address)
}

// statusError maps a CONNECT answer that is not 2xx.
func (p *HTTPProxy) statusError(code int, status string, header http.Header, network, address string) *errs.Error {
	if code == http.StatusProxyAuthRequired {
		return p.AuthError(header, network, address)
	}
	status = shortStatus(code, status)
	var e *errs.Error
	if code == http.StatusForbidden {
		e = errs.New(errs.Network, "The HTTP proxy at "+p.cfg.Address+" does not allow connections to "+address+" ("+status+").").
			WithCode(CodeHTTPProxyForbidden).WithHint(HintHTTPProxyForbidden)
	} else {
		e = errs.New(errs.Network, "The HTTP proxy at "+p.cfg.Address+" could not connect to "+address+" ("+status+").").
			WithCode(CodeHTTPProxyConnectFailed).WithHint(HintHTTPProxyConnectFailed)
	}
	return withAddr(e.WithDetail("httpStatus", strconv.Itoa(code)), network, address).WithDetail("via", p.via)
}

// shortStatus is the status line for a message: the code and at most 64
// printable characters of the reason the proxy gave.
func shortStatus(code int, status string) string {
	status = strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, status)
	if len(status) > 64 {
		status = status[:64]
	}
	if status == "" {
		status = strconv.Itoa(code)
	}
	return status
}

// AuthError is the error of a 407 answer from the proxy, to a CONNECT or
// to an absolute-form request to address, given the answer's header:
// CodeHTTPProxyAuthUnsupported when the proxy offers no Basic
// authentication, CodeHTTPProxyAuthRequired when PostSonde had no
// credentials to send, CodeHTTPProxyAuthFailed when the proxy refused
// them. Its details are network, address, via, httpStatus 407 and
// proxyAuthenticate, the schemes the proxy offers ("Basic, NTLM"); it
// never holds a credential.
func (p *HTTPProxy) AuthError(header http.Header, network, address string) *errs.Error {
	schemes := challengeSchemes(header.Values("Proxy-Authenticate"))
	basic := len(schemes) == 0
	for _, s := range schemes {
		if strings.EqualFold(s, "Basic") {
			basic = true
		}
	}
	addr := p.cfg.Address
	var e *errs.Error
	switch {
	case !basic:
		e = errs.New(errs.Network, "The HTTP proxy at "+addr+" asks for an authentication PostSonde does not support ("+strings.Join(schemes, ", ")+").").
			WithCode(CodeHTTPProxyAuthUnsupported).WithHint(HintHTTPProxyAuthUnsupported)
	case p.auth == "":
		e = errs.New(errs.Network, "The HTTP proxy at "+addr+" requires a user name and password.").
			WithCode(CodeHTTPProxyAuthRequired).WithHint(HintHTTPProxyAuthRequired)
	default:
		e = errs.New(errs.Network, "The HTTP proxy at "+addr+" rejected the user name or password.").
			WithCode(CodeHTTPProxyAuthFailed).WithHint(HintHTTPProxyAuthFailed)
	}
	e = e.WithDetail("httpStatus", strconv.Itoa(http.StatusProxyAuthRequired))
	if len(schemes) > 0 {
		e = e.WithDetail("proxyAuthenticate", strings.Join(schemes, ", "))
	}
	return withAddr(e, network, address).WithDetail("via", p.via)
}

// challengeSchemes returns the auth schemes of Proxy-Authenticate field
// values (RFC 9110 section 11.6.1), without their parameters: a list
// item whose first word has no "=" starts a challenge.
func challengeSchemes(values []string) []string {
	var out []string
	for _, v := range values {
		for _, item := range splitQuoted(v) {
			item = strings.TrimSpace(item)
			word, _, _ := strings.Cut(item, " ")
			if word == "" || strings.Contains(word, "=") || strings.ContainsFunc(word, isControl) {
				continue
			}
			if len(word) > 32 {
				word = word[:32]
			}
			out = append(out, word)
		}
	}
	return out
}

// splitQuoted splits s at commas outside quoted strings.
func splitQuoted(s string) []string {
	var out []string
	start, quoted, escaped := 0, false, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case escaped:
			escaped = false
		case quoted && c == '\\':
			escaped = true
		case c == '"':
			quoted = !quoted
		case c == ',' && !quoted:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// prefixConn returns prefix before what it reads from Conn: the bytes a
// proxy sent right after its CONNECT answer.
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixConn) Read(b []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(b, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(b)
}

// Unwrap returns the connection to the proxy.
func (c *prefixConn) Unwrap() net.Conn { return c.Conn }

// forwardKey carries the HTTP proxy of an absolute-form request's dial.
type forwardKey struct{}

// ForwardContext returns a copy of ctx that tells ProxyRouter to connect
// the dial to p itself (DialForward) instead of routing it: the
// connection of an HTTP request that will be written in absolute form to
// that proxy. httpclient sets it on the dials of its forward-proxy pool,
// after asking ForwardProxier.ForwardProxy.
func ForwardContext(ctx context.Context, p *HTTPProxy) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, forwardKey{}, p)
}

func forwardFrom(ctx context.Context) *HTTPProxy {
	p, _ := ctx.Value(forwardKey{}).(*HTTPProxy)
	return p
}

// ForwardProxier is a Dialer that also says which HTTP proxy plain HTTP
// requests to an address go to in absolute form (NET-02). The app's
// dialer is one (NewForwardProxier over ProxyRouter.ForwardProxy);
// httpclient.New looks for it.
type ForwardProxier interface {
	Dialer
	// ForwardProxy returns the HTTP proxy that an http:// request to
	// address (host:port) in ctx goes to in absolute form, or nil when it
	// goes directly or through a tunnel (SOCKS, a loopback target, an
	// invalid proxy, which then fails the dial).
	ForwardProxy(ctx context.Context, address string) *HTTPProxy
}

// NewForwardProxier returns d with lookup as its ForwardProxy.
func NewForwardProxier(d Dialer, lookup func(ctx context.Context, address string) *HTTPProxy) ForwardProxier {
	return forwardProxier{Dialer: d, lookup: lookup}
}

type forwardProxier struct {
	Dialer
	lookup func(ctx context.Context, address string) *HTTPProxy
}

func (f forwardProxier) ForwardProxy(ctx context.Context, address string) *HTTPProxy {
	if f.lookup == nil {
		return nil
	}
	return f.lookup(ctx, address)
}
