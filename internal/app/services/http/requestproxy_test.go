package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// ambientDialer is a dialer built like the app's, whose proxy in effect is
// ambient and whose direct dialer resolves proxyHost to 127.0.0.1 (so a
// direct connection to it is not a loopback target the route would keep
// direct anyway).
func ambientDialer(ambient netx.ProxyURL) netx.Dialer {
	resolver, _ := netxtest.RecordingResolver(map[string]string{proxyHost: "127.0.0.1"})
	router := netx.NewProxyRouter(netx.NewDirect(netx.Options{Resolver: resolver}), func(context.Context) (netx.ProxyURL, error) { return ambient, nil }, netx.ProxyOptions{})
	chain := netx.Chain(netx.Routed(router.Route), netx.Guarded(netx.RequireUserOp(nil)))
	return netx.NewForwardProxier(chain, router.ForwardProxy)
}

// TestSendThroughRequestProxy: a request's own proxy (settings.proxy)
// replaces the proxy in effect. custom goes through the request's SOCKS5
// proxy with its credentials, none connects directly, inherit uses the
// proxy in effect; each keeps its own kept-alive connections, so a
// connection is reused within one setting only. The response says which
// setting applied, and the proxy password shows in no response, history
// entry, log or masked cURL command.
func TestSendThroughRequestProxy(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_, _ = io.WriteString(w, "origin "+r.URL.Path)
	}))
	t.Cleanup(origin.Close)
	_, port, _ := net.SplitHostPort(origin.Listener.Addr().String())
	url := "http://" + net.JoinHostPort(proxyHost, port) + "/items"

	ambient := &netxtest.SOCKS5Server{Hosts: map[string]string{proxyHost: "127.0.0.1"}}
	ambientURL, _ := netx.ParseProxyURL("socks5h://" + ambient.Start(t))
	own := &netxtest.SOCKS5Server{Username: "bob", Password: proxySecret, Hosts: map[string]string{proxyHost: "127.0.0.1"}}
	ownAddr := own.Start(t)

	var logs safeBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st, historyOpt := historyStore(t)
	f := newProxyFixture(t, httpSource(url), ambientDialer(ambientURL), logger, historyOpt)

	send := func(p projectfile.RequestProxy) ResponseDTO {
		t.Helper()
		f.src.Request.Settings.Proxy = p
		id := f.send(t)
		if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
			t.Fatalf("done = %+v, error %+v", done, done.Error)
		}
		resp := f.rec.response(t, id)
		if resp.StatusCode != 200 || resp.Body != "origin /items" {
			t.Fatalf("response %d %q", resp.StatusCode, resp.Body)
		}
		return resp
	}
	custom := projectfile.RequestProxy{Mode: projectfile.ProxyCustom, URL: "socks5h://bob@" + ownAddr, Password: proxySecret, LocalSecrets: []string{"password"}}

	// Custom, twice: one connection through the request's proxy, kept
	// alive for the second send.
	first := send(custom)
	second := send(custom)
	if own.Accepted() != 1 || len(own.Requests()) != 1 || !own.Requests()[0].Name || own.Requests()[0].Host != proxyHost {
		t.Errorf("own proxy: accepted %d, requests %+v", own.Accepted(), own.Requests())
	}
	if creds := own.Credentials(); len(creds) != 1 || creds[0] != "bob:"+proxySecret {
		t.Errorf("own proxy credentials = %v", creds)
	}
	if ambient.Accepted() != 0 {
		t.Errorf("the proxy in effect got %d connections for a request with its own", ambient.Accepted())
	}
	if first.RequestProxy != "socks5h://bob@"+ownAddr || second.RequestProxy != first.RequestProxy {
		t.Errorf("RequestProxy = %q", first.RequestProxy)
	}
	if c := first.Timeline.Hops[0].Conn; c.Via != "socks5h://"+ownAddr || c.Proxy == nil || c.Proxy.Auth != netx.AuthUserPass {
		t.Errorf("connection = %+v", c)
	}

	// Inherit: the proxy in effect, on a new connection.
	inherit := send(projectfile.RequestProxy{})
	if ambient.Accepted() != 1 || own.Accepted() != 1 || inherit.RequestProxy != "" {
		t.Errorf("inherit: ambient accepted %d, own %d, RequestProxy %q", ambient.Accepted(), own.Accepted(), inherit.RequestProxy)
	}
	if inherit.Timeline.Hops[0].Conn.Via != "socks5h://"+ambientURL.Address() {
		t.Errorf("inherit went via %q", inherit.Timeline.Hops[0].Conn.Via)
	}

	// None: directly, although a proxy is in effect; the URL is kept.
	none := send(projectfile.RequestProxy{Mode: projectfile.ProxyNone, URL: custom.URL})
	if ambient.Accepted() != 1 || own.Accepted() != 1 {
		t.Errorf("none: ambient accepted %d, own %d", ambient.Accepted(), own.Accepted())
	}
	if c := none.Timeline.Hops[0].Conn; c.Via != "" || c.Reused || none.RequestProxy != "none" {
		t.Errorf("none: connection %+v, RequestProxy %q", c, none.RequestProxy)
	}

	// The password shows nowhere.
	f.src.Request.Settings.Proxy = custom
	curl, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(curl.Command, "-x socks5h://"+ownAddr) || !strings.Contains(curl.Command, "--proxy-user 'bob:••••••'") || !curl.Masked {
		t.Errorf("cURL command = %s", curl.Command)
	}
	waitEntries(t, st, 4)
	dto, _ := json.Marshal([]ResponseDTO{first, second, inherit, none})
	for what, text := range map[string]string{
		"responses": string(dto), "history": historyFiles(t, st), "cURL": curl.Command, "log": logs.String(),
	} {
		if strings.Contains(text, proxySecret) {
			t.Errorf("the %s shows the proxy password: %s", what, text)
		}
	}
	withSecrets, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1", IncludeSecrets: true})
	if err != nil || !strings.Contains(withSecrets.Command, "--proxy-user bob:"+proxySecret) {
		t.Errorf("cURL command with secrets = %s, %v", withSecrets.Command, err)
	}
}

