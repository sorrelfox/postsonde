package settings

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/sysproxy"
)

// The proxy (SOCKS-03, SOCKS-04): network.proxyMode and network.proxyUrl,
// and its bypass list network.proxyBypass (SOCKS-09, SetProxyBypass).
// The settings screen reads and changes them with Proxy and SetProxy, not
// with Get and Update, because the manual proxy's password is kept apart:
// it goes to the local secret storage (appsecrets.ProxyPasswordKey,
// WithSecrets) and never comes back to the UI. The app's dialer reads the
// same settings, secret and environment on every dial (internal/app,
// proxySource). See docs/network.md#proxy-settings.

// Setting keys of the proxy, for the admin policy (SET-02).
const (
	keyProxyMode   = "network.proxyMode"
	keyProxyURL    = "network.proxyUrl"
	keyProxyBypass = "network.proxyBypass"
)

// WithProxyEnv sets how the environment is read for the ALL_PROXY proxy
// (os.Getenv in the app). Without it the environment is empty.
func WithProxyEnv(getenv func(string) string) Option {
	return func(s *SettingsService) { s.getenv = getenv }
}

// WithSystemProxy sets how the OS proxy settings of the "system" mode are
// read (NET-01; the app passes its cached sysproxy reader). Without it
// the OS sets no proxy.
func WithSystemProxy(read func() (sysproxy.Settings, error)) Option {
	return func(s *SettingsService) { s.systemProxy = read }
}

// SystemProxyDTO is what the operating system's proxy settings say
// (NET-01), for the "system" mode.
type SystemProxyDTO struct {
	// Source is where they were read: "macos", "windows", "gnome" or
	// "env" (http_proxy and friends); "" when they could not be.
	Source string `json:"source"`
	// HTTP, HTTPS and SOCKS are the OS's proxies of each kind, without
	// credentials; "" for none.
	HTTP  string `json:"http"`
	HTTPS string `json:"https"`
	SOCKS string `json:"socks"`
	// PAC is the URL of an automatic configuration file, which PostSonde
	// cannot run; "" for none. AutoDiscovery is set when WPAD is on,
	// which PostSonde ignores.
	PAC           string `json:"pac"`
	AutoDiscovery bool   `json:"autoDiscovery"`
	// Bypass is the OS's bypass list as PostSonde uses it; Skipped the
	// entries it cannot use.
	Bypass  []string `json:"bypass"`
	Skipped []string `json:"skipped"`
}

// ProxyDTO is the proxy setting and the proxy connections use now.
type ProxyDTO struct {
	// Mode is "system" (the OS's settings), "env" (ALL_PROXY), "manual"
	// (URL) or "off".
	Mode string `json:"mode"`
	// URL is the manual proxy, without its password: socks5h://user@host:1080.
	// Kept while Mode is not "manual". "" when none is set.
	URL string `json:"url"`
	// HasPassword is true when the manual proxy has a password (stored, or
	// written into settings.json by hand). The password never leaves Go.
	HasPassword bool `json:"hasPassword"`
	// EnvVar is the environment variable a proxy is set in ("ALL_PROXY" or
	// "all_proxy"), "" when neither is set.
	EnvVar string `json:"envVar"`
	// EnvURL is that variable's proxy without its password; "" when it is
	// not set or not a valid proxy URL.
	EnvURL string `json:"envUrl"`
	// Effective is the proxy every connection goes through now, without
	// its password; "" for direct connections (or when Error is set). With
	// the system proxy, http:// requests go through EffectiveHTTP
	// instead ("" for directly).
	Effective     string `json:"effective"`
	EffectiveHTTP string `json:"effectiveHttp"`
	// System is what the OS proxy settings say, in the "system" mode;
	// null otherwise.
	System *SystemProxyDTO `json:"system"`
	// RemoteDNS is true when that proxy resolves host names itself
	// (socks5h), so nothing is looked up locally.
	RemoteDNS bool `json:"remoteDns"`
	// Error is why connections fail now: the proxy in effect is not a
	// valid URL (netx.proxy_invalid_url) or of a kind PostSonde cannot
	// use yet (netx.proxy_unsupported). Null when it is fine.
	Error *apperr.ErrorDTO `json:"error"`
	// Bypass is network.proxyBypass (SOCKS-09): the targets that connect
	// directly whatever proxy is in effect, as written.
	Bypass []string `json:"bypass"`
	// NoProxyVar is the environment variable with a bypass list for the
	// ALL_PROXY proxy ("NO_PROXY" or "no_proxy"), "" when neither is set;
	// NoProxy is its value.
	NoProxyVar string `json:"noProxyVar"`
	NoProxy    string `json:"noProxy"`
}

