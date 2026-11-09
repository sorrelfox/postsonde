package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// The proxy settings (SOCKS-04): network.proxyMode says where the proxy of
// every connection comes from (the OS's settings by default, NET-01),
// network.proxyUrl is the manual one. The
// manual proxy's password is not in the file: the app keeps it in the local
// secret storage (appsecrets.ProxyPasswordKey) and hands it to
// Network.EffectiveProxy. network.proxyBypass is the bypass list
// (SOCKS-09). The project and environment proxies (SOCKS-08) are not here
// but in the project's local side files (internal/core/proxyscope), which
// fall back to these settings. HTTP proxies (NET-02) are http:// and
// https:// URLs here. See docs/network.md#proxy-settings.

// Values of Network.ProxyMode.
const (
	// ProxyModeSystem follows the operating system's proxy settings
	// (NET-01, internal/core/sysproxy): the default.
	ProxyModeSystem = "system"
	// ProxyModeEnv takes the proxy from the ALL_PROXY (or all_proxy)
	// environment variable; without one, connections go direct.
	ProxyModeEnv = "env"
	// ProxyModeManual uses Network.ProxyURL.
	ProxyModeManual = "manual"
	// ProxyModeOff connects directly, whatever the environment says.
	ProxyModeOff = "off"
)

// ProxyModes lists the allowed Network.ProxyMode values.
var ProxyModes = []string{ProxyModeSystem, ProxyModeEnv, ProxyModeManual, ProxyModeOff}

// Sources of EffectiveProxy.Source besides an environment variable's name.
const (
	// ProxySourceSettings: the manual proxy of the settings.
	ProxySourceSettings = "settings"
	// ProxySourceSystem: the operating system's proxy settings.
	ProxySourceSystem = "system"
)

// SystemProxy reads the operating system's proxy settings for
// ProxyModeSystem: the proxy of every connection, the one of http://
// requests (SplitHTTP) and the OS's bypass list. The app passes a cached
// sysproxy reader; nil means the OS sets none.
type SystemProxy func() (netx.ProxyConfig, error)

func validProxyMode(v string) bool {
	for _, m := range ProxyModes {
		if v == m {
			return true
		}
	}
	return false
}

// checkProxyURL accepts "" and any URL netx.ParseProxyURL accepts (a
// scheme that cannot be dialed yet included: that is reported when it is
// used, not as an invalid setting).
func checkProxyURL(v string) error {
	if v == "" {
		return nil
	}
	if strings.TrimSpace(v) != v {
		return errs.New(errs.InvalidArgument, "The proxy URL has spaces around it.")
	}
	_, err := netx.ParseProxyURL(v)
	return err
}

// invalidProxyURL is the CodeInvalidValue error of network.proxyUrl. The
// value is shown without its user info, which may hold a password, and the
// reason goes into the hint.
func invalidProxyURL(v string, reason error, fallback string) *errs.Error {
	e := invalid("network.proxyUrl", strconv.Quote(maskProxyURL(v)), proxyURLExpected, fallback)
	if r, ok := errors.AsType[*errs.Error](reason); ok && r.Message != "" {
		e = e.WithHint(r.Message + " " + e.Hint)
	}
	return e
}

// maskProxyURL returns v with everything from after "://" to the last
// "@" replaced by "…", so user info, and a password in it, never reaches a
// message or detail.
func maskProxyURL(v string) string {
	prefix, rest := "", v
	if i := strings.Index(v, "://"); i >= 0 {
		prefix, rest = v[:i+3], v[i+3:]
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = "…@" + rest[at+1:]
	}
	return prefix + rest
}

// decodeProxyURL decodes network.proxyUrl like decodeField, with the
// value masked in the problem.
func decodeProxyURL(problems *[]*errs.Error, raw json.RawMessage, dst *string) {
	if isAbsent(raw) {
		return
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		*problems = append(*problems, invalid("network.proxyUrl", "(not a string)", proxyURLExpected, `"" (none)`))
		return
	}
	if err := checkProxyURL(v); err != nil {
		*problems = append(*problems, invalidProxyURL(v, err, `"" (none)`))
		return
	}
	*dst = v
}

