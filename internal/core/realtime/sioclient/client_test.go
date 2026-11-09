package sioclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient/siotest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

// traces records a client's traces.
type traces struct {
	mu   sync.Mutex
	list []Trace
	wake chan struct{}
}

func newTraces() *traces { return &traces{wake: make(chan struct{}, 1)} }

func (tr *traces) add(t Trace) {
	tr.mu.Lock()
	tr.list = append(tr.list, t)
	tr.mu.Unlock()
	select {
	case tr.wake <- struct{}{}:
	default:
	}
}

func (tr *traces) all() []Trace {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]Trace(nil), tr.list...)
}

// wait waits for a trace that matches and returns it.
func (tr *traces) wait(t *testing.T, what string, match func(Trace) bool) Trace {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		for _, x := range tr.all() {
			if match(x) {
				return x
			}
		}
		select {
		case <-tr.wake:
		case <-deadline:
			t.Fatalf("no %s trace; got %v", what, kinds(tr.all()))
			return Trace{}
		}
	}
}

func kinds(list []Trace) []string {
	var out []string
	for _, t := range list {
		k := string(t.Kind)
		if t.Packet != nil {
			k += ":" + t.Packet.Type.String()
			if t.Sent {
				k += "(sent)"
			}
		}
		out = append(out, k)
	}
	return out
}

func options(srv *siotest.Server) (Options, *traces) {
	tr := newTraces()
	return Options{
		URL:     srv.URL,
		Dialer:  netx.NewDirect(netx.Options{}),
		OnTrace: tr.add,
	}, tr
}

func connect(t *testing.T, o Options) (*Client, *ConnectInfo) {
	t.Helper()
	c, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := c.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Disconnect(context.Background()) })
	return c, info
}

func waitDone(t *testing.T, c *Client) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the client did not close")
	}
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func isEvent(name string, sent bool) func(Trace) bool {
	return func(t Trace) bool {
		return t.Kind == TracePacket && t.Sent == sent && t.Packet != nil && t.Packet.EventName() == name
	}
}

// TestTransports connects over every transport, emits an event with JSON
// and binary arguments and an ack, receives the echo and the ack, and
// disconnects (SIO-01, SIO-03, SIO-05).
func TestTransports(t *testing.T) {
	t.Parallel()
	tests := []struct {
		transport Transport
		want      string
		fallback  bool
	}{
		{TransportWebSocket, "websocket", false},
		{TransportPolling, "polling", false},
		{TransportUpgrade, "websocket", false},
		{TransportAuto, "websocket", false},
		{"", "websocket", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.transport), func(t *testing.T) {
			t.Parallel()
			srv := siotest.Start(siotest.Config{}, t.Cleanup)
			o, tr := options(srv)
			o.Transport = tt.transport
			c, info := connect(t, o)
			if info.Transport != tt.want || c.Transport() != tt.want || info.Namespace != "/" || info.SocketID == "" {
				t.Errorf("info = %+v, transport %q", info, c.Transport())
			}
			if info.Handshake.SID == "" || info.Handshake.PingInterval != 25000 || info.Handshake.PingTimeout != 20000 {
				t.Errorf("handshake = %+v", info.Handshake)
			}
			res, err := c.Emit("echo", []any{"hi", map[string]any{"n": 1}, []byte{0, 1, 2, 0xff}}, true)
			if err != nil {
				t.Fatalf("Emit: %v", err)
			}
			if !res.HasAck || res.AckID != 0 || res.Attachments != 1 || res.Size == 0 {
				t.Errorf("Emit = %+v", res)
			}
			echo := tr.wait(t, "echo", isEvent("echo", false))
			args, err := echo.Packet.Args()
			if err != nil || len(args) != 4 || args[1] != "hi" || !bytes.Equal(args[3].([]byte), []byte{0, 1, 2, 0xff}) {
				t.Errorf("echo args = %v, %v", args, err)
			}
			if echo.Packet.Type != socketio.BinaryEvent {
				t.Errorf("echo type = %v", echo.Packet.Type)
			}
			ack := tr.wait(t, "ack", func(x Trace) bool { return x.Kind == TracePacket && x.Matched })
			if ack.AckID != 0 || ack.AckEvent != "echo" || ack.Packet.Type != socketio.BinaryAck || !testenv.Measured(ack.RTT) {
				t.Errorf("ack = %+v", ack)
			}
			// A second emit gets the next ack ID.
			if res, err := c.Emit("noack", nil, true); err != nil || res.AckID != 1 {
				t.Errorf("second Emit = %+v, %v", res, err)
			}
			c.Disconnect(context.Background())
			waitDone(t, c)
			if c.Reason() != ReasonClientDisconnect || c.Err() != nil {
				t.Errorf("reason %q, err %v", c.Reason(), c.Err())
			}
			if !srv.WaitReceived(func(ps []socketio.Packet) bool {
				return len(ps) > 0 && ps[len(ps)-1].Type == socketio.Disconnect
			}) {
				t.Errorf("server received %v", srv.Received())
			}
			got := kinds(tr.all())
			if got[0] != "open" || got[len(got)-1] != "closed" {
				t.Errorf("traces = %v", got)
			}
			if tt.transport == TransportUpgrade {
				tr.wait(t, "upgrade", func(x Trace) bool { return x.Kind == TraceUpgrade && x.Transport == "websocket" })
			}
		})
	}
}

