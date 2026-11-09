package netx

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

// lastHop returns the proxy hop of the last DialDone rec saw.
func lastHop(t *testing.T, rec *recorder) ProxyHop {
	t.Helper()
	_, dones := rec.events()
	if len(dones) == 0 || dones[len(dones)-1].Proxy == nil {
		t.Fatalf("no proxy hop in the trace: %+v", dones)
	}
	return *dones[len(dones)-1].Proxy
}

// TestProxyHopSOCKS5 (SOCKS-12): the SOCKS5 dialer reports the proxy's
// address, version, authentication, DNS mode, reply and timings, on
// success and on a failure reply, and nothing of the password.
func TestProxyHopSOCKS5(t *testing.T) {
	t.Parallel()
	const name = "api.example.test"
	resolver, _ := netxtest.RecordingResolver(map[string]string{name: "127.0.0.1"})
	tests := []struct {
		name      string
		srv       *socksServer
		cfg       SOCKS5Config
		target    string
		wantErr   string
		wantAuth  string
		wantReply int
		wantText  string
		remote    bool
		lookup    bool
	}{
		{name: "socks5h, no auth", cfg: SOCKS5Config{RemoteDNS: true}, target: name + ":443",
			wantAuth: AuthNone, wantText: "succeeded", remote: true},
		{name: "socks5, local DNS", cfg: SOCKS5Config{Resolver: resolver}, target: name + ":443",
			wantAuth: AuthNone, wantText: "succeeded", lookup: true},
		{name: "username/password", srv: &socksServer{User: "alice", Pass: proxyPassword},
			cfg: SOCKS5Config{RemoteDNS: true, Username: "alice", Password: proxyPassword}, target: name + ":443",
			wantAuth: AuthUserPass, wantText: "succeeded", remote: true},
		{name: "refused", srv: &socksServer{Reply: SOCKSReplyConnectionRefused}, cfg: SOCKS5Config{RemoteDNS: true}, target: name + ":443",
			wantErr: CodeSOCKSConnectionRefused, wantAuth: AuthNone, wantReply: 5, wantText: "connection refused", remote: true},
		{name: "auth failed", srv: &socksServer{User: "alice", Pass: "other"},
			cfg: SOCKS5Config{RemoteDNS: true, Username: "alice", Password: proxyPassword}, target: name + ":443",
			wantErr: CodeSOCKSAuthFailed, wantAuth: AuthUserPass, wantReply: -1, remote: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := tt.srv
			if srv == nil {
				srv = &socksServer{}
			}
			proxy := srv.start(t)
			cfg := tt.cfg
			cfg.Address = proxy
			cfg.Now = (&fakeClock{now: time.Unix(1000, 0), step: time.Millisecond}).Now
			d := newSOCKS(t, cfg)
			rec := &recorder{}
			c, err := d.DialContext(WithTrace(t.Context(), rec.trace()), "tcp", tt.target)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				c.Close()
			} else {
				wantErr(t, err, errs.KindOf(err), tt.wantErr)
			}
			hop := lastHop(t, rec)
			scheme := "socks5"
			if tt.remote {
				scheme = "socks5h"
			}
			if hop.Scheme != scheme || hop.Version != "SOCKS5" || hop.Proxy != proxy || hop.ProxyAddr != proxy || hop.Target != tt.target ||
				hop.Auth != tt.wantAuth || hop.RemoteDNS != tt.remote || hop.Forward {
				t.Errorf("hop = %+v", hop)
			}
			if tt.wantReply >= 0 && (!hop.Replied || hop.Reply != tt.wantReply || hop.ReplyText != tt.wantText) {
				t.Errorf("reply = %v %d %q, want %d %q", hop.Replied, hop.Reply, hop.ReplyText, tt.wantReply, tt.wantText)
			}
			if tt.wantReply < 0 && hop.Replied {
				t.Errorf("reply = %d, want none", hop.Reply)
			}
			if !testenv.Measured(hop.Connect) || !testenv.Measured(hop.Handshake) || hop.TLS != 0 || !testenv.MeasuredWhen(hop.Lookup, tt.lookup) {
				t.Errorf("timings = lookup %v, connect %v, tls %v, handshake %v", hop.Lookup, hop.Connect, hop.TLS, hop.Handshake)
			}
			s := hop.String()
			for _, want := range []string{"SOCKS5 " + proxy, "auth " + tt.wantAuth, hop.DNSMode() + " DNS", "connect ", "handshake "} {
				if !strings.Contains(s, want) {
					t.Errorf("String() = %q, want %q in it", s, want)
				}
			}
			if strings.Contains(s, proxyPassword) {
				t.Errorf("String() shows the password: %q", s)
			}
		})
	}
}

