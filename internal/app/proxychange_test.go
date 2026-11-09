package app

import (
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/settings"
	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// TestProxyChangeClosesIdlePools (SOCKS-05): a kept-alive connection
// opened through one proxy is not reused after the proxy changes on the
// settings screen: the settings:changed event makes the router read the
// proxy again (refreshProxyOnSettings) and its OnChange hook closes the
// idle pools, so the next request goes through the new proxy. Without
// the hook (the control) the old connection, and the old proxy, would be
// used again.
func TestProxyChangeClosesIdlePools(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(origin.Close)
	url := "http://" + net.JoinHostPort(e2eHost, port(origin.Listener.Addr())) + "/"

	for _, hooked := range []bool{true, false} {
		name := "hooked"
		if !hooked {
			name = "control, no hook"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := coresettings.Open("", nil)
			secrets := appsecrets.New("")
			logger := slog.New(slog.DiscardHandler)
			dialer, router := newRoutedDialer(logger, netlog.New(netlog.Options{}), dialerConfig{
				Proxy:   proxySource(func() coresettings.Network { return store.Settings().Network }, secrets, nil),
				RootCAs: func() (*x509.CertPool, error) { return nil, nil },
			})
			client := httpclient.New(dialer, httpclient.TransportOptions{})
			t.Cleanup(client.CloseIdleConnections)
			if hooked {
				router.OnChange(client.CloseIdleConnections)
			}
			svc := settings.New(store, refreshProxyOnSettings(router, nil, func(string, any) {}), logger, settings.WithSecrets(secrets))

			a := &netxtest.SOCKS5Server{Hosts: map[string]string{e2eHost: "127.0.0.1"}}
			b := &netxtest.SOCKS5Server{Hosts: map[string]string{e2eHost: "127.0.0.1"}}
			a.Start(t)
			b.Start(t)
			ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test.send", ID: "1"})
			send := func() *httpclient.Response {
				t.Helper()
				resp, err := client.Send(ctx, httpclient.Request{Method: http.MethodGet, URL: url, Settings: httpclient.DefaultSettings()})
				if err != nil {
					t.Fatal(err)
				}
				return resp
			}

			if _, err := svc.SetProxy(t.Context(), settings.ProxyInputDTO{Mode: "manual", URL: "socks5h://" + a.Addr()}); err != nil {
				t.Fatal(err)
			}
			if resp := send(); resp.Conn.Via != "socks5h://"+a.Addr() {
				t.Fatalf("first request via %q, want proxy A", resp.Conn.Via)
			}
			if _, err := svc.SetProxy(t.Context(), settings.ProxyInputDTO{Mode: "manual", URL: "socks5h://" + b.Addr()}); err != nil {
				t.Fatal(err)
			}
			resp := send()
			if hooked {
				if resp.Conn.Reused || resp.Conn.Via != "socks5h://"+b.Addr() || len(b.Requests()) != 1 || len(a.Requests()) != 1 {
					t.Errorf("after the change: reused %v via %q; CONNECTs A %d, B %d", resp.Conn.Reused, resp.Conn.Via, len(a.Requests()), len(b.Requests()))
				}
				return
			}
			// The control: the stale connection through A is reused.
			if !resp.Conn.Reused || resp.Conn.Via != "socks5h://"+a.Addr() || len(b.Requests()) != 0 {
				t.Errorf("control: reused %v via %q; CONNECTs B %d", resp.Conn.Reused, resp.Conn.Via, len(b.Requests()))
			}
		})
	}
}

// TestDirectDialsRecorded checks the check of TestEveryProtocolThroughProxy:
// a dial that does not go through the proxy (here a loopback target, which
// the route sends direct on purpose) shows up in the direct dialer's
// record as an address other than the proxy's.
func TestDirectDialsRecorded(t *testing.T) {
	t.Parallel()
	p := &netxtest.SOCKS5Server{}
	addr := p.Start(t)
	w := newE2EWorld(t, "socks5h://"+addr, nil)
	echo, _ := echoListener(t)
	c, err := w.dialer.DialContext(w.ctx, "tcp", net.JoinHostPort("127.0.0.1", echo))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if dials := w.direct.list(); len(dials) != 1 || dials[0] != "127.0.0.1:"+echo || dials[0] == addr {
		t.Errorf("direct dials = %q, want the loopback target", dials)
	}
}
