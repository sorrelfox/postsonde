package app

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

const httpProxyPassword = "http-pr0xy-pw"

// TestDialerHTTPProxy (NET-02) through the app's dialer: an http:// or
// https:// proxy from the settings (the password from the app secrets) or
// from ALL_PROXY tunnels with CONNECT and Basic credentials; the target's
// name goes to the proxy unresolved; the connection log names the proxy;
// a wrong password fails with netx.http_proxy_auth_failed; and neither the
// errors, the connection log nor the debug log show the credentials.
func TestDialerHTTPProxy(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "proxy CA")
	proxyTLS := &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{CommonName: "proxy"}).TLS}}
	tests := []struct {
		name   string
		tls    bool
		setup  func(w *proxyWorld, t *testing.T, addr string)
		scheme string
	}{
		{"settings http", false, func(w *proxyWorld, t *testing.T, addr string) {
			w.set(t, "manual", "http://alice@"+addr, httpProxyPassword)
		}, "http"},
		{"settings https", true, func(w *proxyWorld, t *testing.T, addr string) {
			w.set(t, "manual", "https://alice@"+addr, httpProxyPassword)
		}, "https"},
		{"ALL_PROXY http", false, func(w *proxyWorld, t *testing.T, addr string) {
			w.env["ALL_PROXY"] = "http://alice:" + httpProxyPassword + "@" + addr
		}, "http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs lockedBuffer
			w := newProxyWorld(t)
			proxy := &netxtest.HTTPProxyServer{Username: "alice", Password: httpProxyPassword, Hosts: map[string]string{proxyTarget: "127.0.0.1"}}
			if tt.tls {
				proxy.TLS = proxyTLS
			}
			addr := proxy.Start(t)
			resolver, lookups := netxtest.RecordingResolver(map[string]string{proxyTarget: "127.0.0.1"})
			w.lookups = lookups
			w.dialer = newDialer(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), w.connLog, dialerConfig{
				Direct:  netx.Options{Resolver: resolver},
				Proxy:   proxySource(func() coresettings.Network { return w.settings.Settings().Network }, w.secrets, func(k string) string { return w.env[k] }),
				RootCAs: func() (*x509.CertPool, error) { return ca.Pool(), nil },
			})
			if _, ok := w.dialer.(netx.ForwardProxier); !ok {
				t.Fatal("the app's dialer is not a netx.ForwardProxier")
			}
			tt.setup(w, t, addr)
			if err := w.dial(t, proxyTarget); err != nil {
				t.Fatalf("dial through the proxy: %v", err)
			}
			dest := net.JoinHostPort(proxyTarget, w.port)
			token := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+httpProxyPassword))
			reqs := proxy.Requests()
			if len(reqs) != 1 || reqs[0].Method != "CONNECT" || reqs[0].Target != dest || reqs[0].Authorization != token {
				t.Fatalf("proxy requests = %+v", reqs)
			}
			if names := w.lookups.Names(); len(names) != 0 {
				t.Errorf("local DNS lookups %q, want none", names)
			}
			via := tt.scheme + "://" + addr
			conns := w.connLog.Conns()
			if len(conns) != 1 || conns[0].Address != dest || conns[0].Via != via || conns[0].RemoteAddr != addr {
				t.Errorf("connection log = %+v, want one dial via %s", conns, via)
			}
			// Localhost still bypasses the proxy.
			if err := w.dial(t, "localhost"); err != nil {
				t.Fatal(err)
			}
			if n := len(proxy.Requests()); n != 1 {
				t.Errorf("the proxy got %d requests after a localhost dial, want 1", n)
			}

			// A wrong password: a coded error without the credentials.
			if w.env["ALL_PROXY"] != "" {
				w.env["ALL_PROXY"] = "http://alice:wrong-" + httpProxyPassword + "@" + addr
			} else if err := w.secrets.Set(appsecrets.ProxyPasswordKey, "wrong-"+httpProxyPassword); err != nil {
				t.Fatal(err)
			}
			err := w.dial(t, proxyTarget)
			if !errorsIs(err, netx.CodeHTTPProxyAuthFailed) {
				t.Fatalf("wrong password: err = %v, want %s", err, netx.CodeHTTPProxyAuthFailed)
			}
			var shown strings.Builder
			shown.WriteString(err.Error() + err.(*errs.Error).Hint)
			for k, v := range err.(*errs.Error).Details {
				shown.WriteString(k + v)
			}
			for _, c := range w.connLog.Conns() {
				shown.WriteString(c.Via + c.Err + c.ErrCode + c.Address)
			}
			shown.WriteString(logs.String())
			for _, secret := range []string{httpProxyPassword, base64.StdEncoding.EncodeToString([]byte("alice:" + httpProxyPassword))} {
				if strings.Contains(shown.String(), secret) {
					t.Errorf("a credential shows in the error, connection log or log: %s", shown.String())
				}
			}
			if last := w.connLog.Conns()[len(w.connLog.Conns())-1]; last.State != netlog.Failed || last.ErrCode != netx.CodeHTTPProxyAuthFailed || last.Via != via {
				t.Errorf("connection log entry = %+v", last)
			}
		})
	}
}

// TestDialerHTTPProxyUDP: an HTTP proxy cannot carry UDP; the dial fails
// rather than going direct.
func TestDialerHTTPProxyUDP(t *testing.T) {
	t.Parallel()
	w := newProxyWorld(t)
	w.set(t, "manual", "http://127.0.0.1:1", "")
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test.dial", ID: "1"})
	c, err := w.dialer.DialContext(ctx, "udp", net.JoinHostPort(proxyTarget, "53"))
	if err == nil {
		c.Close()
	}
	if !errorsIs(err, netx.CodeHTTPProxyUnsupportedNetwork) {
		t.Errorf("err = %v, want %s", err, netx.CodeHTTPProxyUnsupportedNetwork)
	}
}
