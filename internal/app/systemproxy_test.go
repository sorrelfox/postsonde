package app

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/sysproxy"
)

// TestSystemProxy (NET-01) through the app's dialer and HTTP client, with
// the default settings ("system") and the OS settings given as scutil
// output (never this machine's): http:// requests go to the OS's web
// proxy in absolute form and other connections through its SOCKS proxy;
// after the OS drops the web proxy (read again by the refresh), http://
// requests tunnel through SOCKS on a new connection; a PAC file alone
// fails every connection, never going direct.
func TestSystemProxy(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(origin.Close)
	url := "http://" + net.JoinHostPort(e2eHost, port(origin.Listener.Addr())) + "/"
	hosts := map[string]string{e2eHost: "127.0.0.1"}
	web := &netxtest.HTTPProxyServer{Hosts: hosts}
	web.Start(t)
	socks := &netxtest.SOCKS5Server{Hosts: hosts}
	socks.Start(t)

	var mu sync.Mutex
	scutil := ""
	setOS := func(out string) {
		mu.Lock()
		defer mu.Unlock()
		scutil = out
	}
	var reads atomic.Int32
	reader := sysproxy.Reader{GOOS: "darwin", Run: func(context.Context, string, ...string) ([]byte, error) {
		reads.Add(1)
		mu.Lock()
		defer mu.Unlock()
		return []byte(scutil), nil
	}}
	hostPort := func(addr string) (string, string) { h, p, _ := net.SplitHostPort(addr); return h, p }
	webHost, webPort := hostPort(web.Addr())
	socksHost, socksPort := hostPort(socks.Addr())
	both := "<dictionary> {\n  HTTPEnable : 1\n  HTTPPort : " + webPort + "\n  HTTPProxy : " + webHost +
		"\n  SOCKSEnable : 1\n  SOCKSPort : " + socksPort + "\n  SOCKSProxy : " + socksHost + "\n}\n"
	setOS(both)

	store := coresettings.Open("", nil)
	if store.Settings().Network.ProxyMode != coresettings.ProxyModeSystem {
		t.Fatalf("default mode %q", store.Settings().Network.ProxyMode)
	}
	system := newSystemProxy(reader.Read)
	resolve := proxyResolver(func() coresettings.Network { return store.Settings().Network }, appsecrets.New(""), nil, nil, system.config)
	resolver, lookups := netxtest.RecordingResolver(hosts)
	dialer, router := newRoutedDialer(slog.New(slog.DiscardHandler), netlog.New(netlog.Options{}), dialerConfig{
		Direct:  netx.Options{Resolver: resolver},
		Proxy:   proxyConfigOf(resolve),
		RootCAs: func() (*x509.CertPool, error) { return nil, nil },
	})
	client := httpclient.New(dialer, httpclient.TransportOptions{})
	t.Cleanup(client.CloseIdleConnections)
	router.OnChange(client.CloseIdleConnections)
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test.send", ID: "1"})
	send := func() *httpclient.Response {
		t.Helper()
		resp, err := client.Send(ctx, httpclient.Request{Method: http.MethodGet, URL: url, Settings: httpclient.DefaultSettings()})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// http:// to the web proxy, in absolute form.
	resp := send()
	if reqs := web.Requests(); len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Target != url || resp.Conn.Via != "http://"+web.Addr() {
		t.Fatalf("http:// request: via %q, web proxy got %+v", resp.Conn.Via, web.Requests())
	}
	// Another connection (a TCP client's) through SOCKS.
	c, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(e2eHost, port(origin.Listener.Addr())))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if reqs := socks.Requests(); len(reqs) != 1 || reqs[0].Host != e2eHost || !reqs[0].Name {
		t.Fatalf("SOCKS CONNECTs = %+v, want the name (socks5h)", reqs)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("scutil ran %d times, want once (cached)", n)
	}

	// The OS drops the web proxy: the refresh reads it again, the pools
	// close, and http:// requests tunnel through SOCKS.
	setOS("<dictionary> {\n  HTTPEnable : 0\n  SOCKSEnable : 1\n  SOCKSPort : " + socksPort + "\n  SOCKSProxy : " + socksHost + "\n}\n")
	system.refresh(router)(t.Context())
	resp = send()
	if resp.Conn.Reused || resp.Conn.Via != "socks5h://"+socks.Addr() || len(socks.Requests()) != 2 || len(web.Requests()) != 1 {
		t.Errorf("after the change: reused %v via %q; SOCKS %d, web %d", resp.Conn.Reused, resp.Conn.Via, len(socks.Requests()), len(web.Requests()))
	}
	if names := lookups.Names(); len(names) != 0 {
		t.Errorf("local lookups %q, want none", names)
	}

	// A PAC file alone: connections fail with the reason.
	setOS("<dictionary> {\n  ProxyAutoConfigEnable : 1\n  ProxyAutoConfigURLString : http://wpad.test/proxy.pac\n}\n")
	system.refresh(router)(t.Context())
	accepted := web.Accepted() + socks.Accepted()
	_, err = client.Send(ctx, httpclient.Request{Method: http.MethodGet, URL: url, Settings: httpclient.DefaultSettings()})
	if !errorsIs(err, sysproxy.CodePACUnsupported) {
		t.Errorf("PAC: err = %v, want %s", err, sysproxy.CodePACUnsupported)
	}
	var e *errs.Error
	if errors.As(err, &e) && !strings.Contains(e.Details["pac"], "wpad.test") {
		t.Errorf("details %v", e.Details)
	}
	if web.Accepted()+socks.Accepted() != accepted {
		t.Error("a PAC system proxy connected somewhere")
	}
}
