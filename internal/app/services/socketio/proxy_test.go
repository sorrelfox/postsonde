package socketio

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient/siotest"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

const (
	// proxyTarget is a name only the test proxies and the test's direct
	// dialer can resolve: reaching it proves which way a connection went.
	proxyTarget = "sio.example.test"
	proxySecret = "sio-pr0xy-s3cret"
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

// proxied is a fixture whose dialer routes like the app's: through the
// ambient SOCKS5 proxy in effect, or a request's own proxy (own, which
// takes only alice with proxySecret). Both can name proxyTarget.
type proxied struct {
	*fixture
	ambient, own *netxtest.SOCKS5Server
}

func newProxied(t *testing.T) *proxied {
	t.Helper()
	hosts := map[string]string{proxyTarget: "127.0.0.1"}
	p := &proxied{
		fixture: &fixture{rec: newRecorder()},
		ambient: &netxtest.SOCKS5Server{Hosts: hosts},
		own:     &netxtest.SOCKS5Server{Username: "alice", Password: proxySecret, Hosts: hosts},
	}
	p.ops = operation.NewRegistry(p.rec.emit, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.ops.Shutdown(ctx)
	})
	ambientURL, err := netx.ParseProxyURL("socks5h://" + p.ambient.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	ownAddr := p.own.Start(t)
	proxies := map[string]projectfile.RequestProxy{
		"req_custom": {Mode: projectfile.ProxyCustom, URL: "socks5h://alice@{{proxyAddr}}", Password: "{{proxyPassword}}"},
		"req_none":   {Mode: projectfile.ProxyNone},
		"req_no_url": {Mode: projectfile.ProxyCustom},
		"req_undef":  {Mode: projectfile.ProxyCustom, URL: "socks5h://{{nope}}:1080"},
	}
	router := netx.NewProxyRouter(hostsDialer(hosts), func(context.Context) (netx.ProxyURL, error) { return ambientURL, nil }, netx.ProxyOptions{})
	dialer := netx.NewForwardProxier(netx.Chain(netx.Routed(router.Route), netx.Guarded(netx.RequireUserOp(nil))), router.ForwardProxy)
	source := func(_ context.Context, id string) (projectfile.RequestProxy, error) { return proxies[id], nil }
	scopes := func(context.Context, string) (template.Chain, error) {
		return template.Chain{template.MapScope{Label: "Globals", Variables: map[string]template.Variable{
			"proxyAddr":     {Value: ownAddr},
			"proxyPassword": {Value: proxySecret, Secret: true},
		}}}, nil
	}
	p.svc = New(p.ops, dialer, nil, nil, WithScopes(scopes), WithProxy(source), withInterval(5*time.Millisecond))
	return p
}

// TestConnectThroughRequestProxy: a socketio request's own proxy replaces
// the one in effect for every connection of every transport (the polling
// requests, the WebSocket, the upgrade's probe): custom connects through
// it with its credentials, its URL and password rendered with the
// request's variables; none connects directly; inherit uses the proxy in
// effect. No item holds the password.
func TestConnectThroughRequestProxy(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{}, t.Cleanup)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	portNum, _ := strconv.Atoi(port)
	for _, mode := range []struct {
		name, requestID    string
		viaOwn, viaAmbient bool
	}{
		{name: "custom", requestID: "req_custom", viaOwn: true},
		{name: "none", requestID: "req_none"},
		{name: "inherit", requestID: "req_inherit", viaAmbient: true},
	} {
		for _, tr := range []string{"polling", "websocket", "upgrade"} {
			t.Run(mode.name+" "+tr, func(t *testing.T) {
				t.Parallel()
				p := newProxied(t)
				id := p.connect(t, ConnectInputDTO{RequestID: mode.requestID, URL: "http://" + net.JoinHostPort(proxyTarget, port), Transport: tr})
				p.rec.waitFor(t, id, "connected", isType(ItemConnected))
				if tr == "upgrade" {
					p.rec.waitFor(t, id, "upgrade", isType(ItemUpgrade))
				}
				if _, err := p.svc.Emit(t.Context(), EmitInputDTO{OpID: id, Event: "echo", Args: `["hi"]`, Ack: true}); err != nil {
					t.Fatal(err)
				}
				p.rec.waitFor(t, id, "ack", isType(ItemAck))
				p.ops.Cancel(id)
				p.rec.wait(t, id)

				check := func(who string, s *netxtest.SOCKS5Server, want bool) {
					t.Helper()
					got := s.Requests()
					if want != (len(got) > 0) {
						t.Errorf("%s got %+v", who, got)
					}
					for _, r := range got {
						if r != (netxtest.SOCKS5Request{Name: true, Host: proxyTarget, Port: portNum}) {
							t.Errorf("%s got %+v", who, r)
						}
					}
				}
				check("the request's proxy", p.own, mode.viaOwn)
				check("the proxy in effect", p.ambient, mode.viaAmbient)
				if mode.viaOwn {
					for _, c := range p.own.Credentials() {
						if c != "alice:"+proxySecret {
							t.Errorf("credentials %q", c)
						}
					}
				}
				all, _ := json.Marshal(p.rec.list(id))
				if strings.Contains(string(all), proxySecret) {
					t.Errorf("an item shows the proxy's password: %s", all)
				}
			})
		}
	}
}

// TestRequestProxyRefused: a request proxy that cannot be used fails
// Connect before anything is dialed: no URL (netx.proxy_invalid_url,
// source request), an undefined variable (template.unresolved, listed
// with the URL's).
func TestRequestProxyRefused(t *testing.T) {
	t.Parallel()
	p := newProxied(t)
	url := "http://" + proxyTarget + ":7"
	_, err := p.svc.Connect(t.Context(), ConnectInputDTO{RequestID: "req_no_url", URL: url})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != netx.CodeProxyInvalidURL || e.Details["source"] != request.ProxySourceRequest {
		t.Errorf("no URL: %v", err)
	}
	_, err = p.svc.Connect(t.Context(), ConnectInputDTO{RequestID: "req_undef", URL: url + "/?{{other}}"})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != template.CodeUnresolved || !strings.Contains(e.Details["names"], "nope") || !strings.Contains(e.Details["names"], "other") {
		t.Errorf("undefined: %v", err)
	}
	if n := p.ambient.Accepted() + p.own.Accepted(); n != 0 {
		t.Errorf("the proxies accepted %d connections", n)
	}
}
