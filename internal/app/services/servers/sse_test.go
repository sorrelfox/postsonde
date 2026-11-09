package servers

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	sseservice "github.com/sorrelfox/postsonde/internal/app/services/sse"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/sseserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// lastSubscribers waits until the last servers:subscribers event of id
// satisfies ok and returns its list.
func (r *recorder) lastSubscribers(t *testing.T, id string, ok func([]SSESubscriberDTO) bool) []SSESubscriberDTO {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		var last *SubscribersEventDTO
		for i := range r.subscribers {
			if r.subscribers[i].ServerID == id {
				last = &r.subscribers[i]
			}
		}
		r.mu.Unlock()
		if last != nil && ok(last.Subscribers) {
			return last.Subscribers
		}
		if time.Now().After(deadline) {
			t.Fatalf("servers:subscribers of %s = %+v", id, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// findTraffic waits until a servers:traffic event satisfies ok and
// returns it.
func (r *recorder) findTraffic(t *testing.T, ok func(TrafficDTO) bool) TrafficDTO {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		for _, e := range r.traffic {
			if ok(e) {
				r.mu.Unlock()
				return e
			}
		}
		r.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("no such traffic event")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// streamRecorder collects the items of the app's SSE client.
type streamRecorder struct {
	mu    sync.Mutex
	items []sseservice.StreamItemDTO
}

func (r *streamRecorder) emit(_ string, data any) {
	if d, ok := data.(operation.DataEventDTO[sseservice.StreamItemDTO]); ok {
		r.mu.Lock()
		r.items = append(r.items, d.Items...)
		r.mu.Unlock()
	}
}

// waitEvent waits for an event item with data and returns it.
func (r *streamRecorder) waitEvent(t *testing.T, data string) sseservice.StreamItemDTO {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		for _, it := range r.items {
			if it.Type == sseservice.ItemEvent && it.Data == data {
				r.mu.Unlock()
				return it
			}
		}
		n := len(r.items)
		r.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no event %q among %d items", data, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSSEServerWithTheSSEClient runs the SSE server against the app's own
// SSE client (services/sse over the HTTP service and the guarded dialer):
// pushed events arrive with their type and ID, and after the server
// disconnects the subscriber the client reconnects with Last-Event-ID,
// which the subscriber list shows.
func TestSSEServerWithTheSSEClient(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)
	srv, err := svc.Create(t.Context(), CreateInputDTO{Type: sseserver.TypeName, Config: ConfigDTO{SSE: &SSEConfigDTO{Path: "/events", RetryMs: 50, AutoID: true, AllowOrigin: "*"}}})
	if err != nil {
		t.Fatal(err)
	}
	if list, err := svc.Subscribers(t.Context(), srv.ID); err != nil || list == nil || len(list) != 0 {
		t.Errorf("Subscribers stopped = %#v, %v", list, err)
	}
	if _, err := svc.SendEvent(t.Context(), SSESendInputDTO{ServerID: srv.ID, Data: "x"}); codeOf(err) != core.CodeNotRunning {
		t.Errorf("send while stopped = %v", err)
	}
	running, err := svc.Start(t.Context(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(running.BoundPort)) + "/events"

	// The app's SSE client, wired as the app wires it.
	logger := slog.New(slog.DiscardHandler)
	stream := &streamRecorder{}
	ops := operation.NewRegistry(stream.emit, logger)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ops.Shutdown(ctx)
	})
	on := true
	src := collection.HTTPSource{
		Request: projectfile.HTTPRequest{ID: "req_sse", Method: "GET", URL: url, Settings: projectfile.RequestSettings{Reconnect: &projectfile.SSEReconnect{Enabled: &on}}},
		Auth:    projectfile.Auth{Type: projectfile.AuthNone},
		SSE:     true,
	}
	load := func(context.Context, string) (collection.HTTPSource, error) { return src, nil }
	dialer := netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(logger)))
	httpSvc := httpservice.New(load, nil, ops, dialer, nil, logger)
	t.Cleanup(func() { _ = httpSvc.ServiceShutdown() })
	client := sseservice.New(func(ctx context.Context, id string, allow bool) (sseservice.Target, error) {
		st, err := httpservice.NewStream(ctx, httpSvc, id, allow)
		if err != nil {
			return sseservice.Target{}, err
		}
		return sseservice.Target{Stream: st, Method: st.Method, URL: st.URL, Reconnect: sseservice.ReconnectFrom(st.Reconnect)}, nil
	}, ops, logger)
	started, err := client.Connect(t.Context(), sseservice.ConnectInputDTO{RequestID: "req_sse"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ops.Cancel(started.OpID) })

	subs := rec.lastSubscribers(t, srv.ID, func(l []SSESubscriberDTO) bool { return len(l) == 1 })
	if subs[0].ID != "1" || subs[0].Target != "/events" || subs[0].ConnectedAt == "" {
		t.Errorf("subscriber = %+v", subs[0])
	}
	res, err := svc.SendEvent(t.Context(), SSESendInputDTO{ServerID: srv.ID, Event: "greeting", Data: "hello\nworld"})
	if err != nil || res.Sent != 1 || res.ID != "1" {
		t.Fatalf("SendEvent = %+v, %v", res, err)
	}
	if it := stream.waitEvent(t, "hello\nworld"); it.Event != "greeting" || it.ID != "1" {
		t.Errorf("event = %+v", it)
	}
	if _, err := svc.SendEvent(t.Context(), SSESendInputDTO{ServerID: srv.ID, Event: "bad\ntype"}); codeOf(err) != sseserver.CodeInvalidMessage || detailOf(err, "field") != "event" {
		t.Errorf("bad type = %v", err)
	}

	if err := svc.DisconnectSubscriber(t.Context(), SSEDisconnectInputDTO{ServerID: srv.ID, SubscriberID: "1"}); err != nil {
		t.Fatal(err)
	}
	subs = rec.lastSubscribers(t, srv.ID, func(l []SSESubscriberDTO) bool { return len(l) == 1 && l[0].ID == "2" })
	if subs[0].LastEventID != "1" {
		t.Errorf("reconnected subscriber = %+v", subs[0])
	}
	if _, err := svc.SendEvent(t.Context(), SSESendInputDTO{ServerID: srv.ID, SubscriberID: "2", Data: "resumed"}); err != nil {
		t.Fatal(err)
	}
	if it := stream.waitEvent(t, "resumed"); it.ID != "2" {
		t.Errorf("resumed event = %+v", it)
	}
	ev := rec.findTraffic(t, func(e TrafficDTO) bool { return e.SSE != nil && e.Kind == "response" && e.SSE.SubscriberID == "2" })
	if ev.SSE.LastEventID != "1" || ev.Summary != "200 OK: subscriber 2 (Last-Event-ID “1”)" {
		t.Errorf("resumed response = %+v", ev)
	}

	if _, err := svc.Stop(t.Context(), srv.ID); err != nil {
		t.Fatal(err)
	}
	rec.lastSubscribers(t, srv.ID, func(l []SSESubscriberDTO) bool { return len(l) == 0 })
	if _, err := svc.Subscribers(t.Context(), "nope"); codeOf(err) != core.CodeNotFound {
		t.Errorf("unknown server = %v", err)
	}
}

// lastPeers waits until the last servers:peers event of id satisfies ok.
func (r *recorder) lastPeers(t *testing.T, id string, ok func([]UDPPeerDTO) bool) []UDPPeerDTO {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		var last *PeersEventDTO
		for i := range r.peers {
			if r.peers[i].ServerID == id {
				last = &r.peers[i]
			}
		}
		r.mu.Unlock()
		if last != nil && ok(last.Peers) {
			return last.Peers
		}
		if time.Now().After(deadline) {
			t.Fatalf("servers:peers of %s = %+v", id, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestUDPServerEndToEnd: a client made with net.DialUDP gets the echo and
// the fixed reply, the peer is listed, and the app sends it text and hex.
func TestUDPServerEndToEnd(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)
	srv, err := svc.Create(t.Context(), CreateInputDTO{Type: udpserver.TypeName, Config: ConfigDTO{UDP: &UDPConfigDTO{Echo: true, FixedReply: true, Reply: "ok"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(t.Context(), CreateInputDTO{Type: udpserver.TypeName, TLS: &ServerTLSInputDTO{Certificate: core.CertGenerated}}); codeOf(err) != core.CodeTLSNotSupported {
		t.Errorf("UDP with TLS = %v", err)
	}
	running, err := svc.Start(t.Context(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: running.BoundPort})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	read := func() string {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 2048)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:n])
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "ping" {
		t.Errorf("echo = %q", got)
	}
	if got := read(); got != "ok" {
		t.Errorf("fixed reply = %q", got)
	}
	peer := c.LocalAddr().String()
	peers := rec.lastPeers(t, srv.ID, func(l []UDPPeerDTO) bool { return len(l) == 1 && l[0].DatagramsOut == 2 })
	if peers[0].Addr != peer || peers[0].DatagramsIn != 1 || peers[0].LastSeen == "" {
		t.Errorf("peers = %+v", peers)
	}
	if res, err := svc.SendDatagram(t.Context(), UDPSendInputDTO{ServerID: srv.ID, Peer: peer, Mode: "text", Text: "hi", LineEnding: "crlf"}); err != nil || res.Bytes != 4 {
		t.Fatalf("SendDatagram = %+v, %v", res, err)
	}
	if got := read(); got != "hi\r\n" {
		t.Errorf("text = %q", got)
	}
	if _, err := svc.SendDatagram(t.Context(), UDPSendInputDTO{ServerID: srv.ID, Peer: peer, Mode: "hex", Text: "01 02"}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "\x01\x02" {
		t.Errorf("hex = %q", got)
	}
	if _, err := svc.SendDatagram(t.Context(), UDPSendInputDTO{ServerID: srv.ID, Peer: "127.0.0.1:9", Mode: "text", Text: "x"}); codeOf(err) != udpserver.CodePeerNotFound {
		t.Errorf("unknown peer = %v", err)
	}
	in := rec.findTraffic(t, func(e TrafficDTO) bool { return e.UDP != nil && e.Direction == "in" })
	if in.Peer != peer || in.Text != "ping" || in.UDP.Local == "" {
		t.Errorf("datagram = %+v", in)
	}
	if _, err := svc.UpdateConfig(t.Context(), srv.ID, ConfigDTO{UDP: &UDPConfigDTO{FixedReply: true, Reply: "zz", ReplyFormat: "hex"}}); codeOf(err) != udpserver.CodeInvalidConfig {
		t.Errorf("bad hex reply = %v", err)
	}
	if _, err := svc.Peers(t.Context(), srv.ID); err != nil {
		t.Error(err)
	}
}
