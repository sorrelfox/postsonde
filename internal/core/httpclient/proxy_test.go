package httpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

// proxyHost is a host name only the test proxy knows.
const proxyHost = "api.example.test"

const proxySecret = "pr0xy-s3cret"

// proxyWorld is an origin reachable as proxyHost over http and https,
// an HTTP test proxy, and a Client whose dialer routes through it like
// the app's (netx.ProxyRouter as a netx.ForwardProxier).
type proxyWorld struct {
	proxy      *netxtest.HTTPProxyServer
	client     *Client
	httpURL    string
	httpsURL   string
	originSeen chan string
}

// newProxyWorld starts it; proxyTLS makes the proxy an https:// one,
// user and password are the proxy's (and the client's when send is set).
func newProxyWorld(t *testing.T, proxyTLS bool, user, password string, send bool) *proxyWorld {
	t.Helper()
	w := &proxyWorld{originSeen: make(chan string, 16)}
	ca := tlstest.NewCA(t, "test CA")
	handler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		select {
		case w.originSeen <- r.Method + " " + r.RequestURI + " host=" + r.Host + " proxyAuth=" + r.Header.Get("Proxy-Authorization"):
		default:
		}
		_, _ = io.WriteString(rw, "hello "+r.Proto)
	})
	plain := httptest.NewUnstartedServer(handler)
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	plain.Config.Protocols = &protocols
	plain.Start()
	t.Cleanup(plain.Close)
	secure := httptest.NewUnstartedServer(handler)
	secure.EnableHTTP2 = true
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{DNSNames: []string{proxyHost}}).TLS}}
	secure.StartTLS()
	t.Cleanup(secure.Close)
	_, plainPort, _ := net.SplitHostPort(plain.Listener.Addr().String())
	_, securePort, _ := net.SplitHostPort(secure.Listener.Addr().String())
	w.httpURL = "http://" + net.JoinHostPort(proxyHost, plainPort)
	w.httpsURL = "https://" + net.JoinHostPort(proxyHost, securePort)

	w.proxy = &netxtest.HTTPProxyServer{Username: user, Password: password, Hosts: map[string]string{proxyHost: "127.0.0.1"}}
	scheme := "http://"
	if proxyTLS {
		w.proxy.TLS = &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{CommonName: "proxy"}).TLS}}
		scheme = "https://"
	}
	addr := w.proxy.Start(t)
	u, err := netx.ParseProxyURL(scheme + addr)
	if err != nil {
		t.Fatal(err)
	}
	if send {
		u.Username, u.Password = user, password
	}
	router := netx.NewProxyRouter(nil, func(context.Context) (netx.ProxyURL, error) { return u, nil },
		netx.ProxyOptions{RootCAs: func() (*x509.CertPool, error) { return ca.Pool(), nil }})
	w.client = New(netx.NewForwardProxier(netx.Routed(router.Route), router.ForwardProxy), TransportOptions{RootCAs: ca.Pool()})
	t.Cleanup(w.client.CloseIdleConnections)
	return w
}

// checkNoProxySecret fails when the response shows the proxy's
// credentials anywhere: the request as sent, the timeline, the redirects.
func checkNoProxySecret(t *testing.T, resp *Response, err error) {
	t.Helper()
	token := base64.StdEncoding.EncodeToString([]byte("alice:" + proxySecret))
	var all strings.Builder
	if err != nil {
		all.WriteString(err.Error() + " " + strings.Join(errs.Chain(err), " "))
		if e, ok := err.(*errs.Error); ok {
			all.WriteString(e.Hint)
			for k, v := range e.Details {
				all.WriteString(k + "=" + v + " ")
			}
		}
	}
	if resp != nil {
		all.Write(resp.Request.Head())
		for _, e := range resp.Events {
			all.WriteString(e.Name + " " + e.Detail + "\n")
		}
		for _, h := range resp.Redirects {
			if h.Request != nil {
				all.Write(h.Request.Head())
			}
		}
	}
	for _, s := range []string{proxySecret, token} {
		if strings.Contains(all.String(), s) {
			t.Errorf("a proxy credential shows: %s", all.String())
		}
	}
	if resp != nil {
		for _, h := range resp.Request.Headers {
			if strings.EqualFold(h.Name, "Proxy-Authorization") {
				t.Errorf("the request as sent lists the proxy's %s field", h.Name)
			}
		}
	}
}

