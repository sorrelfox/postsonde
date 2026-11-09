package netx

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Proxy URL schemes (SOCKS-04): socks5, socks5h, socks4 and socks4a
// (SOCKS-06), http and https (NET-02).
const (
	// ProxySOCKS5 resolves host names locally and sends the proxy an IP
	// address.
	ProxySOCKS5 = "socks5"
	// ProxySOCKS5H sends host names to the proxy unresolved (remote DNS).
	ProxySOCKS5H = "socks5h"
	// ProxySOCKS4 resolves host names locally to an IPv4 address.
	ProxySOCKS4 = "socks4"
	// ProxySOCKS4A sends host names to the proxy unresolved.
	ProxySOCKS4A = "socks4a"
	// ProxyHTTP is an HTTP proxy: CONNECT tunnels, and absolute-form
	// requests for http:// URLs.
	ProxyHTTP = "http"
	// ProxyHTTPS is an HTTP proxy reached over TLS.
	ProxyHTTPS = "https"
)

// ProxySchemes lists the schemes ParseProxyURL accepts.
var ProxySchemes = []string{ProxySOCKS5, ProxySOCKS5H, ProxySOCKS4, ProxySOCKS4A, ProxyHTTP, ProxyHTTPS}

// Default ports of a proxy URL without one, as curl uses them.
const (
	DefaultSOCKSPort      = 1080
	DefaultHTTPProxyPort  = 1080
	DefaultHTTPSProxyPort = 443
)

// MaxProxyCredentialLen bounds a SOCKS proxy's username and password, in
// bytes (RFC 1929 sends each with a one-byte length).
const MaxProxyCredentialLen = 255

// Error codes of proxy URLs and the proxy route. See docs/network.md.
const (
	// CodeProxyInvalidURL: a proxy URL that cannot be used (kind
	// invalid_argument). Messages never contain the URL's password.
	CodeProxyInvalidURL = "netx.proxy_invalid_url"
	// CodeProxyUnsupported: a proxy of a kind PostSonde cannot dial (kind
	// unsupported). Every scheme ParseProxyURL accepts is dialed, so only
	// a ProxyURL built by hand gets it. Details: scheme.
	CodeProxyUnsupported = "netx.proxy_unsupported"
)

// HintProxyInvalidURL is the hint of CodeProxyInvalidURL errors.
const HintProxyInvalidURL = "Write the proxy as scheme://[user[:password]@]host[:port], for example socks5h://proxy.example.com:1080 or http://proxy.example.com:3128. Percent-encode special characters in the user name and password."

// ProxyURL is a parsed proxy URL (SOCKS-04):
//
//	socks5h://user:password@proxy.example.com:1080
//
// It is comparable, so it can key a cache. String never shows Password.
type ProxyURL struct {
	// Scheme is one of ProxySchemes, lower-case.
	Scheme string
	// Host is the proxy's host name or IP address, without brackets.
	Host string
	// Port is the proxy's port; the scheme's default when the URL has none.
	Port int
	// Username and Password are the credentials of the URL's user info,
	// percent-decoded. A Password needs a Username.
	Username string
	Password string
}

// IsZero reports whether u is the zero ProxyURL: no proxy.
func (u ProxyURL) IsZero() bool { return u == ProxyURL{} }

// Address returns the proxy's host:port.
func (u ProxyURL) Address() string {
	return net.JoinHostPort(u.Host, strconv.Itoa(u.Port))
}

// RemoteDNS reports whether target host names go to the proxy unresolved:
// socks5h and socks4a, and HTTP proxies, which always get the name.
func (u ProxyURL) RemoteDNS() bool {
	return u.Scheme != ProxySOCKS5 && u.Scheme != ProxySOCKS4
}

// WithoutPassword returns u with the password removed.
func (u ProxyURL) WithoutPassword() ProxyURL {
	u.Password = ""
	return u
}

