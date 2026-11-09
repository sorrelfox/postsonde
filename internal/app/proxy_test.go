package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// proxyTarget is a host name only the test proxy and the recording
// resolver know; it points at a local echo server.
const proxyTarget = "api.example.test"

// echoListener accepts on loopback and echoes what it reads; it returns
// the port and counts the connections.
func echoListener(t *testing.T) (string, func() int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted := make(chan struct{}, 100)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port, func() int { return len(accepted) }
}

// proxyWorld is the app's dialer over a settings store, app secrets and an
// environment, with a SOCKS5 test proxy, a recording resolver and an echo
// server behind the name proxyTarget.
type proxyWorld struct {
	settings *coresettings.Store
	secrets  *appsecrets.Store
	env      map[string]string
	proxy    *netxtest.SOCKS5Server
	lookups  *netxtest.Lookups
	connLog  *netlog.Log
	dialer   netx.Dialer
	router   *netx.ProxyRouter
	port     string
	accepted func() int
}

func newProxyWorld(t *testing.T) *proxyWorld {
	t.Helper()
	w := &proxyWorld{
		settings: coresettings.Open("", nil),
		secrets:  appsecrets.New(""),
		env:      map[string]string{},
		connLog:  netlog.New(netlog.Options{}),
	}
	// ALL_PROXY's mode: the default, the OS's settings, has none here.
	env := coresettings.ProxyModeEnv
	if _, _, err := w.settings.Update(coresettings.Patch{Network: coresettings.NetworkPatch{ProxyMode: &env}}); err != nil {
		t.Fatal(err)
	}
	w.port, w.accepted = echoListener(t)
	w.proxy = &netxtest.SOCKS5Server{Username: "alice", Password: "s3cret", Hosts: map[string]string{proxyTarget: "127.0.0.1"}}
	w.proxy.Start(t)
	resolver, lookups := netxtest.RecordingResolver(map[string]string{proxyTarget: "127.0.0.1"})
	w.lookups = lookups
	getenv := func(k string) string { return w.env[k] }
	w.dialer, w.router = newRoutedDialer(slog.New(slog.DiscardHandler), w.connLog, dialerConfig{
		Direct: netx.Options{Resolver: resolver},
		Proxy:  proxySource(func() coresettings.Network { return w.settings.Settings().Network }, w.secrets, getenv),
	})
	return w
}

func (w *proxyWorld) set(t *testing.T, mode, url, password string) {
	t.Helper()
	if _, _, err := w.settings.Update(coresettings.Patch{Network: coresettings.NetworkPatch{ProxyMode: &mode, ProxyURL: &url}}); err != nil {
		t.Fatal(err)
	}
	if err := w.secrets.Set(appsecrets.ProxyPasswordKey, password); err != nil {
		t.Fatal(err)
	}
}

// dial connects to host:port as a user operation and checks the echo.
func (w *proxyWorld) dial(t *testing.T, host string) error {
	t.Helper()
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test.dial", ID: "1"})
	c, err := w.dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, w.port))
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
	return nil
}