// checkProxyBypass accepts the lists netx.ParseBypass accepts.
func checkProxyBypass(v []string) error {
	_, err := netx.ParseBypass(v)
	return err
}

// invalidProxyBypass is the CodeInvalidValue error of network.proxyBypass,
// with the reason in the hint and the entry in the details.
func invalidProxyBypass(v []string, reason error, fallback string) *errs.Error {
	e := invalid("network.proxyBypass", compactJSON(mustJSON(v)), proxyBypassExpected, fallback)
	if r, ok := errors.AsType[*errs.Error](reason); ok {
		if r.Message != "" {
			e = e.WithHint(r.Message + " " + e.Hint)
		}
		if entry, ok := r.Details["entry"]; ok {
			e = e.WithDetail("entry", entry)
		}
	}
	return e
}

// decodeProxyBypass decodes network.proxyBypass like decodeField, with
// the reason of an invalid entry in the problem.
func decodeProxyBypass(problems *[]*errs.Error, raw json.RawMessage, dst *[]string) {
	if isAbsent(raw) {
		return
	}
	var v []string
	if err := json.Unmarshal(raw, &v); err != nil {
		*problems = append(*problems, invalid("network.proxyBypass", compactJSON(raw), proxyBypassExpected, "none"))
		return
	}
	if err := checkProxyBypass(v); err != nil {
		*problems = append(*problems, invalidProxyBypass(v, err, "none"))
		return
	}
	if len(v) > 0 {
		*dst = v
	}
}

// Bypass returns the parsed network.proxyBypass. The settings are
// validated when loaded or changed, so an error only comes from a Network
// built by hand.
func (n Network) Bypass() (netx.Bypass, error) {
	return netx.ParseBypass(n.ProxyBypass)
}

// EffectiveProxy is the proxy connections go through, as the settings
// and the environment give it.
type EffectiveProxy struct {
	// URL is the proxy, with its password; the zero netx.ProxyURL means
	// none: connections go direct.
	URL netx.ProxyURL
	// SplitHTTP and HTTP are the system proxy of http:// requests
	// (netx.ProxyConfig.SplitHTTP).
	SplitHTTP bool
	HTTP      netx.ProxyURL
	// Bypass lists the targets that connect directly anyway: those of
	// network.proxyBypass and, for the ALL_PROXY proxy, of NO_PROXY, for
	// the system proxy, the OS's list (SOCKS-09). Empty when there is no
	// proxy.
	Bypass netx.Bypass
	// Source is where it comes from: ProxySourceSettings,
	// ProxySourceSystem, the name of the environment variable
	// ("ALL_PROXY"), or "" for none.
	Source string
	// NoProxyVar is the environment variable the ALL_PROXY proxy's
	// bypass list was read from too ("NO_PROXY" or "no_proxy"); "" when
	// neither is set or the proxy is not from the environment.
	NoProxyVar string
}

// Config returns the proxy and its bypass list for netx.ProxyRouter.
func (e EffectiveProxy) Config() netx.ProxyConfig {
	return netx.ProxyConfig{Proxy: e.URL, Bypass: e.Bypass, SplitHTTP: e.SplitHTTP, HTTP: e.HTTP}
}

// EffectiveProxy returns the proxy of n's ProxyMode:
//
//   - "system": the operating system's proxies (system; nil means none):
//     one for http:// requests and one for every other connection.
//   - "off": none.
//   - "env": the proxy in ALL_PROXY or all_proxy (netx.ProxyFromEnv, read
//     with getenv), none when neither is set.
//   - "manual": ProxyURL, with password (the stored one) when the URL
//     has a user name and no password of its own; a SOCKS4 proxy takes a
//     user ID only, so it never gets one.
//
// Its bypass list is network.proxyBypass, plus NO_PROXY (or no_proxy)
// for the proxy of the environment and the OS's list for the system
// proxy; entries of those that cannot be used are skipped.
//
// A proxy that is set but cannot be used is an error with
// netx.CodeProxyInvalidURL, never "none": connections that were meant to
// go through a proxy must not silently go direct.
func (n Network) EffectiveProxy(password string, getenv func(string) string, system SystemProxy) (EffectiveProxy, error) {
	if n.ProxyMode == ProxyModeSystem {
		return n.systemProxy(system)
	}
	p, err := n.effectiveProxy(password, getenv)
	if err != nil || p.URL.IsZero() {
		return p, err
	}
	bypass, err := n.Bypass()
	if err != nil {
		return EffectiveProxy{}, err
	}
	if p.Source != ProxySourceSettings {
		name, value := netx.NoProxyFromEnv(getenv)
		if value != "" {
			env, _ := netx.ParseBypassList(value)
			bypass = netx.JoinBypass(bypass, env)
			p.NoProxyVar = name
		}
	}
	p.Bypass = bypass
	return p, nil
}