// String returns the URL without its password, the port always written:
// socks5h://user@proxy.example.com:1080. It is what the settings file
// keeps and what logs and the UI show. The zero ProxyURL is "".
func (u ProxyURL) String() string {
	if u.IsZero() {
		return ""
	}
	v := url.URL{Scheme: u.Scheme, Host: u.Address()}
	if u.Username != "" {
		v.User = url.User(u.Username)
	}
	return v.String()
}

// IsHTTP reports whether u is an HTTP proxy (http:// or https://).
func (u ProxyURL) IsHTTP() bool { return u.Scheme == ProxyHTTP || u.Scheme == ProxyHTTPS }

// IsSOCKS4 reports whether u is a SOCKS4 or SOCKS4a proxy.
func (u ProxyURL) IsSOCKS4() bool { return u.Scheme == ProxySOCKS4 || u.Scheme == ProxySOCKS4A }

// CheckSupported returns nil when PostSonde can dial through u, and a
// CodeProxyUnsupported error for a scheme it does not know.
func (u ProxyURL) CheckSupported() error {
	switch u.Scheme {
	case ProxySOCKS5, ProxySOCKS5H, ProxySOCKS4, ProxySOCKS4A, ProxyHTTP, ProxyHTTPS:
		return nil
	}
	return errs.New(errs.Unsupported, "This kind of proxy is not supported.").
		WithCode(CodeProxyUnsupported).
		WithHint("Use a SOCKS proxy (socks5h://, socks5://, socks4a:// or socks4://) or an HTTP proxy (http:// or https://), or choose No proxy under Settings, Network, Proxy.").
		WithDetail("scheme", u.Scheme)
}

// ProxyOptions are the settings of the dialers ProxyURL.Dialer builds.
type ProxyOptions struct {
	// Timeout bounds a proxy's handshake; see SOCKS5Config.Timeout and
	// HTTPProxyConfig.Timeout.
	Timeout time.Duration
	// RootCAs are the CAs an https:// proxy's certificate is verified
	// against; see HTTPProxyConfig.RootCAs.
	RootCAs func() (*x509.CertPool, error)
	// Resolver resolves target host names locally (socks5); nil means
	// net.DefaultResolver.
	Resolver *net.Resolver
	// Now is the clock for trace durations; nil means time.Now.
	Now func() time.Time
}

// Dialer returns a dialer that tunnels through the proxy u, reaching the
// proxy through forward (nil means NewDirect(Options{})): a SOCKS5,
// SOCKS4 or HTTPProxy. An unknown scheme fails with CodeProxyUnsupported.
func (u ProxyURL) Dialer(forward Dialer, o ProxyOptions) (Dialer, error) {
	if err := u.CheckSupported(); err != nil {
		return nil, err
	}
	if u.IsHTTP() {
		return NewHTTPProxy(HTTPProxyConfig{
			Address:  u.Address(),
			TLS:      u.Scheme == ProxyHTTPS,
			Username: u.Username,
			Password: u.Password,
			Timeout:  o.Timeout,
			RootCAs:  o.RootCAs,
			Now:      o.Now,
		}, forward)
	}
	if u.IsSOCKS4() {
		return NewSOCKS4(SOCKS4Config{
			Address:   u.Address(),
			RemoteDNS: u.RemoteDNS(),
			UserID:    u.Username,
			Timeout:   o.Timeout,
			Resolver:  o.Resolver,
			Now:       o.Now,
		}, forward)
	}
	return NewSOCKS5(SOCKS5Config{
		Address:   u.Address(),
		RemoteDNS: u.RemoteDNS(),
		Username:  u.Username,
		Password:  u.Password,
		Timeout:   o.Timeout,
		Resolver:  o.Resolver,
		Now:       o.Now,
	}, forward)
}

