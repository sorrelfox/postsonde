package servers

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/sioserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/tcpserver"
)

// lastSockets waits until the last servers:sockets event of id satisfies
// ok and returns its list.
func (r *recorder) lastSockets(t *testing.T, id string, ok func([]SIOSocketDTO) bool) []SIOSocketDTO {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		var last *SocketsEventDTO
		for i := range r.sockets {
			if r.sockets[i].ServerID == id {
				last = &r.sockets[i]
			}
		}
		r.mu.Unlock()
		if last != nil && ok(last.Sockets) {
			return last.Sockets
		}
		if time.Now().After(deadline) {
			t.Fatalf("servers:sockets of %s = %+v", id, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sioTraces collects the traces of the app's Socket.IO client.
type sioTraces struct {
	mu   sync.Mutex
	list []sioclient.Trace
}

func (tr *sioTraces) add(x sioclient.Trace) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.list = append(tr.list, x)
}

func (tr *sioTraces) wait(t *testing.T, what string, ok func(sioclient.Trace) bool) sioclient.Trace {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tr.mu.Lock()
		for _, x := range tr.list {
			if ok(x) {
				tr.mu.Unlock()
				return x
			}
		}
		tr.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no %s trace", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSocketIOServerWithTheSocketIOClient runs the Socket.IO server
// through the service against the app's own Socket.IO client
// (realtime/sioclient, what the Socket.IO editor runs) upgrading from
// long-polling to WebSocket: the socket list, an emit from the composer
// with a binary argument and an ack, an event from the client answered by
// an auto-reply rule, rooms, and a disconnect from the server.
func TestSocketIOServerWithTheSocketIOClient(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)
	cfg := &SocketIOConfigDTO{
		Namespaces: []string{"/chat"},
		Auth:       `{"token": "abc"}`,
		Rules:      []SIORuleDTO{{Enabled: true, Match: "exact", Pattern: "ping", Ack: `["pong"]`}},
	}
	srv, err := svc.Create(t.Context(), CreateInputDTO{Type: sioserver.TypeName, Config: ConfigDTO{SocketIO: cfg}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.Config.SocketIO == nil || srv.Config.SocketIO.Path != "/socket.io/" || srv.Config.SocketIO.Auth != `{"token":"abc"}` || srv.Config.SocketIO.AckMode != "echo" {
		t.Fatalf("config = %+v", srv.Config.SocketIO)
	}
	if list, err := svc.Sockets(t.Context(), srv.ID); err != nil || list == nil || len(list) != 0 {
		t.Errorf("Sockets stopped = %#v, %v", list, err)
	}
	if _, err := svc.EmitToSockets(t.Context(), SIOEmitInputDTO{ServerID: srv.ID, Event: "x"}); codeOf(err) != core.CodeNotRunning {
		t.Errorf("emit while stopped = %v", err)
	}
	if srv, err = svc.Start(t.Context(), srv.ID); err != nil {
		t.Fatal(err)
	}

	tr := &sioTraces{}
	client, err := sioclient.New(sioclient.Options{
		URL: "http://localhost:" + strconv.Itoa(srv.BoundPort) + "/chat", Auth: json.RawMessage(`{"token":"abc"}`),
		Transport: sioclient.TransportUpgrade, Dialer: netx.NewDirect(netx.Options{}), OnTrace: tr.add,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Disconnect(context.Background()) })
	socks := rec.lastSockets(t, srv.ID, func(l []SIOSocketDTO) bool { return len(l) == 1 && l[0].Transport == "websocket" })
	if s := socks[0]; s.ID != "1/chat" || s.Namespace != "/chat" || s.Auth != `{"token":"abc"}` || s.Rooms == nil {
		t.Errorf("socket = %+v", s)
	}

	res, err := svc.EmitToSockets(t.Context(), SIOEmitInputDTO{ServerID: srv.ID, SocketID: "1/chat", Event: "news", Args: `["hi", {"$base64": "AAE="}]`, Ack: true})
	if err != nil || res.Sent != 1 {
		t.Fatalf("EmitToSockets = %+v, %v", res, err)
	}
	news := tr.wait(t, "news", func(x sioclient.Trace) bool {
		return x.Kind == sioclient.TracePacket && !x.Sent && x.Packet != nil && x.Packet.EventName() == "news"
	})
	if args, _ := news.Packet.Args(); news.Packet.Type != socketio.BinaryEvent || !news.Packet.HasID || len(args) != 3 || !bytes.Equal(args[2].([]byte), []byte{0, 1}) {
		t.Errorf("news = %+v %v", news.Packet, args)
	}
	out := rec.findTraffic(t, func(e TrafficDTO) bool { return e.SIO != nil && e.SIO.Event == "news" })
	if out.Direction != "out" || out.SIO.Packet != "BINARY_EVENT" || !out.SIO.HasAck || out.SIO.AckID != 0 || out.SIO.SocketID != "1/chat" || out.Text != `["hi",{"$base64":"AAE="}]` {
		t.Errorf("traffic = %+v %+v", out, out.SIO)
	}

	if _, err := client.Emit("ping", nil, true); err != nil {
		t.Fatal(err)
	}
	tr.wait(t, "pong", func(x sioclient.Trace) bool { return x.Matched && x.AckEvent == "ping" })
	rule := rec.findTraffic(t, func(e TrafficDTO) bool { return e.SIO != nil && e.SIO.Source == "auto-reply rule 1" })
	if rule.SIO.Packet != "ACK" || rule.Text != `["pong"]` {
		t.Errorf("rule reply = %+v", rule)
	}

	if err := svc.SetSocketRooms(t.Context(), SIORoomsInputDTO{ServerID: srv.ID, SocketID: "1/chat", Rooms: []string{"lobby"}}); err != nil {
		t.Fatal(err)
	}
	rec.lastSockets(t, srv.ID, func(l []SIOSocketDTO) bool { return len(l) == 1 && len(l[0].Rooms) == 1 && l[0].Rooms[0] == "lobby" })
	if res, err := svc.EmitToSockets(t.Context(), SIOEmitInputDTO{ServerID: srv.ID, Room: "lobby", Event: "room"}); err != nil || res.Sent != 1 {
		t.Errorf("room emit = %+v, %v", res, err)
	}
	if _, err := svc.EmitToSockets(t.Context(), SIOEmitInputDTO{ServerID: srv.ID, Event: "x", Args: "[1,"}); codeOf(err) != socketio.CodeInvalidArgs || detailOf(err, "offset") != "3" {
		t.Errorf("bad args = %v", err)
	}

	if err := svc.DisconnectSocket(t.Context(), SIODisconnectInputDTO{ServerID: srv.ID, SocketID: "1/chat"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the client did not close")
	}
	if client.Reason() != sioclient.ReasonServerDisconnect {
		t.Errorf("reason = %q", client.Reason())
	}
	rec.lastSockets(t, srv.ID, func(l []SIOSocketDTO) bool { return len(l) == 0 })

	tcp, err := svc.Create(t.Context(), CreateInputDTO{Type: tcpserver.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sockets(t.Context(), tcp.ID); codeOf(err) != CodeNotSocketIO {
		t.Errorf("sockets of a TCP server: %v", err)
	}
	if _, err := svc.UpdateConfig(t.Context(), srv.ID, ConfigDTO{}); codeOf(err) != sioserver.CodeInvalidConfig {
		t.Errorf("update without settings: %v", err)
	}
	upd, err := svc.UpdateConfig(t.Context(), srv.ID, ConfigDTO{SocketIO: &SocketIOConfigDTO{Echo: true, AckMode: "none"}})
	if err != nil || !upd.Config.SocketIO.Echo || upd.Config.SocketIO.AckMode != "none" {
		t.Errorf("UpdateConfig = %+v, %v", upd.Config.SocketIO, err)
	}
}