// TestDialerProxyRemoteDNS proves SOCKS-03 through the app's dialer, with
// the proxy taken from the settings (and the password from the app
// secrets) or from ALL_PROXY: in socks5h mode the proxy is given the host
// name and no local DNS lookup happens; in socks5 mode the name is
// resolved here and the proxy gets the IP address.
func TestDialerProxyRemoteDNS(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		setup    func(w *proxyWorld, t *testing.T)
		wantName bool
		source   string
	}{
		{"settings socks5h", func(w *proxyWorld, t *testing.T) {
			w.set(t, "manual", "socks5h://alice@"+w.proxy.Addr(), "s3cret")
		}, true, "settings"},
		{"settings socks5", func(w *proxyWorld, t *testing.T) {
			w.set(t, "manual", "socks5://alice@"+w.proxy.Addr(), "s3cret")
		}, false, "settings"},
		{"ALL_PROXY socks5h", func(w *proxyWorld, t *testing.T) {
			w.env["ALL_PROXY"] = "socks5h://alice:s3cret@" + w.proxy.Addr()
		}, true, "env"},
		{"all_proxy socks5", func(w *proxyWorld, t *testing.T) {
			w.env["all_proxy"] = "socks5://alice:s3cret@" + w.proxy.Addr()
		}, false, "env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := newProxyWorld(t)
			tt.setup(w, t)
			if err := w.dial(t, proxyTarget); err != nil {
				t.Fatalf("dial through the proxy: %v", err)
			}
			reqs := w.proxy.Requests()
			if len(reqs) != 1 {
				t.Fatalf("the proxy got %d CONNECTs, want 1", len(reqs))
			}
			names := w.lookups.Names()
			if tt.wantName {
				if !reqs[0].Name || reqs[0].Host != proxyTarget || len(names) != 0 {
					t.Errorf("socks5h: CONNECT %+v, local lookups %q; want the name and no lookup", reqs[0], names)
				}
			} else if reqs[0].Name || reqs[0].Host != "127.0.0.1" || !slices.Contains(names, proxyTarget) {
				t.Errorf("socks5: CONNECT %+v, local lookups %q; want the IP, resolved here", reqs[0], names)
			}
			if strconv.Itoa(reqs[0].Port) != w.port {
				t.Errorf("CONNECT port %d, want %s", reqs[0].Port, w.port)
			}
			if creds := w.proxy.Credentials(); len(creds) != 1 || creds[0] != "alice:s3cret" {
				t.Errorf("credentials = %q", creds)
			}
			// The connection log names the target as asked for and the proxy.
			via := "socks5://" + w.proxy.Addr()
			if tt.wantName {
				via = "socks5h://" + w.proxy.Addr()
			}
			conns := w.connLog.Conns()
			if len(conns) != 1 || conns[0].Address != net.JoinHostPort(proxyTarget, w.port) || conns[0].Via != via {
				t.Errorf("connection log = %+v, want one dial via %s", conns, via)
			}
			if n := w.accepted(); n != 1 {
				t.Errorf("the server accepted %d connections, want 1 (the proxy's)", n)
			}
		})
	}
}

// TestDialerProxyRoutes: without a proxy, or with "off" whatever ALL_PROXY
// says, connections go direct; loopback targets always do; a change of the
// settings or of the stored password applies to the next dial.
func TestDialerProxyRoutes(t *testing.T) {
	t.Parallel()
	w := newProxyWorld(t)

	// The "env" mode with no ALL_PROXY: direct.
	if err := w.dial(t, proxyTarget); err != nil {
		t.Fatalf("direct: %v", err)
	}
	// "off" ignores ALL_PROXY.
	w.env["ALL_PROXY"] = "socks5h://alice:s3cret@" + w.proxy.Addr()
	w.set(t, "off", "", "")
	if err := w.dial(t, proxyTarget); err != nil {
		t.Fatalf("off: %v", err)
	}
	if n := len(w.proxy.Requests()); n != 0 {
		t.Fatalf("the proxy got %d CONNECTs while none was to be used", n)
	}

	// A manual proxy applies from the next dial; loopback stays direct.
	w.set(t, "manual", "socks5h://alice@"+w.proxy.Addr(), "s3cret")
	if err := w.dial(t, proxyTarget); err != nil {
		t.Fatalf("manual: %v", err)
	}
	for _, host := range []string{"127.0.0.1", "localhost"} {
		if err := w.dial(t, host); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
	}
	if reqs := w.proxy.Requests(); len(reqs) != 1 || reqs[0].Host != proxyTarget {
		t.Errorf("CONNECTs = %+v, want only the non-loopback target", reqs)
	}

	// A new password is used by the next dial.
	if err := w.secrets.Set(appsecrets.ProxyPasswordKey, "wrong"); err != nil {
		t.Fatal(err)
	}
	if err := w.dial(t, proxyTarget); !errorsIs(err, netx.CodeSOCKSAuthFailed) {
		t.Errorf("wrong password: err = %v, want %s", err, netx.CodeSOCKSAuthFailed)
	}
}

