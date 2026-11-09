package settings

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/sysproxy"
)

// TestProxySystemMode (NET-01): the default "system" mode shows what the
// OS settings say, which proxy each kind of connection uses, and why they
// cannot be used (a PAC file); another mode does not read them.
func TestProxySystemMode(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	os := sysproxy.Settings{Source: sysproxy.SourceMacOS, HTTP: "web.proxy.test:3128", SOCKS: "socks.proxy.test:1080", SOCKSScheme: "socks5h",
		Bypass: []string{"*.local", "169.254/16", "bad*entry"}}
	var readErr error
	reads := 0
	read := func() (sysproxy.Settings, error) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		return os, readErr
	}
	dir := t.TempDir()
	svc := New(core.Open(filepath.Join(dir, core.FileName), nil), (&recorder{}).emit, nil,
		WithSecrets(appsecrets.New(filepath.Join(dir, "data"))), WithSystemProxy(read))

	got, err := svc.Proxy(t.Context())
	if err != nil || got.Mode != "system" || got.Error != nil || got.System == nil {
		t.Fatalf("Proxy = %+v, %v", got, err)
	}
	if got.Effective != "socks5h://socks.proxy.test:1080" || got.EffectiveHTTP != "http://web.proxy.test:3128" || !got.RemoteDNS {
		t.Errorf("effective %q, http %q", got.Effective, got.EffectiveHTTP)
	}
	sys := got.System
	if sys.Source != "macos" || sys.HTTP != "http://web.proxy.test:3128" || sys.HTTPS != "" || sys.SOCKS != "socks.proxy.test:1080" ||
		strings.Join(sys.Bypass, ",") != ".local,169.254.0.0/16" || strings.Join(sys.Skipped, ",") != "bad*entry" {
		t.Errorf("system = %+v", sys)
	}

	mu.Lock()
	os = sysproxy.Settings{Source: sysproxy.SourceWindows, PAC: "http://wpad/wpad.dat"}
	mu.Unlock()
	got, _ = svc.Proxy(t.Context())
	if got.Error == nil || got.Error.Code != sysproxy.CodePACUnsupported || got.Effective != "" || got.System.PAC != "http://wpad/wpad.dat" {
		t.Errorf("PAC: %+v", got)
	}

	mu.Lock()
	readErr = errors.New("scutil failed")
	mu.Unlock()
	if got, _ = svc.Proxy(t.Context()); got.Error == nil || got.Error.Details["source"] != "system" {
		t.Errorf("read failure: %+v", got)
	}

	// Another mode does not read the OS settings.
	if _, err := svc.SetProxy(t.Context(), ProxyInputDTO{Mode: "off"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	before := reads
	mu.Unlock()
	if got, _ = svc.Proxy(t.Context()); got.System != nil || got.Error != nil {
		t.Errorf("off: %+v", got)
	}
	mu.Lock()
	after := reads
	mu.Unlock()
	if after != before {
		t.Error("the OS settings were read in another mode")
	}
	if _, err := svc.SetProxy(t.Context(), ProxyInputDTO{Mode: "system"}); err != nil {
		t.Errorf("back to system: %v", err)
	}
}
