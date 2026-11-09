package wsserver_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
)

type events struct {
	mu   sync.Mutex
	list []servers.Event
}

func (e *events) add(ev servers.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

func (e *events) all() []servers.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

func (e *events) ofKind(kind servers.EventKind) []servers.Event {
	var out []servers.Event
	for _, ev := range e.all() {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// waitFor polls until n events of kind have been recorded and returns them.
func (e *events) waitFor(t *testing.T, kind servers.EventKind, n int) []servers.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := e.ofKind(kind)
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("saw %d %s events, want %d", len(got), kind, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type fixture struct {
	m    *servers.Manager
	id   string
	port int
	rec  *events
}

func start(t *testing.T, cfg wsserver.Config) *fixture {
	t.Helper()
	rec := &events{}
	m := servers.NewManager(servers.Options{
		Types:       []servers.Type{wsserver.New(wsserver.Options{CloseTimeout: 500 * time.Millisecond})},
		OnTraffic:   rec.add,
		StopTimeout: 2 * time.Second,
		Logger:      slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: wsserver.TypeName, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if s.Spec.Name != "WebSocket" {
		t.Errorf("default name = %q", s.Spec.Name)
	}
	s, err = m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{m: m, id: s.ID, port: s.Port(), rec: rec}
}

func (f *fixture) hub(t *testing.T) wsserver.Hub {
	t.Helper()
	inst, err := f.m.Running(f.id)
	if err != nil {
		t.Fatal(err)
	}
	return inst.(wsserver.Hub)
}

func (f *fixture) addr() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(f.port)) }

// waitClients polls until the server lists n clients.
func (f *fixture) waitClients(t *testing.T, n int) []wsserver.Client {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := f.hub(t).Clients()
		if len(got) == n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("server lists %d clients, want %d", len(got), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type conn struct {
	c        *wsclient.Client
	messages chan wsclient.Message
}

func dial(t *testing.T, f *fixture, path string, subprotocols ...string) *conn {
	t.Helper()
	msgs := make(chan wsclient.Message, 100)
	c, err := wsclient.New(wsclient.Options{
		URL:          "ws://" + f.addr() + path,
		Dialer:       netx.NewDirect(netx.Options{}),
		Subprotocols: subprotocols,
		CloseLinger:  time.Second,
		OnMessage:    func(m wsclient.Message) { msgs <- m },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.Abort)
	return &conn{c: c, messages: msgs}
}

func (c *conn) next(t *testing.T) wsclient.Message {
	t.Helper()
	select {
	case m := <-c.messages:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
	return wsclient.Message{}
}

func (c *conn) waitClosed(t *testing.T) *wsclient.CloseInfo {
	t.Helper()
	select {
	case <-c.c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client not closed")
	}
	return c.c.CloseInfo()
}

func TestExchangeTextAndBinary(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{Path: "/ws"})
	c := dial(t, f, "/ws?token=1")

	clients := f.waitClients(t, 1)
	cl := clients[0]
	if cl.ID == "" || cl.Target != "/ws?token=1" || cl.Subprotocol != "" || cl.ConnectedAt.IsZero() || !strings.HasPrefix(cl.Peer, "127.0.0.1:") {
		t.Errorf("client = %+v", cl)
	}

	if err := c.c.SendText("hello"); err != nil {
		t.Fatal(err)
	}
	if err := c.c.SendBinary([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	in := f.rec.waitFor(t, servers.KindMessage, 2)
	if in[0].Direction != servers.In || string(in[0].Content) != "hello" || in[0].Binary || in[0].WS.Opcode != "text" || in[0].WS.ClientID != cl.ID {
		t.Errorf("text in = %+v %+v", in[0], in[0].WS)
	}
	// A binary message stays binary even when its bytes are UTF-8.
	if string(in[1].Content) != "abc" || !in[1].Binary || in[1].WS.Opcode != "binary" {
		t.Errorf("binary in = %+v", in[1])
	}

	hub := f.hub(t)
	if err := hub.Send(cl.ID, false, []byte("hi there")); err != nil {
		t.Fatal(err)
	}
	if err := hub.Send(cl.ID, true, []byte{0, 1, 0xff}); err != nil {
		t.Fatal(err)
	}
	if m := c.next(t); m.Opcode != wsframe.OpText || string(m.Data) != "hi there" {
		t.Errorf("client got %v %q", m.Opcode, m.Data)
	}
	if m := c.next(t); m.Opcode != wsframe.OpBinary || string(m.Data) != "\x00\x01\xff" {
		t.Errorf("client got %v %q", m.Opcode, m.Data)
	}
	out := f.rec.waitFor(t, servers.KindMessage, 4)[2:]
	if out[0].Direction != servers.Out || string(out[0].Content) != "hi there" || out[1].WS.Opcode != "binary" || !out[1].Binary {
		t.Errorf("out = %+v", out)
	}
	cl = f.hub(t).Clients()[0]
	if cl.MessagesIn != 2 || cl.BytesIn != 8 || cl.MessagesOut != 2 || cl.BytesOut != 11 {
		t.Errorf("counts = %+v", cl)
	}

	// Invalid UTF-8 as text and unknown clients are refused.
	if err := hub.Send(cl.ID, false, []byte{0xff}); !hasCode(err, wsserver.CodeInvalidMessage) {
		t.Errorf("invalid text: %v", err)
	}
	if err := hub.Send("999", false, []byte("x")); !hasCode(err, wsserver.CodeClientNotFound) {
		t.Errorf("unknown client: %v", err)
	}

	// The upgrade is logged as a request and a 101 response for the client.
	reqs := f.rec.ofKind(servers.KindRequest)
	resps := f.rec.ofKind(servers.KindResponse)
	if len(reqs) != 1 || reqs[0].HTTP.Target != "/ws?token=1" || reqs[0].WS.ClientID != cl.ID || !hasHeader(reqs[0].HTTP.Headers, "Sec-WebSocket-Key") {
		t.Errorf("request = %+v", reqs)
	}
	if len(resps) != 1 || resps[0].HTTP.Status != 101 || resps[0].Summary != "101 Switching Protocols" || !hasHeader(resps[0].HTTP.Headers, "Sec-WebSocket-Accept") {
		t.Errorf("response = %+v", resps)
	}
	if open := f.rec.ofKind(servers.KindConnectionOpen); len(open) != 1 || open[0].ConnID != cl.ConnID {
		t.Errorf("connection_open = %+v", open)
	}
}

func hasCode(err error, code string) bool {
	e, ok := errors.AsType[*errs.Error](err)
	return ok && e.Code == code
}

func hasHeader(h []servers.Header, name string) bool {
	for _, x := range h {
		if strings.EqualFold(x.Name, name) {
			return true
		}
	}
	return false
}

func TestSubprotocolNegotiation(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{Path: wsserver.AnyPath, Subprotocols: []string{"b", "a"}})

	// The first subprotocol the client offers that the server accepts.
	c := dial(t, f, "/any/path", "x", "a", "b")
	if got := c.c.Subprotocol(); got != "a" {
		t.Errorf("subprotocol = %q, want a", got)
	}
	none := dial(t, f, "/", "x")
	if got := none.c.Subprotocol(); got != "" {
		t.Errorf("subprotocol = %q, want none", got)
	}
	clients := f.waitClients(t, 2)
	if clients[0].Subprotocol != "a" || clients[1].Subprotocol != "" {
		t.Errorf("clients = %+v", clients)
	}
	resp := f.rec.waitFor(t, servers.KindResponse, 2)[0]
	if resp.Summary != "101 Switching Protocols (subprotocol a)" || resp.WS.Subprotocol != "a" {
		t.Errorf("response = %+v", resp)
	}
}

func TestBroadcast(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{})
	a := dial(t, f, "/")
	b := dial(t, f, "/")
	f.waitClients(t, 2)
	res := f.hub(t).Broadcast(false, []byte("to all"))
	if res.Sent != 2 || res.Failed != 0 {
		t.Errorf("broadcast = %+v", res)
	}
	for _, c := range []*conn{a, b} {
		if m := c.next(t); string(m.Data) != "to all" {
			t.Errorf("got %q", m.Data)
		}
	}
	out := f.rec.waitFor(t, servers.KindMessage, 2)
	if out[0].WS.ClientID == out[1].WS.ClientID {
		t.Errorf("broadcast events = %+v", out)
	}
}

func TestServerDisconnect(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{})
	c := dial(t, f, "/")
	cl := f.waitClients(t, 1)[0]
	hub := f.hub(t)

	if err := hub.Disconnect(t.Context(), cl.ID, 1006, ""); !hasCode(err, wsframe.CodeInvalidArgument) {
		t.Errorf("1006: %v", err)
	}
	if err := hub.Disconnect(t.Context(), "nope", 1000, ""); !hasCode(err, wsserver.CodeClientNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if err := hub.Disconnect(t.Context(), cl.ID, 4000, "bye"); err != nil {
		t.Fatal(err)
	}
	info := c.waitClosed(t)
	if info.Code != 4000 || info.Reason != "bye" || info.InitiatedBy != wsclient.InitiatorRemote || !info.WasClean {
		t.Errorf("client close = %+v", info)
	}
	if got := hub.Clients(); len(got) != 0 {
		t.Errorf("clients after disconnect = %+v", got)
	}
	closed := f.rec.waitFor(t, servers.KindConnectionClose, 1)[0]
	if closed.WS.InitiatedBy != "server" || closed.WS.CloseCode != 4000 || closed.WS.CloseReason != "bye" ||
		closed.Summary != "Disconnected by the server: 4000 “bye”" {
		t.Errorf("close event = %+v %+v", closed, closed.WS)
	}
	if err := hub.Send(cl.ID, false, []byte("late")); !hasCode(err, wsserver.CodeClientNotFound) {
		t.Errorf("send after disconnect: %v", err)
	}
}

func TestClientClose(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{})
	c := dial(t, f, "/")
	f.waitClients(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.c.Close(ctx, 1000, "done"); err != nil {
		t.Fatal(err)
	}
	info := c.waitClosed(t)
	if info.Code != 1000 || !info.WasClean || info.InitiatedBy != wsclient.InitiatorLocal {
		t.Errorf("client close = %+v", info)
	}
	closed := f.rec.waitFor(t, servers.KindConnectionClose, 1)[0]
	if closed.WS.InitiatedBy != "client" || closed.WS.CloseCode != 1000 || closed.WS.CloseReason != "done" ||
		closed.Summary != "Closed by the client: 1000 (normal closure) “done”" {
		t.Errorf("close event = %+v %+v", closed, closed.WS)
	}
	f.waitClients(t, 0)
}

func TestStopClosesClientsWithGoingAway(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{})
	a := dial(t, f, "/")
	b := dial(t, f, "/")
	f.waitClients(t, 2)
	if _, err := f.m.Stop(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*conn{a, b} {
		info := c.waitClosed(t)
		if info.Code != wsframe.CloseGoingAway || info.Reason != wsserver.StopReason || info.InitiatedBy != wsclient.InitiatorRemote {
			t.Errorf("client close = %+v", info)
		}
	}
	closes := f.rec.ofKind(servers.KindConnectionClose)
	if len(closes) != 2 {
		t.Fatalf("close events = %+v", closes)
	}
	for _, ev := range closes {
		if ev.WS.CloseCode != 1001 || !strings.HasPrefix(ev.Summary, "Closed: the server is stopping") {
			t.Errorf("close event = %+v", ev)
		}
	}
	all := f.rec.all()
	if last := all[len(all)-1]; last.Kind != servers.KindStopped {
		t.Errorf("last event = %+v", last)
	}
	if _, err := f.m.Running(f.id); !hasCode(err, servers.CodeNotRunning) {
		t.Errorf("Running after stop: %v", err)
	}
}

func TestEchoAndReconfigure(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{})
	c := dial(t, f, "/")
	f.waitClients(t, 1)
	if _, err := f.m.UpdateConfig(f.id, wsserver.Config{Echo: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.c.SendText("ping me"); err != nil {
		t.Fatal(err)
	}
	if m := c.next(t); string(m.Data) != "ping me" || m.Opcode != wsframe.OpText {
		t.Errorf("echo = %v %q", m.Opcode, m.Data)
	}
	msgs := f.rec.waitFor(t, servers.KindMessage, 2)
	if msgs[1].Direction != servers.Out || !strings.Contains(msgs[1].Summary, "echoed") {
		t.Errorf("echo event = %+v", msgs[1])
	}

	// A new path applies to new connections.
	if _, err := f.m.UpdateConfig(f.id, wsserver.Config{Path: "/v2"}); err != nil {
		t.Fatal(err)
	}
	status, _, _ := rawRequest(t, f, "GET /v1 HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	if status != 404 {
		t.Errorf("old path = %d", status)
	}
	dial(t, f, "/v2")
}

func TestMaxMessageSize(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{MaxMessageSize: 8})
	c := dial(t, f, "/")
	f.waitClients(t, 1)
	if err := c.c.SendText("this is too long"); err != nil {
		t.Fatal(err)
	}
	info := c.waitClosed(t)
	if info.Code != wsframe.CloseMessageTooBig {
		t.Errorf("close = %+v", info)
	}
	closed := f.rec.waitFor(t, servers.KindConnectionClose, 1)[0]
	if closed.WS.CloseCode != 1009 || closed.WS.InitiatedBy != "server" {
		t.Errorf("close event = %+v %+v", closed, closed.WS)
	}
	if errsEv := f.rec.ofKind(servers.KindError); len(errsEv) != 1 {
		t.Errorf("error events = %+v", errsEv)
	}
}

// rawRequest sends req on a new connection and returns the status, the
// headers and a reader positioned after them.
func rawRequest(t *testing.T, f *fixture, req string) (int, http.Header, *bufio.Reader) {
	t.Helper()
	status, h, br, _ := rawConn(t, f, req)
	return status, h, br
}

// rawConn is rawRequest that also returns the connection.
func rawConn(t *testing.T, f *fixture, req string) (int, http.Header, *bufio.Reader, net.Conn) {
	t.Helper()
	nc, err := net.DialTimeout("tcp", f.addr(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	_ = nc.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(nc, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return resp.StatusCode, resp.Header, br, nc
}

func TestBadUpgradeRequests(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{Path: "/ws"})
	const key = "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"
	const up = "Upgrade: websocket\r\nConnection: Upgrade\r\n"
	tests := []struct {
		name, req string
		status    int
		header    string // a response header that must be set
		value     string
	}{
		{"plain GET", "GET /ws HTTP/1.1\r\nHost: x\r\n\r\n", 426, "Sec-WebSocket-Version", "13"},
		{"other path", "GET /nope HTTP/1.1\r\nHost: x\r\n" + up + key + "Sec-WebSocket-Version: 13\r\n\r\n", 404, "", ""},
		{"POST", "POST /ws HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n" + up + key + "Sec-WebSocket-Version: 13\r\n\r\n", 405, "Allow", "GET"},
		{"HTTP/1.0", "GET /ws HTTP/1.0\r\nHost: x\r\n" + up + key + "Sec-WebSocket-Version: 13\r\n\r\n", 400, "", ""},
		{"no Connection upgrade", "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: keep-alive\r\n" + key + "Sec-WebSocket-Version: 13\r\n\r\n", 400, "", ""},
		{"old version", "GET /ws HTTP/1.1\r\nHost: x\r\n" + up + key + "Sec-WebSocket-Version: 8\r\n\r\n", 426, "Sec-WebSocket-Version", "13"},
		{"no version", "GET /ws HTTP/1.1\r\nHost: x\r\n" + up + key + "\r\n", 426, "Sec-WebSocket-Version", "13"},
		{"short key", "GET /ws HTTP/1.1\r\nHost: x\r\n" + up + "Sec-WebSocket-Key: c2hvcnQ=\r\nSec-WebSocket-Version: 13\r\n\r\n", 400, "", ""},
		{"no key", "GET /ws HTTP/1.1\r\nHost: x\r\n" + up + "Sec-WebSocket-Version: 13\r\n\r\n", 400, "", ""},
		{"no Host", "GET /ws HTTP/1.1\r\n" + up + key + "Sec-WebSocket-Version: 13\r\n\r\n", 400, "", ""},
	}
	for _, tt := range tests {
		status, h, _ := rawRequest(t, f, tt.req)
		if status != tt.status {
			t.Errorf("%s: status %d, want %d", tt.name, status, tt.status)
		}
		if tt.header != "" && h.Get(tt.header) != tt.value {
			t.Errorf("%s: %s = %q, want %q", tt.name, tt.header, h.Get(tt.header), tt.value)
		}
	}
	// Every rejection the handler saw is logged with why ("no Host" is
	// refused by net/http before it).
	resps := f.rec.waitFor(t, servers.KindResponse, len(tests)-1)
	if !strings.HasPrefix(resps[0].Summary, "426 Upgrade Required (not a WebSocket upgrade)") {
		t.Errorf("first response = %q", resps[0].Summary)
	}
	if len(f.hub(t).Clients()) != 0 {
		t.Error("a bad upgrade became a client")
	}
}

// TestBrowserHandshake upgrades with the request a browser sends, which
// offers permessage-deflate: the server must not accept the extension, and
// frames from the client are masked.
func TestBrowserHandshake(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{Echo: true, Subprotocols: []string{"chat"}})
	req := "GET / HTTP/1.1\r\n" +
		"Host: localhost:" + strconv.Itoa(f.port) + "\r\n" +
		"Connection: Upgrade\r\n" +
		"Pragma: no-cache\r\n" +
		"Cache-Control: no-cache\r\n" +
		"User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36\r\n" +
		"Upgrade: websocket\r\n" +
		"Origin: null\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Accept-Encoding: gzip, deflate, br, zstd\r\n" +
		"Accept-Language: en-US,en;q=0.9\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Extensions: permessage-deflate; client_max_window_bits\r\n" +
		"Sec-WebSocket-Protocol: chat, superchat\r\n" +
		"\r\n"
	status, h, br, nc := rawConn(t, f, req)
	if status != 101 {
		t.Fatalf("status = %d", status)
	}
	if got := h.Get("Sec-WebSocket-Accept"); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Errorf("accept = %q", got)
	}
	if h.Get("Upgrade") != "websocket" || h.Get("Connection") != "Upgrade" || h.Get("Sec-WebSocket-Protocol") != "chat" {
		t.Errorf("headers = %v", h)
	}
	if _, ok := h["Sec-Websocket-Extensions"]; ok {
		t.Errorf("extension accepted: %v", h)
	}
	cl := f.waitClients(t, 1)[0]
	if cl.Origin != "null" || !strings.HasPrefix(cl.UserAgent, "Mozilla/5.0") {
		t.Errorf("client = %+v", cl)
	}

	// A masked text frame is echoed back unmasked.
	var out []byte
	out, err := wsframe.AppendFrame(out, wsframe.Frame{
		Header:  wsframe.Header{Fin: true, Opcode: wsframe.OpText, Masked: true, MaskKey: [4]byte{1, 2, 3, 4}},
		Payload: []byte("Hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nc.Write(out); err != nil {
		t.Fatal(err)
	}
	fr, err := wsframe.ReadFrame(br, wsframe.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if fr.Masked || fr.Opcode != wsframe.OpText || string(fr.Payload) != "Hello" {
		t.Errorf("echo frame = %+v %q", fr.Header, fr.Payload)
	}
}

// TestUnmaskedFrameIsAProtocolError: a client must mask its frames; the
// server closes with 1002 and logs why.
func TestUnmaskedFrameIsAProtocolError(t *testing.T) {
	t.Parallel()
	f := start(t, wsserver.Config{})
	status, _, br, nc := rawConn(t, f, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	if status != 101 {
		t.Fatalf("status = %d", status)
	}
	if err := wsframe.WriteFrame(nc, wsframe.Frame{Header: wsframe.Header{Fin: true, Opcode: wsframe.OpText}, Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	fr, err := wsframe.ReadFrame(br, wsframe.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := wsframe.ParseClosePayload(fr.Payload); fr.Opcode != wsframe.OpClose || code != wsframe.CloseProtocolError {
		t.Errorf("frame = %+v %q", fr.Header, fr.Payload)
	}
	closed := f.rec.waitFor(t, servers.KindConnectionClose, 1)[0]
	if closed.WS.CloseCode != 1002 || closed.WS.InitiatedBy != "server" {
		t.Errorf("close event = %+v %+v", closed, closed.WS)
	}
	if errEv := f.rec.ofKind(servers.KindError); len(errEv) != 1 || errEv[0].WS == nil {
		t.Errorf("error events = %+v", errEv)
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	got, err := wsserver.Normalize(wsserver.Config{Path: "  ", Subprotocols: []string{" chat ", "", "v2.json"}})
	if err != nil || got.Path != "/" || !slices.Equal(got.Subprotocols, []string{"chat", "v2.json"}) {
		t.Errorf("Normalize = %+v, %v", got, err)
	}
	if got, err := wsserver.Normalize(wsserver.Config{Path: "*"}); err != nil || got.Path != "*" {
		t.Errorf("any path = %+v, %v", got, err)
	}
	bad := []wsserver.Config{
		{Path: "ws"},
		{Path: "/a b"},
		{Path: "/a?b"},
		{Path: "/" + strings.Repeat("a", wsserver.MaxPathBytes)},
		{Subprotocols: []string{"a b"}},
		{Subprotocols: []string{"a", "a"}},
		{Subprotocols: make([]string, wsserver.MaxSubprotocols+1)},
		{MaxMessageSize: -1},
		{MaxMessageSize: wsserver.MaxMessageSizeLimit + 1},
	}
	for _, c := range bad {
		if _, err := wsserver.Normalize(c); !hasCode(err, wsserver.CodeInvalidConfig) {
			t.Errorf("Normalize(%+v) = %v", c, err)
		}
	}
	def, err := wsserver.New(wsserver.Options{}).NormalizeConfig(nil)
	if err != nil || def.(wsserver.Config).Path != "/" {
		t.Errorf("default = %+v, %v", def, err)
	}
}