// TestDialerProxyFailsClosed: a proxy that is set but cannot be used fails
// every dial, and nothing is connected or looked up.
func TestDialerProxyFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(w *proxyWorld, t *testing.T)
		code  string
	}{
		{"ALL_PROXY without a scheme", func(w *proxyWorld, t *testing.T) { w.env["ALL_PROXY"] = w.proxy.Addr() }, netx.CodeProxyInvalidURL},
		{"socks4a ALL_PROXY with a password", func(w *proxyWorld, t *testing.T) { w.env["ALL_PROXY"] = "socks4a://bob:pw@" + w.proxy.Addr() }, netx.CodeProxyInvalidURL},
		{"http ALL_PROXY with a user name holding a colon", func(w *proxyWorld, t *testing.T) {
			w.env["ALL_PROXY"] = "http://a%3Ab:pw@" + w.proxy.Addr()
		}, netx.CodeProxyInvalidURL},
		{"manual without a URL", func(w *proxyWorld, t *testing.T) { w.set(t, "manual", "", "") }, netx.CodeProxyInvalidURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := newProxyWorld(t)
			tt.setup(w, t)
			err := w.dial(t, proxyTarget)
			if !errorsIs(err, tt.code) {
				t.Fatalf("err = %v, want %s", err, tt.code)
			}
			if w.proxy.Accepted() != 0 || w.accepted() != 0 || len(w.lookups.Names()) != 0 {
				t.Errorf("a failed proxy connected or looked up: proxy %d, server %d, lookups %q",
					w.proxy.Accepted(), w.accepted(), w.lookups.Names())
			}
			if conns := w.connLog.Conns(); len(conns) != 1 || conns[0].State != netlog.Failed || conns[0].ErrCode != tt.code {
				t.Errorf("connection log = %+v", conns)
			}
		})
	}
}

// TestProxySourceBrokenSecrets: a manual proxy whose password cannot be
// read fails rather than connecting without it.
func TestProxySourceBrokenSecrets(t *testing.T) {
	t.Parallel()
	secrets := appsecrets.New(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(secrets.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secrets.Path(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	manual, url := "manual", "socks5h://alice@127.0.0.1:1080"
	store := coresettings.Open("", nil)
	if _, _, err := store.Update(coresettings.Patch{Network: coresettings.NetworkPatch{ProxyMode: &manual, ProxyURL: &url}}); err != nil {
		t.Fatal(err)
	}
	source := proxySource(func() coresettings.Network { return store.Settings().Network }, secrets, nil)
	if _, err := source(context.Background()); !errorsIs(err, appsecrets.CodeLocalDataFailed) {
		t.Errorf("err = %v, want %s", err, appsecrets.CodeLocalDataFailed)
	}
	// Without a manual proxy the secrets are not read.
	off := "off"
	if _, _, err := store.Update(coresettings.Patch{Network: coresettings.NetworkPatch{ProxyMode: &off}}); err != nil {
		t.Fatal(err)
	}
	if p, err := source(context.Background()); err != nil || !p.Proxy.IsZero() {
		t.Errorf("off: %+v, %v", p, err)
	}
}

// TestDialerSOCKS4 (SOCKS-06) through the app's dialer: a socks4a proxy
// from the settings gets the host name and the user ID, a socks4 one from
// ALL_PROXY gets the address resolved here; the connection log names the
// proxy (NET-06).
func TestDialerSOCKS4(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		scheme   string
		env      bool
		wantName bool
	}{
		{"socks4a", false, true},
		{"socks4", true, false},
	} {
		t.Run(tt.scheme, func(t *testing.T) {
			t.Parallel()
			w := newProxyWorld(t)
			proxy := &netxtest.SOCKS4Server{UserID: "bob", Hosts: map[string]string{proxyTarget: "127.0.0.1"}}
			addr := proxy.Start(t)
			if tt.env {
				w.env["ALL_PROXY"] = tt.scheme + "://bob@" + addr
			} else {
				w.set(t, "manual", tt.scheme+"://bob@"+addr, "")
			}
			if err := w.dial(t, proxyTarget); err != nil {
				t.Fatalf("dial through %s: %v", tt.scheme, err)
			}
			reqs := proxy.Requests()
			if len(reqs) != 1 || reqs[0].Name != tt.wantName || reqs[0].UserID != "bob" {
				t.Errorf("CONNECTs = %+v", reqs)
			}
			if names := w.lookups.Names(); tt.wantName != (len(names) == 0) {
				t.Errorf("local lookups = %q", names)
			}
			conns := w.connLog.Conns()
			if len(conns) != 1 || conns[0].Via != tt.scheme+"://"+addr || conns[0].RemoteAddr != addr {
				t.Errorf("connection log = %+v", conns)
			}
		})
	}
}
