package settings

import (
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// TestEffectiveProxySystem (NET-01): the "system" mode, the default, takes
// the OS's proxies, one for http:// requests and one for the rest, with
// the OS's bypass list after the app's; no reader or no OS proxy is
// direct; a failure fails the connections with source "system".
func TestEffectiveProxySystem(t *testing.T) {
	t.Parallel()
	if Defaults().Network.ProxyMode != ProxyModeSystem {
		t.Fatalf("default mode %q, want system", Defaults().Network.ProxyMode)
	}
	web, _ := netx.ParseProxyURL("http://web.proxy.test:3128")
	secure, _ := netx.ParseProxyURL("http://secure.proxy.test:3129")
	osBypass, _ := netx.ParseBypass([]string{"*.local"})
	n := Network{ProxyMode: ProxyModeSystem, ProxyBypass: []string{"*.corp"}, ProxyURL: "socks5h://ignored.lan"}
	env := map[string]string{"ALL_PROXY": "socks5h://env.lan", "NO_PROXY": "env.test"}
	getenv := func(k string) string { return env[k] }

	p, err := n.EffectiveProxy("pw", getenv, func() (netx.ProxyConfig, error) {
		return netx.ProxyConfig{Proxy: secure, SplitHTTP: true, HTTP: web, Bypass: osBypass}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != secure || !p.SplitHTTP || p.HTTP != web || p.Source != ProxySourceSystem || p.NoProxyVar != "" {
		t.Errorf("system = %+v", p)
	}
	if p.Bypass.String() != ".corp,.local" {
		t.Errorf("bypass = %q, want the app's then the OS's (NO_PROXY is not read)", p.Bypass.String())
	}
	if c := p.Config(); c.ForHTTP() != web || c.Proxy != secure {
		t.Errorf("Config = %+v", c)
	}

	for name, system := range map[string]SystemProxy{
		"no reader": nil,
		"no proxy":  func() (netx.ProxyConfig, error) { return netx.ProxyConfig{SplitHTTP: true}, nil },
	} {
		if p, err := n.EffectiveProxy("", getenv, system); err != nil || !p.Config().IsZero() {
			t.Errorf("%s: %+v, %v; want direct", name, p, err)
		}
	}

	pac := errs.New(errs.Unsupported, "PAC").WithCode("sysproxy.pac_unsupported")
	_, err = n.EffectiveProxy("", getenv, func() (netx.ProxyConfig, error) { return netx.ProxyConfig{}, pac })
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != "sysproxy.pac_unsupported" || e.Details["source"] != ProxySourceSystem {
		t.Errorf("failure: %v", err)
	}
	_, err = n.EffectiveProxy("", getenv, func() (netx.ProxyConfig, error) { return netx.ProxyConfig{}, errors.New("plain") })
	if !errors.As(err, &e) || e.Details["source"] != ProxySourceSystem {
		t.Errorf("plain failure: %v", err)
	}
}
