package websocket

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
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