// seen returns what the origin saw of the next request.
func (w *proxyWorld) seen(t *testing.T) string {
	t.Helper()
	select {
	case s := <-w.originSeen:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("the origin got no request")
		return ""
	}
}

func hasEvent(resp *Response, name, detail string) bool {
	for _, e := range resp.Events {
		if e.Name == name && strings.Contains(e.Detail, detail) {
			return true
		}
	}
	return false
}

// TestProxyAbsoluteForm (NET-02): an http:// URL goes to an HTTP proxy
// in absolute form with Basic credentials, over http:// and https://
// proxies; connections are reused; the request as sent shows the
// absolute-form target and no Proxy-Authorization; the timeline names the
// proxy.
func TestProxyAbsoluteForm(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		proxyTLS bool
		user     string
		version  HTTPVersion
	}{
		{"http proxy, no auth", false, "", HTTPVersionAuto},
		{"http proxy, basic auth", false, "alice", HTTPVersionAuto},
		{"http proxy, HTTP/1.1", false, "alice", HTTPVersion1},
		{"https proxy, basic auth", true, "alice", HTTPVersionAuto},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pass := ""
			if tt.user != "" {
				pass = proxySecret
			}
			w := newProxyWorld(t, tt.proxyTLS, tt.user, pass, true)
			req := get(w.httpURL + "/items?id=1")
			req.Settings.HTTPVersion = tt.version
			for i := range 2 {
				resp, err := w.client.Send(t.Context(), req)
				if err != nil {
					t.Fatalf("Send %d = %v", i, err)
				}
				if string(resp.Body) != "hello HTTP/1.1" {
					t.Errorf("body = %q (%s)", resp.Body, resp.Status)
				}
				host := strings.TrimPrefix(w.httpURL, "http://")
				if got := w.seen(t); got != "GET /items?id=1 host="+host+" proxyAuth=" {
					t.Errorf("origin saw %q", got)
				}
				if resp.Request.Target != w.httpURL+"/items?id=1" {
					t.Errorf("recorded target = %q, want the absolute form", resp.Request.Target)
				}
				if resp.URL != w.httpURL+"/items?id=1" || resp.Request.URL != resp.URL {
					t.Errorf("URL = %q, request URL %q", resp.URL, resp.Request.URL)
				}
				via := "http://" + w.proxy.Addr()
				if tt.proxyTLS {
					via = "https://" + w.proxy.Addr()
				}
				if !hasEvent(resp, EventProxy, via) {
					t.Errorf("no proxy event naming %s: %+v", via, resp.Events)
				}
				if i == 0 && !hasEvent(resp, EventDialStart, "via "+via) {
					t.Errorf("no dial_start via the proxy: %+v", resp.Events)
				}
				if resp.Conn.RemoteAddr != w.proxy.Addr() {
					t.Errorf("connection to %s, want the proxy %s", resp.Conn.RemoteAddr, w.proxy.Addr())
				}
				checkNoProxySecret(t, resp, nil)
			}
			reqs := w.proxy.Requests()
			if len(reqs) != 2 || reqs[0].Method != http.MethodGet || reqs[0].Target != w.httpURL+"/items?id=1" {
				t.Fatalf("proxy requests = %+v", reqs)
			}
			wantAuth := ""
			if tt.user != "" {
				wantAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+proxySecret))
			}
			if reqs[0].Authorization != wantAuth || reqs[1].Authorization != wantAuth {
				t.Errorf("Proxy-Authorization = %q, %q; want %q", reqs[0].Authorization, reqs[1].Authorization, wantAuth)
			}
			if w.proxy.Accepted() != 1 {
				t.Errorf("the proxy accepted %d connections, want 1 (kept alive)", w.proxy.Accepted())
			}
		})
	}
}