// ParseProxyURL parses a proxy URL (SOCKS-04):
//
//	scheme://[user[:password]@]host[:port]
//
// scheme is one of ProxySchemes, in any case. host is a name, an IPv4
// address or an IPv6 address in brackets; port defaults to 1080 (443 for
// https). The user info is percent-decoded; a password needs a user name,
// SOCKS credentials are at most 255 bytes each and a SOCKS4 URL takes a
// user ID but no password, and an HTTP proxy's user name cannot contain a
// colon (Basic authentication). A path of "/" is allowed; any other path, a
// query or a fragment is not. Spaces around the URL are ignored.
//
// Errors are invalid_argument with CodeProxyInvalidURL; their messages
// never contain the password, or the URL, which may hold it.
func ParseProxyURL(raw string) (ProxyURL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ProxyURL{}, invalidProxyURL("The proxy URL is empty.")
	}
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok || scheme == "" {
		return ProxyURL{}, invalidProxyURL("The proxy URL has no scheme; start it with socks5h://, socks5://, socks4a://, socks4://, http:// or https://.")
	}
	scheme = strings.ToLower(scheme)
	if !knownProxyScheme(scheme) {
		return ProxyURL{}, invalidProxyURL("The proxy URL's scheme is not one PostSonde knows; use socks5h://, socks5://, socks4a://, socks4://, http:// or https://.")
	}
	// url.Parse's own errors quote the whole URL, password included, so
	// they are never passed on.
	v, err := url.Parse(scheme + "://" + rest)
	if err != nil {
		return ProxyURL{}, invalidProxyURL("The proxy URL is not a valid URL.")
	}
	if (v.Path != "" && v.Path != "/") || v.RawQuery != "" || v.ForceQuery || v.Fragment != "" || strings.Contains(rest, "#") {
		return ProxyURL{}, invalidProxyURL("A proxy URL has no path, query or fragment, only scheme://[user[:password]@]host[:port].")
	}
	u := ProxyURL{Scheme: scheme, Host: v.Hostname()}
	if u.Host == "" {
		return ProxyURL{}, invalidProxyURL("The proxy URL has no host.")
	}
	if len(u.Host) > 255 {
		return ProxyURL{}, invalidProxyURL("The proxy URL's host is longer than 255 bytes.")
	}
	if strings.HasPrefix(v.Host, "[") {
		if _, err := netip.ParseAddr(u.Host); err != nil {
			return ProxyURL{}, invalidProxyURL("The proxy URL's host in brackets is not an IPv6 address.")
		}
	}
	switch p := v.Port(); {
	case p == "" && strings.HasSuffix(v.Host, ":"):
		return ProxyURL{}, invalidProxyURL("The proxy URL's port is empty.")
	case p == "":
		u.Port = defaultProxyPort(scheme)
	default:
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return ProxyURL{}, invalidProxyURL("The proxy URL's port must be a number from 1 to 65535.")
		}
		u.Port = n
	}
	if v.User != nil {
		u.Username = v.User.Username()
		u.Password, _ = v.User.Password()
	}
	isSOCKS := strings.HasPrefix(scheme, "socks")
	switch {
	case u.Password != "" && u.Username == "":
		return ProxyURL{}, invalidProxyURL("The proxy URL has a password but no user name.")
	case (scheme == ProxySOCKS4 || scheme == ProxySOCKS4A) && u.Password != "":
		return ProxyURL{}, invalidProxyURL("A SOCKS4 proxy takes a user ID but no password.")
	case isSOCKS && len(u.Username) > MaxProxyCredentialLen:
		return ProxyURL{}, invalidProxyURL("The proxy user name is longer than 255 bytes.")
	case isSOCKS && len(u.Password) > MaxProxyCredentialLen:
		return ProxyURL{}, invalidProxyURL("The proxy password is longer than 255 bytes.")
	case !isSOCKS && strings.Contains(u.Username, ":"):
		return ProxyURL{}, invalidProxyURL("An HTTP proxy's user name cannot contain a colon.")
	case strings.ContainsFunc(u.Username+u.Password, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return ProxyURL{}, invalidProxyURL("The proxy user name or password contains a control character.")
	}
	return u, nil
}

