package settings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

const proxySecret = "hunter2secret"

// noSecret fails when the error shows the proxy password anywhere.
func noSecret(t *testing.T, err error) {
	t.Helper()
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		return
	}
	all := err.Error() + e.Hint + strings.Join(errs.Chain(err), " ")
	for _, v := range e.Details {
		all += v
	}
	if strings.Contains(all, proxySecret) {
		t.Errorf("the error shows the proxy password: %q", all)
	}
}

func TestLoadProxySettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		network  string
		wantMode string
		wantURL  string
		problems []string
	}{
		{"defaults: the system proxy", `{}`, "system", "", nil},
		{"env", `{"proxyMode": "env"}`, "env", "", nil},
		{"manual", `{"proxyMode": "manual", "proxyUrl": "socks5h://alice@proxy.lan:1080"}`, "manual", "socks5h://alice@proxy.lan:1080", nil},
		{"off keeps the URL", `{"proxyMode": "off", "proxyUrl": "socks5://proxy.lan:1080"}`, "off", "socks5://proxy.lan:1080", nil},
		{"hand-written password", `{"proxyMode": "manual", "proxyUrl": "socks5h://alice:` + proxySecret + `@proxy.lan"}`, "manual", "socks5h://alice:" + proxySecret + "@proxy.lan", nil},
		{"socks4 is a valid setting", `{"proxyUrl": "socks4a://proxy.lan"}`, "system", "socks4a://proxy.lan", nil},
		{"unknown mode", `{"proxyMode": "pac"}`, "system", "", []string{"network.proxyMode"}},
		{"mode not a string", `{"proxyMode": 1}`, "system", "", []string{"network.proxyMode"}},
		{"invalid URL", `{"proxyMode": "manual", "proxyUrl": "ftp://alice:` + proxySecret + `@proxy.lan"}`, "manual", "", []string{"network.proxyUrl"}},
		{"URL not a string", `{"proxyUrl": 5}`, "system", "", []string{"network.proxyUrl"}},
		{"URL with spaces", `{"proxyUrl": " socks5h://proxy.lan "}`, "system", "", []string{"network.proxyUrl"}},
		{"null is the default", `{"proxyMode": null, "proxyUrl": null}`, "system", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, r, err := Load(writeSettings(t, `{"version": 1, "network": `+tt.network+`}`))
			if err != nil {
				t.Fatal(err)
			}
			if s.Network.ProxyMode != tt.wantMode || s.Network.ProxyURL != tt.wantURL {
				t.Errorf("proxy = %q %q, want %q %q", s.Network.ProxyMode, s.Network.ProxyURL, tt.wantMode, tt.wantURL)
			}
			var fields []string
			for _, p := range r.Problems {
				fields = append(fields, p.Details["field"])
				noSecret(t, p)
			}
			if strings.Join(fields, ",") != strings.Join(tt.problems, ",") {
				t.Errorf("problems %v, want %v", fields, tt.problems)
			}
		})
	}
}

func TestApplyProxySettings(t *testing.T) {
	t.Parallel()
	manual, url := "manual", "socks5h://alice@proxy.lan:1080"
	next, err := Defaults().Apply(Patch{Network: NetworkPatch{ProxyMode: &manual, ProxyURL: &url}})
	if err != nil || next.Network.ProxyMode != "manual" || next.Network.ProxyURL != url {
		t.Fatalf("Apply = %+v, %v", next.Network, err)
	}
	if next.Equal(Defaults()) {
		t.Error("Equal does not compare the proxy settings")
	}
	if err := next.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	for name, p := range map[string]NetworkPatch{
		"mode":         {ProxyMode: new("socks")},
		"URL":          {ProxyURL: new("socks5h://alice:" + proxySecret + "@proxy.lan:0")},
		"URL, no host": {ProxyURL: new("socks5h://alice:" + proxySecret + "@")},
	} {
		_, err := Defaults().Apply(Patch{Network: p})
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeInvalidValue {
			t.Errorf("%s: err = %v, want %s", name, err, CodeInvalidValue)
			continue
		}
		noSecret(t, err)
		if name != "mode" && (e.Details["field"] != "network.proxyUrl" || !strings.Contains(e.Hint, "proxy URL")) {
			t.Errorf("%s: %+v", name, e)
		}
	}

	// The password is never quoted, however the URL is broken.
	for _, v := range []string{
		"socks5h://alice:" + proxySecret + "@proxy.lan:0",
		"alice:" + proxySecret + "@proxy.lan",
		"socks5h://alice:" + proxySecret + "#frag@proxy.lan",
		"socks5h://alice:" + proxySecret + "@proxy.lan/path@x",
	} {
		if m := maskProxyURL(v); strings.Contains(m, proxySecret) || !strings.Contains(m, "…@") {
			t.Errorf("maskProxyURL(%q) = %q", v, m)
		}
	}
}

