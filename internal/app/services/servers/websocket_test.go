package servers

import (
	"context"
	"encoding/base64"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/echo"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
)

// lastClients waits until the last servers:clients event of id lists n
// clients and returns it.
func (r *recorder) lastClients(t *testing.T, id string, n int) []WSClientDTO {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		var last *ClientsEventDTO
		for i := range r.clients {
			if r.clients[i].ServerID == id {
				last = &r.clients[i]
			}
		}
		r.mu.Unlock()
		if last != nil && len(last.Clients) == n {
			return last.Clients
		}
		if time.Now().After(deadline) {
			t.Fatalf("servers:clients of %s = %+v, want %d clients", id, last, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWebSocketServer(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)

	def, err := svc.Create(t.Context(), CreateInputDTO{Type: wsserver.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if def.Config.WebSocket == nil || def.Config.WebSocket.Path != "/" || def.Config.WebSocket.Subprotocols == nil || def.Name != "WebSocket" {
		t.Errorf("default = %+v", def.Config)
	}
	if _, err := svc.UpdateConfig(t.Context(), def.ID, ConfigDTO{}); codeOf(err) != wsserver.CodeInvalidConfig {
		t.Errorf("UpdateConfig without settings = %v", err)
	}
	if _, err := svc.UpdateConfig(t.Context(), def.ID, ConfigDTO{WebSocket: &WebSocketConfigDTO{Path: "nope"}}); codeOf(err) != wsserver.CodeInvalidConfig {
		t.Errorf("UpdateConfig bad path = %v", err)
	}
	badRule := ConfigDTO{WebSocket: &WebSocketConfigDTO{Rules: []WSRuleDTO{{Enabled: true, Match: "exact"}, {Match: "regex", Pattern: "[a-"}}}}
	if _, err := svc.UpdateConfig(t.Context(), def.ID, badRule); codeOf(err) != wsserver.CodeInvalidConfig || detailOf(err, "ruleIndex") != "1" || detailOf(err, "ruleField") != "pattern" {
		t.Errorf("UpdateConfig bad regex = %v", err)
	}
	srv, err := svc.UpdateConfig(t.Context(), def.ID, ConfigDTO{WebSocket: &WebSocketConfigDTO{Path: "/ws", Subprotocols: []string{"chat"}}})
	if err != nil || srv.Config.WebSocket.Path != "/ws" {
		t.Fatalf("UpdateConfig = %+v, %v", srv, err)
	}

	// Not running: no clients, operations refused.
	if list, err := svc.Clients(t.Context(), srv.ID); err != nil || list == nil || len(list) != 0 {
		t.Errorf("Clients stopped = %#v, %v", list, err)
	}
	if err := svc.SendToClient(t.Context(), WSSendInputDTO{ServerID: srv.ID, ClientID: "1", Message: WSMessageDTO{Text: "x"}}); codeOf(err) != core.CodeNotRunning {
		t.Errorf("send while stopped = %v", err)
	}

	running, err := svc.Start(t.Context(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	msgs := make(chan wsclient.Message, 10)
	c, err := wsclient.New(wsclient.Options{
		URL:          "ws://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(running.BoundPort)) + "/ws",
		Dialer:       netx.NewDirect(netx.Options{}),
		Subprotocols: []string{"chat"},
		OnMessage:    func(m wsclient.Message) { msgs <- m },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Abort)

	cl := rec.lastClients(t, srv.ID, 1)[0]
	if cl.Subprotocol != "chat" || cl.Target != "/ws" || cl.ConnectedAt == "" {
		t.Errorf("client = %+v", cl)
	}
	if list, err := svc.Clients(t.Context(), srv.ID); err != nil || len(list) != 1 || list[0].ID != cl.ID {
		t.Errorf("Clients = %+v, %v", list, err)
	}

	if err := svc.SendToClient(t.Context(), WSSendInputDTO{ServerID: srv.ID, ClientID: cl.ID, Message: WSMessageDTO{Text: "one"}}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Broadcast(t.Context(), WSBroadcastInputDTO{ServerID: srv.ID, Message: WSMessageDTO{Binary: true, DataBase64: base64.StdEncoding.EncodeToString([]byte{1, 2})}})
	if err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Errorf("Broadcast = %+v, %v", res, err)
	}
	for _, want := range []string{"one", "\x01\x02"} {
		select {
		case m := <-msgs:
			if string(m.Data) != want {
				t.Errorf("client got %q, want %q", m.Data, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no message")
		}
	}
	if _, err := svc.Broadcast(t.Context(), WSBroadcastInputDTO{ServerID: srv.ID, Message: WSMessageDTO{Binary: true, DataBase64: "!!"}}); codeOf(err) != wsserver.CodeInvalidMessage {
		t.Errorf("bad base64 = %v", err)
	}
	// The counts reach the UI.
	deadline := time.Now().Add(5 * time.Second)
	for rec.lastClients(t, srv.ID, 1)[0].MessagesOut != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("counts = %+v", rec.lastClients(t, srv.ID, 1))
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := svc.DisconnectClient(t.Context(), WSDisconnectInputDTO{ServerID: srv.ID, ClientID: cl.ID, Code: 1015}); codeOf(err) != wsframe.CodeInvalidArgument {
		t.Errorf("disconnect 1015 = %v", err)
	}
	if err := svc.DisconnectClient(t.Context(), WSDisconnectInputDTO{ServerID: srv.ID, ClientID: cl.ID, Reason: "kick"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client not closed")
	}
	if info := c.CloseInfo(); info.Code != 1000 || info.Reason != "kick" {
		t.Errorf("close = %+v", info)
	}
	rec.lastClients(t, srv.ID, 0)
	rec.waitTraffic(t, "started,connection_open,request,response,message,message,connection_close")
	rec.mu.Lock()
	closeEv := rec.traffic[len(rec.traffic)-1]
	rec.mu.Unlock()
	if closeEv.WS == nil || closeEv.WS.CloseCode != 1000 || closeEv.WS.InitiatedBy != "server" {
		t.Errorf("close event = %+v", closeEv)
	}

	// Client operations on another type of server.
	other, err := svc.Create(t.Context(), CreateInputDTO{Type: echo.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Clients(t.Context(), other.ID); codeOf(err) != CodeNotWebSocket {
		t.Errorf("Clients of echo = %v", err)
	}
	if _, err := svc.Clients(t.Context(), "srv_missing"); codeOf(err) != core.CodeNotFound {
		t.Errorf("Clients of unknown = %v", err)
	}
}
