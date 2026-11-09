package servers

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/hexbytes"
	"github.com/sorrelfox/postsonde/internal/core/realtime/tcpclient"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/echo"
	"github.com/sorrelfox/postsonde/internal/core/servers/tcpserver"
)

// lastConnections waits until the last servers:connections event of id
// lists n connections and returns it.
func (r *recorder) lastConnections(t *testing.T, id string, n int) []TCPConnectionDTO {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		var last *ConnectionsEventDTO
		for i := range r.connections {
			if r.connections[i].ServerID == id {
				last = &r.connections[i]
			}
		}
		r.mu.Unlock()
		if last != nil && len(last.Connections) == n {
			return last.Connections
		}
		if time.Now().After(deadline) {
			t.Fatalf("servers:connections of %s = %+v, want %d connections", id, last, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// received collects what the app's TCP client receives.
type received struct {
	mu   sync.Mutex
	data []byte
}

func (r *received) add(c tcpclient.Chunk) {
	if c.Direction != tcpclient.Received {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, c.Data...)
}

func (r *received) wait(t *testing.T, want []byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		got := bytes.Clone(r.data)
		r.mu.Unlock()
		if bytes.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("client received %q, want %q", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTCPServerWithTheTCPClient runs the TCP server against the app's own
// TCP client, in both directions, text and hex.
func TestTCPServerWithTheTCPClient(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)

	srv, err := svc.Create(t.Context(), CreateInputDTO{Type: tcpserver.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if srv.Name != "TCP" || srv.Config.WebSocket != nil || srv.Config.MockHTTP != nil {
		t.Errorf("created = %+v", srv)
	}
	// Not running: no connections, operations refused.
	if list, err := svc.Connections(t.Context(), srv.ID); err != nil || list == nil || len(list) != 0 {
		t.Errorf("Connections stopped = %#v, %v", list, err)
	}
	if _, err := svc.SendToConnection(t.Context(), TCPSendInputDTO{ServerID: srv.ID, ConnectionID: "1", Mode: "text", Text: "x"}); codeOf(err) != core.CodeNotRunning {
		t.Errorf("send while stopped = %v", err)
	}

	running, err := svc.Start(t.Context(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.Scope != "loopback" {
		t.Errorf("scope = %q", running.Scope)
	}
	got := &received{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := tcpclient.Connect(ctx, tcpclient.Options{
		Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(running.BoundPort)),
		Dialer:  netx.NewDirect(netx.Options{}),
		OnData:  got.add,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	conn := rec.lastConnections(t, srv.ID, 1)[0]
	if conn.ID != "1" || conn.Peer != c.LocalAddr() || conn.ConnectedAt == "" {
		t.Errorf("connection = %+v (client %s)", conn, c.LocalAddr())
	}
	if list, err := svc.Connections(t.Context(), srv.ID); err != nil || len(list) != 1 || list[0].ID != conn.ID {
		t.Errorf("Connections = %+v, %v", list, err)
	}

	// Client to server.
	if err := c.Send([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	rec.waitTraffic(t, "started,connection_open,message")

	// Server to client: text with a line ending, then hex.
	res, err := svc.SendToConnection(t.Context(), TCPSendInputDTO{ServerID: srv.ID, ConnectionID: conn.ID, Mode: "text", Text: "pong", LineEnding: "crlf"})
	if err != nil || res.Bytes != 6 {
		t.Fatalf("send text = %+v, %v", res, err)
	}
	res, err = svc.SendToConnection(t.Context(), TCPSendInputDTO{ServerID: srv.ID, ConnectionID: conn.ID, Mode: "hex", Text: "DE AD 0xBEEF"})
	if err != nil || res.Bytes != 4 {
		t.Fatalf("send hex = %+v, %v", res, err)
	}
	got.wait(t, []byte("pong\r\n\xde\xad\xbe\xef"))

	for _, tt := range []struct {
		in   TCPSendInputDTO
		code string
	}{
		{TCPSendInputDTO{Mode: "hex", Text: "DE AG"}, hexbytes.CodeInvalidChar},
		{TCPSendInputDTO{Mode: "hex", Text: "ABC"}, hexbytes.CodeOddDigits},
		{TCPSendInputDTO{Mode: "base64", Text: "AA=="}, CodeInvalidTCPMessage},
		{TCPSendInputDTO{Mode: "text", Text: "x", LineEnding: "cr"}, CodeInvalidTCPMessage},
		{TCPSendInputDTO{Mode: "text", Text: ""}, tcpserver.CodeEmptyMessage},
		{TCPSendInputDTO{Mode: "hex", Text: " "}, tcpserver.CodeEmptyMessage},
		{TCPSendInputDTO{Mode: "text", Text: "x", ConnectionID: "9"}, tcpserver.CodeConnectionNotFound},
	} {
		in := tt.in
		in.ServerID = srv.ID
		if in.ConnectionID == "" {
			in.ConnectionID = conn.ID
		}
		if _, err := svc.SendToConnection(t.Context(), in); codeOf(err) != tt.code {
			t.Errorf("send %+v = %v, want %s", tt.in, err, tt.code)
		}
	}

	// The counts reach the UI.
	deadline := time.Now().Add(5 * time.Second)
	for {
		cur := rec.lastConnections(t, srv.ID, 1)[0]
		if cur.BytesIn == 5 && cur.BytesOut == 10 && cur.MessagesOut == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("counts = %+v", cur)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The traffic carries the bytes both ways, text and binary.
	rec.mu.Lock()
	var in, out []TrafficDTO
	for _, ev := range rec.traffic {
		if ev.Kind == "message" && ev.Direction == "in" {
			in = append(in, ev)
		}
		if ev.Kind == "message" && ev.Direction == "out" {
			out = append(out, ev)
		}
	}
	rec.mu.Unlock()
	if len(in) != 1 || in[0].Text != "ping\n" || in[0].Binary || in[0].TCP == nil {
		t.Errorf("in = %+v", in)
	}
	if len(out) != 2 || out[0].Text != "pong\r\n" || !out[1].Binary || out[1].DataBase64 != "3q2+7w==" {
		t.Errorf("out = %+v", out)
	}

	// The server closes the connection; the client sees the peer close.
	if err := svc.CloseConnection(t.Context(), TCPCloseInputDTO{ServerID: srv.ID, ConnectionID: conn.ID}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client not closed")
	}
	if info := c.CloseInfo(); info.InitiatedBy != tcpclient.InitiatorRemote {
		t.Errorf("client close = %+v", info)
	}
	rec.lastConnections(t, srv.ID, 0)
	rec.waitTraffic(t, "started,connection_open,message,message,message,connection_close")
	rec.mu.Lock()
	closeEv := rec.traffic[len(rec.traffic)-1]
	rec.mu.Unlock()
	if closeEv.TCP == nil || closeEv.TCP.InitiatedBy != "server" || closeEv.TCP.BytesIn != 5 || closeEv.TCP.BytesOut != 10 {
		t.Errorf("close event = %+v", closeEv)
	}
	if err := svc.CloseConnection(t.Context(), TCPCloseInputDTO{ServerID: srv.ID, ConnectionID: conn.ID}); codeOf(err) != tcpserver.CodeConnectionNotFound {
		t.Errorf("close again = %v", err)
	}

	// Connection operations on another type of server.
	other, err := svc.Create(t.Context(), CreateInputDTO{Type: echo.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Connections(t.Context(), other.ID); codeOf(err) != CodeNotTCP {
		t.Errorf("Connections of echo = %v", err)
	}
	if err := svc.CloseConnection(t.Context(), TCPCloseInputDTO{ServerID: other.ID, ConnectionID: "1"}); codeOf(err) != CodeNotTCP {
		t.Errorf("CloseConnection of echo = %v", err)
	}
	if _, err := svc.Connections(t.Context(), "srv_missing"); codeOf(err) != core.CodeNotFound {
		t.Errorf("Connections of unknown = %v", err)
	}
}

// TestTCPServerStopEmptiesTheConnections: stopping closes every connection
// and the last servers:connections event is empty.
func TestTCPServerStopEmptiesTheConnections(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)
	srv, err := svc.Create(t.Context(), CreateInputDTO{Type: tcpserver.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	running, err := svc.Start(t.Context(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(running.BoundPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	rec.lastConnections(t, srv.ID, 1)
	if _, err := svc.Stop(t.Context(), srv.ID); err != nil {
		t.Fatal(err)
	}
	rec.lastConnections(t, srv.ID, 0)
	rec.waitTraffic(t, "started,connection_open,connection_close,stopped")
}
