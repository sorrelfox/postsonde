package websocket

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

const (
	proxyHost   = "api.example.test"
	proxySecret = "ws-pr0xy-s3cret"
)

// wsEcho upgrades every request and echoes text messages with "echo "
// before them.
var wsEcho = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	nc, brw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = nc.Close() }()
	_, _ = nc.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n"))
	conn := wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleServer, Reader: brw.Reader})
	for {
		op, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.WriteMessage(op, append([]byte("echo "), data...))
	}
})

// TestConnectThroughHTTPProxy (NET-02): ws:// and wss:// connections go
// through an http:// or https:// proxy's CONNECT tunnel, with and without
// Basic credentials, TLS end to end for wss; a wrong password closes the
// connection with netx.http_proxy_auth_failed; no event shows the
// credentials.
func TestConnectThroughHTTPProxy(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "test CA")
	plain := httptest.NewServer(wsEcho)
	t.Cleanup(plain.Close)
	secure := httptest.NewUnstartedServer(wsEcho)
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{DNSNames: []string{proxyHost}}).TLS}}
	secure.Config.ErrorLog = log.New(io.Discard, "", 0)
	secure.StartTLS()
	t.Cleanup(secure.Close)
	_, plainPort, _ := net.SplitHostPort(plain.Listener.Addr().String())
	_, securePort, _ := net.SplitHostPort(secure.Listener.Addr().String())
	proxyTLS := &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{CommonName: "proxy"}).TLS}}
	token := base64.StdEncoding.EncodeToString([]byte("alice:" + proxySecret))

	for _, tt := range []struct {
		name     string
		wss      bool
		proxyTLS bool
		auth     bool
		password string // the client's, when not the proxy's
	}{
		{name: "ws via http proxy"},
		{name: "ws via http proxy with auth", auth: true},
		{name: "wss via http proxy with auth", wss: true, auth: true},
		{name: "wss via https proxy with auth", wss: true, proxyTLS: true, auth: true},
		{name: "ws via https proxy", proxyTLS: true},
		{name: "wrong password", auth: true, password: "wrong-" + proxySecret},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proxy := &netxtest.HTTPProxyServer{Hosts: map[string]string{proxyHost: "127.0.0.1"}}
			if tt.auth {
				proxy.Username, proxy.Password = "alice", proxySecret
			}
			scheme := "http://"
			if tt.proxyTLS {
				proxy.TLS, scheme = proxyTLS, "https://"
			}
			addr := proxy.Start(t)
			u, _ := netx.ParseProxyURL(scheme + addr)
			if tt.auth {
				u.Username, u.Password = "alice", proxySecret
				if tt.password != "" {
					u.Password = tt.password
				}
			}
			router := netx.NewProxyRouter(nil, func(context.Context) (netx.ProxyURL, error) { return u, nil },
				netx.ProxyOptions{RootCAs: func() (*x509.CertPool, error) { return ca.Pool(), nil }})
			dialer := netx.NewForwardProxier(netx.Chain(netx.Routed(router.Route), netx.Guarded(netx.RequireUserOp(nil))), router.ForwardProxy)
			ev := newEvents()
			s := New(dialer, nil, ev.emit, nil, withInterval(5*time.Millisecond),
				withClientOptions(func(o *wsclient.Options) { o.CloseLinger = 50 * time.Millisecond }),
				WithTrust(func() (*x509.CertPool, error) { return ca.Pool(), nil }))
			t.Cleanup(func() { _ = s.ServiceShutdown() })

			target := net.JoinHostPort(proxyHost, plainPort)
			url := "ws://" + target + "/feed"
			if tt.wss {
				target = net.JoinHostPort(proxyHost, securePort)
				url = "wss://" + target + "/feed"
			}
			conn, err := s.Connect(t.Context(), ConnectInputDTO{URL: url})
			if err != nil {
				t.Fatal(err)
			}
			id := conn.ConnID
			ev.waitFor(t, "open or closed", func(e *events) bool {
				st := e.statesOf(id)
				return st == "connecting,open" || strings.HasSuffix(st, "closed")
			})
			if tt.password != "" {
				closed := ev.state(id, "closed")
				if closed == nil || closed.Error == nil || closed.Error.Code != netx.CodeHTTPProxyAuthFailed || closed.Error.Hint == "" {
					t.Fatalf("closed = %+v, want %s", closed, netx.CodeHTTPProxyAuthFailed)
				}
			} else {
				if closed := ev.state(id, "closed"); closed != nil {
					t.Fatalf("closed = %+v, %+v", closed, closed.Error)
				}
				if _, err := s.Send(t.Context(), SendInputDTO{ConnID: id, Text: "hi"}); err != nil {
					t.Fatal(err)
				}
				ev.waitFor(t, "the echo", func(e *events) bool { return len(e.log(id)) >= 2 })
				ev.mu.Lock()
				got := ev.log(id)
				ev.mu.Unlock()
				if got[1] != "received text echo hi" {
					t.Errorf("log = %q", got)
				}
				if hs := ev.state(id, "open").Handshake; hs == nil || hs.RemoteAddr != addr {
					t.Errorf("handshake = %+v, want the proxy %s as remote address", hs, addr)
				}
				if err := s.Close(t.Context(), CloseInputDTO{ConnID: id, Code: 1000}); err != nil {
					t.Fatal(err)
				}
			}
			reqs := proxy.Requests()
			if len(reqs) != 1 || reqs[0].Method != http.MethodConnect || reqs[0].Target != target {
				t.Fatalf("proxy requests = %+v, want one CONNECT %s", reqs, target)
			}
			if tt.auth && tt.password == "" && reqs[0].Authorization != "Basic "+token {
				t.Errorf("Proxy-Authorization = %q", reqs[0].Authorization)
			}
			ev.mu.Lock()
			all, _ := json.Marshal(ev.states)
			ev.mu.Unlock()
			for _, secret := range []string{proxySecret, token} {
				if strings.Contains(string(all), secret) {
					t.Errorf("an event shows the proxy's credentials: %s", all)
				}
			}
		})
	}
}