// TestProxyConnectTunnel: https:// URLs, an http:// URL with a Host
// header of its own and h2c with prior knowledge go through a CONNECT
// tunnel, with TLS end to end to the origin.
func TestProxyConnectTunnel(t *testing.T) {
	t.Parallel()
	for _, proxyTLS := range []bool{false, true} {
		w := newProxyWorld(t, proxyTLS, "alice", proxySecret, true)
		host := strings.TrimPrefix(w.httpsURL, "https://")
		resp, err := w.client.Send(t.Context(), get(w.httpsURL+"/secure"))
		if err != nil {
			t.Fatalf("https through the proxy (tls=%v): %v", proxyTLS, err)
		}
		if string(resp.Body) != "hello HTTP/2.0" || resp.Conn.TLS == nil {
			t.Errorf("body %q, TLS %+v; want HTTP/2 over TLS to the origin", resp.Body, resp.Conn.TLS)
		}
		if resp.Request.Target != "/secure" || hasEvent(resp, EventProxy, "") || !hasEvent(resp, EventDialStart, "via ") {
			t.Errorf("target %q, events %+v", resp.Request.Target, resp.Events)
		}
		checkNoProxySecret(t, resp, nil)
		w.seen(t)

		// A Host header of its own keeps an http:// request in origin form.
		resp, err = w.client.Send(t.Context(), get(w.httpURL+"/vhost", on("Host", "virtual.test")))
		if err != nil {
			t.Fatal(err)
		}
		if got := w.seen(t); got != "GET /vhost host=virtual.test proxyAuth=" || resp.Request.Target != "/vhost" {
			t.Errorf("origin saw %q, target %q", got, resp.Request.Target)
		}
		// h2c with prior knowledge is not sent in absolute form.
		h2c := get(w.httpURL + "/h2c")
		h2c.Settings.HTTPVersion = HTTPVersion2
		if resp, err = w.client.Send(t.Context(), h2c); err != nil {
			t.Fatal(err)
		}
		if string(resp.Body) != "hello HTTP/2.0" || resp.Request.Target != "/h2c" {
			t.Errorf("h2c: body %q, target %q", resp.Body, resp.Request.Target)
		}
		w.seen(t)
		reqs := w.proxy.Requests()
		if len(reqs) != 3 || reqs[0].Method != http.MethodConnect || reqs[0].Target != host ||
			reqs[1].Method != http.MethodConnect || reqs[2].Method != http.MethodConnect {
			t.Errorf("proxy requests = %+v, want three CONNECTs", reqs)
		}
	}
}

// TestProxyAuthErrors: a 407 is an error with the proxy codes, for
// absolute-form requests and CONNECT alike, never showing the
// credentials.
func TestProxyAuthErrors(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		send  bool
		https bool
		code  string
	}{
		{"absolute form, no credentials", false, false, netx.CodeHTTPProxyAuthRequired},
		{"absolute form, wrong password", true, false, netx.CodeHTTPProxyAuthFailed},
		{"CONNECT, no credentials", false, true, netx.CodeHTTPProxyAuthRequired},
		{"CONNECT, wrong password", true, true, netx.CodeHTTPProxyAuthFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := newProxyWorld(t, false, "alice", "other", false)
			if tt.send {
				// The client sends proxySecret, the proxy wants "other".
				w = newProxyWorldWith(t, "alice", "other", "alice", proxySecret)
			}
			url := w.httpURL
			if tt.https {
				url = w.httpsURL
			}
			resp, err := w.client.Send(t.Context(), get(url+"/"))
			if err == nil {
				t.Fatalf("Send succeeded: %d", resp.StatusCode)
			}
			e := wantErr(t, err, errs.Network, tt.code)
			if e.Details["httpStatus"] != "407" || e.Details["via"] != "http://"+w.proxy.Addr() || e.Hint == "" {
				t.Errorf("details %v, hint %q", e.Details, e.Hint)
			}
			checkNoProxySecret(t, nil, err)
		})
	}
}