func knownProxyScheme(s string) bool {
	for _, k := range ProxySchemes {
		if s == k {
			return true
		}
	}
	return false
}

func defaultProxyPort(scheme string) int {
	switch scheme {
	case ProxyHTTPS:
		return DefaultHTTPSProxyPort
	case ProxyHTTP:
		return DefaultHTTPProxyPort
	default:
		return DefaultSOCKSPort
	}
}

func invalidProxyURL(msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeProxyInvalidURL).WithHint(HintProxyInvalidURL)
}

// ProxyEnvVars are the environment variables a proxy is read from, in
// order (SOCKS-04): the first one set to something other than spaces
// wins. They name a proxy for every protocol, as curl reads them.
var ProxyEnvVars = []string{"ALL_PROXY", "all_proxy"}

// ProxyFromEnv returns the first of ProxyEnvVars that getenv has a value
// for, and that value; "", "" when none is set. getenv is os.Getenv in the
// app; nil reads nothing.
func ProxyFromEnv(getenv func(string) string) (name, value string) {
	if getenv == nil {
		return "", ""
	}
	for _, n := range ProxyEnvVars {
		if v := strings.TrimSpace(getenv(n)); v != "" {
			return n, v
		}
	}
	return "", ""
}

// NoProxyEnvVars are the environment variables a bypass list is read
// from with an environment proxy (SOCKS-09), in order: the first one set
// wins, as ProxyEnvVars.
var NoProxyEnvVars = []string{"NO_PROXY", "no_proxy"}

// NoProxyFromEnv returns the first of NoProxyEnvVars that getenv has a
// value for, and that value; "", "" when none is set. Parse the value with
// ParseBypassList.
func NoProxyFromEnv(getenv func(string) string) (name, value string) {
	if getenv == nil {
		return "", ""
	}
	for _, n := range NoProxyEnvVars {
		if v := strings.TrimSpace(getenv(n)); v != "" {
			return n, v
		}
	}
	return "", ""
}

// ProxyConfig is the proxy in effect for a dial and the targets that
// bypass it (SOCKS-08, SOCKS-09).
type ProxyConfig struct {
	// Proxy is the proxy of every connection; the zero ProxyURL for none.
	Proxy ProxyURL
	// Bypass lists the targets connected to directly although Proxy is
	// set. Loopback targets are always direct (IsLoopbackTarget).
	Bypass Bypass
	// SplitHTTP makes http:// requests use HTTP instead of Proxy (the
	// zero HTTP: directly), as the OS proxy settings name a proxy per
	// scheme (NET-01): the HTTP client's http:// requests are sent to an
	// HTTP proxy in absolute form (ForwardProxy) or, marked with
	// PlainHTTPContext, tunnelled through a SOCKS one. Every other
	// connection uses Proxy.
	SplitHTTP bool
	HTTP      ProxyURL
}

// ForHTTP returns the proxy of http:// requests: HTTP with SplitHTTP,
// else Proxy.
func (c ProxyConfig) ForHTTP() ProxyURL {
	if c.SplitHTTP {
		return c.HTTP
	}
	return c.Proxy
}

// IsZero reports whether c sets no proxy at all.
func (c ProxyConfig) IsZero() bool { return c.Proxy.IsZero() && c.ForHTTP().IsZero() }

// Bypasses reports whether a dial to address connects directly: no proxy,
// a loopback target or one on the bypass list.
func (c ProxyConfig) Bypasses(address string) bool {
	return c.Proxy.IsZero() || c.bypassed(address)
}

// bypassed reports whether address is loopback or on the bypass list.
func (c ProxyConfig) bypassed(address string) bool {
	return IsLoopbackTarget(address) || c.Bypass.Match(address)
}

type plainHTTPKey struct{}

