package sysproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func mustCheck(t *testing.T, s Settings) Proxies {
	t.Helper()
	p, err := s.Check()
	if err != nil {
		t.Fatalf("Check(%+v): %v", s, err)
	}
	return p
}

// TestParseScutil: the top-level proxies of scutil --proxy, not the
// per-interface __SCOPED__ ones; exceptions and ExcludeSimpleHostnames;
// a PAC file; nothing enabled.
func TestParseScutil(t *testing.T) {
	t.Parallel()
	s := ParseScutil(fixture(t, "scutil_manual.txt"))
	if s.Source != SourceMacOS || s.HTTP != "web.proxy.test:3128" || s.HTTPS != "secure.proxy.test:8443" || s.SOCKS != "socks.proxy.test:1080" {
		t.Errorf("proxies = %+v", s)
	}
	if !slices.Equal(s.Bypass, []string{"*.local", "169.254/16", "intranet.corp.example", "<local>"}) || s.PAC != "" || s.AutoDiscovery {
		t.Errorf("bypass = %q, pac %q, wpad %v", s.Bypass, s.PAC, s.AutoDiscovery)
	}
	p := mustCheck(t, s)
	if p.Tunnel.String() != "http://secure.proxy.test:8443" || p.HTTP.String() != "http://web.proxy.test:3128" {
		t.Errorf("tunnel %v, http %v", p.Tunnel, p.HTTP)
	}
	if p.Bypass.String() != ".local,169.254.0.0/16,intranet.corp.example,<local>" || len(p.Skipped) != 0 {
		t.Errorf("bypass %q, skipped %q", p.Bypass.String(), p.Skipped)
	}

	// Only SOCKS (an IPv6 host without a port): every connection through it.
	s = ParseScutil(fixture(t, "scutil_socks_only.txt"))
	if s.HTTP != "" || s.SOCKS != "[fd00::1]" {
		t.Errorf("socks only = %+v", s)
	}
	p = mustCheck(t, s)
	if p.Tunnel.String() != "socks5h://[fd00::1]:1080" || p.HTTP != p.Tunnel {
		t.Errorf("socks only: tunnel %v, http %v", p.Tunnel, p.HTTP)
	}

	s = ParseScutil(fixture(t, "scutil_pac.txt"))
	if s.PAC != "http://wpad.corp.example/proxy.pac" || !s.AutoDiscovery {
		t.Errorf("pac = %+v", s)
	}
	if _, err := s.Check(); codeOf(err) != CodePACUnsupported || errs.KindOf(err) != errs.Unsupported {
		t.Errorf("a PAC file alone: %v, want %s", err, CodePACUnsupported)
	}

	s = ParseScutil(fixture(t, "scutil_none.txt"))
	if !s.IsZero() {
		t.Errorf("none = %+v", s)
	}
	if p := mustCheck(t, s); !p.Config().IsZero() {
		t.Errorf("none: %+v", p)
	}
}

