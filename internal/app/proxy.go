package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/services/settings"
	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	coreenvironments "github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/proxyscope"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/sysproxy"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// proxySource returns the proxy of every dial (SOCKS-04,
// docs/network.md#proxy-settings) from the app's settings alone: that of
// the network settings in effect (network.proxyMode and network.proxyUrl,
// admin locks applied), with the manual proxy's password from the app
// secrets, or the ALL_PROXY environment variable read with getenv, and
// the bypass list (network.proxyBypass, NO_PROXY). The app's dialer adds
// the project's scopes (proxyResolver with proxyScopes).
func proxySource(network func() coresettings.Network, secrets *appsecrets.Store, getenv func(string) string) func(context.Context) (netx.ProxyConfig, error) {
	return proxyConfigOf(proxyResolver(network, secrets, getenv, nil, nil))
}

// proxyResolver returns the proxy in effect for a dial and the scope it
// comes from (SOCKS-08, docs/network.md#proxy-scopes): that of the active
// environment's or the project's scope (scopes; nil means none), else
// that of the app's settings, with the OS's proxies (system; nil means
// none) in the "system" mode. They are read on every dial, so a change on
// the settings screen or by hand applies to the next connection. A proxy
// that is set but cannot be used fails the dial rather than going direct.
func proxyResolver(network func() coresettings.Network, secrets *appsecrets.Store, getenv func(string) string,
	scopes func(context.Context) (proxyscope.Active, error), system coresettings.SystemProxy) func(context.Context) (proxyscope.Effective, error) {
	return func(ctx context.Context) (proxyscope.Effective, error) {
		n := network()
		var active proxyscope.Active
		if scopes != nil {
			a, err := scopes(ctx)
			if err != nil {
				return proxyscope.Effective{Level: proxyscope.LevelProject}, err
			}
			active = a
		}
		password := ""
		if n.ProxyMode == coresettings.ProxyModeManual && !active.Decides() {
			p, err := secrets.Get(appsecrets.ProxyPasswordKey)
			if err != nil {
				return proxyscope.Effective{Level: proxyscope.LevelApp}, err
			}
			password = p
		}
		return proxyscope.Resolve(n, password, getenv, system, active)
	}
}

// proxyConfigOf adapts a resolver to the router's source.
func proxyConfigOf(resolve func(context.Context) (proxyscope.Effective, error)) func(context.Context) (netx.ProxyConfig, error) {
	return func(ctx context.Context) (netx.ProxyConfig, error) {
		e, err := resolve(ctx)
		return e.Config, err
	}
}

// proxyScopes reads the proxy scopes of the open project and of its
// active environment from the project's local side files
// (proxyscope.Store); none without an open project.
func proxyScopes(workspaces *coreworkspace.Manager) func(context.Context) (proxyscope.Active, error) {
	return func(context.Context) (proxyscope.Active, error) {
		w := workspaces.Current()
		if w == nil {
			return proxyscope.Active{}, nil
		}
		env := coreenvironments.New(w.Root, w.LocalDir, true).ActiveID()
		return proxyscope.New(w.LocalDir).Active(env)
	}
}

// refreshProxyOnSettings wraps emit for the settings service: after every
// settings:changed event (a change on the settings screen, the proxy
// password, a hand edit of the file) the router reads the proxy in effect
// again, so its OnChange hooks close the idle pools as soon as the proxy
// changes (SOCKS-05, docs/network.md#pools-and-proxy-changes).
//
// The OS proxy settings are read again too (system, nil for none), so
// choosing the "system" mode, or saving any setting, picks up a change
// made there.
func refreshProxyOnSettings(router *netx.ProxyRouter, system *systemProxy, emit func(name string, data any)) func(name string, data any) {
	return func(name string, data any) {
		if name == settings.EventChanged {
			system.invalidate()
			router.Refresh(context.Background())
		}
		emit(name, data)
	}
}

// systemProxyRefresh is how often the OS proxy settings are read again
// while the app runs (NET-01): nothing announces their changes.
const systemProxyRefresh = 30 * time.Second

// systemProxy is the OS proxy settings of the "system" mode (NET-01),
// read through a cache so a dial does not run a command: read when first
// needed, again after invalidate (a settings change) or once older than
// systemProxyRefresh. A nil *systemProxy, or one without a reader, has
// none.
type systemProxy struct {
	cache *sysproxy.Cache
}

func newSystemProxy(read func(context.Context) (sysproxy.Settings, error)) *systemProxy {
	if read == nil {
		return &systemProxy{}
	}
	return &systemProxy{cache: sysproxy.NewCache(read, systemProxyRefresh)}
}

// settings returns the OS settings as read (for the settings screen).
func (s *systemProxy) settings() (sysproxy.Settings, error) {
	if s == nil || s.cache == nil {
		return sysproxy.Settings{}, nil
	}
	return s.cache.Get(context.Background())
}

// config returns the OS proxies checked, for the route
// (settings.SystemProxy).
func (s *systemProxy) config() (netx.ProxyConfig, error) {
	st, err := s.settings()
	if err != nil {
		return netx.ProxyConfig{}, err
	}
	p, err := st.Check()
	if err != nil {
		return netx.ProxyConfig{}, err
	}
	return p.Config(), nil
}

func (s *systemProxy) invalidate() {
	if s != nil && s.cache != nil {
		s.cache.Invalidate()
	}
}

// refresh reads the OS settings again and lets the router notice a
// change (ProxyScopesService's timer).
func (s *systemProxy) refresh(router *netx.ProxyRouter) func(context.Context) {
	return func(ctx context.Context) {
		s.invalidate()
		router.Refresh(ctx)
	}
}

// refreshProxyOn wraps emit for a service whose every event may change
// the proxy in effect (a scope saved, another active environment,
// SOCKS-08): the router reads it again before the event goes out.
func refreshProxyOn(router *netx.ProxyRouter, emit func(name string, data any)) func(name string, data any) {
	return func(name string, data any) {
		router.Refresh(context.Background())
		emit(name, data)
	}
}

// proxyTester is the proxy connection test of the settings screen
// (SOCKS-11, docs/network.md#testing-the-proxy): router.Test through the
// proxy in effect, recorded in the connection log and guarded like every
// other dial, so it needs the user operation SettingsService.TestProxy
// marks.
func proxyTester(router *netx.ProxyRouter, connLog *netlog.Log, logger *slog.Logger) settings.ProxyTester {
	return func(ctx context.Context, target string) netx.ProxyTest {
		return router.Test(ctx, target, connLog.Middleware(), netx.Guarded(netx.RequireUserOp(logger)))
	}
}