// reqProxyTarget is a name only the test proxies and the test's direct
// dialer can resolve: reaching it proves which way a connection went.
const reqProxyTarget = "ws.example.test"

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

// reqProxied is a service whose dialer routes like the app's: through
// the ambient SOCKS5 proxy in effect, or a request's own proxy (own,
// which takes only alice with proxySecret). Both can name reqProxyTarget.
type reqProxied struct {
	s            *WebSocketService
	ev           *events
	ambient, own *netxtest.SOCKS5Server
}

func newReqProxied(t *testing.T) *reqProxied {
	t.Helper()
	hosts := map[string]string{reqProxyTarget: "127.0.0.1"}
	p := &reqProxied{
		ev:      newEvents(),
		ambient: &netxtest.SOCKS5Server{Hosts: hosts},
		own:     &netxtest.SOCKS5Server{Username: "alice", Password: proxySecret, Hosts: hosts},
	}
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
	p.s = New(dialer, nil, p.ev.emit, nil, withInterval(5*time.Millisecond),
		withClientOptions(func(o *wsclient.Options) { o.CloseLinger = 50 * time.Millisecond }),
		WithScopes(scopes), WithProxy(source))
	t.Cleanup(func() { _ = p.s.ServiceShutdown() })
	return p
}

// TestConnectThroughRequestProxy: a websocket request's own proxy
// replaces the one in effect, for the first attempt and every reconnect:
// custom connects through it with its credentials, its URL and password
// rendered with the request's variables; none connects directly; inherit
// uses the proxy in effect. No event holds the password.
func TestConnectThroughRequestProxy(t *testing.T) {
	t.Parallel()
	srv := scriptServer(t, nil, func(_ int, _ *http.Request, conn *wsframe.Conn, nc net.Conn) { echoAll(conn, nc) })
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	portNum, _ := strconv.Atoi(port)
	target := netxtest.SOCKS5Request{Name: true, Host: reqProxyTarget, Port: portNum}
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
			p := newReqProxied(t)
			on, delay := true, int64(100)
			conn, err := p.s.Connect(t.Context(), ConnectInputDTO{RequestID: tt.requestID, URL: "ws://" + net.JoinHostPort(reqProxyTarget, port) + "/feed",
				AutoReconnect: &on, ReconnectDelayMs: &delay})
			if err != nil {
				t.Fatal(err)
			}
			id := conn.ConnID
			opens := func(e *events) int { return strings.Count(e.statesOf(id), "open") }
			p.ev.waitFor(t, "open", func(e *events) bool { return opens(e) == 1 })
			// The server drops the first connection: the reconnect goes
			// the same way.
			if _, err := p.s.Send(t.Context(), SendInputDTO{ConnID: id, Text: "drop"}); err != nil {
				t.Fatal(err)
			}
			p.ev.waitFor(t, "the reconnect", func(e *events) bool { return opens(e) == 2 })
			hs := p.ev.state(id, "open").Handshake
			if err := p.s.Close(t.Context(), CloseInputDTO{ConnID: id, Code: 1000}); err != nil {
				t.Fatal(err)
			}
			want := []netxtest.SOCKS5Request{target, target}
			if got := p.own.Requests(); tt.viaOwn != (len(got) > 0) || tt.viaOwn && !slices.Equal(got, want) {
				t.Errorf("the request's proxy got %+v", got)
			}
			if got := p.ambient.Requests(); tt.viaAmbient != (len(got) > 0) || tt.viaAmbient && !slices.Equal(got, want) {
				t.Errorf("the proxy in effect got %+v", got)
			}
			wantRemote := "127.0.0.1:" + port
			switch {
			case tt.viaOwn:
				wantRemote = p.own.Addr()
				if got := p.own.Credentials(); !slices.Equal(got, []string{"alice:" + proxySecret, "alice:" + proxySecret}) {
					t.Errorf("credentials = %q", got)
				}
			case tt.viaAmbient:
				wantRemote = p.ambient.Addr()
			}
			if hs == nil || hs.RemoteAddr != wantRemote {
				t.Errorf("handshake = %+v, want remote address %s", hs, wantRemote)
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
// Connect before anything is dialed: no URL (netx.proxy_invalid_url,
// source request), an undefined variable (template.unresolved, listed
// with the URL's).
func TestRequestProxyRefused(t *testing.T) {
	t.Parallel()
	p := newReqProxied(t)
	url := "ws://" + reqProxyTarget + ":7/feed"
	_, err := p.s.Connect(t.Context(), ConnectInputDTO{RequestID: "req_no_url", URL: url})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != netx.CodeProxyInvalidURL || e.Details["source"] != request.ProxySourceRequest {
		t.Errorf("no URL: %v", err)
	}
	_, err = p.s.Connect(t.Context(), ConnectInputDTO{RequestID: "req_undef", URL: url + "?{{other}}"})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != template.CodeUnresolved || e.Details["names"] != "other,nope" && e.Details["names"] != "nope,other" {
		t.Errorf("undefined: %v %v", err, e)
	}
	if n := p.ambient.Accepted() + p.own.Accepted(); n != 0 {
		t.Errorf("the proxies accepted %d connections", n)
	}
}