// PlainHTTPContext marks ctx as the dial of an http:// request that goes
// to no HTTP proxy in absolute form, so a ProxyConfig with SplitHTTP routes
// it with its HTTP proxy (a SOCKS one, or none) rather than Proxy. The HTTP
// client marks such requests.
func PlainHTTPContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, plainHTTPKey{}, true)
}

func isPlainHTTP(ctx context.Context) bool {
	v, _ := ctx.Value(plainHTTPKey{}).(bool)
	return v
}

// IsLoopbackTarget reports whether address (host:port, or a host) names
// this machine without a DNS lookup: localhost or a name under
// .localhost (RFC 6761), any case, trailing dot allowed, or a loopback IP
// address (127.0.0.0/8, ::1, IPv4-mapped too). The proxy route connects to
// such targets directly.
func IsLoopbackTarget(address string) bool {
	host := address
	if h, _, err := net.SplitHostPort(address); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && ip.WithZone("").Unmap().IsLoopback()
}

// ProxyRouter is the Route of a dialer whose connections may go through a
// proxy (SOCKS-04, NET-02). On every dial it asks for the proxy in effect
// and picks:
//
//   - the direct dialer when there is none, when the target is this
//     machine (IsLoopbackTarget: localhost is always connected directly)
//     or when it is on the proxy's bypass list (ProxyConfig.Bypass,
//     SOCKS-09);
//   - otherwise a dialer for that proxy, reaching it through the direct
//     dialer. It is built once and reused while the proxy stays the same.
//     An HTTP proxy tunnels with CONNECT.
//
// A dial whose ctx carries ForwardContext (the connection of an
// absolute-form HTTP request, which ForwardProxy chose the proxy for) is
// connected to that proxy without CONNECT, unless its target is loopback.
// ForwardProxy leaves out targets on the bypass list.
//
// An error from the proxy source (an invalid proxy URL) or a proxy that
// cannot be dialed yet fails the dial: a connection meant for a proxy never
// falls back to going direct. It is safe for concurrent use.
type ProxyRouter struct {
	direct  Dialer
	current func(ctx context.Context) (ProxyConfig, error)
	opts    ProxyOptions

	mu    sync.Mutex
	key   ProxyURL
	proxy Dialer

	// seen is the proxy in effect the router last read (Route,
	// ForwardProxy, Refresh); onChange are called when it changes.
	seenMu   sync.Mutex
	seen     proxyState
	seenSet  bool
	onChange []func()
}

// proxyState is the proxy in effect as the router read it: a URL (the
// zero one for none) and its bypass list, or a failure to determine it.
type proxyState struct {
	url    ProxyURL
	http   ProxyURL
	split  bool
	bypass string
	failed bool
}

// NewProxyRouter returns a router over direct (nil means
// NewDirect(Options{})). current returns the proxy for a dial, the zero
// ProxyURL for none; it may read ctx, so the proxy can differ per request
// (SOCKS-08).
func NewProxyRouter(direct Dialer, current func(ctx context.Context) (ProxyURL, error), o ProxyOptions) *ProxyRouter {
	var config func(ctx context.Context) (ProxyConfig, error)
	if current != nil {
		config = func(ctx context.Context) (ProxyConfig, error) {
			p, err := current(ctx)
			return ProxyConfig{Proxy: p}, err
		}
	}
	return NewProxyConfigRouter(direct, config, o)
}

// NewProxyConfigRouter is NewProxyRouter with a bypass list: config returns
// the proxy of a dial and the targets that connect directly anyway
// (SOCKS-09). It may read ctx.
func NewProxyConfigRouter(direct Dialer, config func(ctx context.Context) (ProxyConfig, error), o ProxyOptions) *ProxyRouter {
	if direct == nil {
		direct = NewDirect(Options{})
	}
	return &ProxyRouter{direct: direct, current: config, opts: o}
}