// TestAutoFallsBack: a server without WebSocket is reached over
// long-polling (SIO-01).
func TestAutoFallsBack(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{NoWebSocket: true}, t.Cleanup)
	o, tr := options(srv)
	_, info := connect(t, o)
	if info.Transport != "polling" || len(info.Handshake.Upgrades) != 0 {
		t.Errorf("info = %+v", info)
	}
	fb := tr.wait(t, "fallback", func(x Trace) bool { return x.Kind == TraceFallback })
	if codeOf(fb.Err) != "wsclient.upgrade_rejected" {
		t.Errorf("fallback err = %v (%s)", fb.Err, codeOf(fb.Err))
	}
}

// TestUpgradeProbeUnanswered: a probe the server ignores keeps the
// connection on long-polling.
func TestUpgradeProbeUnanswered(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{IgnoreProbe: true, PingTimeout: 150 * time.Millisecond}, t.Cleanup)
	o, tr := options(srv)
	o.Transport = TransportUpgrade
	c, info := connect(t, o)
	if info.Transport != "polling" {
		t.Errorf("transport = %q", info.Transport)
	}
	f := tr.wait(t, "upgrade failed", func(x Trace) bool { return x.Kind == TraceUpgradeFailed })
	if codeOf(f.Err) != CodeUpgradeFailed {
		t.Errorf("err = %v", f.Err)
	}
	if _, err := c.Emit("echo", []any{1}, false); err != nil {
		t.Fatal(err)
	}
	tr.wait(t, "echo", isEvent("echo", false))
}

// TestNamespacesAndPath: the namespace from the option or the URL's
// path, a custom handshake path, and an unknown namespace (SIO-06).
func TestNamespacesAndPath(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{Path: "/rt/io/", Namespaces: []string{"/admin"}}, t.Cleanup)
	for _, transport := range []Transport{TransportWebSocket, TransportPolling} {
		o, tr := options(srv)
		o.Transport = transport
		o.Path = "rt/io"
		o.Namespace = "admin"
		c, info := connect(t, o)
		if info.Namespace != "/admin" || c.Namespace() != "/admin" {
			t.Errorf("%s: namespace = %q", transport, info.Namespace)
		}
		if _, err := c.Emit("echo", []any{"x"}, false); err != nil {
			t.Fatal(err)
		}
		echo := tr.wait(t, "echo", isEvent("echo", false))
		if echo.Packet.Namespace != "/admin" {
			t.Errorf("echo namespace = %q", echo.Packet.Namespace)
		}

		o, _ = options(srv)
		o.Transport = transport
		o.URL = srv.URL + "/admin?room=1"
		o.Path = "/rt/io/"
		_, info = connect(t, o)
		if info.Namespace != "/admin" || info.URL != srv.URL+"/rt/io/" {
			t.Errorf("%s: info = %+v", transport, info)
		}
	}
	for _, r := range srv.Requests() {
		if r.URL.Path != "/rt/io/" {
			t.Errorf("request path %q", r.URL.Path)
		}
	}

	o, tr := options(srv)
	o.Path = "/rt/io/"
	o.Namespace = "/nope"
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Connect(context.Background())
	e, ok := errors.AsType[*errs.Error](err)
	if !ok || e.Code != CodeConnectError || e.Kind != errs.PermissionDenied || e.Details["message"] != "Invalid namespace" || e.Details["namespace"] != "/nope" {
		t.Errorf("err = %#v", err)
	}
	waitDone(t, c)
	if c.Reason() != ReasonConnectFailed {
		t.Errorf("reason %q", c.Reason())
	}
	tr.wait(t, "connect error", func(x Trace) bool { return x.Packet != nil && x.Packet.Type == socketio.ConnectError })

	// The default path finds nothing on this server.
	o, _ = options(srv)
	o.Transport = TransportPolling
	c, _ = New(o)
	if _, err := c.Connect(context.Background()); codeOf(err) != CodeHandshakeRejected {
		t.Errorf("default path: err = %v", err)
	}
}

