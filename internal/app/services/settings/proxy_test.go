package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
)

const secretPassword = "hunter2secret"

// proxyService is a service over a settings file and a secret store in a
// temp directory, with env as the environment.
func proxyService(t *testing.T, content string, env map[string]string) (*SettingsService, *recorder, string, *appsecrets.Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, core.FileName)
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	secrets := appsecrets.New(filepath.Join(dir, "data"))
	rec := &recorder{}
	svc := New(core.Open(path, nil), rec.emit, nil, WithSecrets(secrets), WithProxyEnv(func(k string) string { return env[k] }))
	return svc, rec, path, secrets
}

// noPassword fails when b (a file, a DTO, an error) holds the password.
func noPassword(t *testing.T, what string, b []byte) {
	t.Helper()
	if strings.Contains(string(b), secretPassword) {
		t.Errorf("%s holds the proxy password:\n%s", what, b)
	}
}

// TestSetProxy (SOCKS-04): the URL is checked, the password goes to the
// local secret storage and never to settings.json or back to the UI, and
// the DTO says which proxy connections use.
func TestSetProxy(t *testing.T) {
	t.Parallel()
	svc, rec, path, secrets := proxyService(t, "", nil)
	ctx := t.Context()

	got, err := svc.Proxy(ctx)
	// The default is the OS's settings (NET-01); without a reader, none.
	if err != nil || got.Mode != "system" || got.URL != "" || got.Effective != "" || got.Error != nil || got.EnvVar != "" || got.System == nil {
		t.Fatalf("defaults: %+v, %v", got, err)
	}

	// Refused, nothing saved.
	for name, in := range map[string]ProxyInputDTO{
		"no URL":            {Mode: "manual"},
		"bad URL":           {Mode: "manual", URL: "alice:" + secretPassword + "@proxy.lan:1080"},
		"bad port":          {Mode: "env", URL: "socks5h://alice:" + secretPassword + "@proxy.lan:0"},
		"password, no user": {Mode: "manual", URL: "socks5h://proxy.lan", Password: ptr(secretPassword)},
		"unknown mode":      {Mode: "pac"},
		"long password":     {Mode: "manual", URL: "socks5h://alice@proxy.lan", Password: ptr(strings.Repeat("p", 256))},
	} {
		_, err := svc.SetProxy(ctx, in)
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Details["field"] == "" {
			t.Errorf("%s: err = %v, want invalid_argument with a field", name, err)
		}
		b, _ := json.Marshal(apperr.ToDTO(err))
		noPassword(t, name+" error", b)
	}
	if _, err := os.Stat(path); err == nil || len(rec.all()) != 0 {
		t.Fatalf("a refused change was saved (%d events)", len(rec.all()))
	}

	// A password typed into the URL moves to the secret storage.
	got, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "manual", URL: " socks5h://alice:" + secretPassword + "@proxy.lan "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "manual" || got.URL != "socks5h://alice@proxy.lan:1080" || !got.HasPassword ||
		got.Effective != "socks5h://alice@proxy.lan:1080" || !got.RemoteDNS || got.Error != nil {
		t.Fatalf("manual: %+v", got)
	}
	if v, _ := secrets.Get(appsecrets.ProxyPasswordKey); v != secretPassword {
		t.Errorf("stored password = %q", v)
	}
	file, _ := os.ReadFile(path)
	noPassword(t, "settings.json", file)
	if !strings.Contains(string(file), `"proxyUrl": "socks5h://alice@proxy.lan:1080"`) || !strings.Contains(string(file), `"proxyMode": "manual"`) {
		t.Errorf("settings.json:\n%s", file)
	}
	b, _ := json.Marshal(got)
	noPassword(t, "ProxyDTO", b)
	if ev := rec.all(); len(ev) != 1 || ev[0].Source != SourceApp {
		t.Errorf("events = %+v", ev)
	}

	// A null password keeps it; switching to socks5 and back to env keeps
	// the URL for later.
	if got, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "manual", URL: "socks5://alice@proxy.lan:1081"}); err != nil || !got.HasPassword || got.RemoteDNS {
		t.Fatalf("keep: %+v, %v", got, err)
	}
	if got, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "off", URL: got.URL}); err != nil || got.Mode != "off" || got.URL != "socks5://alice@proxy.lan:1081" || got.Effective != "" {
		t.Fatalf("off: %+v, %v", got, err)
	}
	if v, _ := secrets.Get(appsecrets.ProxyPasswordKey); v != secretPassword {
		t.Errorf("password lost when switching mode: %q", v)
	}

	// A password typed into the field only: an event even though the
	// settings file does not change.
	n := len(rec.all())
	if got, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "off", URL: got.URL, Password: ptr("other")}); err != nil || !got.HasPassword {
		t.Fatalf("new password: %+v, %v", got, err)
	}
	if v, _ := secrets.Get(appsecrets.ProxyPasswordKey); v != "other" || len(rec.all()) != n+1 {
		t.Errorf("password %q, %d new events", v, len(rec.all())-n)
	}

	// "" removes it; a URL without a user forgets it.
	if got, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "manual", URL: got.URL, Password: ptr("")}); err != nil || got.HasPassword {
		t.Fatalf("remove: %+v, %v", got, err)
	}
	_ = secrets.Set(appsecrets.ProxyPasswordKey, "again")
	if got, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "manual", URL: "socks5h://[::1]:9050"}); err != nil || got.HasPassword || got.URL != "socks5h://[::1]:9050" {
		t.Fatalf("no user: %+v, %v", got, err)
	}
	if v, _ := secrets.Get(appsecrets.ProxyPasswordKey); v != "" {
		t.Errorf("password kept without a user: %q", v)
	}

	// SOCKS4a (SOCKS-06) is used, resolves names itself, and takes a
	// user ID but no password: a stored one is dropped, a typed one refused.
	_ = secrets.Set(appsecrets.ProxyPasswordKey, "stale")
	if got, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "manual", URL: "socks4a://bob@proxy.lan"}); err != nil || got.Error != nil ||
		got.Effective != "socks4a://bob@proxy.lan:1080" || !got.RemoteDNS || got.HasPassword {
		t.Fatalf("socks4a: %+v, %v", got, err)
	}
	if v, _ := secrets.Get(appsecrets.ProxyPasswordKey); v != "" {
		t.Errorf("a SOCKS4 proxy kept a password: %q", v)
	}
	if _, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "manual", URL: "socks4a://bob@proxy.lan", Password: ptr("pw")}); errs.KindOf(err) != errs.InvalidArgument {
		t.Fatal("a SOCKS4 password was accepted")
	}
	if got, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "manual", URL: "socks4://proxy.lan"}); err != nil || got.Error != nil || got.RemoteDNS {
		t.Fatalf("socks4: %+v, %v", got, err)
	}
	// An HTTP proxy (NET-02) is used, and resolves names itself.
	if got, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "manual", URL: "https://bob:pw@proxy.lan:8443"}); err != nil || got.Error != nil ||
		got.Effective != "https://bob@proxy.lan:8443" || !got.RemoteDNS || !got.HasPassword {
		t.Fatalf("https proxy: %+v, %v", got, err)
	}
	// A user name with a colon cannot be sent as Basic credentials.
	if _, err = svc.SetProxy(ctx, ProxyInputDTO{Mode: "manual", URL: "http://a%3Ab@proxy.lan:3128"}); err == nil {
		t.Fatal("a user name with a colon was accepted")
	}
}