func TestEffectiveProxy(t *testing.T) {
	t.Parallel()
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	socksEnv := env(map[string]string{"ALL_PROXY": "socks5h://bob:envpass@envproxy.lan:9050"})
	tests := []struct {
		name     string
		network  Network
		password string
		getenv   func(string) string
		want     netx.ProxyURL
		source   string
		errCode  string
	}{
		{"off ignores the environment", Network{ProxyMode: "off", ProxyURL: "socks5h://proxy.lan"}, "pw", socksEnv, netx.ProxyURL{}, "", ""},
		{"env without variables", Network{ProxyMode: "env"}, "", env(nil), netx.ProxyURL{}, "", ""},
		{"env with no getenv", Network{ProxyMode: "env"}, "", nil, netx.ProxyURL{}, "", ""},
		{"env ALL_PROXY", Network{ProxyMode: "env", ProxyURL: "socks5://ignored.lan"}, "stored", socksEnv,
			netx.ProxyURL{Scheme: "socks5h", Host: "envproxy.lan", Port: 9050, Username: "bob", Password: "envpass"}, "ALL_PROXY", ""},
		{"env all_proxy", Network{ProxyMode: "env"}, "", env(map[string]string{"all_proxy": "socks5://127.0.0.1:1081"}),
			netx.ProxyURL{Scheme: "socks5", Host: "127.0.0.1", Port: 1081}, "all_proxy", ""},
		{"env socks4 is passed on", Network{ProxyMode: "env"}, "", env(map[string]string{"ALL_PROXY": "socks4a://p.lan"}),
			netx.ProxyURL{Scheme: "socks4a", Host: "p.lan", Port: 1080}, "ALL_PROXY", ""},
		{"env invalid fails, never direct", Network{ProxyMode: "env"}, "", env(map[string]string{"ALL_PROXY": "alice:" + proxySecret + "@proxy.lan:1080"}),
			netx.ProxyURL{}, "", netx.CodeProxyInvalidURL},
		{"manual with the stored password", Network{ProxyMode: "manual", ProxyURL: "socks5h://alice@proxy.lan"}, "stored", socksEnv,
			netx.ProxyURL{Scheme: "socks5h", Host: "proxy.lan", Port: 1080, Username: "alice", Password: "stored"}, "settings", ""},
		{"manual, the URL's own password wins", Network{ProxyMode: "manual", ProxyURL: "socks5h://alice:inurl@proxy.lan"}, "stored", nil,
			netx.ProxyURL{Scheme: "socks5h", Host: "proxy.lan", Port: 1080, Username: "alice", Password: "inurl"}, "settings", ""},
		{"manual without a user takes no password", Network{ProxyMode: "manual", ProxyURL: "socks5://proxy.lan:1"}, "stored", nil,
			netx.ProxyURL{Scheme: "socks5", Host: "proxy.lan", Port: 1}, "settings", ""},
		{"manual without a URL fails", Network{ProxyMode: "manual"}, "", socksEnv, netx.ProxyURL{}, "", netx.CodeProxyInvalidURL},
		{"manual invalid fails", Network{ProxyMode: "manual", ProxyURL: "socks5h://proxy.lan:0"}, "", nil, netx.ProxyURL{}, "", netx.CodeProxyInvalidURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.network.EffectiveProxy(tt.password, tt.getenv, nil)
			if tt.errCode != "" {
				e, ok := errors.AsType[*errs.Error](err)
				if !ok || e.Code != tt.errCode || e.Kind != errs.InvalidArgument || e.Details["source"] == "" {
					t.Fatalf("err = %v, want %s with a source", err, tt.errCode)
				}
				noSecret(t, err)
				if !got.URL.IsZero() {
					t.Errorf("a failed proxy returned %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.URL != tt.want || got.Source != tt.source {
				t.Errorf("EffectiveProxy = %+v, want %+v from %q", got, tt.want, tt.source)
			}
		})
	}
}

// TestPolicyLocksProxy: the new keys are lockable like any other setting
// (SET-02), an administrator can force "no proxy" or a proxy URL.
func TestPolicyLocksProxy(t *testing.T) {
	t.Parallel()
	policy := LoadPolicy(writePolicy(t, `{"version": 1, "locked": {"network.proxyMode": "manual", "network.proxyUrl": "socks5h://corp.lan:1080"}}`))
	if len(policy.Problems) != 0 {
		t.Fatalf("problems: %v", policy.Problems)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"network": {"proxyMode": "off"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(path, nil, WithPolicy(policy))
	if n := s.Settings().Network; n.ProxyMode != "manual" || n.ProxyURL != "socks5h://corp.lan:1080" {
		t.Fatalf("settings in effect: %+v", n)
	}
	for _, p := range []NetworkPatch{{ProxyMode: new("off")}, {ProxyURL: new("socks5h://home.lan:1080")}} {
		_, changed, err := s.Update(Patch{Network: p})
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeLocked || changed {
			t.Errorf("Update %+v: changed %v, err %v, want %s", p, changed, err, CodeLocked)
		}
	}

	bad := LoadPolicy(writePolicy(t, `{"locked": {"network.proxyMode": "pac", "network.proxyUrl": "ftp://x"}}`))
	if got := problemFields(bad); strings.Join(got, ",") != "network.proxyMode,network.proxyUrl" {
		t.Errorf("problems %v", got)
	}
}
