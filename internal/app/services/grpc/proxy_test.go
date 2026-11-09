package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

const (
	// proxyTarget is a name only the test proxies and the test's direct
	// dialer can resolve: reaching it proves which way a call went.
	proxyTarget = "grpc.example.test"
	proxySecret = "grpc-pr0xy-s3cret"
)

// proxied is a service whose dialer routes like the app's: through the
// ambient SOCKS5 proxy in effect, or a request's own proxy (own, which
// takes only alice with proxySecret). Both can name proxyTarget; direct
// counts the connections made to it without a proxy.
type proxied struct {
	s            *GRPCService
	rec          *recorder
	ambient, own *netxtest.SOCKS5Server
	direct       atomic.Int32
}

func newProxied(t *testing.T) *proxied {
	t.Helper()
	hosts := map[string]string{proxyTarget: "127.0.0.1"}
	p := &proxied{
		ambient: &netxtest.SOCKS5Server{Hosts: hosts},
		own:     &netxtest.SOCKS5Server{Username: "alice", Password: proxySecret, Hosts: hosts},
	}
	ambientURL, err := netx.ParseProxyURL("socks5h://" + p.ambient.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	ownAddr := p.own.Start(t)
	// The direct dialer: it reaches proxyTarget at its address, so only
	// a connection without a proxy names it here.
	plain := netx.NewDirect(netx.Options{})
	direct := netx.DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		if h, port, err := net.SplitHostPort(address); err == nil && h == proxyTarget {
			p.direct.Add(1)
			address = net.JoinHostPort(hosts[h], port)
		}
		return plain.DialContext(ctx, network, address)
	})
	proxies := map[string]projectfile.RequestProxy{
		"req_custom": {Mode: projectfile.ProxyCustom, URL: "socks5h://alice@{{proxyAddr}}", Password: "{{proxyPassword}}"},
		"req_none":   {Mode: projectfile.ProxyNone},
		"req_no_url": {Mode: projectfile.ProxyCustom},
		"req_undef":  {Mode: projectfile.ProxyCustom, URL: "socks5h://{{nope}}:1080"},
	}
	router := netx.NewProxyRouter(direct, func(context.Context) (netx.ProxyURL, error) { return ambientURL, nil }, netx.ProxyOptions{})
	dialer := netx.NewForwardProxier(netx.Chain(netx.Routed(router.Route), netx.Guarded(netx.RequireUserOp(nil))), router.ForwardProxy)
	source := func(_ context.Context, id string) (projectfile.RequestProxy, error) { return proxies[id], nil }
	scopes := func(context.Context, string) (template.Chain, error) {
		return template.Chain{template.MapScope{Label: "Globals", Variables: map[string]template.Variable{
			"proxyAddr":     {Value: ownAddr},
			"proxyPassword": {Value: proxySecret, Secret: true},
		}}}, nil
	}
	p.s, p.rec = newCallServiceWith(t, dialer, WithProxy(source, scopes))
	return p
}

// TestInvokeThroughRequestProxy: a grpc request's own proxy replaces the
// one in effect for unary and streaming calls: custom calls through it
// with its credentials, its URL and password rendered with the request's
// variables; none connects directly; inherit uses the proxy in effect.
// The cached connections are kept apart per proxy setting: calls to the
// same server with another setting never reuse one, calls with the same
// setting do. No item holds the password.
func TestInvokeThroughRequestProxy(t *testing.T) {
	t.Parallel()
	srv := newCallServer(t, false)
	_, port, _ := net.SplitHostPort(srv.Addr)
	portNum, _ := strconv.Atoi(port)
	target := netxtest.SOCKS5Request{Name: true, Host: proxyTarget, Port: portNum}
	p := newProxied(t)

	var all []CallEventDTO
	call := func(requestID, method string) {
		t.Helper()
		in := input(net.JoinHostPort(proxyTarget, port), method)
		in.RequestID = requestID
		items, done := invoke(t, p.s, p.rec, in)
		if done.Status != operation.StatusOK {
			t.Fatalf("%s %s: done %+v %+v", requestID, method, done, done.Error)
		}
		all = append(all, items...)
	}
	// counts checks how many connections went through the request's
	// proxy, the proxy in effect and directly so far.
	counts := func(step string, own, ambient, direct int) {
		t.Helper()
		if got := p.own.Requests(); len(got) != own || own > 0 && !slices.Equal(got, slices.Repeat([]netxtest.SOCKS5Request{target}, own)) {
			t.Errorf("%s: the request's proxy got %+v, want %d", step, got, own)
		}
		if got := p.ambient.Requests(); len(got) != ambient || ambient > 0 && !slices.Equal(got, slices.Repeat([]netxtest.SOCKS5Request{target}, ambient)) {
			t.Errorf("%s: the proxy in effect got %+v, want %d", step, got, ambient)
		}
		if got := int(p.direct.Load()); got != direct {
			t.Errorf("%s: %d direct connections, want %d", step, got, direct)
		}
	}

	call("req_custom", "Echo")
	counts("custom", 1, 0, 0)
	call("req_inherit", "Echo")
	counts("inherit after custom", 1, 1, 0)
	call("req_none", "Repeat")
	counts("none after inherit", 1, 1, 1)
	call("req_custom", "Repeat")
	call("req_inherit", "Repeat")
	call("req_none", "Echo")
	call("", "Echo") // no request: the proxy in effect
	counts("each setting again", 1, 1, 1)
	if got := p.own.Credentials(); !slices.Equal(got, []string{"alice:" + proxySecret}) {
		t.Errorf("credentials = %q", got)
	}
	data, _ := json.Marshal(all)
	if strings.Contains(string(data), proxySecret) {
		t.Errorf("an item shows the proxy's password: %s", data)
	}
}

// TestRequestProxyRefused: a request proxy that cannot be used fails
// Invoke before anything is dialed: no URL (netx.proxy_invalid_url,
// source request), an undefined variable (template.unresolved).
func TestRequestProxyRefused(t *testing.T) {
	t.Parallel()
	p := newProxied(t)
	for id, code := range map[string]string{
		"req_no_url": netx.CodeProxyInvalidURL,
		"req_undef":  template.CodeUnresolved,
	} {
		in := input(proxyTarget+":7", "Echo")
		in.RequestID = id
		_, err := p.s.Invoke(t.Context(), in)
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != code {
			t.Errorf("%s: %v, want %s", id, err, code)
			continue
		}
		if code == netx.CodeProxyInvalidURL && e.Details["source"] != request.ProxySourceRequest {
			t.Errorf("%s: details %v", id, e.Details)
		}
	}
	if n := p.ambient.Accepted() + p.own.Accepted() + int(p.direct.Load()); n != 0 {
		t.Errorf("%d connections were made", n)
	}
}