// TestParseWindows: one proxy for every protocol, a proxy per protocol
// (socks= as SOCKS4a), the override list with <local> and IP wildcards,
// ProxyEnable off, and a PAC script.
func TestParseWindows(t *testing.T) {
	t.Parallel()
	s := ParseWindows(RegistryValues{ProxyEnable: true, ProxyServer: "proxy.corp.example:8080", ProxyOverride: "<local>;*.corp.example;10.*;192.168.*;172.16.1.*;*contoso*"})
	p := mustCheck(t, s)
	if p.Tunnel.String() != "http://proxy.corp.example:8080" || p.HTTP != p.Tunnel {
		t.Errorf("one proxy: tunnel %v, http %v", p.Tunnel, p.HTTP)
	}
	if p.Bypass.String() != "<local>,.corp.example,10.0.0.0/8,192.168.0.0/16,172.16.1.0/24" || !slices.Equal(p.Skipped, []string{"*contoso*"}) {
		t.Errorf("bypass %q, skipped %q", p.Bypass.String(), p.Skipped)
	}
	for _, target := range []string{"intranet:80", "git.corp.example:443", "10.9.9.9:443", "192.168.1.2:80", "172.16.1.40:22"} {
		if !p.Bypass.Match(target) {
			t.Errorf("%s is not bypassed", target)
		}
	}

	s = ParseWindows(RegistryValues{ProxyEnable: true, ProxyServer: "http=web.proxy.test:3128; https=https://secure.proxy.test:443;ftp=ftp.proxy.test:21;socks=socks.proxy.test:1080"})
	p = mustCheck(t, s)
	if p.HTTP.String() != "http://web.proxy.test:3128" || p.Tunnel.String() != "https://secure.proxy.test:443" {
		t.Errorf("per protocol: tunnel %v, http %v", p.Tunnel, p.HTTP)
	}
	s = ParseWindows(RegistryValues{ProxyEnable: true, ProxyServer: "socks=socks.proxy.test:1080"})
	if p := mustCheck(t, s); p.Tunnel.String() != "socks4a://socks.proxy.test:1080" || p.HTTP != p.Tunnel {
		t.Errorf("socks only: %+v", p)
	}
	s = ParseWindows(RegistryValues{ProxyEnable: true, ProxyServer: "socks=socks5://socks.proxy.test:1080"})
	if p := mustCheck(t, s); p.Tunnel.Scheme != "socks5" {
		t.Errorf("explicit socks5: %v", p.Tunnel)
	}

	// Off: the stale server is not used.
	s = ParseWindows(RegistryValues{ProxyServer: "stale.proxy.test:8080"})
	if !s.IsZero() {
		t.Errorf("disabled = %+v", s)
	}
	s = ParseWindows(RegistryValues{AutoConfigURL: "http://wpad/wpad.dat"})
	if _, err := s.Check(); codeOf(err) != CodePACUnsupported {
		t.Errorf("setup script: %v", err)
	}
	// A PAC file next to a manual proxy: the manual proxy is used.
	s = ParseWindows(RegistryValues{ProxyEnable: true, ProxyServer: "p.test:1", AutoConfigURL: "http://wpad/wpad.dat"})
	if p := mustCheck(t, s); p.Tunnel.String() != "http://p.test:1" {
		t.Errorf("PAC and manual: %+v", p)
	}
	// An unusable proxy fails, never going direct.
	s = ParseWindows(RegistryValues{ProxyEnable: true, ProxyServer: "http=bad host:x"})
	if _, err := s.Check(); codeOf(err) != CodeInvalidProxy {
		t.Errorf("invalid: %v", err)
	}
}

// TestRegistryDecoding: REG_SZ as UTF-16LE up to the NUL, REG_DWORD and
// the old REG_BINARY ProxyEnable.
func TestRegistryDecoding(t *testing.T) {
	t.Parallel()
	sz := []byte{'p', 0, 'r', 0, 0xe9, 0, ':', 0, '1', 0, 0, 0, 'x', 0}
	if got := regString(sz); got != "pré:1" {
		t.Errorf("regString = %q", got)
	}
	if regString(nil) != "" || regString([]byte{1}) != "" {
		t.Error("short REG_SZ")
	}
	if regDWORD([]byte{1, 0, 0, 0}) != 1 || regDWORD([]byte{0, 0, 0, 0}) != 0 || regDWORD([]byte{1}) != 1 || regDWORD(nil) != 0 {
		t.Error("regDWORD")
	}
}