// TestAuth: the auth payload is sent in the CONNECT packet as typed; a
// refused one is a connect_error with the server's message and data
// (SIO-07).
func TestAuth(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{Authorize: func(nsp string, auth json.RawMessage) error {
		var a struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(auth, &a) != nil || a.Token != "s3cret" {
			return errors.New("not authorized")
		}
		return nil
	}}, t.Cleanup)
	for _, transport := range []Transport{TransportWebSocket, TransportPolling} {
		o, _ := options(srv)
		o.Transport = transport
		o.Auth = json.RawMessage("{\n  \"token\": \"s3cret\",\n  \"n\": 1.50\n}")
		connect(t, o)

		o.Auth = json.RawMessage(`{"token":"wrong"}`)
		c, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Connect(context.Background())
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != CodeConnectError || e.Details["message"] != "not authorized" || e.Details["data"] != `{"reason":"auth"}` ||
			!strings.Contains(e.Message, "not authorized") {
			t.Errorf("%s: err = %#v", transport, err)
		}
	}
	var auths []string
	for _, p := range srv.Received() {
		if p.Type == socketio.Connect {
			auths = append(auths, string(p.Data))
		}
	}
	want := []string{`{"token":"s3cret","n":1.50}`, `{"token":"wrong"}`}
	if len(auths) != 4 || auths[0] != want[0] || auths[1] != want[1] {
		t.Errorf("auth payloads = %q", auths)
	}
}

// TestAckTimeout: an emit whose ack never comes is reported after the ack
// timeout.
func TestAckTimeout(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{}, t.Cleanup)
	o, tr := options(srv)
	o.AckTimeout = 80 * time.Millisecond
	c, _ := connect(t, o)
	res, err := c.Emit("noack", []any{"x"}, true)
	if err != nil {
		t.Fatal(err)
	}
	x := tr.wait(t, "ack timeout", func(x Trace) bool { return x.Kind == TraceAckTimeout })
	if x.AckID != res.AckID || x.AckEvent != "noack" || x.RTT < 80*time.Millisecond {
		t.Errorf("timeout = %+v", x)
	}
}

// TestHeartbeat: pings are answered, so the server keeps the session.
func TestHeartbeat(t *testing.T) {
	t.Parallel()
	for _, transport := range []Transport{TransportWebSocket, TransportPolling} {
		t.Run(string(transport), func(t *testing.T) {
			t.Parallel()
			srv := siotest.Start(siotest.Config{PingInterval: 30 * time.Millisecond, PingTimeout: 300 * time.Millisecond}, t.Cleanup)
			o, tr := options(srv)
			o.Transport = transport
			c, _ := connect(t, o)
			time.Sleep(250 * time.Millisecond)
			if c.State() != StateOpen {
				t.Fatalf("state = %s (%s, %v)", c.State(), c.Reason(), c.Err())
			}
			pings, pongs := 0, 0
			for _, x := range tr.all() {
				switch x.Kind {
				case TracePing:
					pings++
				case TracePong:
					pongs++
				}
			}
			if pings < 3 || pongs < 3 {
				t.Errorf("pings %d, pongs %d", pings, pongs)
			}
			if len(srv.Sessions()) != 1 {
				t.Errorf("sessions = %d", len(srv.Sessions()))
			}
		})
	}
}

