package udp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

const (
	// proxyTarget is a name only the test proxies and the test's direct
	// dialer can resolve: reaching it proves which way a socket went.
	proxyTarget = "udp.example.test"
	proxySecret = "udp-pr0xy-s3cret"
)

// hostsDialer connects directly, to the address hosts gives a target's
// name, so a name that is not loopback can be reached without a proxy.
func hostsDialer(hosts map[string]string) netx.Dialer {
	direct := netx.NewDirect(netx.Options{})
	return netx.DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		if h, p, err := net.SplitHostPort(address); err == nil {
			if ip, ok := hosts[h]; ok {
				address = net.JoinHostPort(ip, p)
			}
		}
		return direct.DialContext(ctx, network, address)
	})
}

// proxied is a service whose dialer routes like the app's: through the
// ambient SOCKS5 proxy in effect, or a request's own proxy: own (SOCKS5,
// only alice with proxySecret), socks4 or http.
type proxied struct {
	s            *UDPService
	ev           *events
	ambient, own *netxtest.SOCKS5Server
	socks4       *netxtest.SOCKS4Server
	http         *netxtest.HTTPProxyServer
}

func newProxied(t *testing.T) *proxied {
	t.Helper()
	hosts := map[string]string{proxyTarget: "127.0.0.1"}
	p := &proxied{
		ambient: &netxtest.SOCKS5Server{Hosts: hosts},
		own:     &netxtest.SOCKS5Server{Username: "alice", Password: proxySecret, Hosts: hosts},
		socks4:  &netxtest.SOCKS4Server{Hosts: hosts},
		http:    &netxtest.HTTPProxyServer{Hosts: hosts},
	}
	ambientURL, err := netx.ParseProxyURL("socks5h://" + p.ambient.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	ownAddr := p.own.Start(t)
	proxies := map[string]projectfile.RequestProxy{
		"req_custom": {Mode: projectfile.ProxyCustom, URL: "socks5h://alice@{{proxyAddr}}", Password: "{{proxyPassword}}"},
		"req_none":   {Mode: projectfile.ProxyNone},
		"req_socks4": {Mode: projectfile.ProxyCustom, URL: "socks4a://" + p.socks4.Start(t)},
		"req_http":   {Mode: projectfile.ProxyCustom, URL: "http://" + p.http.Start(t)},
		"req_no_url": {Mode: projectfile.ProxyCustom},
		"req_undef":  {Mode: projectfile.ProxyCustom, URL: "socks5h://{{nope}}:1080"},
	}
	router := netx.NewProxyRouter(hostsDialer(hosts), func(context.Context) (netx.ProxyURL, error) { return ambientURL, nil }, netx.ProxyOptions{})
	source := func(_ context.Context, id string) (projectfile.RequestProxy, error) { return proxies[id], nil }
	scopes := func(context.Context, string) (template.Chain, error) {
		return template.Chain{template.MapScope{Label: "Globals", Variables: map[string]template.Variable{
			"proxyAddr":     {Value: ownAddr},
			"proxyPassword": {Value: proxySecret, Secret: true},
		}}}, nil
	}
	p.s, p.ev = newService(t, netx.Routed(router.Route), WithScopes(scopes), WithProxy(source))
	return p
}

// TestOpenThroughRequestProxy: a udp request's own proxy replaces the one
// in effect: a custom SOCKS5 proxy relays the datagrams with UDP
// ASSOCIATE, with its credentials and its URL and password rendered with
// the request's variables; none sends directly; inherit uses the proxy in
// effect. No event holds the password.
func TestOpenThroughRequestProxy(t *testing.T) {
	t.Parallel()
	port := echoPort(t)
	portNum, _ := strconv.Atoi(port)
	for _, tt := range []struct {
		name, requestID    string
		viaOwn, viaAmbient bool
	}{
		{name: "custom", requestID: "req_custom", viaOwn: true},
		{name: "none", requestID: "req_none"},
		{name: "inherit", requestID: "req_inherit", viaAmbient: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProxied(t)
			conn, err := p.s.Open(t.Context(), OpenInputDTO{RequestID: tt.requestID, Address: net.JoinHostPort(proxyTarget, port)})
			if err != nil {
				t.Fatal(err)
			}
			id := conn.ConnID
			waitOpen(t, p.ev, id)
			if _, err := p.s.Send(t.Context(), SendInputDTO{ConnID: id, Mode: "text", Text: "hi"}); err != nil {
				t.Fatal(err)
			}
			p.ev.waitFor(t, "the reply", func(e *events) bool { return len(e.datagramsOf(t, id, "received")) == 1 })
			open := p.ev.state(id, "open")
			if err := p.s.Close(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			want := []netxtest.SOCKS5Request{{Name: true, Host: proxyTarget, Port: portNum}}
			if got := p.own.UDPDatagrams(); tt.viaOwn != (len(got) > 0) || tt.viaOwn && !slices.Equal(got, want) {
				t.Errorf("the request's proxy relayed %+v", got)
			}
			if got := p.ambient.UDPDatagrams(); tt.viaAmbient != (len(got) > 0) || tt.viaAmbient && !slices.Equal(got, want) {
				t.Errorf("the proxy in effect relayed %+v", got)
			}
			if tt.viaOwn {
				if got := p.own.Credentials(); !slices.Equal(got, []string{"alice:" + proxySecret}) {
					t.Errorf("credentials = %q", got)
				}
				if open.Via != "socks5h://"+p.own.Addr() || open.Proxy == nil || open.Proxy.Command != "UDP ASSOCIATE" {
					t.Errorf("open = %+v, hop %+v", open, open.Proxy)
				}
			}
			if !tt.viaOwn && !tt.viaAmbient && (open.Via != "" || open.Proxy != nil || p.ambient.Accepted() != 0) {
				t.Errorf("none: open = %+v, the proxy in effect accepted %d", open, p.ambient.Accepted())
			}
			p.ev.mu.Lock()
			all, _ := json.Marshal(p.ev.states)
			p.ev.mu.Unlock()
			if strings.Contains(string(all), proxySecret) {
				t.Errorf("an event shows the proxy's password: %s", all)
			}
		})
	}
}

// TestRequestProxyRefusesUDP: a request's own SOCKS4 or HTTP proxy cannot
// relay datagrams, so opening fails with the unsupported-network error,
// never sending directly or through the proxy in effect; a proxy that
// cannot be used fails Open before anything is dialed.
func TestRequestProxyRefusesUDP(t *testing.T) {
	t.Parallel()
	p := newProxied(t)
	for id, code := range map[string]string{
		"req_socks4": netx.CodeSOCKSUnsupportedNetwork,
		"req_http":   netx.CodeHTTPProxyUnsupportedNetwork,
	} {
		conn, err := p.s.Open(t.Context(), OpenInputDTO{RequestID: id, Address: proxyTarget + ":53"})
		if err != nil {
			t.Fatal(err)
		}
		p.ev.waitFor(t, "closed", func(e *events) bool { return strings.HasSuffix(e.statesOf(conn.ConnID), "closed") })
		if closed := p.ev.state(conn.ConnID, "closed"); closed.Error == nil || closed.Error.Code != code {
			t.Errorf("%s: closed = %+v, %+v; want %s", id, closed, closed.Error, code)
		}
	}
	for id, code := range map[string]string{
		"req_no_url": netx.CodeProxyInvalidURL,
		"req_undef":  template.CodeUnresolved,
	} {
		_, err := p.s.Open(t.Context(), OpenInputDTO{RequestID: id, Address: proxyTarget + ":53"})
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != code {
			t.Errorf("%s: %v, want %s", id, err, code)
			continue
		}
		if code == netx.CodeProxyInvalidURL && e.Details["source"] != request.ProxySourceRequest {
			t.Errorf("%s: details %v", id, e.Details)
		}
	}
	if n := p.ambient.Accepted() + p.own.Accepted() + p.socks4.Accepted() + p.http.Accepted(); n != 0 {
		t.Errorf("the proxies accepted %d connections", n)
	}
}