// TestParseGSettings: GNOME's manual mode with credentials and
// ignore-hosts, use-same-proxy, auto (a PAC file) and none.
func TestParseGSettings(t *testing.T) {
	t.Parallel()
	s, ok := ParseGSettings(fixture(t, "gsettings_manual.txt"))
	if !ok || s.HTTP != "web.proxy.test:3128" || s.HTTPS != "secure.proxy.test:3129" || s.SOCKS != "socks.proxy.test:1080" ||
		s.HTTPUser != "carol" || s.HTTPPassword != "pa ss" {
		t.Fatalf("manual = %+v, %v", s, ok)
	}
	if !slices.Equal(s.Bypass, []string{"localhost", "127.0.0.0/8", "::1", "*.corp.example", "it's-odd"}) {
		t.Errorf("ignore-hosts = %q", s.Bypass)
	}
	p := mustCheck(t, s)
	if p.HTTP.String() != "http://carol@web.proxy.test:3128" || p.HTTP.Password != "pa ss" || p.Tunnel.String() != "http://secure.proxy.test:3129" {
		t.Errorf("tunnel %v, http %v", p.Tunnel, p.HTTP)
	}
	if p.Tunnel.Username != "" {
		t.Error("the HTTP proxy's credentials went to another proxy")
	}
	if !slices.Equal(p.Skipped, []string{"it's-odd"}) {
		t.Errorf("skipped = %q", p.Skipped)
	}

	s, ok = ParseGSettings(fixture(t, "gsettings_same_proxy.txt"))
	if p := mustCheck(t, s); !ok || p.Tunnel.String() != "http://web.proxy.test:8080" || p.HTTP != p.Tunnel || len(s.Bypass) != 0 {
		t.Errorf("use-same-proxy: %+v, %+v", s, p)
	}

	s, ok = ParseGSettings(fixture(t, "gsettings_auto.txt"))
	if _, err := s.Check(); !ok || s.PAC != "http://wpad.corp.example/proxy.pac" || codeOf(err) != CodePACUnsupported {
		t.Errorf("auto: %+v, %v", s, err)
	}
	if _, ok := ParseGSettings(fixture(t, "gsettings_none.txt")); ok {
		t.Error("mode none is taken as a setting")
	}
	if _, ok := ParseGSettings(""); ok {
		t.Error("no output is taken as a setting")
	}
}

// TestFromEnv: the Linux variables, lower case first, a value without a
// scheme as an HTTP proxy, all_proxy as the fallback of both.
func TestFromEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"http_proxy":  "web.proxy.test:3128",
		"HTTP_PROXY":  "ignored.test:1",
		"HTTPS_PROXY": "http://alice:pw@secure.proxy.test:3129",
		"no_proxy":    ".corp.example,10.0.0.0/8",
	}
	s := FromEnv(func(k string) string { return env[k] })
	p := mustCheck(t, s)
	if p.HTTP.String() != "http://web.proxy.test:3128" || p.Tunnel.String() != "http://alice@secure.proxy.test:3129" || p.Tunnel.Password != "pw" {
		t.Errorf("tunnel %v, http %v", p.Tunnel, p.HTTP)
	}
	if p.Bypass.String() != ".corp.example,10.0.0.0/8" {
		t.Errorf("bypass %q", p.Bypass.String())
	}
	s = FromEnv(func(k string) string { return map[string]string{"ALL_PROXY": "socks5h://s.test:1080"}[k] })
	if p := mustCheck(t, s); p.Tunnel.String() != "socks5h://s.test:1080" || p.HTTP != p.Tunnel {
		t.Errorf("all_proxy: %+v", p)
	}
	if s := FromEnv(nil); !s.IsZero() || s.Source != SourceEnv {
		t.Errorf("no env: %+v", s)
	}
}