// TestProxyHopHTTP (SOCKS-12): the HTTP proxy dialer reports its hop:
// CONNECT's status as the reply, Basic auth, the TLS handshake of an
// https:// proxy, and absolute-form connections without a reply.
func TestProxyHopHTTP(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	_, port, _ := net.SplitHostPort(target)
	dest := net.JoinHostPort(httpProxyTarget, port)
	ca, serverTLS := testProxyCA(t)
	tests := []struct {
		name      string
		srv       *netxtest.HTTPProxyServer
		tls       bool
		user      string
		forward   bool
		wantErr   string
		wantAuth  string
		wantReply int
		wantText  string
	}{
		{name: "http", wantAuth: AuthNone, wantReply: 200, wantText: "200 Connection established"},
		{name: "https, basic", srv: &netxtest.HTTPProxyServer{Username: "alice", Password: proxyPassword}, tls: true, user: "alice",
			wantAuth: AuthBasic, wantReply: 200, wantText: "200 Connection established"},
		{name: "forbidden", srv: &netxtest.HTTPProxyServer{ConnectStatus: http.StatusForbidden}, wantErr: CodeHTTPProxyForbidden,
			wantAuth: AuthNone, wantReply: 403, wantText: "403 Forbidden"},
		{name: "absolute form", forward: true, wantAuth: AuthNone, wantReply: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := tt.srv
			if srv == nil {
				srv = &netxtest.HTTPProxyServer{}
			}
			srv.Hosts = map[string]string{httpProxyTarget: "127.0.0.1"}
			if tt.tls {
				srv.TLS = serverTLS
			}
			addr := srv.Start(t)
			cfg := HTTPProxyConfig{Address: addr, TLS: tt.tls, RootCAs: func() (*x509.CertPool, error) { return ca.Pool(), nil },
				Now: (&fakeClock{now: time.Unix(1000, 0), step: time.Millisecond}).Now}
			if tt.user != "" {
				cfg.Username, cfg.Password = tt.user, proxyPassword
			}
			p, err := NewHTTPProxy(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			rec := &recorder{}
			dial := p.DialContext
			if tt.forward {
				dial = p.DialForward
			}
			c, err := dial(WithTrace(t.Context(), rec.trace()), "tcp", dest)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				c.Close()
			} else {
				wantErr(t, err, errs.Network, tt.wantErr)
			}
			hop := lastHop(t, rec)
			scheme := "http"
			if tt.tls {
				scheme = "https"
			}
			if hop.Scheme != scheme || hop.Version != "HTTP/1.1" || hop.Proxy != addr || hop.ProxyAddr != addr || hop.Target != dest ||
				hop.Auth != tt.wantAuth || !hop.RemoteDNS || hop.Forward != tt.forward {
				t.Errorf("hop = %+v", hop)
			}
			if tt.wantReply >= 0 && (!hop.Replied || hop.Reply != tt.wantReply || hop.ReplyText != tt.wantText) {
				t.Errorf("reply = %v %d %q, want %d %q", hop.Replied, hop.Reply, hop.ReplyText, tt.wantReply, tt.wantText)
			}
			if tt.wantReply < 0 && (hop.Replied || hop.Handshake != 0) {
				t.Errorf("an absolute-form connection has a reply: %+v", hop)
			}
			if !testenv.Measured(hop.Connect) || !testenv.MeasuredWhen(hop.TLS, tt.tls) || !testenv.MeasuredWhen(hop.Handshake, !tt.forward) || hop.Lookup != 0 {
				t.Errorf("timings = lookup %v, connect %v, tls %v, handshake %v", hop.Lookup, hop.Connect, hop.TLS, hop.Handshake)
			}
			if s := hop.String(); strings.Contains(s, proxyPassword) || !strings.Contains(s, "HTTP/1.1 "+addr) {
				t.Errorf("String() = %q", s)
			}
		})
	}
}