// newProxyWorldWith is newProxyWorld with other credentials on the
// client than on the proxy.
func newProxyWorldWith(t *testing.T, proxyUser, proxyPassword, user, password string) *proxyWorld {
	t.Helper()
	w := newProxyWorld(t, false, proxyUser, proxyPassword, false)
	u, _ := netx.ParseProxyURL("http://" + w.proxy.Addr())
	u.Username, u.Password = user, password
	router := netx.NewProxyRouter(nil, func(context.Context) (netx.ProxyURL, error) { return u, nil }, netx.ProxyOptions{})
	w.client = New(netx.NewForwardProxier(netx.Routed(router.Route), router.ForwardProxy), TransportOptions{InsecureSkipVerify: true})
	t.Cleanup(w.client.CloseIdleConnections)
	return w
}

// TestProxyHopTimeline (SOCKS-12): a request through a proxy tunnel shows
// the hop in its timeline: a proxy_hop event after dial_done, the
// connection's Proxy (version, authentication, DNS mode, reply, timings)
// and the proxy phase between connect and TLS. A reused connection has no
// proxy phase, an absolute-form request no CONNECT reply.
func TestProxyHopTimeline(t *testing.T) {
	t.Parallel()
	w := newProxyWorld(t, false, "alice", proxySecret, true)
	resp, err := w.client.Send(t.Context(), get(w.httpsURL+"/secure"))
	if err != nil {
		t.Fatal(err)
	}
	w.seen(t)
	hop := resp.Conn.Proxy
	if hop == nil {
		t.Fatalf("no proxy hop on the connection: %+v", resp.Conn)
	}
	if hop.Version != "HTTP/1.1" || hop.Proxy != w.proxy.Addr() || hop.Auth != netx.AuthBasic || !hop.RemoteDNS ||
		!hop.Replied || hop.Reply != 200 || hop.Forward || !testenv.Measured(hop.Handshake) {
		t.Errorf("hop = %+v", hop)
	}
	if !resp.Timing.Observed.Has(PhaseConnect|PhaseProxy|PhaseTLS) || !testenv.Measured(resp.Timing.Proxy) {
		t.Errorf("phases %v, proxy %v", resp.Timing.Observed, resp.Timing.Proxy)
	}
	if o := resp.Timing.Offsets; o.Proxy < o.Connect || o.TLS < o.Proxy {
		t.Errorf("offsets connect %v, proxy %v, tls %v: not in wire order", o.Connect, o.Proxy, o.TLS)
	}
	var names []string
	for _, e := range resp.Events {
		names = append(names, e.Name)
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, EventDialDone+" "+EventProxyHop) || !hasEvent(resp, EventProxyHop, "HTTP/1.1 "+w.proxy.Addr()+", auth basic, remote DNS, reply 200") {
		t.Errorf("events = %+v", resp.Events)
	}
	checkNoProxySecret(t, resp, nil)

	// The kept-alive connection: no new hop, no proxy phase.
	resp, err = w.client.Send(t.Context(), get(w.httpsURL+"/again"))
	if err != nil {
		t.Fatal(err)
	}
	w.seen(t)
	if !resp.Conn.Reused || resp.Conn.Proxy != nil || resp.Timing.Observed.Has(PhaseProxy) {
		t.Errorf("reused: conn %+v, phases %v", resp.Conn, resp.Timing.Observed)
	}
	// NET-06: the proxy is named on the reused (TLS) connection too.
	if resp.Conn.Via != "http://"+w.proxy.Addr() {
		t.Errorf("reused: Via = %q", resp.Conn.Via)
	}

	// Absolute form: the hop without a CONNECT reply or proxy phase.
	resp, err = w.client.Send(t.Context(), get(w.httpURL+"/plain"))
	if err != nil {
		t.Fatal(err)
	}
	w.seen(t)
	if resp.Conn.Via != "http://"+w.proxy.Addr() {
		t.Errorf("absolute form: Via = %q", resp.Conn.Via)
	}
	if hop := resp.Conn.Proxy; hop == nil || !hop.Forward || hop.Replied || resp.Timing.Observed.Has(PhaseProxy) {
		t.Errorf("absolute form: hop %+v, phases %v", hop, resp.Timing.Observed)
	}
}