// Route picks the dialer for one dial; use it with Routed.
func (r *ProxyRouter) Route(ctx context.Context, network, address string) (Dialer, error) {
	if fp := forwardFrom(ctx); fp != nil && !IsLoopbackTarget(address) {
		return DialerFunc(fp.DialForward), nil
	}
	var c ProxyConfig
	if r.current != nil {
		var err error
		c, err = r.current(ctx)
		r.observe(c, err)
		if err != nil {
			return nil, routeError(err, network, address)
		}
	}
	p := c.Proxy
	if isPlainHTTP(ctx) {
		p = c.ForHTTP()
	}
	if p.IsZero() || c.bypassed(address) {
		return r.direct, nil
	}
	d, err := r.dialer(p)
	if err != nil {
		return nil, routeError(err, network, address).WithDetail("via", p.String())
	}
	return d, nil
}

// ForwardProxy returns the HTTP proxy an http:// request to address goes
// to in absolute form: the proxy in effect when it is an HTTP proxy and
// address is not loopback; nil otherwise, also when the proxy in effect
// cannot be determined (the dial then fails with the reason). It is the
// lookup of NewForwardProxier.
func (r *ProxyRouter) ForwardProxy(ctx context.Context, address string) *HTTPProxy {
	if r.current == nil || IsLoopbackTarget(address) {
		return nil
	}
	c, err := r.current(ctx)
	r.observe(c, err)
	p := c.ForHTTP()
	if err != nil || !p.IsHTTP() || c.bypassed(address) {
		return nil
	}
	d, err := r.dialer(p)
	if err != nil {
		return nil
	}
	hp, _ := d.(*HTTPProxy)
	return hp
}

// OnChange registers fn, called whenever the proxy in effect differs from
// the one the router read last (SOCKS-05): a new proxy, another password,
// a proxy set or removed, a setting that became invalid or valid again.
// The app closes the idle connections of every client's pool in it, so a
// kept-alive connection opened with the old route is not reused. The
// router reads the proxy on every Route and ForwardProxy and on Refresh;
// the first reading only records it. fn runs synchronously on the reading
// goroutine, possibly inside a dial: it must be quick, must not block and
// must not dial.
func (r *ProxyRouter) OnChange(fn func()) {
	if fn == nil {
		return
	}
	r.seenMu.Lock()
	defer r.seenMu.Unlock()
	r.onChange = append(r.onChange, fn)
}

// Refresh reads the proxy in effect now and calls the OnChange hooks when
// it changed. The app calls it after every change of the settings, so the
// pools are closed when the proxy changes, not only at the next dial.
func (r *ProxyRouter) Refresh(ctx context.Context) {
	if r.current == nil {
		return
	}
	c, err := r.current(ctx)
	r.observe(c, err)
}

// observe records the proxy in effect and calls the OnChange hooks when
// it differs from the last one recorded.
func (r *ProxyRouter) observe(c ProxyConfig, err error) {
	st := proxyState{url: c.Proxy, http: c.HTTP, split: c.SplitHTTP, bypass: c.Bypass.String(), failed: err != nil}
	if st.failed {
		st = proxyState{failed: true}
	}
	r.seenMu.Lock()
	if !r.seenSet || r.seen == st {
		r.seen, r.seenSet = st, true
		r.seenMu.Unlock()
		return
	}
	r.seen = st
	hooks := append([]func(){}, r.onChange...)
	r.seenMu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}

// dialer returns the dialer of p, built on first use and kept while the
// proxy stays the same.
func (r *ProxyRouter) dialer(p ProxyURL) (Dialer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proxy != nil && r.key == p {
		return r.proxy, nil
	}
	d, err := p.Dialer(r.direct, r.opts)
	if err != nil {
		return nil, err
	}
	r.key, r.proxy = p, d
	return d, nil
}

// routeError returns err as an *errs.Error with the dial's details.
func routeError(err error, network, address string) *errs.Error {
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		e = errs.Wrap(err, errs.KindOf(err), "The proxy for "+address+" could not be determined.")
	}
	return withAddr(e, network, address)
}
