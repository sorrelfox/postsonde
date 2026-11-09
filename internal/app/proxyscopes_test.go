package app

import (
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"

	coreenvironments "github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/proxyscope"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// TestDialerProxyScopes (SOCKS-08, SOCKS-09, SOCKS-10) through the app's
// dialer: the app's proxy, then the project's scope, then the active
// environment's win in turn; the scopes' passwords come from the local
// side file; a bypassed target connects directly; "off" in the
// environment beats a project proxy; switching the environment (its
// event) closes the idle pools; the connection test uses the scoped proxy.
func TestDialerProxyScopes(t *testing.T) {
	t.Parallel()
	w := newProxyWorld(t)
	other := &netxtest.SOCKS5Server{Hosts: map[string]string{proxyTarget: "127.0.0.1", "skip.example.test": "127.0.0.1"}}
	other.Start(t)

	data := t.TempDir()
	m := coreworkspace.New(data)
	ws, err := m.Create(filepath.Join(t.TempDir(), "shop"), "Shop")
	if err != nil {
		t.Fatal(err)
	}
	envs := coreenvironments.New(ws.Root, ws.LocalDir, false)
	staging, err := envs.Create("Staging")
	if err != nil {
		t.Fatal(err)
	}

	resolver, _ := netxtest.RecordingResolver(map[string]string{proxyTarget: "127.0.0.1", "skip.example.test": "127.0.0.1"})
	resolve := proxyResolver(func() coresettings.Network { return w.settings.Settings().Network }, w.secrets, nil, proxyScopes(m), nil)
	connLog := netlog.New(netlog.Options{})
	dialer, router := newRoutedDialer(slog.New(slog.DiscardHandler), connLog, dialerConfig{
		Direct: netx.Options{Resolver: resolver},
		Proxy:  proxyConfigOf(resolve),
	})
	w.dialer = dialer
	var changes atomic.Int32
	router.OnChange(func() { changes.Add(1) })
	scopes := proxyscope.New(ws.LocalDir)
	set := func(in proxyscope.Input) {
		t.Helper()
		if err := scopes.Set(in, nil); err != nil {
			t.Fatal(err)
		}
		refreshProxyOn(router, func(string, any) {})("proxyscopes:changed", nil)
	}

	// The app's proxy (the SOCKS5 server with a password).
	w.set(t, "manual", "socks5h://alice@"+w.proxy.Addr(), "s3cret")
	if err := w.dial(t, proxyTarget); err != nil || len(w.proxy.Requests()) != 1 {
		t.Fatalf("app proxy: %v, %d CONNECTs", err, len(w.proxy.Requests()))
	}

	// The project's scope: the other server, and a bypass list.
	set(proxyscope.Input{Level: proxyscope.LevelProject, Mode: proxyscope.ModeManual, URL: "socks5h://" + other.Addr(), Bypass: []string{"skip.example.test"}})
	if err := w.dial(t, proxyTarget); err != nil || len(other.Requests()) != 1 || len(w.proxy.Requests()) != 1 {
		t.Fatalf("project scope: %v; CONNECTs app %d, project %d", err, len(w.proxy.Requests()), len(other.Requests()))
	}
	if eff, err := resolve(t.Context()); err != nil || eff.Level != proxyscope.LevelProject {
		t.Errorf("resolve = %+v, %v", eff, err)
	}
	accepted := w.accepted()
	if err := w.dial(t, "skip.example.test"); err != nil || len(other.Requests()) != 1 || w.accepted() != accepted+1 {
		t.Fatalf("bypassed target: %v, %d CONNECTs", err, len(other.Requests()))
	}
	if res := router.Test(netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test", ID: "1"}), "skip.example.test:"+w.port); !res.OK() || !res.Bypassed ||
		res.Proxy.Address() != other.Addr() {
		t.Errorf("connection test = %+v", res)
	}

	// The active environment's scope, with a password from the side file.
	set(proxyscope.Input{Level: proxyscope.LevelEnvironment, EnvironmentID: staging.ID, Mode: proxyscope.ModeManual,
		URL: "socks5h://alice@" + w.proxy.Addr(), Password: new("s3cret")})
	before := changes.Load()
	if err := envs.SetActive(staging.ID); err != nil {
		t.Fatal(err)
	}
	refreshProxyOn(router, func(string, any) {})("environments:changed", nil)
	if changes.Load() != before+1 {
		t.Errorf("switching the environment: %d changes, want 1", changes.Load()-before)
	}
	if err := w.dial(t, proxyTarget); err != nil || len(w.proxy.Requests()) != 2 {
		t.Fatalf("environment scope: %v, %d CONNECTs", err, len(w.proxy.Requests()))
	}
	if creds := w.proxy.Credentials(); creds[len(creds)-1] != "alice:s3cret" {
		t.Errorf("credentials = %q", creds)
	}
	if res := router.Test(netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test", ID: "2"}), proxyTarget+":"+w.port); !res.OK() || res.Proxy.Address() != w.proxy.Addr() {
		t.Errorf("connection test = %+v", res)
	}

	// "off" in the environment beats the project's proxy.
	set(proxyscope.Input{Level: proxyscope.LevelEnvironment, EnvironmentID: staging.ID, Mode: proxyscope.ModeOff})
	accepted, appReqs, projectReqs := w.accepted(), len(w.proxy.Requests()), len(other.Requests())
	if err := w.dial(t, proxyTarget); err != nil || len(w.proxy.Requests()) != appReqs || len(other.Requests()) != projectReqs || w.accepted() != accepted+1 {
		t.Fatalf("environment off: %v", err)
	}

	// No project open: the app's proxy again.
	if _, err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.dial(t, proxyTarget); err != nil || len(w.proxy.Requests()) != appReqs+1 {
		t.Fatalf("no project: %v, %d CONNECTs", err, len(w.proxy.Requests()))
	}
}
