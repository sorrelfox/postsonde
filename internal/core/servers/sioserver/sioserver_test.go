package sioserver_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/sioserver"
)

// These tests drive the server with PostSonde's own Socket.IO client
// (realtime/sioclient) over every transport, and with a raw WebSocket
// client where sioclient cannot say what is needed (answering the
// server's ack requests, not answering pings).

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

func (e *events) waitFor(t *testing.T, what string, ok func(servers.Event) bool) servers.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, ev := range e.all() {
			if ok(ev) {
				return ev
			}
		}
		if time.Now().After(deadline) {
			var sums []string
			for _, ev := range e.all() {
				sums = append(sums, ev.Summary)
			}
			t.Fatalf("no %s event among:\n%s", what, strings.Join(sums, "\n"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func summaryHas(s string) func(servers.Event) bool {
	return func(ev servers.Event) bool { return strings.Contains(ev.Summary, s) }
}

type fixture struct {
	m    *servers.Manager
	id   string
	port int
	url  string
	rec  *events
}

func start(t *testing.T, cfg sioserver.Config) *fixture {
	t.Helper()
	rec := &events{}
	m := servers.NewManager(servers.Options{
		Types:       []servers.Type{sioserver.New(sioserver.Options{ConnectTimeout: 5 * time.Second, UpgradeTimeout: 2 * time.Second})},
		OnTraffic:   rec.add,
		StopTimeout: 2 * time.Second,
		Logger:      slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: sioserver.TypeName, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	s, err = m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{m: m, id: s.ID, port: s.Port(), url: "http://127.0.0.1:" + strconv.Itoa(s.Port()), rec: rec}
}

func (f *fixture) hub(t *testing.T) sioserver.Hub {
	t.Helper()
	inst, err := f.m.Running(f.id)
	if err != nil {
		t.Fatal(err)
	}
	return inst.(sioserver.Hub)
}

func (f *fixture) waitSockets(t *testing.T, n int) []sioserver.Socket {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list := f.hub(t).Sockets()
		if len(list) == n {
			return list
		}
		if time.Now().After(deadline) {
			t.Fatalf("sockets = %+v, want %d", list, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// traces records a client's traces.
type traces struct {
	mu   sync.Mutex
	list []sioclient.Trace
}

func (tr *traces) add(x sioclient.Trace) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.list = append(tr.list, x)
}

func (tr *traces) wait(t *testing.T, what string, ok func(sioclient.Trace) bool) sioclient.Trace {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tr.mu.Lock()
		list := slices.Clone(tr.list)
		tr.mu.Unlock()
		for _, x := range list {
			if ok(x) {
				return x
			}
		}
		if time.Now().After(deadline) {
			var kinds []string
			for _, x := range list {
				k := string(x.Kind)
				if x.Packet != nil {
					k += ":" + x.Packet.Type.String() + ":" + x.Packet.EventName()
				}
				kinds = append(kinds, k)
			}
			t.Fatalf("no %s trace; got %v", what, kinds)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func received(name string) func(sioclient.Trace) bool {
	return func(x sioclient.Trace) bool {
		return x.Kind == sioclient.TracePacket && !x.Sent && x.Packet != nil && x.Packet.EventName() == name
	}
}

func dial(t *testing.T, f *fixture, o sioclient.Options) (*sioclient.Client, *sioclient.ConnectInfo, *traces, error) {
	t.Helper()
	tr := &traces{}
	o.URL = f.url + o.URL
	o.Dialer = netx.NewDirect(netx.Options{})
	o.OnTrace = tr.add
	o.AckTimeout = time.Second
	c, err := sioclient.New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	info, err := c.Connect(ctx)
	t.Cleanup(func() { c.Disconnect(context.Background()) })
	return c, info, tr, err
}

func mustDial(t *testing.T, f *fixture, o sioclient.Options) (*sioclient.Client, *sioclient.ConnectInfo, *traces) {
	t.Helper()
	c, info, tr, err := dial(t, f, o)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c, info, tr
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

// TestTransports connects the app's client over WebSocket, long-polling
// and the upgrade from one to the other, with an auth payload; emits an
// event with JSON and binary arguments that asks for an ack and gets the
// ack with the same arguments (auto-ack); gets an event the server emits
// with binary data; and disconnects.
func TestTransports(t *testing.T) {
	t.Parallel()
	for _, transport := range []sioclient.Transport{sioclient.TransportWebSocket, sioclient.TransportPolling, sioclient.TransportUpgrade} {
		t.Run(string(transport), func(t *testing.T) {
			t.Parallel()
			f := start(t, sioserver.Config{Auth: `{"token":"abc"}`})
			c, info, tr := mustDial(t, f, sioclient.Options{Transport: transport, Auth: json.RawMessage(`{"token":"abc","user":"ann"}`)})
			want := "websocket"
			if transport == sioclient.TransportPolling {
				want = "polling"
			}
			if info.Transport != want || info.SocketID == "" || info.Handshake.PingInterval != 25000 || info.Handshake.MaxPayload != sioserver.DefaultMaxPayload {
				t.Errorf("info = %+v", info)
			}
			if transport == sioclient.TransportUpgrade {
				tr.wait(t, "upgrade", func(x sioclient.Trace) bool { return x.Kind == sioclient.TraceUpgrade })
				f.rec.waitFor(t, "upgraded", summaryHas("upgraded to WebSocket"))
			}
			socks := f.waitSockets(t, 1)
			if s := socks[0]; s.ID != "1" || s.Namespace != "/" || s.SID != info.SocketID || s.Auth != `{"token":"abc","user":"ann"}` || s.Transport != want {
				t.Errorf("socket = %+v", s)
			}

			if _, err := c.Emit("upload", []any{"f.bin", map[string]any{"n": 1}, []byte{0, 1, 2, 0xff}}, true); err != nil {
				t.Fatal(err)
			}
			ack := tr.wait(t, "ack", func(x sioclient.Trace) bool { return x.Matched })
			args, _ := ack.Packet.Args()
			if ack.Packet.Type != socketio.BinaryAck || len(args) != 3 || args[0] != "f.bin" || !bytes.Equal(args[2].([]byte), []byte{0, 1, 2, 0xff}) {
				t.Errorf("ack = %v %v", ack.Packet.Type, args)
			}
			in := f.rec.waitFor(t, "event in", summaryHas("Event “upload” from socket 1 (ack 0)"))
			if in.SIO == nil || in.SIO.Packet != "BINARY_EVENT" || in.SIO.Attachments != 1 || in.Direction != servers.In ||
				string(in.Content) != `["f.bin",{"n":1},{"$base64":"AAEC/w=="}]` {
				t.Errorf("event in = %+v %+v %s", in, in.SIO, in.Content)
			}
			f.rec.waitFor(t, "auto-ack", summaryHas("Ack 0 to socket 1 (auto-ack)"))

			res, err := f.hub(t).Emit(sioserver.Target{SocketID: "1"}, "news", []any{json.RawMessage(`{"a":1}`), []byte("hi")}, false)
			if err != nil || res.Sent != 1 {
				t.Fatalf("Emit = %+v, %v", res, err)
			}
			news := tr.wait(t, "news", received("news"))
			if args, _ := news.Packet.Args(); len(args) != 3 || string(args[2].([]byte)) != "hi" {
				t.Errorf("news args = %v", args)
			}

			c.Disconnect(context.Background())
			f.rec.waitFor(t, "left", summaryHas("Socket 1 left /"))
			closeEv := f.rec.waitFor(t, "closed", func(ev servers.Event) bool { return ev.Kind == servers.KindConnectionClose && ev.SIO != nil })
			if closeEv.SIO.Reason != sioserver.ReasonTransportClose || closeEv.SIO.InitiatedBy != "client" {
				t.Errorf("close = %+v", closeEv.SIO)
			}
			f.waitSockets(t, 0)
		})
	}
}

// TestNamespacesAndAuth: listed namespaces are joined, another is refused
// as "Invalid namespace", a missing or wrong token as "Not authorized".
func TestNamespacesAndAuth(t *testing.T) {
	t.Parallel()
	f := start(t, sioserver.Config{Namespaces: []string{"admin", "/chat"}, Auth: `{"token": "abc"}`})
	good := json.RawMessage(`{"token":"abc"}`)
	c, info, _ := mustDial(t, f, sioclient.Options{URL: "/admin", Auth: good})
	if info.Namespace != "/admin" {
		t.Errorf("namespace = %q", info.Namespace)
	}
	socks := f.waitSockets(t, 1)
	if socks[0].ID != "1/admin" || socks[0].Namespace != "/admin" {
		t.Errorf("socket = %+v", socks[0])
	}
	if _, err := c.Emit("hello", nil, true); err != nil {
		t.Fatal(err)
	}
	f.rec.waitFor(t, "event on /admin", summaryHas("Event “hello” from socket 1/admin (ack 0)"))

	tests := []struct {
		name, url string
		auth      json.RawMessage
		message   string
	}{
		{"unknown namespace", "/other", good, "Invalid namespace"},
		{"no auth", "/chat", nil, "Not authorized"},
		{"wrong token", "/", json.RawMessage(`{"token":"xyz"}`), "Not authorized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := dial(t, f, sioclient.Options{URL: tt.url, Auth: tt.auth})
			e, ok := errors.AsType[*errs.Error](err)
			if !ok || e.Code != sioclient.CodeConnectError || e.Details["message"] != tt.message {
				t.Fatalf("err = %v", err)
			}
		})
	}
	f.rec.waitFor(t, "refused", summaryHas("/other refused (Invalid namespace)"))
}

// TestRules: the first matching rule acknowledges and emits (dynamic
// values rendered, after a delay for one), echo answers the others, and
// the acknowledgement modes empty and none.
func TestRules(t *testing.T) {
	t.Parallel()
	f := start(t, sioserver.Config{
		Echo: true,
		Rules: []sioserver.Rule{
			{Enabled: false, Match: sioserver.MatchExact, Pattern: "ping", Ack: `["disabled"]`},
			{Enabled: true, Match: sioserver.MatchExact, Pattern: "ping", Ack: `["pong", {"at": {{$timestamp}}}]`, Emit: "pinged", EmitArgs: `{"$hex": "CAFE"}`},
			{Enabled: true, Match: sioserver.MatchRegex, Pattern: `^slow\.`, Ack: `"late"`, DelayMs: 150},
		},
	})
	c, _, tr := mustDial(t, f, sioclient.Options{})
	if _, err := c.Emit("ping", nil, true); err != nil {
		t.Fatal(err)
	}
	ack := tr.wait(t, "ack of ping", func(x sioclient.Trace) bool { return x.Matched && x.AckEvent == "ping" })
	if args, _ := ack.Packet.Args(); len(args) != 2 || args[0] != "pong" {
		t.Errorf("ack args = %v", args)
	} else if at, ok := args[1].(map[string]any)["at"].(json.Number); !ok || len(at.String()) < 9 {
		t.Errorf("at = %v", args[1])
	}
	pinged := tr.wait(t, "pinged", received("pinged"))
	if args, _ := pinged.Packet.Args(); len(args) != 2 || !bytes.Equal(args[1].([]byte), []byte{0xca, 0xfe}) {
		t.Errorf("pinged = %v", args)
	}
	f.rec.waitFor(t, "rule source", summaryHas("Ack 0 to socket 1 (auto-reply rule 2)"))

	start := time.Now()
	if _, err := c.Emit("slow.one", nil, true); err != nil {
		t.Fatal(err)
	}
	late := tr.wait(t, "late ack", func(x sioclient.Trace) bool { return x.Matched && x.AckEvent == "slow.one" })
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("late ack after %v", d)
	}
	if args, _ := late.Packet.Args(); len(args) != 1 || args[0] != "late" {
		t.Errorf("late args = %v", args)
	}

	// No rule: echo, then the auto-ack with the arguments.
	if _, err := c.Emit("chat", []any{"hi"}, true); err != nil {
		t.Fatal(err)
	}
	echo := tr.wait(t, "echo", received("chat"))
	if args, _ := echo.Packet.Args(); len(args) != 2 || args[1] != "hi" {
		t.Errorf("echo = %v", args)
	}
	tr.wait(t, "chat ack", func(x sioclient.Trace) bool { return x.Matched && x.AckEvent == "chat" })

	// Ack modes, changed while connected.
	inst, _ := f.m.Running(f.id)
	inst.(servers.Reconfigurable).Reconfigure(mustNormalize(t, sioserver.Config{AckMode: sioserver.AckEmpty}))
	if _, err := c.Emit("empty", []any{"x"}, true); err != nil {
		t.Fatal(err)
	}
	empty := tr.wait(t, "empty ack", func(x sioclient.Trace) bool { return x.Matched && x.AckEvent == "empty" })
	if args, _ := empty.Packet.Args(); len(args) != 0 {
		t.Errorf("empty ack args = %v", args)
	}
	inst.(servers.Reconfigurable).Reconfigure(mustNormalize(t, sioserver.Config{AckMode: sioserver.AckNone}))
	if _, err := c.Emit("none", nil, true); err != nil {
		t.Fatal(err)
	}
	tr.wait(t, "ack timeout", func(x sioclient.Trace) bool { return x.Kind == sioclient.TraceAckTimeout && x.AckEvent == "none" })
}

func mustNormalize(t *testing.T, c sioserver.Config) sioserver.Config {
	t.Helper()
	n, err := sioserver.Normalize(c)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestEmitTargets: emit to one socket, to a room and to everyone; a
// server-side disconnect of a socket and of a whole client.
func TestEmitTargets(t *testing.T) {
	t.Parallel()
	f := start(t, sioserver.Config{})
	a, _, trA := mustDial(t, f, sioclient.Options{Transport: sioclient.TransportWebSocket})
	b, _, trB := mustDial(t, f, sioclient.Options{Transport: sioclient.TransportPolling, URL: "/chat"})
	socks := f.waitSockets(t, 2)
	if socks[0].ID != "1" || socks[1].ID != "2/chat" {
		t.Fatalf("sockets = %+v", socks)
	}
	hub := f.hub(t)
	if err := hub.SetRooms("2/chat", []string{"lobby", " ", "vip"}); err != nil {
		t.Fatal(err)
	}
	if got := hub.Sockets()[1].Rooms; !slices.Equal(got, []string{"lobby", "vip"}) {
		t.Errorf("rooms = %v", got)
	}
	if err := hub.SetRooms("2/chat", []string{"a,b"}); codeOf(err) != sioserver.CodeInvalidRoom {
		t.Errorf("bad room: %v", err)
	}
	if res, err := hub.Emit(sioserver.Target{Room: "lobby"}, "room", []any{1}, false); err != nil || res.Sent != 1 {
		t.Errorf("room emit = %+v, %v", res, err)
	}
	trB.wait(t, "room event", received("room"))
	if res, err := hub.Emit(sioserver.Target{}, "all", nil, true); err != nil || res.Sent != 2 {
		t.Errorf("emit to all = %+v, %v", res, err)
	}
	trA.wait(t, "all on a", received("all"))
	ev := trB.wait(t, "all on b", received("all"))
	if ev.Packet.Namespace != "/chat" || !ev.Packet.HasID {
		t.Errorf("b got %+v", ev.Packet)
	}
	if _, err := hub.Emit(sioserver.Target{SocketID: "9"}, "x", nil, false); codeOf(err) != sioserver.CodeSocketNotFound {
		t.Errorf("unknown socket: %v", err)
	}
	if _, err := hub.Emit(sioserver.Target{}, "disconnect", nil, false); codeOf(err) != sioserver.CodeInvalidEvent {
		t.Errorf("reserved name: %v", err)
	}
	if _, err := hub.Emit(sioserver.Target{}, "", nil, false); codeOf(err) != sioserver.CodeInvalidEvent {
		t.Errorf("no name: %v", err)
	}

	if err := hub.Disconnect("2/chat", false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("b did not close")
	}
	if b.Reason() != sioclient.ReasonServerDisconnect {
		t.Errorf("b reason = %q", b.Reason())
	}
	if err := hub.Disconnect("1", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a did not close")
	}
	if a.Reason() != sioclient.ReasonTransportClose {
		t.Errorf("a reason = %q", a.Reason())
	}
	ev2 := f.rec.waitFor(t, "forced close", summaryHas("Client 1 closed: forced close"))
	if ev2.SIO.InitiatedBy != "server" {
		t.Errorf("close = %+v", ev2.SIO)
	}
}

// rawClient is a WebSocket Engine.IO client written by hand, for what
// sioclient does not do.
type rawClient struct {
	nc   net.Conn
	conn *wsframe.Conn
}

func rawDial(t *testing.T, f *fixture) *rawClient {
	t.Helper()
	return rawDialQuery(t, f, "EIO=4&transport=websocket")
}

// rawDialQuery opens a WebSocket to the Engine.IO path with the query.
func rawDialQuery(t *testing.T, f *fixture, query string) *rawClient {
	t.Helper()
	nc, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(f.port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	req := "GET /socket.io/?" + query + " HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := nc.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	return &rawClient{nc: nc, conn: wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleClient, Reader: br})}
}

func (c *rawClient) read(t *testing.T) string {
	t.Helper()
	_ = c.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

// readUntil skips pings until a message with prefix.
func (c *rawClient) readUntil(t *testing.T, prefix string) string {
	t.Helper()
	for {
		m := c.read(t)
		if strings.HasPrefix(m, prefix) {
			return m
		}
	}
}

func (c *rawClient) write(t *testing.T, s string) {
	t.Helper()
	if err := c.conn.WriteMessage(wsframe.OpText, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

// TestServerAcks: an event the server emits with an ack is answered by
// the client, and the ack is logged with the event's name and round trip.
func TestServerAcks(t *testing.T) {
	t.Parallel()
	f := start(t, sioserver.Config{})
	c := rawDial(t, f)
	if m := c.read(t); !strings.HasPrefix(m, `0{"sid":`) || !strings.Contains(m, `"upgrades":[]`) {
		t.Fatalf("open = %s", m)
	}
	c.write(t, "40")
	if m := c.read(t); !strings.HasPrefix(m, `40{"sid":"`) {
		t.Fatalf("connect = %s", m)
	}
	if _, err := f.hub(t).Emit(sioserver.Target{SocketID: "1"}, "ask", []any{"q"}, true); err != nil {
		t.Fatal(err)
	}
	if m := c.readUntil(t, "42"); m != `420["ask","q"]` {
		t.Fatalf("event = %s", m)
	}
	c.write(t, `430["answer"]`)
	ev := f.rec.waitFor(t, "ack in", summaryHas("Ack 0 from socket 1 (answers “ask”"))
	if ev.SIO.Event != "ask" || ev.SIO.RTT <= 0 || string(ev.Content) != `["answer"]` {
		t.Errorf("ack = %+v %s", ev.SIO, ev.Content)
	}
	// An ack nobody waits for, and a packet for a namespace not joined.
	c.write(t, `437[]`)
	f.rec.waitFor(t, "stray ack", summaryHas("Ack 7 from socket 1 (no event of the server waits for it)"))
	c.write(t, `42/x,["e"]`)
	f.rec.waitFor(t, "not joined", summaryHas("for /x, which it has not joined (ignored)"))
	// A packet that cannot be decoded closes the client.
	c.write(t, `4x`)
	f.rec.waitFor(t, "parse error", summaryHas("Client 1 closed: parse error"))
}

// TestHeartbeat: the server pings at its interval (the client answers),
// and closes a client that does not answer within the timeout.
func TestHeartbeat(t *testing.T) {
	t.Parallel()
	// The timeout leaves room for a long-polling pong under load; the
	// unanswered ping still ends the client well within waitFor's 5 s.
	f := start(t, sioserver.Config{PingIntervalMs: 250, PingTimeoutMs: 1500})
	_, info, tr := mustDial(t, f, sioclient.Options{Transport: sioclient.TransportPolling})
	if info.Handshake.PingInterval != 250 || info.Handshake.PingTimeout != 1500 {
		t.Errorf("handshake = %+v", info.Handshake)
	}
	tr.wait(t, "second pong", func(x sioclient.Trace) bool {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		n := 0
		for _, y := range tr.list {
			if y.Kind == sioclient.TracePong {
				n++
			}
		}
		return n >= 2
	})

	raw := rawDial(t, f)
	raw.read(t)
	raw.write(t, "40")
	if m := raw.readUntil(t, "2"); m != "2" {
		t.Fatalf("ping = %q", m)
	}
	ev := f.rec.waitFor(t, "ping timeout", summaryHas("Client 2 closed: ping timeout"))
	if ev.SIO.Reason != sioserver.ReasonPingTimeout {
		t.Errorf("reason = %+v", ev.SIO)
	}
}

// TestStop: stopping closes every client with a close packet, logged
// before the stopped event.
func TestStop(t *testing.T) {
	t.Parallel()
	f := start(t, sioserver.Config{})
	a, _, _ := mustDial(t, f, sioclient.Options{Transport: sioclient.TransportWebSocket})
	b, _, _ := mustDial(t, f, sioclient.Options{Transport: sioclient.TransportPolling})
	f.waitSockets(t, 2)
	if _, err := f.m.Stop(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*sioclient.Client{a, b} {
		select {
		case <-c.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("a client did not close")
		}
		if c.Reason() != sioclient.ReasonTransportClose {
			t.Errorf("reason = %q, err %v", c.Reason(), c.Err())
		}
	}
	all := f.rec.all()
	last := all[len(all)-1]
	if last.Kind != servers.KindStopped {
		t.Errorf("last event = %+v", last)
	}
	n := 0
	for _, ev := range all {
		if ev.Kind == servers.KindConnectionClose && ev.SIO != nil && ev.SIO.Reason == sioserver.ReasonServerShutdown {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d shutdown closes", n)
	}
}

// TestStopStalledUpgrade: an upgrade WebSocket that sends nothing, not
// even the probe, does not hold Stop.
func TestStopStalledUpgrade(t *testing.T) {
	t.Parallel()
	f := start(t, sioserver.Config{})
	resp, err := http.Get(f.url + "/socket.io/?EIO=4&transport=polling")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var hs struct {
		SID string `json:"sid"`
	}
	if !bytes.HasPrefix(body, []byte("0")) || json.Unmarshal(body[1:], &hs) != nil || hs.SID == "" {
		t.Fatalf("handshake = %q", body)
	}
	c := rawDialQuery(t, f, "EIO=4&transport=websocket&sid="+hs.SID)
	f.rec.waitFor(t, "upgrade accepted", summaryHas("101 Switching Protocols: client 1 probes WebSocket"))
	// The poll that takes the close packet, as a long-polling client keeps one.
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		if resp, err := http.Get(f.url + "/socket.io/?EIO=4&transport=polling&sid=" + hs.SID); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	begin := time.Now()
	if _, err := f.m.Stop(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	// Well under the fixture's upgrade timeout and stop timeout (2 s).
	if d := time.Since(begin); d > time.Second {
		t.Errorf("Stop took %v", d)
	}
	_ = c.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := c.conn.ReadMessage(); err == nil {
		t.Error("the upgrade WebSocket is still open")
	}
	<-polled
}

// TestRequests: what is not a Socket.IO request is answered as engine.io
// answers it, and logged.
func TestRequests(t *testing.T) {
	t.Parallel()
	f := start(t, sioserver.Config{Transports: sioserver.TransportsWebSocket, AllowOrigin: "*"})
	tests := []struct {
		name, method, target string
		status               int
		body                 string
	}{
		{"other path", "GET", "/x", 404, "No Socket.IO endpoint at /x."},
		{"engine.io 3", "GET", "/socket.io/?EIO=3&transport=polling", 400, `{"code":5,"message":"Unsupported protocol version"}`},
		{"polling off", "GET", "/socket.io/?EIO=4&transport=polling", 400, `{"code":0,"message":"Transport unknown"}`},
		{"unknown session", "GET", "/socket.io/?EIO=4&transport=websocket&sid=nope", 400, `{"code":1,"message":"Session ID unknown"}`},
		{"not an upgrade", "GET", "/socket.io/?EIO=4&transport=websocket", 400, `{"code":3,"message":"Bad request"}`},
		{"preflight", "OPTIONS", "/socket.io/", 204, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(tt.method, f.url+tt.target, nil)
			req.Header.Set("Origin", "http://page.test")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tt.status || !strings.HasPrefix(string(body), tt.body) {
				t.Errorf("%d %s", resp.StatusCode, body)
			}
			if resp.Header.Get("Access-Control-Allow-Origin") != "http://page.test" {
				t.Errorf("CORS = %v", resp.Header)
			}
		})
	}
	f.rec.waitFor(t, "logged rejection", summaryHas("400 Bad Request (transport \"polling\" is not accepted)"))
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	c, err := sioserver.Normalize(sioserver.Config{Path: " /io ", Namespaces: []string{"admin", "/", " /chat ", "/admin"}, Auth: ` { "token" : "a" } `})
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != "/io/" || !slices.Equal(c.Namespaces, []string{"/admin", "/chat"}) || c.Auth != `{"token":"a"}` ||
		c.PingIntervalMs != 25000 || c.PingTimeoutMs != 20000 || c.MaxPayload != sioserver.DefaultMaxPayload || c.AckMode != sioserver.AckEcho {
		t.Errorf("normalized = %+v", c)
	}
	tests := []struct {
		name  string
		cfg   sioserver.Config
		field string
		rule  string
	}{
		{"path", sioserver.Config{Path: "io"}, "path", ""},
		{"namespace", sioserver.Config{Namespaces: []string{"/a,b"}}, "namespaces", ""},
		{"auth not object", sioserver.Config{Auth: `[1]`}, "auth", ""},
		{"auth not json", sioserver.Config{Auth: `{`}, "auth", ""},
		{"transports", sioserver.Config{Transports: "grpc"}, "transports", ""},
		{"origin", sioserver.Config{AllowOrigin: "localhost"}, "allowOrigin", ""},
		{"ping", sioserver.Config{PingIntervalMs: 5}, "pingIntervalMs", ""},
		{"payload", sioserver.Config{MaxPayload: -1}, "maxPayload", ""},
		{"ack mode", sioserver.Config{AckMode: "maybe"}, "ackMode", ""},
		{"match", sioserver.Config{Rules: []sioserver.Rule{{Match: "glob"}}}, "rules", "match"},
		{"regex", sioserver.Config{Rules: []sioserver.Rule{{Match: "regex", Pattern: "(a"}}}, "rules", "pattern"},
		{"ack args", sioserver.Config{Rules: []sioserver.Rule{{Ack: "[1,"}}}, "rules", "ack"},
		{"emit args", sioserver.Config{Rules: []sioserver.Rule{{EmitArgs: `{"$hex":"zz"}`}}}, "rules", "emitArgs"},
		{"emit reserved", sioserver.Config{Rules: []sioserver.Rule{{Emit: "connect"}}}, "rules", "emit"},
		{"delay", sioserver.Config{Rules: []sioserver.Rule{{DelayMs: 60_001}}}, "rules", "delayMs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sioserver.Normalize(tt.cfg)
			e, ok := errors.AsType[*errs.Error](err)
			if !ok || e.Code != sioserver.CodeInvalidConfig || e.Details["field"] != tt.field || e.Details["ruleField"] != tt.rule {
				t.Fatalf("err = %v (%+v)", err, e)
			}
		})
	}
}