// ProxyInputDTO is a change of the proxy setting.
type ProxyInputDTO struct {
	// Mode is "system", "env", "manual" or "off".
	Mode string `json:"mode"`
	// URL is the manual proxy: socks5h://[user[:password]@]host[:port].
	// Required for "manual"; for the other modes it is kept for later and
	// may be "". A password in it is moved to the secret storage.
	URL string `json:"url"`
	// Password of the manual proxy's user, when the URL has none. Null
	// keeps the stored one, "" removes it.
	Password *string `json:"password"`
}

// Proxy returns the proxy setting and the proxy in effect.
func (s *SettingsService) Proxy(ctx context.Context) (ProxyDTO, error) {
	if err := ctx.Err(); err != nil {
		return ProxyDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the proxy settings was interrupted.")
	}
	return s.describeProxy(), nil
}

// SetProxy changes the proxy setting and returns it as Proxy does. The URL
// is checked first (netx.proxy_invalid_url, detail field "url"); a
// password needs a user name in the URL (field "password"). The password
// goes to the local secret storage, the URL without it to settings.json.
// A locked setting (SET-02) is settings.locked and changes nothing. A
// change is followed by a settings:changed event.
func (s *SettingsService) SetProxy(ctx context.Context, in ProxyInputDTO) (ProxyDTO, error) {
	if err := ctx.Err(); err != nil {
		return ProxyDTO{}, errs.Wrap(err, errs.KindOf(err), "Saving the proxy settings was interrupted.")
	}
	mode := in.Mode
	switch mode {
	case core.ProxyModeSystem, core.ProxyModeEnv, core.ProxyModeManual, core.ProxyModeOff:
	default:
		return ProxyDTO{}, errs.New(errs.InvalidArgument, "The proxy mode must be system, env, manual or off.").
			WithCode(core.CodeInvalidValue).WithDetail("field", "mode")
	}
	raw := strings.TrimSpace(in.URL)
	var u netx.ProxyURL
	if raw == "" && mode == core.ProxyModeManual {
		return ProxyDTO{}, errs.New(errs.InvalidArgument, "Enter the proxy URL.").
			WithCode(netx.CodeProxyInvalidURL).WithHint(netx.HintProxyInvalidURL).WithDetail("field", "url")
	}
	if raw != "" {
		var err error
		if u, err = netx.ParseProxyURL(raw); err != nil {
			if e, ok := errors.AsType[*errs.Error](err); ok {
				return ProxyDTO{}, e.WithDetail("field", "url")
			}
			return ProxyDTO{}, err
		}
	}

	// From reading the settings to saving them: one change at a time.
	s.listMu.Lock()
	defer s.listMu.Unlock()
	current := s.store.Settings().Network
	secrets := s.secretStore()
	stored, err := secrets.Get(appsecrets.ProxyPasswordKey)
	if err != nil {
		return ProxyDTO{}, err
	}
	// The password: typed into the URL, typed into the field, or the one
	// in effect, which may have been written into settings.json by hand:
	// it then moves to the secret storage.
	password := stored
	if old, err := netx.ParseProxyURL(current.ProxyURL); err == nil && old.Password != "" && old.Username == u.Username {
		password = old.Password
	}
	switch {
	case u.Password != "":
		password = u.Password
	case in.Password != nil:
		password = *in.Password
	}
	if u.Username == "" {
		// Without a user there is no password to send; forget it.
		if in.Password != nil && *in.Password != "" {
			return ProxyDTO{}, errs.New(errs.InvalidArgument, "A proxy password needs a user name; write it into the URL as socks5h://user@host:port.").
				WithCode(netx.CodeProxyInvalidURL).WithDetail("field", "password")
		}
		password = ""
	}
	if u.IsSOCKS4() {
		// SOCKS4 sends a user ID and no password (SOCKS-06).
		if in.Password != nil && *in.Password != "" {
			return ProxyDTO{}, errs.New(errs.InvalidArgument, "A SOCKS4 proxy takes a user ID but no password.").
				WithCode(netx.CodeProxyInvalidURL).WithDetail("field", "password")
		}
		password = ""
	}
	if strings.HasPrefix(u.Scheme, "socks") && len(password) > netx.MaxProxyCredentialLen {
		return ProxyDTO{}, errs.New(errs.InvalidArgument, "The proxy password is longer than 255 bytes.").
			WithCode(netx.CodeProxyInvalidURL).WithDetail("field", "password")
	}
	url := u.String() // without the password

	policy := s.store.Policy()
	if mode != current.ProxyMode {
		if err := policy.CheckLocked(keyProxyMode); err != nil {
			return ProxyDTO{}, err
		}
	}
	if url != current.ProxyURL || password != stored {
		// Refused before the password is stored for a URL that could not
		// be saved.
		if err := policy.CheckLocked(keyProxyURL); err != nil {
			return ProxyDTO{}, err
		}
	}
	if password != stored {
		if err := secrets.Set(appsecrets.ProxyPasswordKey, password); err != nil {
			return ProxyDTO{}, err
		}
	}
	_, changed, err := s.store.Update(core.Patch{Network: core.NetworkPatch{ProxyMode: &mode, ProxyURL: &url}})
	if err != nil {
		if password != stored {
			if rerr := secrets.Set(appsecrets.ProxyPasswordKey, stored); rerr != nil {
				s.logger.Warn("proxy password not restored", "err", rerr)
			}
		}
		return ProxyDTO{}, err
	}
	if changed || password != stored {
		s.logger.Info("proxy settings changed", "mode", mode, "url", url, "password", password != "")
		s.publish(SourceApp)
	}
	return s.describeProxy(), nil
}

