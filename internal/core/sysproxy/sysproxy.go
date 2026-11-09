// Package sysproxy reads the operating system's proxy settings (NET-01)
// for the "system" proxy mode, with the standard library only:
//
//   - macOS: the output of /usr/sbin/scutil --proxy (the SystemConfiguration
//     proxies of the primary network service); no cgo.
//   - Windows: HKCU\Software\Microsoft\Windows\CurrentVersion\Internet
//     Settings (ProxyEnable, ProxyServer, ProxyOverride, AutoConfigURL),
//     read with syscall's registry functions.
//   - Linux and other systems: GNOME's org.gnome.system.proxy (gsettings
//     list-recursively) when gsettings is there and the mode is manual or
//     auto; else the environment variables http_proxy, https_proxy,
//     all_proxy and no_proxy (either case).
//
// The OS settings name a proxy per scheme (web, secure web, SOCKS);
// Settings.Config turns them into a netx.ProxyConfig: http:// requests use
// the HTTP proxy, every other connection (HTTPS, WebSocket, gRPC, TCP) the
// HTTPS proxy, and either falls back to the SOCKS proxy. Proxy
// auto-configuration (PAC) files need a JavaScript engine, which PostSonde
// does not have: a PAC URL without a manual proxy is an error
// (CodePACUnsupported), never a direct connection; WPAD auto-discovery on
// its own is ignored. The parsers take the command output or registry
// values as input, so they are tested from fixtures without reading the
// machine's settings. See docs/network.md#system-proxy.
package sysproxy

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Sources of Settings.Source.
const (
	SourceMacOS   = "macos"
	SourceWindows = "windows"
	SourceGNOME   = "gnome"
	SourceEnv     = "env"
)

// Error codes.
const (
	// CodePACUnsupported: the system proxy is a PAC file (automatic proxy
	// configuration) and names no proxy of its own (kind unsupported).
	// Details: pac (the file's URL).
	CodePACUnsupported = "sysproxy.pac_unsupported"
	// CodeReadFailed: the OS proxy settings could not be read (kind of the
	// cause). Details: source.
	CodeReadFailed = "sysproxy.read_failed"
	// CodeInvalidProxy: the OS names a proxy PostSonde cannot use (kind
	// invalid_argument). Details: source, scheme ("http", "https",
	// "socks").
	CodeInvalidProxy = "sysproxy.invalid_proxy"
)

// Settings are the OS proxy settings as read, before they are checked.
type Settings struct {
	// Source is where they were read: SourceMacOS, SourceWindows,
	// SourceGNOME or SourceEnv.
	Source string
	// HTTP, HTTPS and SOCKS are the proxies of each kind, host:port or a
	// URL; "" for none. All is the proxy of every scheme (all_proxy).
	HTTP, HTTPS, SOCKS, All string
	// SOCKSScheme is the scheme of a SOCKS proxy given without one:
	// socks5h (macOS, GNOME), socks4a (Windows, whose socks= is SOCKS4).
	SOCKSScheme string
	// HTTPUser and HTTPPassword are the HTTP proxy's credentials, when
	// the OS keeps them in its settings (GNOME).
	HTTPUser, HTTPPassword string
	// Bypass are the OS's bypass entries as written (ExceptionsList,
	// ProxyOverride, ignore-hosts, no_proxy).
	Bypass []string
	// PAC is the URL of a proxy auto-configuration file; "" for none.
	PAC string
	// AutoDiscovery is set when WPAD is on (macOS, GNOME auto without a
	// URL); PostSonde does not do it.
	AutoDiscovery bool
}

// IsZero reports whether the OS sets no proxy at all.
func (s Settings) IsZero() bool {
	return s.HTTP == "" && s.HTTPS == "" && s.SOCKS == "" && s.All == "" && s.PAC == ""
}

// Proxies is Settings checked: the proxy of each kind of connection.
type Proxies struct {
	// Tunnel is the proxy of every connection but http:// requests:
	// HTTPS, else SOCKS, else All.
	Tunnel netx.ProxyURL
	// HTTP is the proxy of http:// requests: HTTP, else SOCKS, else All.
	HTTP netx.ProxyURL
	// Bypass is the OS's bypass list, entries PostSonde cannot use left
	// out (Skipped).
	Bypass  netx.Bypass
	Skipped []string
}

// Config returns the proxies as a netx.ProxyConfig: Tunnel for every
// connection, HTTP for http:// requests (SplitHTTP).
func (p Proxies) Config() netx.ProxyConfig {
	return netx.ProxyConfig{Proxy: p.Tunnel, HTTP: p.HTTP, SplitHTTP: true, Bypass: p.Bypass}
}