// TestPingTimeout: a server that stops pinging is given up after
// pingInterval + pingTimeout, and the client does not reconnect.
func TestPingTimeout(t *testing.T) {
	t.Parallel()
	for _, transport := range []Transport{TransportWebSocket, TransportPolling} {
		t.Run(string(transport), func(t *testing.T) {
			t.Parallel()
			srv := siotest.Start(siotest.Config{NoPings: true, PingInterval: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond}, t.Cleanup)
			o, tr := options(srv)
			o.Transport = transport
			c, _ := connect(t, o)
			waitDone(t, c)
			if c.Reason() != ReasonPingTimeout || codeOf(c.Err()) != CodePingTimeout || errs.KindOf(c.Err()) != errs.Timeout {
				t.Errorf("reason %q, err %v", c.Reason(), c.Err())
			}
			closed := tr.wait(t, "closed", func(x Trace) bool { return x.Kind == TraceClosed })
			if closed.Reason != ReasonPingTimeout {
				t.Errorf("closed = %+v", closed)
			}
			n := len(srv.Requests())
			time.Sleep(150 * time.Millisecond)
			if got := len(srv.Requests()); got != n {
				t.Errorf("requests after the close: %d, want %d (no reconnect)", got, n)
			}
			if _, err := c.Emit("x", nil, false); codeOf(err) != CodeNotOpen {
				t.Errorf("Emit after close = %v", err)
			}
		})
	}
}

// TestServerEnds: a DISCONNECT from the server, an Engine.IO close and a
// dropped transport end the connection with socket.io-client's reasons.
func TestServerEnds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, event, reason string
		failed              bool
	}{
		{"disconnect", "bye", ReasonServerDisconnect, false},
		{"close", "close", ReasonTransportClose, false},
	}
	for _, tt := range tests {
		for _, transport := range []Transport{TransportWebSocket, TransportPolling} {
			t.Run(tt.name+"/"+string(transport), func(t *testing.T) {
				t.Parallel()
				srv := siotest.Start(siotest.Config{}, t.Cleanup)
				o, _ := options(srv)
				o.Transport = transport
				c, _ := connect(t, o)
				// The connection may end before the POST of the emit is
				// answered.
				if _, err := c.Emit(tt.event, nil, false); err != nil && codeOf(err) != CodeNotOpen {
					t.Fatal(err)
				}
				waitDone(t, c)
				if c.Reason() != tt.reason || c.Err() != nil {
					t.Errorf("reason %q, err %v", c.Reason(), c.Err())
				}
			})
		}
	}
	t.Run("killed", func(t *testing.T) {
		t.Parallel()
		srv := siotest.Start(siotest.Config{}, t.Cleanup)
		o, _ := options(srv)
		o.Transport = TransportWebSocket
		c, _ := connect(t, o)
		srv.Sessions()[0].Kill()
		waitDone(t, c)
		if c.Reason() != ReasonTransportClose && c.Reason() != ReasonTransportError {
			t.Errorf("reason %q, err %v", c.Reason(), c.Err())
		}
	})
}

// TestServerEvents: events the server emits arrive, binary ones with their
// attachments in place.
func TestServerEvents(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{}, t.Cleanup)
	for _, transport := range []Transport{TransportWebSocket, TransportPolling} {
		o, tr := options(srv)
		o.Transport = transport
		connect(t, o)
		var ss *siotest.Session
		for _, s := range srv.Sessions() {
			if s.Transport() == string(transport) {
				ss = s
			}
		}
		ss.Emit("/", "news", map[string]any{"title": "ünïcode"}, []byte("raw"))
		x := tr.wait(t, "news", isEvent("news", false))
		args, _ := x.Packet.Args()
		if len(args) != 3 || args[1].(map[string]any)["title"] != "ünïcode" || string(args[2].([]byte)) != "raw" {
			t.Errorf("%s: args = %v", transport, args)
		}
	}
}