// TestProxyFromEnvironment: the DTO shows ALL_PROXY, without its password,
// and an invalid one as the error connections fail with.
func TestProxyFromEnvironment(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := proxyService(t, `{"network": {"proxyMode": "env"}}`, map[string]string{"ALL_PROXY": "socks5h://bob:" + secretPassword + "@envproxy.lan:9050"})
	got, err := svc.Proxy(t.Context())
	if err != nil || got.EnvVar != "ALL_PROXY" || got.EnvURL != "socks5h://bob@envproxy.lan:9050" ||
		got.Effective != "socks5h://bob@envproxy.lan:9050" || !got.RemoteDNS || got.Error != nil {
		t.Fatalf("env: %+v, %v", got, err)
	}
	b, _ := json.Marshal(got)
	noPassword(t, "ProxyDTO", b)

	// "off" ignores it, but still shows it.
	if got, err = svc.SetProxy(t.Context(), ProxyInputDTO{Mode: "off"}); err != nil || got.Effective != "" || got.EnvVar != "ALL_PROXY" {
		t.Fatalf("off: %+v, %v", got, err)
	}

	bad, _, _, _ := proxyService(t, `{"network": {"proxyMode": "env"}}`, map[string]string{"all_proxy": "bob:" + secretPassword + "@envproxy.lan"})
	got, _ = bad.Proxy(t.Context())
	if got.EnvVar != "all_proxy" || got.EnvURL != "" || got.Error == nil || got.Error.Code != netx.CodeProxyInvalidURL || got.Error.Details["source"] != "all_proxy" {
		t.Fatalf("invalid env: %+v", got)
	}
	b, _ = json.Marshal(got)
	noPassword(t, "ProxyDTO", b)
}