// TestReader: which settings each system reads, with fixtures standing in
// for the commands and the registry; GNOME falls back to the environment
// without gsettings, when it fails and in mode none.
func TestReader(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var ran []string
	run := func(out string, err error) func(context.Context, string, ...string) ([]byte, error) {
		return func(_ context.Context, name string, args ...string) ([]byte, error) {
			ran = append(ran, name+" "+strings.Join(args, " "))
			return []byte(out), err
		}
	}
	env := func(k string) string { return map[string]string{"https_proxy": "env.proxy.test:3128"}[k] }

	s, err := Reader{GOOS: "darwin", Run: run(fixture(t, "scutil_manual.txt"), nil)}.Read(ctx)
	if err != nil || s.Source != SourceMacOS || s.HTTPS != "secure.proxy.test:8443" || ran[0] != ScutilPath+" --proxy" {
		t.Errorf("darwin: %+v, %v, ran %q", s, err, ran)
	}
	if _, err := (Reader{GOOS: "darwin", Run: run("", errors.New("exit status 1"))}).Read(ctx); codeOf(err) != CodeReadFailed {
		t.Errorf("darwin failure: %v", err)
	}

	s, err = Reader{GOOS: "windows", Registry: func() (RegistryValues, error) {
		return RegistryValues{ProxyEnable: true, ProxyServer: "proxy.test:8080"}, nil
	}}.Read(ctx)
	if err != nil || s.Source != SourceWindows || s.HTTP != "proxy.test:8080" {
		t.Errorf("windows: %+v, %v", s, err)
	}
	if _, err := (Reader{GOOS: "windows", Registry: func() (RegistryValues, error) { return RegistryValues{}, errors.New("denied") }}).Read(ctx); codeOf(err) != CodeReadFailed {
		t.Errorf("windows failure: %v", err)
	}

	found := func(string) (string, error) { return "/usr/bin/gsettings", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	ran = nil
	s, _ = Reader{GOOS: "linux", LookPath: found, Run: run(fixture(t, "gsettings_manual.txt"), nil), Getenv: env}.Read(ctx)
	if s.Source != SourceGNOME || ran[0] != "/usr/bin/gsettings list-recursively org.gnome.system.proxy" {
		t.Errorf("gnome: %+v, ran %q", s, ran)
	}
	for name, r := range map[string]Reader{
		"no gsettings":     {GOOS: "linux", LookPath: missing, Run: run("", nil), Getenv: env},
		"gsettings fails":  {GOOS: "linux", LookPath: found, Run: run("", errors.New("no session bus")), Getenv: env},
		"GNOME mode none":  {GOOS: "linux", LookPath: found, Run: run(fixture(t, "gsettings_none.txt"), nil), Getenv: env},
		"another system":   {GOOS: "freebsd", Getenv: env},
		"no command known": {GOOS: "linux", Getenv: env},
	} {
		s, err := r.Read(ctx)
		if err != nil || s.Source != SourceEnv || s.HTTPS != "env.proxy.test:3128" {
			t.Errorf("%s: %+v, %v; want the environment", name, s, err)
		}
	}
}

// TestPerSchemeChoice (NET-01): which OS proxy each kind of connection
// takes: http:// requests the HTTP proxy, everything else the HTTPS one,
// either falling back to SOCKS, then all_proxy; with only a web proxy,
// other connections go direct.
func TestPerSchemeChoice(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		s            Settings
		tunnel, http string
	}{
		{"all three", Settings{HTTP: "h:1", HTTPS: "s:2", SOCKS: "k:3"}, "http://s:2", "http://h:1"},
		{"web and SOCKS", Settings{HTTP: "h:1", SOCKS: "k:3"}, "socks5h://k:3", "http://h:1"},
		{"secure web only", Settings{HTTPS: "s:2"}, "http://s:2", ""},
		{"web only", Settings{HTTP: "h:1"}, "", "http://h:1"},
		{"SOCKS only, Windows", Settings{SOCKS: "k:3", SOCKSScheme: "socks4a"}, "socks4a://k:3", "socks4a://k:3"},
		{"socks:// URL", Settings{SOCKS: "socks://k:3"}, "socks5h://k:3", "socks5h://k:3"},
		{"all_proxy", Settings{All: "socks5://a:4", HTTP: "h:1"}, "socks5://a:4", "http://h:1"},
		{"https proxy URL", Settings{HTTPS: "https://s:443"}, "https://s:443", ""},
	} {
		p := mustCheck(t, tt.s)
		if p.Tunnel.String() != tt.tunnel || p.HTTP.String() != tt.http {
			t.Errorf("%s: tunnel %q, http %q; want %q, %q", tt.name, p.Tunnel.String(), p.HTTP.String(), tt.tunnel, tt.http)
		}
		c := p.Config()
		if !c.SplitHTTP || c.Proxy != p.Tunnel || c.ForHTTP() != p.HTTP {
			t.Errorf("%s: Config = %+v", tt.name, c)
		}
	}
}

func TestCache(t *testing.T) {
	t.Parallel()
	var reads atomic.Int32
	c := NewCache(func(context.Context) (Settings, error) {
		reads.Add(1)
		return Settings{HTTP: "h:1"}, nil
	}, time.Minute)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	for range 3 {
		if s, err := c.Get(t.Context()); err != nil || s.HTTP != "h:1" {
			t.Fatal(s, err)
		}
	}
	if reads.Load() != 1 {
		t.Errorf("%d reads, want 1", reads.Load())
	}
	c.Invalidate()
	_, _ = c.Get(t.Context())
	now = now.Add(2 * time.Minute)
	_, _ = c.Get(t.Context())
	if reads.Load() != 3 {
		t.Errorf("%d reads after Invalidate and the TTL, want 3", reads.Load())
	}
}
