package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/har"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// proxyHost is a name only the test proxy knows.
const proxyHost = "api.example.test"

const proxySecret = "svc-pr0xy-s3cret"

// proxyDialer is a dialer built like the app's (internal/app newDialer):
// the guard outside, the proxy route inside, a netx.ForwardProxier.
func proxyDialer(p netx.ProxyURL, roots *x509.CertPool) netx.Dialer {
	router := netx.NewProxyRouter(nil, func(context.Context) (netx.ProxyURL, error) { return p, nil },
		netx.ProxyOptions{RootCAs: func() (*x509.CertPool, error) { return roots, nil }})
	chain := netx.Chain(netx.Routed(router.Route), netx.Guarded(netx.RequireUserOp(nil)))
	return netx.NewForwardProxier(chain, router.ForwardProxy)
}

// newProxyFixture is newFixture over dialer.
func newProxyFixture(t *testing.T, src collection.HTTPSource, dialer netx.Dialer, logger *slog.Logger, opts ...Option) *fixture {
	t.Helper()
	f := &fixture{rec: newRecorder(), src: src}
	f.ops = operation.NewRegistry(f.rec.emit, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.ops.Shutdown(ctx)
	})
	load := func(ctx context.Context, id string) (collection.HTTPSource, error) {
		if id != f.src.Request.ID {
			return collection.HTTPSource{}, errs.New(errs.NotFound, "no such request").WithCode("collection.not_found")
		}
		return f.src, nil
	}
	f.svc = New(load, func() time.Duration { return 0 }, f.ops, dialer, nil, logger, opts...)
	t.Cleanup(func() { _ = f.svc.ServiceShutdown() })
	return f
}

// TestSendThroughHTTPProxy (NET-02) through the HTTP service: http:// and
// https:// URLs through an http:// and an https:// proxy, with and
// without Basic credentials. http:// goes in absolute form, https:// through
// CONNECT with TLS to the origin; the timeline names the proxy; the
// proxy's credentials show nowhere: not in the response or its timeline,
// the history files, a HAR export of the history entry, the cURL command
// (with secrets) or the log.
func TestSendThroughHTTPProxy(t *testing.T) {
	t.Parallel()
	ca := tlstest.NewCA(t, "test CA")
	handler := nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_, _ = io.WriteString(w, "origin "+r.Method+" "+r.RequestURI+" proxyAuth="+r.Header.Get("Proxy-Authorization"))
	})
	plain := httptest.NewServer(handler)
	t.Cleanup(plain.Close)
	secure := httptest.NewUnstartedServer(handler)
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{DNSNames: []string{proxyHost}}).TLS}}
	secure.StartTLS()
	t.Cleanup(secure.Close)
	_, plainPort, _ := net.SplitHostPort(plain.Listener.Addr().String())
	_, securePort, _ := net.SplitHostPort(secure.Listener.Addr().String())
	proxyTLS := &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.CertOptions{CommonName: "proxy"}).TLS}}
	token := base64.StdEncoding.EncodeToString([]byte("alice:" + proxySecret))

	for _, tt := range []struct {
		name     string
		proxyTLS bool
		auth     bool
		https    bool
	}{
		{"http via http proxy", false, false, false},
		{"http via http proxy with auth", false, true, false},
		{"https via http proxy", false, false, true},
		{"https via http proxy with auth", false, true, true},
		{"http via https proxy with auth", true, true, false},
		{"https via https proxy with auth", true, true, true},
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
			u, err := netx.ParseProxyURL(scheme + addr)
			if err != nil {
				t.Fatal(err)
			}
			if tt.auth {
				u.Username, u.Password = "alice", proxySecret
			}
			url := "http://" + net.JoinHostPort(proxyHost, plainPort) + "/items?q=1"
			if tt.https {
				url = "https://" + net.JoinHostPort(proxyHost, securePort) + "/items?q=1"
			}
			var logs safeBuffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			st, historyOpt := historyStore(t)
			f := newProxyFixture(t, httpSource(url), proxyDialer(u, ca.Pool()), logger, historyOpt,
				WithTrust(func() (*x509.CertPool, error) { return ca.Pool(), nil }))

			id := f.send(t)
			if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
				t.Fatalf("done = %+v", done)
			}
			resp := f.rec.response(t, id)
			if resp.StatusCode != 200 || resp.Body != "origin GET /items?q=1 proxyAuth=" {
				t.Fatalf("response %d %q", resp.StatusCode, resp.Body)
			}
			reqs := proxy.Requests()
			if len(reqs) != 1 {
				t.Fatalf("proxy requests = %+v", reqs)
			}
			wantMethod, wantTarget := nethttp.MethodGet, url
			if tt.https {
				wantMethod, wantTarget = nethttp.MethodConnect, net.JoinHostPort(proxyHost, securePort)
			}
			if reqs[0].Method != wantMethod || reqs[0].Target != wantTarget {
				t.Errorf("proxy got %s %s, want %s %s", reqs[0].Method, reqs[0].Target, wantMethod, wantTarget)
			}
			if tt.auth != (reqs[0].Authorization == "Basic "+token) {
				t.Errorf("Proxy-Authorization = %q", reqs[0].Authorization)
			}

			// The timeline shows the proxy hop.
			dto, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(dto), "via "+scheme+addr) {
				t.Errorf("the timeline does not name the proxy %s%s: %s", scheme, addr, dto)
			}
			if !tt.https && !strings.Contains(string(dto), `"name":"`+httpclient.EventProxy+`"`) {
				t.Errorf("no proxy event in the timeline: %s", dto)
			}
			// SOCKS-12: the hop with its details and, for a tunnel, its
			// own phase between connect and TLS.
			final := resp.Timeline.Hops[len(resp.Timeline.Hops)-1]
			hop := final.Conn.Proxy
			wantAuth := netx.AuthNone
			if tt.auth {
				wantAuth = netx.AuthBasic
			}
			if hop == nil || hop.Proxy != addr || hop.Version != "HTTP/1.1" || hop.Auth != wantAuth || !hop.RemoteDNS ||
				hop.Forward == tt.https || (hop.Reply != nil) != tt.https || hop.ConnectMs <= 0 {
				t.Errorf("proxy hop = %+v", hop)
			}
			var phases []string
			for _, p := range final.Timing.Phases {
				phases = append(phases, p.Phase)
			}
			wantPhases := "dns connect tls request_write wait download"
			if tt.https || tt.proxyTLS {
				wantPhases = "dns connect proxy tls request_write wait download"
			}
			if strings.Join(phases, " ") != wantPhases {
				t.Errorf("phases = %v, want %s", phases, wantPhases)
			}
			if !strings.Contains(string(dto), `"name":"`+httpclient.EventProxyHop+`"`) {
				t.Errorf("no proxy_hop event in the timeline: %s", dto)
			}

			// The credentials show nowhere.
			entries := waitEntries(t, st, 1)
			entry, err := st.Get(entries[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			harEntry, err := json.Marshal(har.FromHistory(entry))
			if err != nil {
				t.Fatal(err)
			}
			curl, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1", IncludeSecrets: true})
			if err != nil {
				t.Fatal(err)
			}
			for what, text := range map[string]string{
				"response": string(dto), "history": historyFiles(t, st), "HAR": string(harEntry),
				"cURL": curl.Command, "log": logs.String(),
			} {
				if strings.Contains(text, proxySecret) || strings.Contains(text, token) || strings.Contains(strings.ToLower(text), "proxy-authorization: basic") {
					t.Errorf("the %s shows the proxy's credentials: %s", what, text)
				}
			}
		})
	}
}

