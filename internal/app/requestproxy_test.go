package app

import (
	"net"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
)

// TestEveryProtocolThroughRequestProxy: through the app's own dialer,
// whose proxy in effect comes from the settings, a dial that carries a
// request's own proxy (netx.WithRequestProxy, what every service does for
// a request with settings.proxy) goes through that proxy for every
// protocol, UDP through its UDP ASSOCIATE relay, and the proxy in effect
// sees nothing; a request that says none connects directly. Neither
// changes the proxy in effect, so no pool is closed.
func TestEveryProtocolThroughRequestProxy(t *testing.T) {
	t.Parallel()
	srv := startE2EServers(t)
	protocols := e2eProtocols(srv)
	udpTarget := net.JoinHostPort(e2eHost, srv.udpPort)

	t.Run("custom socks5", func(t *testing.T) {
		t.Parallel()
		ambient := &netxtest.SOCKS5Server{Hosts: map[string]string{e2eHost: "127.0.0.1"}}
		ambientAddr := ambient.Start(t)
		own := &netxtest.SOCKS5Server{Username: "bob", Password: "req-s3cret", Hosts: map[string]string{e2eHost: "127.0.0.1"}}
		ownAddr := own.Start(t)
		ownURL, err := netx.ParseProxyURL("socks5h://bob:req-s3cret@" + ownAddr)
		if err != nil {
			t.Fatal(err)
		}
		w := newE2EWorld(t, "socks5h://"+ambientAddr, srv.ca.Pool())
		var changes atomic.Int32
		w.router.Refresh(t.Context())
		w.router.OnChange(func() { changes.Add(1) })
		w.ctx = netx.WithRequestProxy(w.ctx, ownURL)

		for _, proto := range protocols {
			if err := proto.run(t, w); err != nil {
				t.Errorf("%s through the request's proxy: %v", proto.name, err)
			}
		}
		if err := udpEcho(w, udpTarget); err != nil {
			t.Errorf("udp through the request's proxy: %v", err)
		}

		if ambient.Accepted() != 0 {
			t.Errorf("the proxy in effect got %d connections", ambient.Accepted())
		}
		if len(own.Requests()) < len(protocols) || len(own.UDPDatagrams()) == 0 {
			t.Errorf("the request's proxy got %d connects and %d datagrams", len(own.Requests()), len(own.UDPDatagrams()))
		}
		for _, c := range own.Credentials() {
			if c != "bob:req-s3cret" {
				t.Errorf("credentials %q", c)
			}
		}
		ownAP := netip.MustParseAddrPort(ownAddr)
		allowed := map[string]bool{ownAP.String(): true}
		for _, c := range w.connLog.Conns() {
			if c.Network == "udp" && c.Via != "" {
				allowed[c.RemoteAddr] = true
			}
		}
		for _, d := range w.direct.list() {
			if !allowed[d] {
				t.Errorf("a direct connection to %s, not through the request's proxy", d)
			}
		}
		for _, c := range w.connLog.Conns() {
			if c.State != netlog.Refused && c.Via != "socks5h://"+ownAddr {
				t.Errorf("a logged connection not via the request's proxy: %+v", c)
			}
		}
		if names := w.lookups.Names(); len(names) != 0 {
			t.Errorf("remote DNS, but %q was looked up here", names)
		}
		if n := changes.Load(); n != 0 {
			t.Errorf("the request's proxy changed the proxy in effect %d times", n)
		}
	})

	t.Run("none", func(t *testing.T) {
		t.Parallel()
		ambient := &netxtest.SOCKS5Server{Hosts: map[string]string{e2eHost: "127.0.0.1"}}
		ambientAddr := ambient.Start(t)
		w := newE2EWorld(t, "socks5h://"+ambientAddr, srv.ca.Pool())
		w.ctx = netx.WithRequestProxy(w.ctx, netx.ProxyURL{})
		w.wantDirect = true

		for _, proto := range protocols {
			if err := proto.run(t, w); err != nil {
				t.Errorf("%s directly: %v", proto.name, err)
			}
		}
		if err := udpEcho(w, udpTarget); err != nil {
			t.Errorf("udp directly: %v", err)
		}
		if ambient.Accepted() != 0 {
			t.Errorf("the proxy in effect got %d connections from requests that say none", ambient.Accepted())
		}
		for _, c := range w.connLog.Conns() {
			if c.Via != "" {
				t.Errorf("a logged connection through a proxy: %+v", c)
			}
		}
	})

	t.Run("inherit", func(t *testing.T) {
		t.Parallel()
		ambient := &netxtest.SOCKS5Server{Hosts: map[string]string{e2eHost: "127.0.0.1"}}
		ambientAddr := ambient.Start(t)
		w := newE2EWorld(t, "socks5h://"+ambientAddr, srv.ca.Pool())
		if err := protocols[0].run(t, w); err != nil {
			t.Fatal(err)
		}
		if ambient.Accepted() == 0 {
			t.Error("a request that inherits did not go through the proxy in effect")
		}
	})
}