// TestProxyHandWrittenPassword: a password written into settings.json by
// hand is used and reported, and moves to the secret storage on the next
// change from the screen.
func TestProxyHandWrittenPassword(t *testing.T) {
	t.Parallel()
	svc, _, path, secrets := proxyService(t, `{"network": {"proxyMode": "manual", "proxyUrl": "socks5h://alice:`+secretPassword+`@proxy.lan:1080"}}`, nil)
	got, err := svc.Proxy(t.Context())
	if err != nil || got.URL != "socks5h://alice@proxy.lan:1080" || !got.HasPassword || got.Effective != got.URL {
		t.Fatalf("hand-written: %+v, %v", got, err)
	}
	b, _ := json.Marshal(got)
	noPassword(t, "ProxyDTO", b)
	if _, err := svc.SetProxy(t.Context(), ProxyInputDTO{Mode: "manual", URL: got.URL}); err != nil {
		t.Fatal(err)
	}
	if v, _ := secrets.Get(appsecrets.ProxyPasswordKey); v != secretPassword {
		t.Errorf("stored password = %q", v)
	}
	file, _ := os.ReadFile(path)
	noPassword(t, "settings.json", file)
}

// TestProxyLocked (SET-02): locked proxy settings are refused before a
// password is stored.
func TestProxyLocked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	secrets := appsecrets.New(filepath.Join(dir, "data"))
	svc, rec, _ := policyService(t, `{"locked": {"network.proxyMode": "manual", "network.proxyUrl": "socks5h://corp.lan:1080"}}`, "", WithSecrets(secrets))
	got, err := svc.Proxy(t.Context())
	if err != nil || got.Mode != "manual" || got.Effective != "socks5h://corp.lan:1080" {
		t.Fatalf("locked: %+v, %v", got, err)
	}
	for name, in := range map[string]ProxyInputDTO{
		"mode":     {Mode: "off", URL: "socks5h://corp.lan:1080"},
		"URL":      {Mode: "manual", URL: "socks5h://alice@home.lan:1080", Password: ptr(secretPassword)},
		"password": {Mode: "manual", URL: "socks5h://corp.lan:1080", Password: ptr("")},
	} {
		_, err := svc.SetProxy(t.Context(), in)
		var e *errs.Error
		if name == "password" {
			// No user, no password: nothing changes, nothing is refused.
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if !errors.As(err, &e) || e.Code != core.CodeLocked {
			t.Errorf("%s: err = %v, want %s", name, err, core.CodeLocked)
		}
	}
	if v, _ := secrets.Get(appsecrets.ProxyPasswordKey); v != "" || len(rec.all()) != 0 {
		t.Errorf("stored %q, %d events after refused changes", v, len(rec.all()))
	}
}
