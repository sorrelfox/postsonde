package servers

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/listen"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/echo"
	"github.com/sorrelfox/postsonde/internal/core/servers/graphqlmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/grpcmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/sioserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/sseserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/tcpserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/webhook"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// recorder collects emitted events.
type recorder struct {
	mu      sync.Mutex
	changed []ChangedEventDTO
	traffic []TrafficDTO
	clients []ClientsEventDTO
	// connections are the servers:connections events.
	connections []ConnectionsEventDTO
	// captures are the servers:captures events.
	captures []CapturesEventDTO
	// subscribers and peers are the servers:subscribers and servers:peers
	// events.
	subscribers []SubscribersEventDTO
	peers       []PeersEventDTO
	// sockets are the servers:sockets events.
	sockets []SocketsEventDTO
	batches int
}

func (r *recorder) emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch name {
	case EventChanged:
		r.changed = append(r.changed, data.(ChangedEventDTO))
	case EventTraffic:
		ev := data.(TrafficEventDTO)
		if len(ev.Events) == 0 {
			panic("empty traffic batch")
		}
		r.batches++
		r.traffic = append(r.traffic, ev.Events...)
	case EventClients:
		r.clients = append(r.clients, data.(ClientsEventDTO))
	case EventConnections:
		r.connections = append(r.connections, data.(ConnectionsEventDTO))
	case EventCaptures:
		r.captures = append(r.captures, data.(CapturesEventDTO))
	case EventSubscribers:
		r.subscribers = append(r.subscribers, data.(SubscribersEventDTO))
	case EventPeers:
		r.peers = append(r.peers, data.(PeersEventDTO))
	case EventSockets:
		r.sockets = append(r.sockets, data.(SocketsEventDTO))
	default:
		panic("unexpected event " + name)
	}
}

func (r *recorder) states(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, ev := range r.changed {
		for _, s := range ev.Servers {
			if s.ID == id && (len(out) == 0 || out[len(out)-1] != s.State) {
				out = append(out, s.State)
			}
		}
	}
	return out
}

func (r *recorder) kinds() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	parts := make([]string, len(r.traffic))
	for i, e := range r.traffic {
		parts[i] = e.Kind
	}
	return strings.Join(parts, ",")
}