// TestSendThroughHTTPProxyAuthErrors: a 407, to an absolute-form request
// or a CONNECT, fails the send with the proxy code and hint; the error
// and the history entry of the failure hold no credential.
func TestSendThroughHTTPProxyAuthErrors(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {}))
	t.Cleanup(origin.Close)
	_, port, _ := net.SplitHostPort(origin.Listener.Addr().String())
	for _, tt := range []struct {
		name     string
		scheme   string
		password string
		code     string
	}{
		{"absolute form, no credentials", "http", "", netx.CodeHTTPProxyAuthRequired},
		{"absolute form, wrong password", "http", proxySecret, netx.CodeHTTPProxyAuthFailed},
		{"CONNECT, wrong password", "https", proxySecret, netx.CodeHTTPProxyAuthFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proxy := &netxtest.HTTPProxyServer{Username: "alice", Password: "the-right-one", Hosts: map[string]string{proxyHost: "127.0.0.1"}}
			addr := proxy.Start(t)
			u, _ := netx.ParseProxyURL("http://" + addr)
			if tt.password != "" {
				u.Username, u.Password = "alice", tt.password
			}
			st, historyOpt := historyStore(t)
			f := newProxyFixture(t, httpSource(tt.scheme+"://"+net.JoinHostPort(proxyHost, port)+"/"), proxyDialer(u, nil), nil, historyOpt)
			done := f.rec.wait(t, f.send(t))
			if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != tt.code || done.Error.Hint == "" ||
				done.Error.Details["via"] != "http://"+addr {
				t.Fatalf("done = %+v, error %+v; want %s", done, done.Error, tt.code)
			}
			dto, _ := json.Marshal(done.Error)
			waitEntries(t, st, 1)
			for what, text := range map[string]string{"error": string(dto), "history": historyFiles(t, st)} {
				if tt.password != "" && strings.Contains(text, tt.password) {
					t.Errorf("the %s shows the password: %s", what, text)
				}
			}
		})
	}
}

// safeBuffer is a buffer the log handler may write to concurrently.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