// SetProxyBypass replaces the bypass list (network.proxyBypass, SOCKS-09)
// and returns the proxy setting as Proxy does. Spaces around entries and
// empty entries are dropped; an entry that cannot be used is
// netx.proxy_invalid_bypass with details entry and field "bypass", and
// nothing is saved. settings.locked for a locked list.
func (s *SettingsService) SetProxyBypass(ctx context.Context, entries []string) (ProxyDTO, error) {
	if err := ctx.Err(); err != nil {
		return ProxyDTO{}, errs.Wrap(err, errs.KindOf(err), "Saving the proxy bypass list was interrupted.")
	}
	list := []string{}
	for _, e := range entries {
		if e = strings.TrimSpace(e); e != "" {
			list = append(list, e)
		}
	}
	if _, err := netx.ParseBypass(list); err != nil {
		if e, ok := errors.AsType[*errs.Error](err); ok {
			return ProxyDTO{}, e.WithDetail("field", "bypass")
		}
		return ProxyDTO{}, err
	}
	s.listMu.Lock()
	defer s.listMu.Unlock()
	if !slices.Equal(list, s.store.Settings().Network.ProxyBypass) {
		if err := s.store.Policy().CheckLocked(keyProxyBypass); err != nil {
			return ProxyDTO{}, err
		}
	}
	_, changed, err := s.store.Update(core.Patch{Network: core.NetworkPatch{ProxyBypass: &list}})
	if err != nil {
		return ProxyDTO{}, err
	}
	if changed {
		s.logger.Info("proxy bypass list changed", "entries", len(list))
		s.publish(SourceApp)
	}
	return s.describeProxy(), nil
}