// TestProxyRouterTest (SOCKS-11): the connection test reports success
// with the hop, and each kind of failure with its code and step.
func TestProxyRouterTest(t *testing.T) {
	t.Parallel()
	target := echoServer(t)
	_, port, _ := net.SplitHostPort(target)
	dest := net.JoinHostPort(httpProxyTarget, port)
	_, untrusted := testProxyCA(t)

	socks := func(s *socksServer) func(t *testing.T) string {
		return func(t *testing.T) string { return "socks5h://" + s.start(t) }
	}
	httpProxy := func(s *netxtest.HTTPProxyServer, scheme string) func(t *testing.T) string {
		return func(t *testing.T) string {
			s.Hosts = map[string]string{httpProxyTarget: "127.0.0.1"}
			return scheme + "://" + s.Start(t)
		}
	}
	tests := []struct {
		name     string
		proxy    func(t *testing.T) string // the proxy URL; "" for none
		cred     string                    // user:password to add to the URL
		target   string
		timeout  time.Duration
		resolver *net.Resolver
		cancel   bool
		wantCode string
		wantStep string
	}{
		{name: "socks5 ok", proxy: func(t *testing.T) string {
			s := &netxtest.SOCKS5Server{Hosts: map[string]string{httpProxyTarget: "127.0.0.1"}}
			return "socks5h://" + s.Start(t)
		}, target: dest},
		{name: "http ok", proxy: httpProxy(&netxtest.HTTPProxyServer{}, "http"), target: dest},
		{name: "no proxy", proxy: func(*testing.T) string { return "" }, target: dest, wantCode: CodeProxyNone, wantStep: ProxyStepSetup},
		{name: "bad target", proxy: socks(&socksServer{}), target: "example.com", wantCode: CodeProxyTestTarget, wantStep: ProxyStepSetup},
		{name: "unreachable", proxy: func(t *testing.T) string { return "socks5h://" + closedPort(t) }, target: dest,
			wantCode: CodeSOCKSProxyUnreachable, wantStep: ProxyStepConnect},
		{name: "not socks", proxy: socks(&socksServer{Version: 4}), target: dest, wantCode: CodeSOCKSProtocolError, wantStep: ProxyStepHandshake},
		{name: "silent proxy", proxy: socks(&socksServer{Hang: true}), target: dest, timeout: 50 * time.Millisecond,
			wantCode: CodeSOCKSTimeout, wantStep: ProxyStepHandshake},
		{name: "auth required", proxy: socks(&socksServer{User: "alice", Pass: proxyPassword}), target: dest,
			wantCode: CodeSOCKSAuthRequired, wantStep: ProxyStepAuth},
		{name: "wrong password", proxy: socks(&socksServer{User: "alice", Pass: "other"}), cred: "alice:" + proxyPassword, target: dest,
			wantCode: CodeSOCKSAuthFailed, wantStep: ProxyStepAuth},
		{name: "target refused", proxy: socks(&socksServer{Reply: SOCKSReplyConnectionRefused}), target: dest,
			wantCode: CodeSOCKSConnectionRefused, wantStep: ProxyStepTarget},
		{name: "not allowed", proxy: socks(&socksServer{Reply: SOCKSReplyNotAllowed}), target: dest,
			wantCode: CodeSOCKSNotAllowed, wantStep: ProxyStepTarget},
		{name: "local DNS", proxy: func(t *testing.T) string { return "socks5://" + (&socksServer{}).start(t) }, target: "api.example.invalid:443",
			resolver: nxdomainResolver(), wantCode: CodeDNSNotFound, wantStep: ProxyStepDNS},
		{name: "http 407", proxy: httpProxy(&netxtest.HTTPProxyServer{Username: "alice", Password: proxyPassword}, "http"), target: dest,
			wantCode: CodeHTTPProxyAuthRequired, wantStep: ProxyStepAuth},
		{name: "http 403", proxy: httpProxy(&netxtest.HTTPProxyServer{ConnectStatus: http.StatusForbidden}, "http"), target: dest,
			wantCode: CodeHTTPProxyForbidden, wantStep: ProxyStepTarget},
		{name: "http 502", proxy: httpProxy(&netxtest.HTTPProxyServer{ConnectStatus: http.StatusBadGateway}, "http"), target: dest,
			wantCode: CodeHTTPProxyConnectFailed, wantStep: ProxyStepTarget},
		{name: "https untrusted", proxy: httpProxy(&netxtest.HTTPProxyServer{TLS: untrusted}, "https"), target: dest,
			wantCode: CodeTLSUnknownAuthority, wantStep: ProxyStepTLS},
		{name: "cancelled", proxy: socks(&socksServer{Hang: true}), target: dest, cancel: true,
			wantCode: CodeCancelled, wantStep: ProxyStepCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := tt.proxy(t)
			if tt.cred != "" {
				scheme, rest, _ := strings.Cut(raw, "://")
				raw = scheme + "://" + tt.cred + "@" + rest
			}
			var u ProxyURL
			if raw != "" {
				var err error
				if u, err = ParseProxyURL(raw); err != nil {
					t.Fatal(err)
				}
			}
			r := NewProxyRouter(NewDirect(Options{}), func(context.Context) (ProxyURL, error) { return u, nil },
				ProxyOptions{Timeout: tt.timeout, Resolver: tt.resolver})
			ctx := t.Context()
			if tt.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			var wrapped int
			res := r.Test(ctx, tt.target, func(next Dialer) Dialer {
				return DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
					wrapped++
					return next.DialContext(ctx, network, address)
				})
			})
			if res.Target != tt.target || res.Duration < 0 || (res.OK() && !testenv.Measured(res.Duration)) || res.Proxy.Password != "" {
				t.Errorf("result = %+v", res)
			}
			if tt.wantCode == "" {
				if !res.OK() || res.Step != "" || res.Hop == nil || !res.Hop.Replied || res.Proxy.IsZero() || wrapped != 1 {
					t.Fatalf("result = %+v, err %v", res, res.Err)
				}
				return
			}
			if res.OK() {
				t.Fatalf("the test passed, want %s", tt.wantCode)
			}
			wantErr(t, res.Err, errs.KindOf(res.Err), tt.wantCode)
			checkNoSecret(t, res.Err)
			if res.Step != tt.wantStep {
				t.Errorf("step = %q, want %q (%v)", res.Step, tt.wantStep, res.Err)
			}
			if (res.Hop == nil) != (tt.wantStep == ProxyStepSetup) {
				t.Errorf("hop = %+v for step %s", res.Hop, res.Step)
			}
		})
	}
}