// waitTraffic waits until the traffic starts with want. Later events may
// already be there: a client can close its connection before the test
// looks, which adds a connection_close after the events asked for.
func (r *recorder) waitTraffic(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for kinds := r.kinds(); kinds != want && !strings.HasPrefix(kinds, want+","); kinds = r.kinds() {
		if time.Now().After(deadline) {
			t.Fatalf("traffic = %s, want it to start with %s", kinds, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTestService(t *testing.T) (*ServersService, *recorder) {
	t.Helper()
	rec := &recorder{}
	svc := newService(rec.emit, slog.New(slog.DiscardHandler), core.Options{StopTimeout: time.Second})
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	return svc, rec
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func detailOf(err error, key string) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Details[key]
	}
	return ""
}

func TestEchoServerLifecycle(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)

	types, err := svc.Types(t.Context())
	if err != nil || len(types) != 10 || types[0].Name != echo.TypeName || types[0].DefaultConfig.MockHTTP != nil ||
		types[1].Name != mockhttp.TypeName || types[1].DefaultConfig.MockHTTP == nil ||
		types[2].Name != wsserver.TypeName || types[2].DefaultConfig.WebSocket == nil || types[2].DefaultConfig.WebSocket.Path != "/" ||
		types[3].Name != tcpserver.TypeName || types[3].Label != "TCP" ||
		types[4].Name != webhook.TypeName || types[4].DefaultConfig.Webhook == nil || types[4].DefaultConfig.Webhook.Status != 200 ||
		types[5].Name != sseserver.TypeName || types[5].DefaultConfig.SSE == nil || types[5].DefaultConfig.SSE.Path != "/events" ||
		types[6].Name != udpserver.TypeName || types[6].DefaultConfig.UDP == nil || !types[6].DefaultConfig.UDP.Echo ||
		types[7].Name != grpcmock.TypeName || types[7].DefaultConfig.GRPCMock == nil || !types[7].DefaultConfig.GRPCMock.Reflection ||
		types[8].Name != sioserver.TypeName || types[8].DefaultConfig.SocketIO == nil || types[8].DefaultConfig.SocketIO.Path != "/socket.io/" ||
		types[9].Name != graphqlmock.TypeName || types[9].DefaultConfig.GraphQLMock == nil || types[9].DefaultConfig.GraphQLMock.Path != "/graphql" {
		t.Fatalf("Types = %+v, %v", types, err)
	}

	created, err := svc.Create(t.Context(), CreateInputDTO{Type: echo.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if created.State != "stopped" || created.Addresses == nil || len(created.Addresses) != 0 || created.Error != nil || created.Name != "Echo HTTP" {
		t.Errorf("created = %+v", created)
	}

	running, err := svc.Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.State != "running" || running.BoundPort == 0 || running.Scope != "loopback" || running.StartedAt == "" || len(running.Addresses) == 0 {
		t.Fatalf("running = %+v", running)
	}

	// Without keep-alive the connection always closes, so the traffic has
	// a known end before the counts below are checked.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(running.BoundPort)) + "/hello?x=1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	rec.waitTraffic(t, "started,connection_open,request,response,connection_close")

	list, err := svc.List(t.Context())
	if err != nil || len(list) != 1 || list[0].Events != 5 {
		t.Errorf("List = %+v, %v", list, err)
	}
	recent, err := svc.Traffic(t.Context(), created.ID)
	if err != nil || len(recent) != 5 || recent[2].HTTP == nil || recent[2].HTTP.Target != "/hello?x=1" || recent[2].Direction != "in" {
		t.Errorf("Traffic = %+v, %v", recent, err)
	}
	if err := svc.ClearTraffic(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	if recent, _ := svc.Traffic(t.Context(), created.ID); recent == nil || len(recent) != 0 {
		t.Errorf("Traffic after clear = %#v, want []", recent)
	}

	stopped, err := svc.Stop(t.Context(), created.ID)
	if err != nil || stopped.State != "stopped" || stopped.BoundPort != 0 {
		t.Fatalf("Stop = %+v, %v", stopped, err)
	}
	rec.waitTraffic(t, "started,connection_open,request,response,connection_close,stopped")
	if got := strings.Join(rec.states(created.ID), ","); got != "stopped,starting,running,stopping,stopped" {
		t.Errorf("states = %s", got)
	}

	if err := svc.Remove(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	last := rec.changed[len(rec.changed)-1]
	rec.mu.Unlock()
	if last.Servers == nil || len(last.Servers) != 0 {
		t.Errorf("last changed = %#v, want no servers", last)
	}
}

func TestStartErrors(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { taken.Close() })

	tests := []struct {
		name string
		in   CreateInputDTO
		code string
	}{
		{"port in use", CreateInputDTO{Type: echo.TypeName, Host: "127.0.0.1", Port: taken.Addr().(*net.TCPAddr).Port}, listen.CodeAddressInUse},
		{"LAN not allowed", CreateInputDTO{Type: echo.TypeName, Host: "0.0.0.0"}, listen.CodeLANNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, err := svc.Create(t.Context(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Start(t.Context(), srv.ID); codeOf(err) != tt.code {
				t.Fatalf("Start err = %v, want %s", err, tt.code)
			}
			rec.mu.Lock()
			last := rec.changed[len(rec.changed)-1]
			rec.mu.Unlock()
			for _, s := range last.Servers {
				if s.ID == srv.ID && (s.State != "failed" || s.Error == nil || s.Error.Code != tt.code) {
					t.Errorf("server = %+v", s)
				}
			}
		})
	}

	if _, err := svc.Create(t.Context(), CreateInputDTO{Type: "nope"}); codeOf(err) != core.CodeUnknownType {
		t.Errorf("Create unknown type = %v", err)
	}
	if _, err := svc.Stop(t.Context(), "srv_missing"); codeOf(err) != core.CodeNotFound {
		t.Errorf("Stop unknown = %v", err)
	}
}

func TestShutdownStopsServers(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)
	var ports []int
	for range 2 {
		srv, err := svc.Create(t.Context(), CreateInputDTO{Type: echo.TypeName, AllowLAN: true, Host: "127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		running, err := svc.Start(t.Context(), srv.ID)
		if err != nil {
			t.Fatal(err)
		}
		ports = append(ports, running.BoundPort)
	}
	if err := svc.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	for _, p := range ports {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), time.Second); err == nil {
			c.Close()
			t.Errorf("port %d still accepts after shutdown", p)
		}
	}
	list, _ := svc.List(t.Context())
	for _, s := range list {
		if s.State != "stopped" {
			t.Errorf("%s is %s", s.ID, s.State)
		}
	}
	// The traffic was flushed before shutdown returned.
	if got := rec.kinds(); strings.Count(got, "stopped") != 2 {
		t.Errorf("traffic = %s", got)
	}
	if _, err := svc.Create(t.Context(), CreateInputDTO{Type: echo.TypeName}); codeOf(err) != core.CodeShuttingDown {
		t.Errorf("Create after shutdown = %v", err)
	}
}

func TestDTOJSON(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 9, 16, 10, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	running := core.Server{
		ID:        "srv_0123456789abcdef",
		Spec:      core.Spec{Type: echo.TypeName, Name: "Echo HTTP", Host: "0.0.0.0", Port: 8080, AllowLAN: true},
		TypeLabel: "Echo HTTP",
		State:     core.StateRunning,
		Addrs:     []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:8080")},
		Scope:     listen.ScopeAllInterfaces,
		Warning:   &listen.Warning{Code: listen.WarnAllInterfacesExposed, Message: "Listening on all network interfaces (0.0.0.0:8080): any device that can reach this computer can connect, including on public Wi-Fi."},
		StartedAt: started,
		Events:    12,
	}
	failed := core.Server{
		ID:        "srv_fedcba9876543210",
		Spec:      core.Spec{Type: echo.TypeName, Name: "API mock", Host: "", Port: 3000},
		TypeLabel: "Echo HTTP",
		State:     core.StateFailed,
		StoppedAt: started.Add(time.Minute),
		Err: errs.New(errs.Network, "127.0.0.1:3000 is already in use.").
			WithCode(listen.CodeAddressInUse).WithHint(listen.HintAddressInUse).
			WithDetail("address", "127.0.0.1:3000").WithDetail("network", "tcp"),
	}
	request := core.Event{
		ServerID: running.ID, Seq: 3, Time: started.Add(1500 * time.Millisecond),
		Peer: "127.0.0.1:53122", ConnID: 1, Direction: core.In, Kind: core.KindRequest,
		Summary: "POST /users?id=1", Content: []byte(`{"name":"Ada"}`), Size: 14,
		HTTP: &core.HTTPInfo{Method: "POST", Target: "/users?id=1", Proto: "HTTP/1.1", Headers: []core.Header{
			{Name: "Host", Value: "127.0.0.1:8080"}, {Name: "Content-Type", Value: "application/json"},
		}},
	}
	binary := core.Event{
		ServerID: running.ID, Seq: 4, Time: started.Add(2 * time.Second),
		Peer: "127.0.0.1:53122", ConnID: 1, Direction: core.In, Kind: core.KindMessage,
		Summary: "4 bytes", Content: []byte{0xff, 0x00, 0x10, 0x20}, Size: 70000, Truncated: true, Binary: true,
	}
	system := core.Event{ServerID: running.ID, Seq: 1, Time: started, Direction: core.System, Kind: core.KindStarted, Summary: "Listening on 0.0.0.0:8080"}
	mockCfg := mockhttp.Config{Routes: []mockhttp.Route{
		{ID: "rt_user", Enabled: true, Method: "GET", Path: "/users/:id", Status: 200,
			Headers: []mockhttp.Header{{Enabled: true, Name: "X-Mock", Value: "yes"}},
			Body:    mockhttp.Body{Mode: "raw", Type: "json", Text: `{"id": 1}`},
			Delay:   mockhttp.Delay{Mode: "random", MinMs: 100, MaxMs: 250}},
		{ID: "rt_bad", Enabled: true, Method: "POST", Path: "/users/{id", Status: 201,
			Body: mockhttp.Body{Mode: "none"}, Delay: mockhttp.Delay{Mode: "none"}},
	}, StrictTrailingSlash: true}
	mock := core.Server{
		ID:        "srv_00000000000000aa",
		Spec:      core.Spec{Type: mockhttp.TypeName, Name: "Mock HTTP", Port: 3000, Config: mockCfg},
		TypeLabel: "Mock HTTP",
		State:     core.StateStopped,
	}
	mockResponse := core.Event{
		ServerID: mock.ID, Seq: 5, Time: started.Add(3 * time.Second),
		Peer: "127.0.0.1:53123", ConnID: 2, Direction: core.Out, Kind: core.KindResponse,
		Summary: "200 OK", Content: []byte(`{"id": 1}`), Size: 9,
		HTTP: &core.HTTPInfo{Method: "GET", Target: "/users/7", Proto: "HTTP/1.1", Status: 200,
			Headers: []core.Header{{Name: "Content-Type", Value: "application/json"}},
			Route:   &core.RouteMatch{ID: "rt_user", Method: "GET", Pattern: "/users/:id", Params: []core.Header{{Name: "id", Value: "7"}}},
			Delay:   180 * time.Millisecond},
	}
	wsServer := core.Server{
		ID: "srv_00000000000000bb",
		Spec: core.Spec{Type: wsserver.TypeName, Name: "WebSocket", Port: 9001, Config: wsserver.Config{Path: "/ws", Subprotocols: []string{"chat"}, Echo: true,
			Rules: []wsserver.Rule{{Enabled: true, Match: wsserver.MatchRegex, Pattern: `^get (\d+)$`, Reply: `{"id": $1}`, DelayMs: 100}}}},
		TypeLabel: "WebSocket",
		State:     core.StateStopped,
	}
	wsMessage := core.Event{
		ServerID: wsServer.ID, Seq: 6, Time: started.Add(4 * time.Second),
		Peer: "127.0.0.1:53124", ConnID: 3, Direction: core.In, Kind: core.KindMessage,
		Summary: "Text message from client 3", Content: []byte("hello"), Size: 5,
		WS: &core.WSInfo{ClientID: "3", Opcode: core.OpcodeText},
	}
	wsClose := core.Event{
		ServerID: wsServer.ID, Seq: 7, Time: started.Add(5 * time.Second),
		Peer: "127.0.0.1:53124", ConnID: 3, Direction: core.System, Kind: core.KindConnectionClose,
		Summary: "Disconnected by the server: 4000 “bye”",
		WS:      &core.WSInfo{ClientID: "3", CloseCode: 4000, CloseReason: "bye", InitiatedBy: "server"},
	}
	wsClient := wsserver.Client{
		ID: "3", ConnID: 3, Peer: "127.0.0.1:53124", Target: "/ws?room=1", Subprotocol: "chat",
		ConnectedAt: started, Origin: "null", UserAgent: "websocat/1.13",
		MessagesIn: 2, MessagesOut: 1, BytesIn: 10, BytesOut: 4,
	}

	tcpServer := core.Server{
		ID:        "srv_00000000000000cc",
		Spec:      core.Spec{Type: tcpserver.TypeName, Name: "TCP", Port: 7000},
		TypeLabel: "TCP",
		State:     core.StateStopped,
	}
	tcpIn := core.Event{
		ServerID: tcpServer.ID, Seq: 8, Time: started.Add(6 * time.Second),
		Peer: "127.0.0.1:53125", ConnID: 4, Direction: core.In, Kind: core.KindMessage,
		Summary: "5 bytes from connection 4", Content: []byte("ping\n"), Size: 5, TCP: &core.TCPInfo{},
	}
	tcpOut := core.Event{
		ServerID: tcpServer.ID, Seq: 9, Time: started.Add(7 * time.Second),
		Peer: "127.0.0.1:53125", ConnID: 4, Direction: core.Out, Kind: core.KindMessage,
		Summary: "4 bytes to connection 4", Content: []byte{0xde, 0xad, 0xbe, 0xef}, Size: 4, Binary: true, TCP: &core.TCPInfo{},
	}
	tcpClose := core.Event{
		ServerID: tcpServer.ID, Seq: 10, Time: started.Add(8 * time.Second),
		Peer: "127.0.0.1:53125", ConnID: 4, Direction: core.System, Kind: core.KindConnectionClose,
		Summary: "Closed by the client (5 bytes received, 4 bytes sent)",
		TCP:     &core.TCPInfo{InitiatedBy: "client", BytesIn: 5, BytesOut: 4},
	}
	tcpConn := tcpserver.Connection{
		ID: "4", ConnID: 4, Peer: "127.0.0.1:53125", Local: "127.0.0.1:7000", ConnectedAt: started,
		BytesIn: 5, BytesOut: 4, MessagesIn: 1, MessagesOut: 1,
	}

	hookServer := core.Server{
		ID: "srv_00000000000000dd",
		Spec: core.Spec{Type: webhook.TypeName, Name: "Webhook receiver", Port: 4000, Config: webhook.Config{
			Status: 202, Headers: []webhook.Header{{Enabled: true, Name: "Content-Type", Value: "application/json"}}, Body: `{"ok":true}`,
		}},
		TypeLabel: "Webhook receiver",
		State:     core.StateStopped,
	}
	hookRequest := core.Event{
		ServerID: hookServer.ID, Seq: 11, Time: started.Add(9 * time.Second),
		Peer: "127.0.0.1:53126", ConnID: 5, Direction: core.In, Kind: core.KindRequest,
		Summary: "POST /hooks/github?delivery=1", Content: []byte(`{"action":"opened"}`), Size: 19, Captured: true,
		HTTP: &core.HTTPInfo{Method: "POST", Target: "/hooks/github?delivery=1", Proto: "HTTP/1.1", Headers: []core.Header{
			{Name: "Host", Value: "127.0.0.1:4000"}, {Name: "Content-Type", Value: "application/json"},
		}},
	}
	capture := core.Capture{
		Seq: 11, Time: started.Add(9 * time.Second), Peer: "127.0.0.1:53126",
		Method: "POST", Target: "/hooks/github?delivery=1&tag=a%20b&flag", Proto: "HTTP/1.1", Host: "127.0.0.1:4000",
		Headers: []core.Header{{Name: "Content-Type", Value: "application/json"}, {Name: "X-Github-Event", Value: "issues"}},
		Body:    []byte(`{"action":"opened"}`), Size: 19,
	}

	sseServer := core.Server{
		ID: "srv_00000000000000f2",
		Spec: core.Spec{Type: sseserver.TypeName, Name: "SSE", Port: 8082, Config: sseserver.Config{
			Path: "/events", AllowOrigin: "*", KeepAliveSec: 15, RetryMs: 3000, AutoID: true,
			Timers: []sseserver.Timer{{Enabled: true, Event: "tick", Data: "{{$timestamp}}", IntervalMs: 1000}},
		}},
		TypeLabel: "SSE",
		State:     core.StateStopped,
	}
	sseResponse := core.Event{
		ServerID: sseServer.ID, Seq: 12, Time: started.Add(10 * time.Second),
		Peer: "127.0.0.1:53127", ConnID: 6, Direction: core.Out, Kind: core.KindResponse,
		Summary: "200 OK: subscriber 2 (Last-Event-ID “7”)",
		HTTP:    &core.HTTPInfo{Method: "GET", Target: "/events", Proto: "HTTP/1.1", Status: 200, Headers: []core.Header{{Name: "Content-Type", Value: "text/event-stream"}}},
		SSE:     &core.SSEInfo{SubscriberID: "2", LastEventID: "7"},
	}
	sseEvent := core.Event{
		ServerID: sseServer.ID, Seq: 13, Time: started.Add(11 * time.Second),
		Direction: core.Out, Kind: core.KindMessage, Summary: "Event “tick” id 8 to 2 subscribers (timer 1)",
		Content: []byte("1790000000"), Size: 10,
		SSE: &core.SSEInfo{Event: "tick", ID: "8", Recipients: 2, Source: "timer 1"},
	}
	sseSub := sseserver.Subscriber{
		ID: "2", ConnID: 6, Peer: "127.0.0.1:53127", Target: "/events", ConnectedAt: started, LastEventID: "7",
		Origin: "http://localhost:3000", UserAgent: "Mozilla/5.0", EventsSent: 3, BytesSent: 90,
	}
	udpServer := core.Server{
		ID:        "srv_00000000000000f3",
		Spec:      core.Spec{Type: udpserver.TypeName, Name: "UDP", Port: 9125, Config: udpserver.Config{Echo: true, FixedReply: true, Reply: "06", ReplyFormat: udpserver.FormatHex}},
		TypeLabel: "UDP",
		State:     core.StateStopped,
	}
	udpIn := core.Event{
		ServerID: udpServer.ID, Seq: 14, Time: started.Add(12 * time.Second),
		Peer: "127.0.0.1:53128", Direction: core.In, Kind: core.KindMessage, Summary: "5 bytes from 127.0.0.1:53128",
		Content: []byte("hello"), Size: 5, UDP: &core.UDPInfo{Local: "127.0.0.1:9125"},
	}
	udpOut := core.Event{
		ServerID: udpServer.ID, Seq: 15, Time: started.Add(12 * time.Second),
		Peer: "127.0.0.1:53128", Direction: core.Out, Kind: core.KindMessage, Summary: "1 byte to 127.0.0.1:53128 (fixed)",
		Content: []byte{0x06}, Size: 1, UDP: &core.UDPInfo{Local: "127.0.0.1:9125", Reply: "fixed"},
	}
	udpPeer := udpserver.Peer{
		Addr: "127.0.0.1:53128", Local: "127.0.0.1:9125", FirstSeen: started, LastSeen: started.Add(12 * time.Second),
		DatagramsIn: 1, DatagramsOut: 2, BytesIn: 5, BytesOut: 6,
	}

	grpcServer := core.Server{
		ID: "srv_00000000000000f4",
		Spec: core.Spec{Type: grpcmock.TypeName, Name: "gRPC mock", Port: 50051, Config: grpcmock.Config{
			Files: []string{"protos/shop/v1/orders.proto"}, ImportPaths: []string{"protos"}, Reflection: true,
			Methods: []grpcmock.Method{{
				Service: "shop.v1.Orders", Method: "GetOrder", Messages: []string{`{"id": "o-1"}`},
				Status: 5, StatusMessage: "gone", Headers: []grpcmock.Metadata{{Enabled: true, Name: "x-mock", Value: "1"}},
				Trailers: []grpcmock.Metadata{}, DelayMs: 250, Bidi: grpcmock.BidiEach,
			}},
		}},
		TypeLabel: "gRPC mock",
		State:     core.StateStopped,
	}
	grpcIn := core.Event{
		ServerID: grpcServer.ID, Seq: 16, Time: started.Add(13 * time.Second),
		Peer: "127.0.0.1:53129", ConnID: 7, Direction: core.In, Kind: core.KindMessage,
		Summary: "Request message 1 of call 1 (shop.v1.GetOrderRequest, 5 bytes)", Content: []byte("{\n  \"id\": \"o-1\"\n}"), Size: 17,
		GRPC: &core.GRPCInfo{Call: 1, Method: "/shop.v1.Orders/GetOrder", Kind: "unary", Type: "shop.v1.GetOrderRequest"},
	}
	grpcEnd := core.Event{
		ServerID: grpcServer.ID, Seq: 17, Time: started.Add(13 * time.Second),
		Peer: "127.0.0.1:53129", ConnID: 7, Direction: core.Out, Kind: core.KindResponse,
		Summary: "NOT_FOUND: gone (call 1, 1 message received, 0 messages sent)",
		HTTP:    &core.HTTPInfo{Method: "POST", Target: "/shop.v1.Orders/GetOrder", Proto: "HTTP/2.0", Status: 200, Headers: []core.Header{{Name: "Content-Type", Value: "application/grpc"}, {Name: "Grpc-Status", Value: "5"}}},
		GRPC:    &core.GRPCInfo{Call: 1, Method: "/shop.v1.Orders/GetOrder", Kind: "unary", Code: 5, CodeName: "NOT_FOUND", Message: "gone", Trailers: []core.Header{{Name: "x-trace", Value: "t1"}}},
	}

	sioServer := core.Server{
		ID: "srv_00000000000000f5",
		Spec: core.Spec{Type: sioserver.TypeName, Name: "Socket.IO", Port: 3000, Config: sioserver.Config{
			Path: "/socket.io/", Namespaces: []string{"/admin"}, Auth: `{"token":"abc"}`, AllowOrigin: "*",
			PingIntervalMs: 25000, PingTimeoutMs: 20000, MaxPayload: 1000000, Echo: true, AckMode: sioserver.AckEcho,
			Rules: []sioserver.Rule{{Enabled: true, Match: sioserver.MatchExact, Pattern: "ping", Ack: `["pong"]`, Emit: "pinged", EmitArgs: `{"at": {{$timestamp}}}`, DelayMs: 100}},
		}},
		TypeLabel: "Socket.IO",
		State:     core.StateStopped,
	}
	sioIn := core.Event{
		ServerID: sioServer.ID, Seq: 18, Time: started.Add(14 * time.Second),
		Peer: "127.0.0.1:53130", ConnID: 8, Direction: core.In, Kind: core.KindMessage,
		Summary: "Event “upload” from socket 2/admin (ack 4)", Content: []byte(`["f.bin",{"$base64":"AAE="}]`), Size: 60,
		SIO: &core.SIOInfo{ClientID: "2", Session: "Z3cq0WmDdBdnFAXfAAAB", Transport: "websocket", SocketID: "2/admin", Namespace: "/admin",
			Packet: "BINARY_EVENT", Event: "upload", AckID: 4, HasAck: true, Attachments: 1},
	}
	sioAck := core.Event{
		ServerID: sioServer.ID, Seq: 19, Time: started.Add(15 * time.Second),
		Peer: "127.0.0.1:53130", ConnID: 8, Direction: core.In, Kind: core.KindMessage,
		Summary: "Ack 0 from socket 2/admin (answers “ask” after 12 ms)", Content: []byte(`["yes"]`), Size: 16,
		SIO: &core.SIOInfo{ClientID: "2", Session: "Z3cq0WmDdBdnFAXfAAAB", Transport: "websocket", SocketID: "2/admin", Namespace: "/admin",
			Packet: "ACK", Event: "ask", HasAck: true, RTT: 12 * time.Millisecond},
	}
	sioClose := core.Event{
		ServerID: sioServer.ID, Seq: 20, Time: started.Add(16 * time.Second),
		Peer: "127.0.0.1:53130", ConnID: 8, Direction: core.System, Kind: core.KindConnectionClose,
		Summary: "Client 2 closed: ping timeout (sockets on /admin)",
		SIO:     &core.SIOInfo{ClientID: "2", Session: "Z3cq0WmDdBdnFAXfAAAB", Transport: "polling", Reason: sioserver.ReasonPingTimeout},
	}
	sioSocket := sioserver.Socket{
		ID: "2/admin", ClientID: "2", SID: "fjKYg1eVmvKzwVSiAAAD", Namespace: "/admin", Session: "Z3cq0WmDdBdnFAXfAAAB", Transport: "websocket",
		Peer: "127.0.0.1:53130", Target: "/socket.io/?EIO=4&transport=polling&t=PXz1", UserAgent: "node", Auth: `{"token":"abc"}`,
		Rooms: []string{"lobby"}, ConnectedAt: started, EventsIn: 3, EventsOut: 2,
	}

	gqlServer := core.Server{
		ID: "srv_00000000000000f6",
		Spec: core.Spec{Type: graphqlmock.TypeName, Name: "GraphQL mock", Port: 4000, Config: graphqlmock.Config{
			Path: "/graphql", SchemaFile: "schema/catalog.graphql", Introspection: true, ListLength: 3, NullPercent: 10, Seed: 7, AllowOrigin: "*",
			Overrides: []graphqlmock.Override{
				{Enabled: true, Target: "Query.product", Args: `{"id": "1"}`, Mode: graphqlmock.ModeValue, Value: `{"name": "Teapot"}`},
				{Enabled: false, Target: "Query.product", Mode: graphqlmock.ModeError, Error: "Not found.", Code: "NOT_FOUND"},
			},
		}},
		TypeLabel: "GraphQL mock",
		State:     core.StateStopped,
	}
	gqlIn := core.Event{
		ServerID: gqlServer.ID, Seq: 21, Time: started.Add(17 * time.Second),
		Peer: "127.0.0.1:53131", ConnID: 9, Direction: core.In, Kind: core.KindRequest,
		Summary: "query Product (POST /graphql)", Content: []byte(`{"query":"query Product($id: ID!) { product(id: $id) { name } }","variables":{"id":"1"}}`), Size: 86,
		HTTP:    &core.HTTPInfo{Method: "POST", Target: "/graphql", Proto: "HTTP/1.1", Headers: []core.Header{{Name: "Content-Type", Value: "application/json"}}},
		GraphQL: &core.GraphQLInfo{Request: 1, Operation: "Product", OperationType: "query", Variables: `{"id":"1"}`},
	}
	gqlOut := core.Event{
		ServerID: gqlServer.ID, Seq: 22, Time: started.Add(17 * time.Second),
		Peer: "127.0.0.1:53131", ConnID: 9, Direction: core.Out, Kind: core.KindResponse,
		Summary: "200 OK: data", Content: []byte(`{"data":{"product":{"name":"Teapot"}}}`), Size: 38,
		HTTP:    &core.HTTPInfo{Method: "POST", Target: "/graphql", Proto: "HTTP/1.1", Status: 200, Headers: []core.Header{{Name: "Content-Type", Value: "application/graphql-response+json; charset=utf-8"}}},
		GraphQL: &core.GraphQLInfo{Request: 1, Operation: "Product", OperationType: "query", Variables: `{"id":"1"}`, HasData: true},
	}

	tests := []struct {
		name string
		v    any
	}{
		{"server_graphql_mock", toServerDTO(gqlServer)},
		{"traffic_graphql_mock", TrafficEventDTO{Events: toTrafficDTOs([]core.Event{gqlIn, gqlOut})}},
		{"graphql_mock_description", toGraphQLMockDescriptionDTO(describeExample(t))},
		{"server_socketio", toServerDTO(sioServer)},
		{"traffic_socketio", TrafficEventDTO{Events: toTrafficDTOs([]core.Event{sioIn, sioAck, sioClose})}},
		{"sockets_event", SocketsEventDTO{ServerID: sioServer.ID, Sockets: toSIOSocketDTOs([]sioserver.Socket{sioSocket})}},
		{"server_grpc_mock", toServerDTO(grpcServer)},
		{"traffic_grpc_mock", TrafficEventDTO{Events: toTrafficDTOs([]core.Event{grpcIn, grpcEnd})}},
		{"server_running", toServerDTO(running)},
		{"server_failed", toServerDTO(failed)},
		{"changed_event", ChangedEventDTO{Servers: toServerDTOs([]core.Server{running, failed})}},
		{"changed_event_empty", ChangedEventDTO{Servers: toServerDTOs(nil)}},
		{"traffic_event", TrafficEventDTO{Events: toTrafficDTOs([]core.Event{system, request, binary})}},
		{"types", toTypeDTOs([]core.Type{echo.New(), mockhttp.New(mockhttp.Options{}), wsserver.New(wsserver.Options{}), tcpserver.New(tcpserver.Options{}), webhook.New(webhook.Options{}), sseserver.New(sseserver.Options{}), udpserver.New(udpserver.Options{}), grpcmock.New(grpcmock.Options{}), sioserver.New(sioserver.Options{}), graphqlmock.New(graphqlmock.Options{})})},
		{"server_sse", toServerDTO(sseServer)},
		{"traffic_sse", TrafficEventDTO{Events: toTrafficDTOs([]core.Event{sseResponse, sseEvent})}},
		{"subscribers_event", SubscribersEventDTO{ServerID: sseServer.ID, Subscribers: toSSESubscriberDTOs([]sseserver.Subscriber{sseSub})}},
		{"server_udp", toServerDTO(udpServer)},
		{"traffic_udp", TrafficEventDTO{Events: toTrafficDTOs([]core.Event{udpIn, udpOut})}},
		{"peers_event", PeersEventDTO{ServerID: udpServer.ID, Peers: toUDPPeerDTOs([]udpserver.Peer{udpPeer})}},
		{"server_mock", toServerDTO(mock)},
		{"traffic_mock", TrafficEventDTO{Events: toTrafficDTOs([]core.Event{mockResponse})}},
		{"server_websocket", toServerDTO(wsServer)},
		{"server_saved", toServerDTO(core.Server{
			ID: "srv_00000000000000ee",
			Spec: core.Spec{Type: tcpserver.TypeName, Name: "TCP", Port: 7001,
				Owner: core.Owner{Workspace: "ws_7c1e4b9a2f3d5e60", Folder: "fld_2c7e5a91d0b3f846", Label: "Chat"}},
			TypeLabel: "TCP",
			State:     core.StateStopped,
		})},
		{"project_servers", ProjectServersDTO{WorkspaceID: "ws_7c1e4b9a2f3d5e60", Problems: []FolderServerProblemDTO{{
			FolderID: "fld_93d0b7a2e5c14f68", FolderName: "Mocks",
			Error: apperr.ToDTO(errs.New(errs.InvalidArgument, `"mqtt" is not a kind of server PostSonde can run.`).WithCode(core.CodeUnknownType)),
		}}}},
		{"traffic_websocket", TrafficEventDTO{Events: toTrafficDTOs([]core.Event{wsMessage, wsClose})}},
		{"clients_event", ClientsEventDTO{ServerID: wsServer.ID, Clients: toWSClientDTOs([]wsserver.Client{wsClient})}},
		{"server_tcp", toServerDTO(tcpServer)},
		{"traffic_tcp", TrafficEventDTO{Events: toTrafficDTOs([]core.Event{tcpIn, tcpOut, tcpClose})}},
		{"connections_event", ConnectionsEventDTO{ServerID: tcpServer.ID, Connections: toTCPConnectionDTOs([]tcpserver.Connection{tcpConn})}},
		{"server_webhook", toServerDTO(hookServer)},
		{"traffic_webhook", TrafficEventDTO{Events: toTrafficDTOs([]core.Event{hookRequest})}},
		{"captures_event", CapturesEventDTO{ServerID: hookServer.ID, Captures: toCaptureSummaryDTOs([]core.Capture{capture})}},
		{"capture", toCaptureDTO(hookServer.ID, capture)},
		{"server_tls", toServerDTO(core.Server{
			ID: "srv_00000000000000f1",
			Spec: core.Spec{Type: echo.TypeName, Name: "Echo HTTP", Host: "localhost", Port: 8443,
				TLS: &core.TLS{Certificate: core.CertFiles, CertFile: "/certs/dev.pem", KeyFile: "/certs/dev-key.pem"}},
			TypeLabel: "Echo HTTP",
			State:     core.StateRunning,
			Addrs:     []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:8443")},
			Scope:     "loopback",
			StartedAt: started,
			Cert: &x509.Certificate{
				Raw:     []byte("certificate"),
				Subject: pkix.Name{CommonName: "localhost"}, Issuer: pkix.Name{CommonName: "Dev CA"},
				NotBefore: started.Add(-time.Hour), NotAfter: started.Add(365 * 24 * time.Hour),
				DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
			},
		})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(tt.v, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "dto/"+tt.name, append(got, '\n'))
		})
	}
}