// Check parses the proxies of s. A PAC file without a manual proxy is
// CodePACUnsupported; a proxy that cannot be parsed is CodeInvalidProxy:
// connections then fail, never going direct.
func (s Settings) Check() (Proxies, error) {
	var out Proxies
	if s.HTTP == "" && s.HTTPS == "" && s.SOCKS == "" && s.All == "" && s.PAC != "" {
		return out, errs.New(errs.Unsupported, "The system proxy is set by an automatic configuration (PAC) file, which PostSonde cannot run.").
			WithCode(CodePACUnsupported).
			WithHint("Choose Manual under Settings, Network, Proxy and enter the proxy the PAC file names, or No proxy. PostSonde has no JavaScript engine to run PAC scripts.").
			WithDetail("pac", s.PAC).WithDetail("source", s.Source)
	}
	socksScheme := s.SOCKSScheme
	if socksScheme == "" {
		socksScheme = netx.ProxySOCKS5H
	}
	parse := func(raw, scheme, kind string) (netx.ProxyURL, error) {
		if raw == "" {
			return netx.ProxyURL{}, nil
		}
		u, err := proxyURL(raw, scheme)
		if err != nil {
			reason := ""
			if e, ok := errors.AsType[*errs.Error](err); ok {
				reason = " " + e.Message
			}
			return netx.ProxyURL{}, errs.New(errs.InvalidArgument, "The system's "+kind+" proxy cannot be used."+reason).
				WithCode(CodeInvalidProxy).
				WithHint("Fix it in the system's proxy settings, or choose Manual or No proxy under Settings, Network, Proxy.").
				WithDetail("source", s.Source).WithDetail("scheme", kind)
		}
		return u, nil
	}
	httpP, err := parse(s.HTTP, netx.ProxyHTTP, "http")
	if err != nil {
		return out, err
	}
	if httpP.IsHTTP() && s.HTTPUser != "" && httpP.Username == "" {
		httpP.Username, httpP.Password = s.HTTPUser, s.HTTPPassword
	}
	httpsP, err := parse(s.HTTPS, netx.ProxyHTTP, "https")
	if err != nil {
		return out, err
	}
	if httpsP.IsHTTP() && s.HTTPUser != "" && httpsP.Username == "" && httpsP.Address() == httpP.Address() {
		httpsP.Username, httpsP.Password = s.HTTPUser, s.HTTPPassword
	}
	socksP, err := parse(s.SOCKS, socksScheme, "socks")
	if err != nil {
		return out, err
	}
	allP, err := parse(s.All, netx.ProxyHTTP, "all")
	if err != nil {
		return out, err
	}
	out.Tunnel = first(httpsP, socksP, allP)
	out.HTTP = first(httpP, socksP, allP)
	out.Bypass, out.Skipped = ParseBypass(s.Bypass)
	return out, nil
}

func first(us ...netx.ProxyURL) netx.ProxyURL {
	for _, u := range us {
		if !u.IsZero() {
			return u
		}
	}
	return netx.ProxyURL{}
}

// proxyURL parses an OS proxy: a URL, or host:port (host alone) taking
// scheme. A "socks://" URL (GNOME, curl) is socks5h.
func proxyURL(raw, scheme string) (netx.ProxyURL, error) {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "://"); i >= 0 {
		if strings.EqualFold(raw[:i], "socks") {
			raw = netx.ProxySOCKS5H + raw[i:]
		}
		return netx.ParseProxyURL(strings.TrimSuffix(raw, "/"))
	}
	return netx.ParseProxyURL(scheme + "://" + raw)
}

// ParseBypass parses the OS's bypass entries: those netx.ParseBypassList
// takes, plus Windows' IP wildcards (10.*, 192.168.*, 10.1.2.*), which
// become CIDR ranges. Entries PostSonde cannot use (other wildcards) are
// returned in skipped.
func ParseBypass(entries []string) (netx.Bypass, []string) {
	var list []string
	for _, e := range entries {
		for _, part := range netx.SplitBypassList(e) {
			list = append(list, ipWildcard(part))
		}
	}
	return netx.ParseBypassList(strings.Join(list, ","))
}