// systemProxy is EffectiveProxy of ProxyModeSystem. A failure to read or
// use the OS settings fails the connections, detail source "system".
func (n Network) systemProxy(system SystemProxy) (EffectiveProxy, error) {
	if system == nil {
		return EffectiveProxy{}, nil
	}
	cfg, err := system()
	if err != nil {
		if e, ok := errors.AsType[*errs.Error](err); ok {
			return EffectiveProxy{}, e.WithDetail("source", ProxySourceSystem)
		}
		return EffectiveProxy{}, errs.Wrap(err, errs.KindOf(err), "The system's proxy settings cannot be used.").WithDetail("source", ProxySourceSystem)
	}
	if cfg.IsZero() {
		return EffectiveProxy{}, nil
	}
	bypass, err := n.Bypass()
	if err != nil {
		return EffectiveProxy{}, err
	}
	return EffectiveProxy{
		URL:       cfg.Proxy,
		SplitHTTP: true,
		HTTP:      cfg.ForHTTP(),
		Bypass:    netx.JoinBypass(bypass, cfg.Bypass),
		Source:    ProxySourceSystem,
	}, nil
}

func (n Network) effectiveProxy(password string, getenv func(string) string) (EffectiveProxy, error) {
	switch n.ProxyMode {
	case ProxyModeOff:
		return EffectiveProxy{}, nil
	case ProxyModeManual:
		if n.ProxyURL == "" {
			return EffectiveProxy{}, errs.New(errs.InvalidArgument, "A manual proxy is chosen, but no proxy URL is set.").
				WithCode(netx.CodeProxyInvalidURL).
				WithHint("Enter the proxy URL under Settings, Network, Proxy, or choose No proxy.").
				WithDetail("source", ProxySourceSettings)
		}
		u, err := netx.ParseProxyURL(n.ProxyURL)
		if err != nil {
			return EffectiveProxy{}, proxySourceError(err, "The proxy URL in the settings cannot be used", ProxySourceSettings,
				"Fix the proxy URL under Settings, Network, Proxy, or choose No proxy.")
		}
		if u.Username != "" && u.Password == "" && !u.IsSOCKS4() {
			u.Password = password
		}
		return EffectiveProxy{URL: u, Source: ProxySourceSettings}, nil
	default: // ProxyModeEnv
		name, value := netx.ProxyFromEnv(getenv)
		if value == "" {
			return EffectiveProxy{}, nil
		}
		u, err := netx.ParseProxyURL(value)
		if err != nil {
			return EffectiveProxy{}, proxySourceError(err, fmt.Sprintf("The proxy in the environment variable %s cannot be used", name), name,
				fmt.Sprintf("Fix %s and restart PostSonde, or choose No proxy or a manual proxy under Settings, Network, Proxy.", name))
		}
		return EffectiveProxy{URL: u, Source: name}, nil
	}
}

// proxySourceError is a parse error of a proxy URL, saying where the URL
// came from.
func proxySourceError(err error, what, source, hint string) *errs.Error {
	reason := ""
	if e, ok := errors.AsType[*errs.Error](err); ok {
		reason = " " + e.Message
	}
	return errs.New(errs.InvalidArgument, what+"."+reason).
		WithCode(netx.CodeProxyInvalidURL).
		WithHint(hint+" "+netx.HintProxyInvalidURL).
		WithDetail("source", source)
}