// TestRequestProxyErrors: a request's own proxy that cannot be used fails
// the send before anything is dialed, never going direct or through the
// proxy in effect; an undefined variable in it is reported like every
// other field's.
func TestRequestProxyErrors(t *testing.T) {
	t.Parallel()
	ambient := &netxtest.SOCKS5Server{Hosts: map[string]string{proxyHost: "127.0.0.1"}}
	ambientURL, _ := netx.ParseProxyURL("socks5h://" + ambient.Start(t))
	for _, tt := range []struct {
		name  string
		proxy projectfile.RequestProxy
		code  string
		field string
	}{
		{"no URL", projectfile.RequestProxy{Mode: projectfile.ProxyCustom}, netx.CodeProxyInvalidURL, request.FieldProxyURL},
		{"bad scheme", projectfile.RequestProxy{Mode: projectfile.ProxyCustom, URL: "ftp://proxy.lan"}, netx.CodeProxyInvalidURL, request.FieldProxyURL},
		{"password elsewhere", projectfile.RequestProxy{Mode: projectfile.ProxyCustom, URL: "socks5h://bob@proxy.lan", LocalSecrets: []string{"password"}}, request.CodeProxyPasswordMissing, request.FieldProxyPassword},
		{"unknown mode", projectfile.RequestProxy{Mode: "pac"}, request.CodeInvalidSetting, request.FieldProxyMode},
		{"undefined variable", projectfile.RequestProxy{Mode: projectfile.ProxyCustom, URL: "socks5h://{{proxyHost}}:1080"}, template.CodeUnresolved, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := httpSource("http://" + proxyHost + ":1/")
			src.Request.Settings.Proxy = tt.proxy
			f := newProxyFixture(t, src, ambientDialer(ambientURL), nil)
			_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
			e := asErr(err)
			if e == nil {
				t.Fatalf("Send = %v, want an error", err)
			}
			if e.Code != tt.code || (tt.field != "" && e.Details["field"] != tt.field) {
				t.Fatalf("err = %+v, want %s on %s", e, tt.code, tt.field)
			}
			if tt.code == netx.CodeProxyInvalidURL && e.Details["source"] != request.ProxySourceRequest {
				t.Errorf("details = %v", e.Details)
			}
			if tt.code == template.CodeUnresolved && !strings.Contains(e.Details["names"], "proxyHost") {
				t.Errorf("unresolved details = %v", e.Details)
			}
			if ambient.Accepted() != 0 {
				t.Error("a failed request proxy dialed the proxy in effect")
			}
		})
	}
}

// TestHandshakeTokenThroughRequestProxy: the OAuth 2.0 token request of a
// websocket, socketio or grpc handshake goes through the request's own
// proxy when the handshake has one, not through the proxy in effect.
func TestHandshakeTokenThroughRequestProxy(t *testing.T) {
	t.Parallel()
	idp := newTokenIDP(t)
	_, port, _ := net.SplitHostPort(idp.Listener.Addr().String())
	ambient := &netxtest.SOCKS5Server{Hosts: map[string]string{proxyHost: "127.0.0.1"}}
	ambientURL, _ := netx.ParseProxyURL("socks5h://" + ambient.Start(t))
	own := &netxtest.SOCKS5Server{Hosts: map[string]string{proxyHost: "127.0.0.1"}}
	ownURL, _ := netx.ParseProxyURL("socks5h://" + own.Start(t))

	src := oauth2Source("ws://feed.test", "http://"+net.JoinHostPort(proxyHost, port))
	src.Handshake = projectfile.KindWebSocket
	f := newProxyFixture(t, src, ambientDialer(ambientURL), nil)
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test", ID: "1"})
	h, err := HandshakeAuth(ctx, f.svc, request.HandshakeInput{RequestID: "req_1", Protocol: request.HandshakeWebSocket, URL: "ws://feed.test/",
		Proxy: request.Proxy{Set: true, URL: ownURL}})
	if err != nil {
		t.Fatal(err)
	}
	if h.Credentials == nil || h.Credentials.Token != "issued-token-1" {
		t.Errorf("handshake = %+v", h)
	}
	if len(own.Requests()) != 1 || ambient.Accepted() != 0 {
		t.Errorf("token request: own proxy %v, proxy in effect %d connections", own.Requests(), ambient.Accepted())
	}
}