// ipWildcard turns 10.* into 10.0.0.0/8 (1 to 3 leading octets, then *);
// other entries are returned as they are.
func ipWildcard(e string) string {
	head, ok := strings.CutSuffix(e, ".*")
	if !ok || head == "" {
		return e
	}
	octets := strings.Split(head, ".")
	if len(octets) > 3 {
		return e
	}
	for _, o := range octets {
		if n, err := strconv.Atoi(o); err != nil || n < 0 || n > 255 {
			return e
		}
	}
	bits := 8 * len(octets)
	for len(octets) < 4 {
		octets = append(octets, "0")
	}
	return strings.Join(octets, ".") + "/" + strconv.Itoa(bits)
}

// Reader reads the OS proxy settings of GOOS. Every way it touches the
// machine is a field, so tests give it fixtures; Default is the real one.
type Reader struct {
	// GOOS is the system whose settings are read ("darwin", "windows",
	// anything else: GNOME or the environment).
	GOOS string
	// Run runs a command and returns its standard output.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// LookPath finds a command (gsettings).
	LookPath func(name string) (string, error)
	// Getenv reads the environment PostSonde started with.
	Getenv func(string) string
	// Registry reads the Windows Internet Settings.
	Registry func() (RegistryValues, error)
	// Timeout bounds a command; 0 means 3 s.
	Timeout time.Duration
}

// ScutilPath is the macOS command that prints the proxy settings.
const ScutilPath = "/usr/sbin/scutil"

// Read reads the settings. A command or registry that cannot be read is
// CodeReadFailed, except GNOME's: without gsettings, or when it fails (no
// session bus), the environment is read instead.
func (r Reader) Read(ctx context.Context) (Settings, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch r.GOOS {
	case "darwin":
		if r.Run == nil {
			return Settings{}, readFailed(errors.New("no command runner"), SourceMacOS)
		}
		out, err := r.Run(ctx, ScutilPath, "--proxy")
		if err != nil {
			return Settings{}, readFailed(err, SourceMacOS)
		}
		return ParseScutil(string(out)), nil
	case "windows":
		if r.Registry == nil {
			return Settings{}, readFailed(errors.New("no registry reader"), SourceWindows)
		}
		v, err := r.Registry()
		if err != nil {
			return Settings{}, readFailed(err, SourceWindows)
		}
		return ParseWindows(v), nil
	default:
		if r.LookPath != nil && r.Run != nil {
			if path, err := r.LookPath("gsettings"); err == nil {
				if out, err := r.Run(ctx, path, "list-recursively", "org.gnome.system.proxy"); err == nil {
					if s, ok := ParseGSettings(string(out)); ok {
						return s, nil
					}
				}
			}
		}
		return FromEnv(r.Getenv), nil
	}
}

func readFailed(err error, source string) error {
	return errs.Wrap(err, errs.KindOf(err), "PostSonde could not read the system's proxy settings.").
		WithCode(CodeReadFailed).
		WithHint("Choose Manual or No proxy under Settings, Network, Proxy, or From ALL_PROXY to use the environment.").
		WithDetail("source", source)
}

// FromEnv reads the Linux convention of proxy environment variables, the
// lower-case name first: http_proxy (HTTP_PROXY), https_proxy
// (HTTPS_PROXY), all_proxy (ALL_PROXY) and no_proxy (NO_PROXY). A value
// without a scheme is an HTTP proxy, as curl reads it.
func FromEnv(getenv func(string) string) Settings {
	s := Settings{Source: SourceEnv}
	if getenv == nil {
		return s
	}
	get := func(name string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return strings.TrimSpace(getenv(strings.ToUpper(name)))
	}
	s.HTTP, s.HTTPS, s.All = get("http_proxy"), get("https_proxy"), get("all_proxy")
	if v := get("no_proxy"); v != "" {
		s.Bypass = []string{v}
	}
	return s
}

// Cache keeps the settings a Reader read, so a dial does not run a
// command: they are read again after Invalidate or once they are older
// than TTL. It is safe for concurrent use.
type Cache struct {
	read func(ctx context.Context) (Settings, error)
	ttl  time.Duration
	now  func() time.Time

	mu    sync.Mutex
	at    time.Time
	valid bool
	s     Settings
	err   error
}

// NewCache returns a cache over read; ttl 0 means 30 s.
func NewCache(read func(ctx context.Context) (Settings, error), ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Cache{read: read, ttl: ttl, now: time.Now}
}

// Get returns the settings, read when there are none yet, after
// Invalidate, or when they are older than the TTL.
func (c *Cache) Get(ctx context.Context) (Settings, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && c.now().Sub(c.at) < c.ttl {
		return c.s, c.err
	}
	c.s, c.err = c.read(ctx)
	c.at, c.valid = c.now(), true
	return c.s, c.err
}

// Invalidate makes the next Get read the settings again.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.valid = false
}
