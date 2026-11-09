package tcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

const (
	// proxyTarget is a name only the test proxies and the test's direct
	// dialer can resolve: reaching it proves which way a connection went.
	proxyTarget = "tcp.example.test"
	proxySecret = "tcp-pr0xy-s3cret"
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
// ambient SOCKS5 proxy in effect, or a request's own proxy (own, which
// takes only alice with proxySecret). Both can name proxyTarget.
type proxied struct {
	s            *TCPService
	ev           *events
	ambient, own *netxtest.SOCKS5Server
}

func newProxied(t *testing.T, proxies map[string]projectfile.RequestProxy) *proxied {
	t.Helper()
	hosts := map[string]string{proxyTarget: "127.0.0.1"}
	p := &proxied{
		ev:      newEvents(),
		ambient: &netxtest.SOCKS5Server{Hosts: hosts},
		own:     &netxtest.SOCKS5Server{Username: "alice", Password: proxySecret, Hosts: hosts},
	}
	ambientURL, err := netx.ParseProxyURL("socks5h://" + p.ambient.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	ownAddr := p.own.Start(t)
	router := netx.NewProxyRouter(hostsDialer(hosts), func(context.Context) (netx.ProxyURL, error) { return ambientURL, nil }, netx.ProxyOptions{})
	dialer := netx.Chain(netx.Routed(router.Route), netx.Guarded(netx.RequireUserOp(nil)))
	source := func(_ context.Context, id string) (projectfile.RequestProxy, error) {
		if id == "req_gone" {
			return projectfile.RequestProxy{}, errs.New(errs.NotFound, "gone").WithCode("collection.not_found")
		}
		return proxies[id], nil
	}
	scopes := func(context.Context, string) (template.Chain, error) {
		return template.Chain{template.MapScope{Label: "Globals", Variables: map[string]template.Variable{
			"proxyAddr":     {Value: ownAddr},
			"proxyPassword": {Value: proxySecret, Secret: true},
		}}}, nil
	}
	p.s = New(dialer, p.ev.emit, nil, withInterval(5*time.Millisecond), WithProxy(source, scopes))
	t.Cleanup(func() { _ = p.s.ServiceShutdown() })
	return p
}

// custom is a request's own proxy: own, its address and password from
// variables.
var custom = projectfile.RequestProxy{Mode: projectfile.ProxyCustom, URL: "socks5h://alice@{{proxyAddr}}", Password: "{{proxyPassword}}"}

// TestConnectThroughRequestProxy: a tcp request's own proxy replaces the
// one in effect: custom connects through it with its credentials, its
// URL and password rendered with the request's variables; none connects
// directly; inherit (also with no request ID) uses the proxy in effect.
// No event holds the password.
func TestConnectThroughRequestProxy(t *testing.T) {
	t.Parallel()
	echo := listen(t, func(c net.Conn) { _, _ = io.Copy(c, c) })
	_, port, _ := net.SplitHostPort(echo)
	target := net.JoinHostPort(proxyTarget, port)
	portNum, _ := strconv.Atoi(port)
	for _, tt := range []struct {
		name, requestID    string
		viaOwn, viaAmbient bool
		wantCredentials    []string
	}{
		{name: "custom", requestID: "req_custom", viaOwn: true, wantCredentials: []string{"alice:" + proxySecret}},
		{name: "none", requestID: "req_none"},
		{name: "inherit", requestID: "req_inherit", viaAmbient: true},
		{name: "no request", viaAmbient: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProxied(t, map[string]projectfile.RequestProxy{
				"req_custom": custom,
				"req_none":   {Mode: projectfile.ProxyNone, URL: "socks5h://unused.test:1080"},
			})
			conn, err := p.s.Connect(t.Context(), ConnectInputDTO{RequestID: tt.requestID, Address: target})
			if err != nil {
				t.Fatal(err)
			}
			id := conn.ConnID
			p.ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(id) == "connecting,open" })
			if _, err := p.s.Send(t.Context(), SendInputDTO{ConnID: id, Mode: "text", Text: "hi"}); err != nil {
				t.Fatal(err)
			}
			p.ev.waitFor(t, "the echo", func(e *events) bool { return e.bytesOf(t, id, "received") == "hi" })
			if err := p.s.Close(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			want := []netxtest.SOCKS5Request{{Name: true, Host: proxyTarget, Port: portNum}}
			if got := p.own.Requests(); tt.viaOwn != (len(got) > 0) || tt.viaOwn && !slices.Equal(got, want) {
				t.Errorf("the request's proxy got %+v", got)
			}
			if got := p.ambient.Requests(); tt.viaAmbient != (len(got) > 0) || tt.viaAmbient && !slices.Equal(got, want) {
				t.Errorf("the proxy in effect got %+v", got)
			}
			if got := p.own.Credentials(); !slices.Equal(got, tt.wantCredentials) {
				t.Errorf("credentials = %q, want %q", got, tt.wantCredentials)
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

// TestRequestProxyRefused: a request proxy that cannot be used fails
// Connect before anything is dialed, never connecting directly or
// through the proxy in effect: no URL (netx.proxy_invalid_url, source
// request), an undefined variable, a mode this version does not know, a
// request that cannot be loaded.
func TestRequestProxyRefused(t *testing.T) {
	t.Parallel()
	p := newProxied(t, map[string]projectfile.RequestProxy{
		"req_no_url": {Mode: projectfile.ProxyCustom},
		"req_undef":  {Mode: projectfile.ProxyCustom, URL: "socks5h://{{nope}}:1080"},
		"req_mode":   {Mode: "tunnel"},
	})
	for id, code := range map[string]string{
		"req_no_url": netx.CodeProxyInvalidURL,
		"req_undef":  template.CodeUnresolved,
		"req_mode":   request.CodeInvalidSetting,
		"req_gone":   "collection.not_found",
	} {
		_, err := p.s.Connect(t.Context(), ConnectInputDTO{RequestID: id, Address: "tcp.example.test:7"})
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != code {
			t.Errorf("%s: %v, want %s", id, err, code)
			continue
		}
		if code == netx.CodeProxyInvalidURL && (e.Details["source"] != request.ProxySourceRequest || e.Details["field"] != request.FieldProxyURL) {
			t.Errorf("%s: details %v", id, e.Details)
		}
	}
	if n := p.ambient.Accepted() + p.own.Accepted(); n != 0 {
		t.Errorf("the proxies accepted %d connections", n)
	}
	p.ev.mu.Lock()
	defer p.ev.mu.Unlock()
	if len(p.ev.states) != 0 {
		t.Errorf("events %+v", p.ev.states)
	}
}