// describeProxy reads the proxy setting, the secret and the environment.
func (s *SettingsService) describeProxy() ProxyDTO {
	n := s.store.Settings().Network
	dto := ProxyDTO{Mode: n.ProxyMode, Bypass: slices.Clone(n.ProxyBypass)}
	if dto.Bypass == nil {
		dto.Bypass = []string{}
	}
	dto.NoProxyVar, dto.NoProxy = netx.NoProxyFromEnv(s.getenv)
	password, secretErr := s.secretStore().Get(appsecrets.ProxyPasswordKey)
	if n.ProxyURL != "" {
		if u, err := netx.ParseProxyURL(n.ProxyURL); err == nil {
			dto.URL = u.String()
			dto.HasPassword = u.Username != "" && !u.IsSOCKS4() && (u.Password != "" || password != "")
		}
	}
	dto.EnvVar, _ = netx.ProxyFromEnv(s.getenv)
	if _, value := netx.ProxyFromEnv(s.getenv); value != "" {
		if u, err := netx.ParseProxyURL(value); err == nil {
			dto.EnvURL = u.String()
		}
	}
	fail := func(err error) ProxyDTO {
		e := apperr.ToDTO(err)
		dto.Error = &e
		return dto
	}
	if secretErr != nil && n.ProxyMode == core.ProxyModeManual {
		return fail(secretErr)
	}
	var system core.SystemProxy
	if n.ProxyMode == core.ProxyModeSystem {
		dto.System = s.describeSystemProxy()
		system = s.systemConfig
	}
	p, err := n.EffectiveProxy(password, s.getenv, system)
	if err != nil {
		return fail(err)
	}
	if p.SplitHTTP && !p.HTTP.IsZero() {
		dto.EffectiveHTTP = p.HTTP.String()
	}
	if p.URL.IsZero() {
		if dto.EffectiveHTTP != "" {
			dto.RemoteDNS = p.HTTP.RemoteDNS()
		}
		return dto
	}
	if err := p.URL.CheckSupported(); err != nil {
		return fail(err)
	}
	dto.Effective = p.URL.String()
	dto.RemoteDNS = p.URL.RemoteDNS()
	return dto
}

// systemConfig is the OS proxies, checked, for EffectiveProxy.
func (s *SettingsService) systemConfig() (netx.ProxyConfig, error) {
	if s.systemProxy == nil {
		return netx.ProxyConfig{}, nil
	}
	st, err := s.systemProxy()
	if err != nil {
		return netx.ProxyConfig{}, err
	}
	p, err := st.Check()
	if err != nil {
		return netx.ProxyConfig{}, err
	}
	return p.Config(), nil
}

// describeSystemProxy reads the OS proxy settings for ProxyDTO.System.
// Why they cannot be used is ProxyDTO.Error.
func (s *SettingsService) describeSystemProxy() *SystemProxyDTO {
	dto := &SystemProxyDTO{Bypass: []string{}, Skipped: []string{}}
	if s.systemProxy == nil {
		return dto
	}
	st, err := s.systemProxy()
	if err != nil {
		return dto
	}
	dto.Source, dto.PAC, dto.AutoDiscovery = st.Source, st.PAC, st.AutoDiscovery
	p, err := st.Check()
	show := func(raw string, u netx.ProxyURL) string {
		if raw == "" {
			return ""
		}
		if !u.IsZero() {
			return u.String()
		}
		return "(invalid)"
	}
	if err == nil {
		dto.HTTP = show(st.HTTP, p.HTTP)
		dto.HTTPS = show(st.HTTPS, p.Tunnel)
		if st.SOCKS != "" {
			dto.SOCKS = st.SOCKS
		}
		if b := p.Bypass.Entries(); b != nil {
			dto.Bypass = b
		}
		if p.Skipped != nil {
			dto.Skipped = p.Skipped
		}
	}
	return dto
}
