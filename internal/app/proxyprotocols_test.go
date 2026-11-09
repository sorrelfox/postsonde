package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/graphql/transportws"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport/grpctest"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient/siotest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
	"github.com/sorrelfox/postsonde/internal/core/realtime/tcpclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/udpclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf/tlstest"
)

// SOCKS-05: every protocol client connects through the proxy in
// effect, end to end, through the app's own dialer (the proxy from the
// settings, the connection log, the user-action guard, the route), and
// nothing is dialed directly while a proxy is set. Each protocol talks to
// a local server under a name only the proxy knows (e2eHost); the direct
// dialer records every address it connects to, which must be the
// proxy's only.

// e2eHost is the name of every test server; only the proxies know it (and
// the recording resolver, for the local-DNS modes).
const e2eHost = "api.e2e.test"

const e2eWait = 5 * time.Second

// e2eServers are the local servers of every protocol, on 127.0.0.1.
type e2eServers struct {
	ca                  *tlstest.CA
	httpPort, tlsPort   string // HTTP, GraphQL, SSE, WebSocket, GraphQL over WebSocket
	sio                 *siotest.Server
	sioPort             string
	grpcPort, grpcsPort string
	tcpPort, tcpsPort   string
	udpPort             string
}

func startE2EServers(t *testing.T) *e2eServers {
	t.Helper()
	s := &e2eServers{ca: tlstest.NewCA(t, "e2e CA")}
	cert := s.ca.Issue(t, tlstest.CertOptions{DNSNames: []string{e2eHost}})
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello over "+r.Proto)
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !strings.Contains(req.Query, "ping") {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"ping":"pong"}}`)
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 2 {
			_, _ = io.WriteString(w, "event: tick\ndata: "+strconv.Itoa(i)+"\n\n")
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		wsUpgrade(t, w, r, "", func(c *wsframe.Conn) {
			for {
				op, data, err := c.ReadMessage()
				if err != nil {
					return
				}
				if err := c.WriteMessage(op, data); err != nil {
					return
				}
			}
		})
	})
	mux.HandleFunc("/graphql-ws", func(w http.ResponseWriter, r *http.Request) {
		wsUpgrade(t, w, r, transportws.Subprotocol, graphQLWS)
	})
	plain := httptest.NewServer(mux)
	t.Cleanup(plain.Close)
	secure := httptest.NewUnstartedServer(mux)
	secure.EnableHTTP2 = true
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{cert.TLS}}
	secure.StartTLS()
	t.Cleanup(secure.Close)
	s.httpPort, s.tlsPort = port(plain.Listener.Addr()), port(secure.Listener.Addr())

	s.sio = siotest.Start(siotest.Config{}, t.Cleanup)
	_, s.sioPort, _ = net.SplitHostPort(strings.TrimPrefix(s.sio.URL, "http://"))

	h2c := grpctest.NewServer(t, false)
	grpcs := grpctest.NewServerWith(t, grpctest.ServerOptions{Security: grpctest.TLS, CA: s.ca, ServerCert: cert})
	_, s.grpcPort, _ = net.SplitHostPort(h2c.Addr)
	_, s.grpcsPort, _ = net.SplitHostPort(grpcs.Addr)

	s.tcpPort = echoOn(t, nil)
	s.tcpsPort = echoOn(t, &tls.Config{Certificates: []tls.Certificate{cert.TLS}})
	s.udpPort = udpEchoOn(t)
	return s
}

// udpEchoOn sends every datagram on loopback back; it returns the port.
func udpEchoOn(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 65536)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], from)
		}
	}()
	return port(pc.LocalAddr())
}

func port(a net.Addr) string {
	_, p, _ := net.SplitHostPort(a.String())
	return p
}

// echoOn accepts on loopback (with TLS when cfg is set) and echoes; it
// returns the port.
func echoOn(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		ln = tls.NewListener(ln, cfg)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return port(ln.Addr())
}

// wsUpgrade answers a WebSocket upgrade (selecting subprotocol when the
// client offers it) and runs serve on the connection.
func wsUpgrade(t *testing.T, w http.ResponseWriter, r *http.Request, subprotocol string, serve func(*wsframe.Conn)) {
	nc, brw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	defer nc.Close()
	extra := ""
	if subprotocol != "" && slices.Contains(strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ", "), subprotocol) {
		extra = "Sec-WebSocket-Protocol: " + subprotocol + "\r\n"
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n" + extra + "\r\n"
	if _, err := nc.Write([]byte(resp)); err != nil {
		return
	}
	_ = nc.SetDeadline(time.Now().Add(e2eWait))
	serve(wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleServer, Reader: brw.Reader}))
}

// graphQLWS is a graphql-transport-ws server: it acknowledges the init,
// answers a subscribe with one next and complete, then waits for the
// client to close.
func graphQLWS(c *wsframe.Conn) {
	var msg struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	read := func() bool {
		op, data, err := c.ReadMessage()
		return err == nil && op == wsframe.OpText && json.Unmarshal(data, &msg) == nil
	}
	if !read() || msg.Type != "connection_init" {
		return
	}
	_ = c.WriteMessage(wsframe.OpText, []byte(`{"type":"connection_ack"}`))
	for read() {
		if msg.Type == "subscribe" {
			_ = c.WriteMessage(wsframe.OpText, []byte(`{"type":"next","id":"`+msg.ID+`","payload":{"data":{"tick":1}}}`))
			_ = c.WriteMessage(wsframe.OpText, []byte(`{"type":"complete","id":"`+msg.ID+`"}`))
		}
	}
}

// directDials records the addresses the direct dialer connects to, the
// proxy's included (netx.Options.Guard sees each resolved address).
type directDials struct {
	mu    sync.Mutex
	addrs []string
}

func (d *directDials) guard(_ context.Context, c netx.DialCheck) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addrs = append(d.addrs, c.Resolved.String())
	return nil
}

func (d *directDials) list() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.addrs)
}

// e2eWorld is the app's dialer with a proxy from the settings.
type e2eWorld struct {
	dialer  netx.Dialer
	router  *netx.ProxyRouter
	connLog *netlog.Log
	direct  *directDials
	lookups *netxtest.Lookups
	ctx     context.Context
}

func newE2EWorld(t *testing.T, proxyURL string, roots *x509.CertPool) *e2eWorld {
	t.Helper()
	settings := coresettings.Open("", nil)
	secrets := appsecrets.New("")
	manual := "manual"
	if _, _, err := settings.Update(coresettings.Patch{Network: coresettings.NetworkPatch{ProxyMode: &manual, ProxyURL: &proxyURL}}); err != nil {
		t.Fatal(err)
	}
	resolver, lookups := netxtest.RecordingResolver(map[string]string{e2eHost: "127.0.0.1"})
	w := &e2eWorld{connLog: netlog.New(netlog.Options{}), direct: &directDials{}, lookups: lookups}
	w.dialer, w.router = newRoutedDialer(slog.New(slog.DiscardHandler), w.connLog, dialerConfig{
		Direct:  netx.Options{Resolver: resolver, Guard: w.direct.guard},
		Proxy:   proxySource(func() coresettings.Network { return settings.Settings().Network }, secrets, nil),
		RootCAs: func() (*x509.CertPool, error) { return roots, nil },
	})
	w.ctx = netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test.e2e", ID: "1"})
	return w
}

// e2eProxy is a test proxy of one kind.
type e2eProxy struct {
	name string
	// start starts it and returns its URL for the settings and its
	// host:port.
	start func(t *testing.T) (url, addr string)
	// remoteDNS is set when target names go to the proxy unresolved.
	remoteDNS bool
	// udp is the code a UDP dial through it fails with; "" when it
	// relays UDP (SOCKS5 UDP ASSOCIATE, SOCKS-07).
	udp string
}

var e2eProxies = []e2eProxy{
	{"socks5h", func(t *testing.T) (string, string) {
		p := &netxtest.SOCKS5Server{Hosts: map[string]string{e2eHost: "127.0.0.1"}}
		addr := p.Start(t)
		return "socks5h://" + addr, addr
	}, true, ""},
	{"socks5 with auth, local DNS", func(t *testing.T) (string, string) {
		p := &netxtest.SOCKS5Server{Username: "alice", Password: "s3cret"}
		addr := p.Start(t)
		return "socks5://alice:s3cret@" + addr, addr
	}, false, ""},
	{"socks4a", func(t *testing.T) (string, string) {
		p := &netxtest.SOCKS4Server{Hosts: map[string]string{e2eHost: "127.0.0.1"}}
		addr := p.Start(t)
		return "socks4a://bob@" + addr, addr
	}, true, netx.CodeSOCKSUnsupportedNetwork},
	{"http proxy", func(t *testing.T) (string, string) {
		p := &netxtest.HTTPProxyServer{Username: "alice", Password: "s3cret", Hosts: map[string]string{e2eHost: "127.0.0.1"}}
		addr := p.Start(t)
		return "http://alice:s3cret@" + addr, addr
	}, true, netx.CodeHTTPProxyUnsupportedNetwork},
}

// TestEveryProtocolThroughProxy (SOCKS-05): HTTP and HTTPS (HTTP/2 end to
// end through the tunnel), GraphQL over HTTP and over WebSocket, SSE,
// WebSocket (ws and wss), Socket.IO (long-polling and WebSocket), gRPC
// (h2c and TLS) and raw TCP (plain and TLS) work through SOCKS5 (remote
// and local DNS), SOCKS4a and an HTTP proxy; UDP works through SOCKS5
// (UDP ASSOCIATE, SOCKS-07) and is refused by the others, never sent
// direct. The direct dialer connects to the proxy only, and with remote
// DNS nothing is looked up here.
func TestEveryProtocolThroughProxy(t *testing.T) {
	t.Parallel()
	srv := startE2EServers(t)
	host := func(p string) string { return net.JoinHostPort(e2eHost, p) }
	protocols := []struct {
		name string
		run  func(t *testing.T, w *e2eWorld) error
	}{
		{"http", func(t *testing.T, w *e2eWorld) error {
			return httpGet(w, srv, "http://"+host(srv.httpPort)+"/hello", "hello over HTTP/1.1")
		}},
		{"https (h2)", func(t *testing.T, w *e2eWorld) error {
			return httpGet(w, srv, "https://"+host(srv.tlsPort)+"/hello", "hello over HTTP/2.0")
		}},
		{"graphql over http", func(t *testing.T, w *e2eWorld) error {
			c := httpclient.New(w.dialer, httpclient.TransportOptions{RootCAs: srv.ca.Pool()})
			defer c.CloseIdleConnections()
			resp, err := c.Send(w.ctx, httpclient.Request{Method: http.MethodPost, URL: "https://" + host(srv.tlsPort) + "/graphql",
				Body: httpclient.RawBody{Data: []byte(`{"query":"{ ping }"}`), Type: "application/json"}, Settings: httpclient.DefaultSettings()})
			if err != nil {
				return err
			}
			return expect(string(resp.Body), `{"data":{"ping":"pong"}}`)
		}},
		{"sse", func(t *testing.T, w *e2eWorld) error {
			c := httpclient.New(w.dialer, httpclient.TransportOptions{RootCAs: srv.ca.Pool()})
			defer c.CloseIdleConnections()
			var body []byte
			_, err := c.SendStream(w.ctx, httpclient.Request{Method: http.MethodGet, URL: "http://" + host(srv.httpPort) + "/sse", Settings: httpclient.DefaultSettings()},
				func(_ *httpclient.Response, r io.Reader) error {
					var err error
					body, err = io.ReadAll(r)
					return err
				})
			if err != nil {
				return err
			}
			return expect(string(body), "event: tick\ndata: 0\n\nevent: tick\ndata: 1\n\n")
		}},
		{"websocket", func(t *testing.T, w *e2eWorld) error {
			return wsEcho(w, "ws://"+host(srv.httpPort)+"/ws", nil)
		}},
		{"websocket over tls", func(t *testing.T, w *e2eWorld) error {
			return wsEcho(w, "wss://"+host(srv.tlsPort)+"/ws", srv.ca.Pool())
		}},
		{"graphql over websocket", func(t *testing.T, w *e2eWorld) error {
			ctx, cancel := context.WithTimeout(w.ctx, e2eWait)
			defer cancel()
			s, err := transportws.Dial(ctx, transportws.Options{WS: wsclient.Options{URL: "wss://" + host(srv.tlsPort) + "/graphql-ws", Dialer: w.dialer, RootCAs: srv.ca.Pool()}})
			if err != nil {
				return err
			}
			defer func() {
				s.Client().Abort()
				<-s.Done()
			}()
			sub, err := s.Subscribe(ctx, transportws.SubscribeRequest{Query: "subscription { tick }"})
			if err != nil {
				return err
			}
			select {
			case r := <-sub.C():
				if r.Kind != transportws.ResultNext {
					return errors.New("subscription result " + string(r.Kind))
				}
				return expect(string(r.Payload), `{"data":{"tick":1}}`)
			case <-ctx.Done():
				return ctx.Err()
			}
		}},
		{"socket.io over polling", func(t *testing.T, w *e2eWorld) error {
			return sioEmit(w, srv, sioclient.TransportPolling)
		}},
		{"socket.io over websocket", func(t *testing.T, w *e2eWorld) error {
			return sioEmit(w, srv, sioclient.TransportWebSocket)
		}},
		{"grpc h2c", func(t *testing.T, w *e2eWorld) error {
			return grpcUnary(w, srv, host(srv.grpcPort), false)
		}},
		{"grpc tls", func(t *testing.T, w *e2eWorld) error {
			return grpcUnary(w, srv, host(srv.grpcsPort), true)
		}},
		{"tcp", func(t *testing.T, w *e2eWorld) error {
			return tcpEcho(w, host(srv.tcpPort), nil)
		}},
		{"tcp over tls", func(t *testing.T, w *e2eWorld) error {
			return tcpEcho(w, host(srv.tcpsPort), &tcpclient.TLSOptions{RootCAs: srv.ca.Pool()})
		}},
	}
	for _, p := range e2eProxies {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			url, addr := p.start(t)
			w := newE2EWorld(t, url, srv.ca.Pool())
			for _, proto := range protocols {
				if err := proto.run(t, w); err != nil {
					t.Errorf("%s through %s: %v", proto.name, p.name, err)
				}
			}

			// UDP goes through SOCKS5's UDP ASSOCIATE relay (SOCKS-07);
			// the other proxies refuse it, and it is not sent directly.
			if p.udp == "" {
				if err := udpEcho(w, host(srv.udpPort)); err != nil {
					t.Errorf("udp through %s: %v", p.name, err)
				}
			} else if _, err := w.dialer.DialContext(w.ctx, "udp", host("5353")); !errorsIs(err, p.udp) {
				t.Errorf("udp through %s: %v, want %s", p.name, err, p.udp)
			}

			// Nothing but the proxy was dialed.
			proxyAP, err := netip.ParseAddrPort(addr)
			if err != nil {
				t.Fatal(err)
			}
			dials := w.direct.list()
			if len(dials) < len(protocols) {
				t.Errorf("the direct dialer made %d connections, want at least %d", len(dials), len(protocols))
			}
			// The proxy, and the UDP relay it named (the logged UDP
			// connection's remote address).
			allowed := map[string]bool{proxyAP.String(): true}
			for _, c := range w.connLog.Conns() {
				if c.Network == "udp" && c.Via != "" {
					allowed[c.RemoteAddr] = true
				}
			}
			if p.udp == "" && len(allowed) != 2 {
				t.Errorf("no UDP relay in the connection log: %v", allowed)
			}
			for _, d := range dials {
				if !allowed[d] {
					t.Errorf("a direct connection to %s while the proxy %s is set", d, addr)
				}
			}
			if names := w.lookups.Names(); p.remoteDNS && len(names) != 0 {
				t.Errorf("remote DNS, but %q was looked up here", names)
			}
			for _, c := range w.connLog.Conns() {
				if c.Via == "" && c.State != netlog.Refused && (c.Network != "udp" || p.udp == "") {
					t.Errorf("a logged connection without the proxy: %+v", c)
				}
			}
		})
	}
}

func expect(got, want string) error {
	if got != want {
		return errors.New("got " + strconv.Quote(got) + ", want " + strconv.Quote(want))
	}
	return nil
}

func httpGet(w *e2eWorld, srv *e2eServers, url, want string) error {
	c := httpclient.New(w.dialer, httpclient.TransportOptions{RootCAs: srv.ca.Pool()})
	defer c.CloseIdleConnections()
	resp, err := c.Send(w.ctx, httpclient.Request{Method: http.MethodGet, URL: url, Settings: httpclient.DefaultSettings()})
	if err != nil {
		return err
	}
	if resp.Conn.Via == "" {
		return errors.New("the response's connection names no proxy")
	}
	return expect(string(resp.Body), want)
}

func wsEcho(w *e2eWorld, url string, roots *x509.CertPool) error {
	got := make(chan string, 1)
	c, err := wsclient.New(wsclient.Options{URL: url, Dialer: w.dialer, RootCAs: roots, OnMessage: func(m wsclient.Message) {
		select {
		case got <- string(m.Data):
		default:
		}
	}})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(w.ctx, e2eWait)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		return err
	}
	defer func() {
		c.Abort()
		<-c.Done()
	}()
	if err := c.SendText("ping"); err != nil {
		return err
	}
	select {
	case m := <-got:
		return expect(m, "ping")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func sioEmit(w *e2eWorld, srv *e2eServers, tr sioclient.Transport) error {
	c, err := sioclient.New(sioclient.Options{URL: "http://" + net.JoinHostPort(e2eHost, srv.sioPort), Transport: tr, Dialer: w.dialer})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(w.ctx, e2eWait)
	defer cancel()
	if _, err := c.Connect(ctx); err != nil {
		return err
	}
	defer func() {
		c.Disconnect(ctx)
		<-c.Done()
	}()
	marker := "through-" + string(tr)
	if _, err := c.Emit("hello", []any{marker}, false); err != nil {
		return err
	}
	if !srv.sio.WaitReceived(func(ps []socketio.Packet) bool {
		return slices.ContainsFunc(ps, func(p socketio.Packet) bool {
			return p.Type == socketio.Event && strings.Contains(string(p.Data), marker)
		})
	}) {
		return errors.New("the server did not get the event")
	}
	return nil
}

func grpcUnary(w *e2eWorld, srv *e2eServers, addr string, withTLS bool) error {
	c := transport.New(w.dialer, transport.Options{Transport: httpclient.TransportOptions{RootCAs: srv.ca.Pool()}})
	defer c.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(w.ctx, e2eWait)
	defer cancel()
	res, err := c.Unary(ctx, transport.CallOptions{Address: addr, TLS: withTLS, Method: grpctest.MethodUnary}, []byte("ping"))
	if err != nil {
		return err
	}
	return expect(string(res.Message), "ping")
}

func udpEcho(w *e2eWorld, addr string) error {
	got := make(chan []byte, 8)
	ctx, cancel := context.WithTimeout(w.ctx, e2eWait)
	defer cancel()
	c, err := udpclient.Open(ctx, udpclient.Options{Address: addr, Dialer: w.dialer, Connected: true, OnDatagram: func(d udpclient.Datagram) {
		if d.Direction == udpclient.Received {
			got <- d.Data
		}
	}})
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Send([]byte("ping")); err != nil {
		return err
	}
	select {
	case b := <-got:
		return expect(string(b), "ping")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func tcpEcho(w *e2eWorld, addr string, tlsOpts *tcpclient.TLSOptions) error {
	got := make(chan []byte, 8)
	ctx, cancel := context.WithTimeout(w.ctx, e2eWait)
	defer cancel()
	c, err := tcpclient.Connect(ctx, tcpclient.Options{Address: addr, Dialer: w.dialer, TLS: tlsOpts, OnData: func(ch tcpclient.Chunk) {
		if ch.Direction == tcpclient.Received {
			got <- ch.Data
		}
	}})
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Send([]byte("ping")); err != nil {
		return err
	}
	var all []byte
	for len(all) < 4 {
		select {
		case b := <-got:
			all = append(all, b...)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return expect(string(all), "ping")
}