// TestHeadersQueryCookies: headers, the URL's query and the jar's cookies
// go with every request; cookies the server sets are stored; userinfo
// becomes Basic auth.
func TestHeadersQueryCookies(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{}, t.Cleanup)
	for _, transport := range []Transport{TransportWebSocket, TransportPolling, TransportUpgrade} {
		jar := &memJar{}
		o, _ := options(srv)
		o.URL = strings.Replace(srv.URL, "://", "://u:p%40ss@", 1) + "?token=a b&x=1"
		o.Transport = transport
		o.Headers = []httpclient.Header{{Name: "X-Trace", Value: " 42 ", Enabled: true}, {Name: "X-Off", Value: "no"}}
		o.Jar = jar
		jar.set("sticky", "lb1")
		connect(t, o)
		for _, r := range srv.Requests() {
			q := r.URL.Query()
			if q.Get("token") != "a b" || q.Get("x") != "1" || q.Get("EIO") != "4" {
				continue
			}
			if r.Header.Get("X-Trace") != "42" || r.Header.Get("X-Off") != "" || r.Header.Get("Authorization") != "Basic dTpwQHNz" {
				t.Errorf("%s: headers = %v", transport, r.Header)
			}
			if ck, err := r.Cookie("sticky"); err != nil || ck.Value != "lb1" {
				t.Errorf("%s: cookie = %v, %v", transport, ck, err)
			}
		}
	}
}

// TestUserOpOnEveryDial: every dial, long-polling requests after Connect
// returned included, carries the context values of Connect.
func TestUserOpOnEveryDial(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{PingInterval: 20 * time.Millisecond}, t.Cleanup)
	var mu sync.Mutex
	dials, marked := 0, 0
	direct := netx.NewDirect(netx.Options{})
	o, tr := options(srv)
	o.Transport = TransportPolling
	o.Dialer = netx.DialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dials++
		if op, ok := netx.UserOpFrom(ctx); ok && op.ID == "op1" {
			marked++
		}
		mu.Unlock()
		return direct.DialContext(ctx, network, addr)
	})
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(netx.WithUserOp(context.Background(), netx.UserOp{Kind: "sio.connect", ID: "op1"}))
	if _, err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	cancel() // the connect context ends; the connection does not
	t.Cleanup(func() { c.Disconnect(context.Background()) })
	if _, err := c.Emit("echo", []any{"after"}, true); err != nil {
		t.Fatal(err)
	}
	tr.wait(t, "ack", func(x Trace) bool { return x.Matched })
	mu.Lock()
	defer mu.Unlock()
	if dials == 0 || marked != dials {
		t.Errorf("dials %d, marked %d", dials, marked)
	}
}

// TestConnectFailures: refused handshakes, a closed port, a timeout and a
// cancel.
func TestConnectFailures(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{NoPolling: true}, t.Cleanup)
	o, _ := options(srv)
	o.Transport = TransportPolling
	c, _ := New(o)
	_, err := c.Connect(context.Background())
	e, ok := errors.AsType[*errs.Error](err)
	if !ok || e.Code != CodeHandshakeRejected || e.Details["status"] != "400" || !strings.Contains(e.Message, "Transport unknown") {
		t.Errorf("polling refused: %#v", err)
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	o, _ = options(srv)
	o.URL = "http://" + addr
	c, _ = New(o)
	if _, err := c.Connect(context.Background()); codeOf(err) != netx.CodeConnectionRefused {
		t.Errorf("closed port: %v (%s)", err, codeOf(err))
	}

	// A server that accepts and never answers.
	hang := make(chan struct{})
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-hang })}
	ln, _ = net.Listen("tcp", "127.0.0.1:0")
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { close(hang); _ = hs.Close() })
	o, _ = options(srv)
	o.URL = "http://" + ln.Addr().String()
	o.Transport = TransportPolling
	o.HandshakeTimeout = 100 * time.Millisecond
	c, _ = New(o)
	if _, err := c.Connect(context.Background()); codeOf(err) != CodeTimeout || errs.KindOf(err) != errs.Timeout {
		t.Errorf("timeout: %v", err)
	}
	o.HandshakeTimeout = -1
	c, _ = New(o)
	go func() {
		time.Sleep(50 * time.Millisecond)
		c.Disconnect(context.Background())
	}()
	if _, err := c.Connect(context.Background()); codeOf(err) != CodeCancelled {
		t.Errorf("disconnect while connecting: %v", err)
	}
	if c.Reason() != ReasonClientDisconnect {
		t.Errorf("reason = %q", c.Reason())
	}
	if _, err := c.Connect(context.Background()); codeOf(err) != CodeAlreadyConnected {
		t.Errorf("second Connect: %v", err)
	}
}

