package settings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// TestProxyBypassSetting (SOCKS-09): network.proxyBypass is checked on
// Apply and on load (an invalid list is a problem and falls back to none),
// written only when set, compared by Equal, and lockable.
func TestProxyBypassSetting(t *testing.T) {
	t.Parallel()
	s, err := Defaults().Apply(Patch{Network: NetworkPatch{ProxyBypass: new([]string{"*.corp", "10.0.0.0/8", "[::1]:8080"})}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Network.ProxyBypass, ",") != "*.corp,10.0.0.0/8,[::1]:8080" || s.Equal(Defaults()) {
		t.Errorf("after Apply: %v", s.Network.ProxyBypass)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	_, err = s.Apply(Patch{Network: NetworkPatch{ProxyBypass: new([]string{"ok.test", "bad*entry"})}})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeInvalidValue || e.Details["entry"] != "bad*entry" {
		t.Errorf("invalid entry: %v", err)
	}
	cleared, err := s.Apply(Patch{Network: NetworkPatch{ProxyBypass: new([]string{})}})
	if err != nil || cleared.Network.ProxyBypass != nil || !cleared.Equal(Defaults()) {
		t.Errorf("cleared: %v, %v", cleared.Network.ProxyBypass, err)
	}

	data, err := Marshal(s, nil)
	if err != nil || !strings.Contains(string(data), `"proxyBypass": [`) {
		t.Errorf("Marshal: %s, %v", data, err)
	}
	if data, _ := Marshal(Defaults(), nil); strings.Contains(string(data), "proxyBypass") {
		t.Error("an empty list is written")
	}

	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"network": {"proxyBypass": [".ok.test", "10.0.0.0/33"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, report, err := Load(path)
	if err != nil || loaded.Network.ProxyBypass != nil || len(report.Problems) != 1 || report.Problems[0].Details["field"] != "network.proxyBypass" {
		t.Errorf("load: %v, %+v, %v", loaded.Network.ProxyBypass, report.Problems, err)
	}

	policy := LoadPolicy(writePolicy(t, `{"version": 1, "locked": {"network.proxyBypass": ["*.corp"]}}`))
	if len(policy.Problems) != 0 {
		t.Fatalf("problems: %v", policy.Problems)
	}
	st := Open("", nil, WithPolicy(policy))
	if got := st.Settings().Network.ProxyBypass; strings.Join(got, ",") != "*.corp" {
		t.Errorf("locked list in effect: %v", got)
	}
	if _, _, err := st.Update(Patch{Network: NetworkPatch{ProxyBypass: new([]string{})}}); !errors.As(err, &e) || e.Code != CodeLocked {
		t.Errorf("changing a locked list: %v", err)
	}
}

// TestEffectiveProxyBypass (SOCKS-09): the app's bypass list comes with
// every proxy of the settings; NO_PROXY (or no_proxy) is added to the
// ALL_PROXY proxy only, its unusable entries skipped.
func TestEffectiveProxyBypass(t *testing.T) {
	t.Parallel()
	env := map[string]string{"ALL_PROXY": "socks5h://env.lan", "no_proxy": "internal.test, bad*entry ,.lan:8080"}
	getenv := func(k string) string { return env[k] }
	n := Network{ProxyMode: ProxyModeEnv, ProxyBypass: []string{"*.corp"}}
	p, err := n.EffectiveProxy("", getenv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Bypass.String() != ".corp,internal.test,.lan:8080" || p.NoProxyVar != "no_proxy" {
		t.Errorf("env bypass = %q from %q", p.Bypass.String(), p.NoProxyVar)
	}
	if cfg := p.Config(); !cfg.Bypasses("api.internal.test:443") || cfg.Bypasses("example.test:443") || cfg.Proxy != p.URL {
		t.Error("Config does not bypass as the list says")
	}
	n.ProxyMode, n.ProxyURL = ProxyModeManual, "http://proxy.lan:3128"
	p, _ = n.EffectiveProxy("", getenv, nil)
	if p.Bypass.String() != ".corp" || p.NoProxyVar != "" {
		t.Errorf("manual bypass = %q from %q; NO_PROXY is for ALL_PROXY only", p.Bypass.String(), p.NoProxyVar)
	}
	n.ProxyMode = ProxyModeOff
	if p, _ := n.EffectiveProxy("", getenv, nil); !p.Bypass.IsZero() {
		t.Errorf("off has a bypass list: %q", p.Bypass.String())
	}
}
