package app

import (
	"log/slog"
	"net"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/settings"
	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// TestProxyConnectionTest (SOCKS-11) through the app's wiring: the
// settings screen's "Test connection" reaches the target through the
// proxy in effect (with the stored password), reports the hop, is in the
// connection log as the user's action with the proxy named, goes through
// the proxy even for a loopback target, and reports each failure with its
// step.
func TestProxyConnectionTest(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)
	tests := []struct {
		name     string
		mode     string
		url      func(w *proxyWorld) string
		password string
		host     string
		wantStep string
		wantCode string
	}{
		{name: "socks5h", mode: "manual", url: func(w *proxyWorld) string { return "socks5h://alice@" + w.proxy.Addr() },
			password: "s3cret", host: proxyTarget},
		{name: "loopback target", mode: "manual", url: func(w *proxyWorld) string { return "socks5h://alice@" + w.proxy.Addr() },
			password: "s3cret", host: "127.0.0.1"},
		{name: "wrong password", mode: "manual", url: func(w *proxyWorld) string { return "socks5h://alice@" + w.proxy.Addr() },
			password: "nope", host: proxyTarget, wantStep: netx.ProxyStepAuth, wantCode: netx.CodeSOCKSAuthFailed},
		{name: "unknown host", mode: "manual", url: func(w *proxyWorld) string { return "socks5h://alice@" + w.proxy.Addr() },
			password: "s3cret", host: "nowhere.example.test", wantStep: netx.ProxyStepTarget, wantCode: netx.CodeSOCKSHostUnreachable},
		{name: "no proxy", mode: "off", url: func(*proxyWorld) string { return "" }, host: proxyTarget,
			wantStep: netx.ProxyStepSetup, wantCode: netx.CodeProxyNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := newProxyWorld(t)
			w.set(t, tt.mode, tt.url(w), tt.password)
			svc := settings.New(w.settings, func(string, any) {}, logger,
				settings.WithSecrets(w.secrets), settings.WithProxyTester(proxyTester(w.router, w.connLog, logger)))
			target := net.JoinHostPort(tt.host, w.port)
			res, err := svc.TestProxy(t.Context(), settings.ProxyTestInputDTO{Target: " " + target + " "})
			if err != nil {
				t.Fatal(err)
			}
			if res.Target != target || res.DurationMs < 0 || (res.OK && res.DurationMs <= 0) {
				t.Errorf("result = %+v", res)
			}
			if tt.wantCode != "" {
				if res.OK || res.Step != tt.wantStep || res.Error == nil || res.Error.Code != tt.wantCode {
					t.Fatalf("result = %+v, error %+v; want step %s, code %s", res, res.Error, tt.wantStep, tt.wantCode)
				}
				return
			}
			if !res.OK || res.Error != nil || res.Step != "" || res.Proxy != "socks5h://alice@"+w.proxy.Addr() {
				t.Fatalf("result = %+v, error %+v", res, res.Error)
			}
			if h := res.Hop; h == nil || h.Version != "SOCKS5" || h.Auth != netx.AuthUserPass || !h.RemoteDNS ||
				h.Reply == nil || *h.Reply != 0 || h.ReplyText != "succeeded" || h.ProxyAddr != w.proxy.Addr() {
				t.Errorf("hop = %+v", res.Hop)
			}
			if reqs := w.proxy.Requests(); len(reqs) != 1 || reqs[0].Host != tt.host {
				t.Errorf("proxy CONNECTs = %+v, want one to %s", reqs, tt.host)
			}
			conns := w.connLog.Conns()
			if len(conns) != 1 || conns[0].Address != target || conns[0].Via != "socks5h://"+w.proxy.Addr() ||
				conns[0].Op.Kind != settings.UserOpProxyTest || conns[0].State != netlog.Closed {
				t.Errorf("connection log = %+v", conns)
			}
		})
	}
}