// TestInvalidOptions: options that cannot be used fail in New.
func TestInvalidOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		o    Options
		code string
	}{
		{"no scheme", Options{URL: "localhost:3000"}, CodeInvalidURL},
		{"ftp", Options{URL: "ftp://h"}, CodeSchemeUnsupported},
		{"fragment", Options{URL: "http://h/#x"}, CodeInvalidURL},
		{"no host", Options{URL: "http:///x"}, CodeInvalidURL},
		{"bad port", Options{URL: "http://h:99999"}, CodeInvalidURL},
		{"path query", Options{URL: "http://h", Path: "/socket.io/?x"}, CodeInvalidPath},
		{"namespace comma", Options{URL: "http://h", Namespace: "/a,b"}, CodeInvalidNamespace},
		{"auth array", Options{URL: "http://h", Auth: json.RawMessage(`[1]`)}, CodeInvalidAuth},
		{"auth broken", Options{URL: "http://h", Auth: json.RawMessage(`{"a":`)}, CodeInvalidAuth},
		{"header", Options{URL: "http://h", Headers: []httpclient.Header{{Name: "a b", Enabled: true}}}, CodeInvalidHeader},
		{"transport", Options{URL: "http://h", Transport: "carrier-pigeon"}, CodeInvalidTransport},
	}
	for _, tt := range tests {
		tt.o.Dialer = netx.NewDirect(netx.Options{})
		if _, err := New(tt.o); codeOf(err) != tt.code || errs.KindOf(err) != errs.InvalidArgument {
			t.Errorf("%s: err = %v, want %s", tt.name, err, tt.code)
		}
	}
	c, err := New(Options{URL: "wss://example.com:8443/chat?x=1", Path: "io", Auth: json.RawMessage("  "), Dialer: netx.NewDirect(netx.Options{})})
	if err != nil {
		t.Fatal(err)
	}
	if c.URL() != "https://example.com:8443/io/" || c.Namespace() != "/chat" {
		t.Errorf("URL %q, namespace %q", c.URL(), c.Namespace())
	}
	if got := c.t.url(true, "a b", 4); got != "wss://example.com:8443/io/?x=1&EIO=4&transport=websocket&sid=a+b" {
		t.Errorf("url = %q", got)
	}
	if got := c.t.url(false, "", 3); got != "https://example.com:8443/io/?x=1&EIO=3&transport=polling&b64=1" {
		t.Errorf("v2 url = %q", got)
	}
	if _, err := c.Emit("x", nil, false); codeOf(err) != CodeNotOpen {
		t.Errorf("Emit before Connect: %v", err)
	}
	c.Disconnect(context.Background()) // idle: nothing to do
}

// TestEmitChecks: reserved event names are refused.
func TestEmitChecks(t *testing.T) {
	t.Parallel()
	srv := siotest.Start(siotest.Config{}, t.Cleanup)
	o, _ := options(srv)
	c, _ := connect(t, o)
	if _, err := c.Emit("connect", nil, false); codeOf(err) != socketio.CodeReservedEvent {
		t.Errorf("reserved: %v", err)
	}
}

// memJar is a cookie jar for tests.
type memJar struct {
	mu      sync.Mutex
	cookies []*http.Cookie
}

func (j *memJar) set(name, value string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cookies = append(j.cookies, &http.Cookie{Name: name, Value: value})
}

func (j *memJar) Cookies(*url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]*http.Cookie(nil), j.cookies...)
}

func (j *memJar) SetCookieLines(_ *url.URL, lines []string) ([]string, []string) {
	return nil, nil
}
