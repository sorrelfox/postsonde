package udp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
	"github.com/sorrelfox/postsonde/internal/core/realtime/udpclient"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
	"github.com/sorrelfox/postsonde/internal/core/template"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// events records what the service emits.
type events struct {
	mu     sync.Mutex
	states []StateEventDTO
	data   []DataEventDTO
	seqs   map[string][]int64
	wake   chan struct{}
}

func newEvents() *events { return &events{seqs: map[string][]int64{}, wake: make(chan struct{}, 1)} }

func (e *events) emit(name string, data any) {
	e.mu.Lock()
	switch d := data.(type) {
	case StateEventDTO:
		if name != EventState {
			panic(name)
		}
		e.states = append(e.states, d)
		e.seqs[d.ConnID] = append(e.seqs[d.ConnID], d.Seq)
	case DataEventDTO:
		if name != EventData || len(d.Datagrams) == 0 {
			panic(name)
		}
		e.data = append(e.data, d)
		e.seqs[d.ConnID] = append(e.seqs[d.ConnID], d.Seq)
	default:
		panic(name)
	}
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *events) waitFor(t *testing.T, what string, cond func(e *events) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		e.mu.Lock()
		ok := cond(e)
		e.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-e.wake:
		case <-deadline:
			e.mu.Lock()
			defer e.mu.Unlock()
			t.Fatalf("timed out waiting for %s; states %+v", what, e.states)
		}
	}
}

// statesOf lists the states of socket id; e.mu must be held.
func (e *events) statesOf(id string) string {
	var s []string
	for _, ev := range e.states {
		if ev.ConnID == id {
			s = append(s, ev.State)
		}
	}
	return strings.Join(s, ",")
}

// datagramsOf returns the datagrams of socket id in direction dir, decoded;
// e.mu must be held.
func (e *events) datagramsOf(t *testing.T, id, dir string) []DatagramDTO {
	var out []DatagramDTO
	for _, ev := range e.data {
		if ev.ConnID != id {
			continue
		}
		for _, d := range ev.Datagrams {
			if d.Direction != dir {
				continue
			}
			b, err := base64.StdEncoding.DecodeString(d.Data)
			if err != nil || d.At == "" {
				t.Errorf("datagram %+v: %v", d, err)
			}
			d.Data = string(b)
			out = append(out, d)
		}
	}
	return out
}

func (e *events) state(id, state string) *StateEventDTO {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.states {
		if e.states[i].ConnID == id && e.states[i].State == state {
			ev := e.states[i]
			return &ev
		}
	}
	return nil
}

func echoPort(t *testing.T) string {
	t.Helper()
	m := servers.NewManager(servers.Options{
		Types:  []servers.Type{udpserver.New(udpserver.Options{})},
		Logger: slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: udpserver.TypeName, Host: "127.0.0.1", Config: udpserver.Config{Echo: true}})
	if err != nil {
		t.Fatal(err)
	}
	if s, err = m.Start(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(s.Port())
}

// guarded is the app's dialer shape: a direct dialer behind the
// user-action guard.
func guarded(d netx.Dialer) netx.Dialer {
	return netx.Chain(d, netx.Guarded(netx.RequireUserOp(slog.New(slog.DiscardHandler))))
}

func newService(t *testing.T, d netx.Dialer, opts ...Option) (*UDPService, *events) {
	t.Helper()
	ev := newEvents()
	s := New(guarded(d), ev.emit, slog.New(slog.DiscardHandler), append([]Option{withInterval(5 * time.Millisecond)}, opts...)...)
	t.Cleanup(func() { _ = s.ServiceShutdown() })
	return s, ev
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func waitOpen(t *testing.T, ev *events, id string) {
	t.Helper()
	ev.waitFor(t, "open", func(e *events) bool { return strings.Contains(e.statesOf(id), "open") })
}

// TestEcho (UDP-01): text with variables, hex and Base64 datagrams
// round-trip with the in-repo UDP server through events, in order, with
// the server as their peer; closing reports the totals.
func TestEcho(t *testing.T) {
	t.Parallel()
	port := echoPort(t)
	scopes := func(_ context.Context, id string) (template.Chain, error) {
		if id != "req_udp" {
			return nil, errors.New("unexpected request " + id)
		}
		return template.Chain{template.Vars("environment", map[string]string{"host": "127.0.0.1", "name": "world"})}, nil
	}
	s, ev := newService(t, netx.NewDirect(netx.Options{}), WithScopes(scopes))
	conn, err := s.Open(t.Context(), OpenInputDTO{RequestID: "req_udp", Address: "{{host}}:" + port, Connected: true})
	if err != nil {
		t.Fatal(err)
	}
	if conn.Address != "127.0.0.1:"+port || !strings.HasPrefix(conn.ConnID, "udp_") {
		t.Errorf("conn = %+v", conn)
	}
	waitOpen(t, ev, conn.ConnID)
	for _, in := range []SendInputDTO{
		{ConnID: conn.ConnID, Mode: "text", Text: "hello {{name}}"},
		{ConnID: conn.ConnID, Mode: "hex", Text: "DE AD"},
		{ConnID: conn.ConnID, Mode: "base64", Text: "aGk="},
		{ConnID: conn.ConnID, Mode: "text"},
	} {
		if _, err := s.Send(t.Context(), in); err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	ev.waitFor(t, "4 replies", func(e *events) bool { return len(e.datagramsOf(t, conn.ConnID, "received")) == 4 })
	if err := s.Close(t.Context(), conn.ConnID); err != nil {
		t.Fatal(err)
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	got := ev.datagramsOf(t, conn.ConnID, "received")
	want := []string{"hello world", "\xde\xad", "hi", ""}
	for i, d := range got {
		if d.Data != want[i] || d.Size != len(want[i]) || d.Peer != "127.0.0.1:"+port {
			t.Errorf("reply %d = %+v", i, d)
		}
	}
	if sent := ev.datagramsOf(t, conn.ConnID, "sent"); len(sent) != 4 || sent[0].Data != "hello world" {
		t.Errorf("sent = %+v", sent)
	}
	if states := ev.statesOf(conn.ConnID); states != "opening,open,closing,closed" {
		t.Errorf("states = %s", states)
	}
	for i, seq := range ev.seqs[conn.ConnID] {
		if seq != int64(i+1) {
			t.Errorf("seqs = %v", ev.seqs[conn.ConnID])
			break
		}
	}
	var open, closed StateEventDTO
	for _, st := range ev.states {
		switch st.State {
		case "open":
			open = st
		case "closed":
			closed = st
		}
	}
	if !open.Connected || open.MaxPayload != netx.MaxUDPPayloadIPv4 || open.Via != "" || open.Proxy != nil || open.RemoteAddr != "127.0.0.1:"+port {
		t.Errorf("open = %+v", open)
	}
	if c := closed.Close; c == nil || c.InitiatedBy != "local" || c.DatagramsSent != 4 || c.DatagramsReceived != 4 || c.BytesSent != 15 || closed.Error != nil {
		t.Errorf("closed = %+v %+v", closed, closed.Close)
	}
}

// TestThroughSOCKS5 (SOCKS-07): through the in-process SOCKS5 proxy's
// UDP ASSOCIATE, with the name sent unresolved; the open state names the
// proxy and the hop.
func TestThroughSOCKS5(t *testing.T) {
	t.Parallel()
	port := echoPort(t)
	p := &netxtest.SOCKS5Server{Hosts: map[string]string{"echo.test": "127.0.0.1"}}
	socks, err := netx.NewSOCKS5(netx.SOCKS5Config{Address: p.Start(t), RemoteDNS: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, ev := newService(t, socks)
	conn, err := s.Open(t.Context(), OpenInputDTO{Address: "echo.test:" + port})
	if err != nil {
		t.Fatal(err)
	}
	waitOpen(t, ev, conn.ConnID)
	if _, err := s.Send(t.Context(), SendInputDTO{ConnID: conn.ConnID, Mode: "text", Text: "via proxy"}); err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "the reply", func(e *events) bool { return len(e.datagramsOf(t, conn.ConnID, "received")) == 1 })
	open := ev.state(conn.ConnID, "open")
	h := open.Proxy
	if open.Via != socks.Via() || h == nil || h.Command != "UDP ASSOCIATE" || h.RelayAddr == "" || !h.RemoteDNS || h.Reply == nil || *h.Reply != 0 ||
		h.Version != "SOCKS5" || open.Connected {
		t.Errorf("open = %+v, hop %+v", open, h)
	}
	ev.mu.Lock()
	if got := ev.datagramsOf(t, conn.ConnID, "received"); got[0].Data != "via proxy" || got[0].Peer != "127.0.0.1:"+port {
		t.Errorf("reply = %+v", got)
	}
	ev.mu.Unlock()
	if err := s.Close(t.Context(), conn.ConnID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.UDPAssociations() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the association outlived Close")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRefusedByProxy: a proxy without UDP support fails opening with its
// reply in the closed state, never sending direct.
func TestRefusedByProxy(t *testing.T) {
	t.Parallel()
	p := &netxtest.SOCKS5Server{NoUDP: true}
	socks, _ := netx.NewSOCKS5(netx.SOCKS5Config{Address: p.Start(t)}, nil)
	s, ev := newService(t, socks)
	conn, err := s.Open(t.Context(), OpenInputDTO{Address: "192.0.2.1:53"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "closed", func(e *events) bool { return strings.HasSuffix(e.statesOf(conn.ConnID), "closed") })
	closed := ev.state(conn.ConnID, "closed")
	if closed.Error == nil || closed.Error.Code != netx.CodeSOCKSCommandNotSupported || closed.Proxy == nil || closed.Proxy.Reply == nil || *closed.Proxy.Reply != 7 ||
		closed.Close.InitiatedBy != "none" {
		t.Errorf("closed = %+v, %+v", closed, closed.Error)
	}
}

// TestOpenInputErrors: input that cannot be used fails Open at once.
func TestOpenInputErrors(t *testing.T) {
	t.Parallel()
	s, ev := newService(t, netx.NewDirect(netx.Options{}))
	big := int64(70000)
	tests := []struct {
		in   OpenInputDTO
		code string
	}{
		{OpenInputDTO{Address: "no-port"}, udpclient.CodeInvalidAddress},
		{OpenInputDTO{Address: "{{missing}}:53"}, "template.unresolved"},
		{OpenInputDTO{Address: "127.0.0.1:53", IPVersion: "ipv5"}, CodeInvalidIPVersion},
		{OpenInputDTO{Address: "127.0.0.1:53", IPVersion: "ipv6"}, udpclient.CodeFamilyMismatch},
		{OpenInputDTO{Address: "127.0.0.1:53", MaxDatagramBytes: &big}, udpclient.CodeInvalidOptions},
	}
	for _, tt := range tests {
		if _, err := s.Open(t.Context(), tt.in); codeOf(err) != tt.code {
			t.Errorf("%+v: %v, want %s", tt.in, err, tt.code)
		}
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if len(ev.states) != 0 {
		t.Errorf("events for refused input: %+v", ev.states)
	}
}

// TestSendErrors: modes, bad hex and Base64, unknown sockets, too large.
func TestSendErrors(t *testing.T) {
	t.Parallel()
	port := echoPort(t)
	s, ev := newService(t, netx.NewDirect(netx.Options{}))
	max := int64(8)
	conn, err := s.Open(t.Context(), OpenInputDTO{Address: "127.0.0.1:" + port, MaxDatagramBytes: &max})
	if err != nil {
		t.Fatal(err)
	}
	waitOpen(t, ev, conn.ConnID)
	tests := []struct {
		in   SendInputDTO
		code string
	}{
		{SendInputDTO{ConnID: conn.ConnID, Mode: "json", Text: "{}"}, CodeInvalidMode},
		{SendInputDTO{ConnID: conn.ConnID, Mode: "hex", Text: "zz"}, "hexbytes.invalid_char"},
		{SendInputDTO{ConnID: conn.ConnID, Mode: "base64", Text: "a$"}, "ws.invalid_base64"},
		{SendInputDTO{ConnID: conn.ConnID, Mode: "text", Text: "123456789"}, udpclient.CodeDatagramTooLarge},
		{SendInputDTO{ConnID: "udp_nope", Mode: "text", Text: "x"}, CodeNotFound},
		{SendInputDTO{ConnID: "udp_nope", Mode: "hex", Text: "zz"}, "hexbytes.invalid_char"},
	}
	for _, tt := range tests {
		if _, err := s.Send(t.Context(), tt.in); codeOf(err) != tt.code {
			t.Errorf("%+v: %v, want %s", tt.in, err, tt.code)
		}
	}
}

// TestCloseWhileOpening and CloseAll: a socket still opening is stopped,
// closed by this side, and the workspace hook closes every socket.
func TestCloseWhileOpening(t *testing.T) {
	t.Parallel()
	block := netx.DialerFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	s, ev := newService(t, block)
	conn, err := s.Open(t.Context(), OpenInputDTO{Address: "192.0.2.1:53"})
	if err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "opening", func(e *events) bool { return e.statesOf(conn.ConnID) == "opening" })
	if err := s.Close(t.Context(), conn.ConnID); err != nil {
		t.Fatal(err)
	}
	if closed := ev.state(conn.ConnID, "closed"); closed == nil || closed.Close.InitiatedBy != "local" {
		t.Errorf("closed = %+v", closed)
	}

	port := echoPort(t)
	s2, ev2 := newService(t, netx.NewDirect(netx.Options{}))
	var ids []string
	for range 3 {
		c, err := s2.Open(t.Context(), OpenInputDTO{Address: "127.0.0.1:" + port})
		if err != nil {
			t.Fatal(err)
		}
		waitOpen(t, ev2, c.ConnID)
		ids = append(ids, c.ConnID)
	}
	OnWorkspaceChanged(s2)
	for _, id := range ids {
		if ev2.state(id, "closed") == nil {
			t.Errorf("%s not closed", id)
		}
	}
	if n, _ := s2.CloseAll(t.Context()); n != 0 {
		t.Errorf("CloseAll after the hook closed %d", n)
	}
}

// TestUserOpGuard: the socket's dial is marked as the user's open, so the
// guard of the app's dialer lets it through (no "dial refused").
func TestUserOpGuard(t *testing.T) {
	t.Parallel()
	port := echoPort(t)
	s, ev := newService(t, netx.NewDirect(netx.Options{}))
	conn, err := s.Open(t.Context(), OpenInputDTO{Address: "127.0.0.1:" + port})
	if err != nil {
		t.Fatal(err)
	}
	waitOpen(t, ev, conn.ConnID)
	if closed := ev.state(conn.ConnID, "closed"); closed != nil {
		t.Errorf("closed at once: %+v", closed.Error)
	}
}

func TestDTOs(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 10, 0, 0, 123_000_000, time.UTC)
	info := &udpclient.CloseInfo{
		InitiatedBy: udpclient.InitiatorLocal, BytesSent: 4, BytesReceived: 1402, DatagramsSent: 1, DatagramsReceived: 2, Notes: 1,
		OpenedAt: at, ClosedAt: at.Add(1500 * time.Millisecond),
	}
	hop := &netx.ProxyHop{Scheme: "socks5h", Version: "SOCKS5", Proxy: "proxy.lan:1080", ProxyAddr: "10.0.0.1:1080", Target: "dns.test:53",
		Auth: netx.AuthUserPass, RemoteDNS: true, Replied: true, Reply: 0, ReplyText: "succeeded", Command: netx.CommandUDPAssociate,
		RelayAddr: "10.0.0.1:40000", Handshake: 1500 * time.Microsecond}
	c := &connection{id: "udp_0123456789abcdef", requestID: "req_dns", address: "dns.test:53"}
	var got []any
	c.emit = func(_ string, data any) { got = append(got, data) }
	note := toNoteDTO(udpclient.Note{At: at, Err: errs.New(errs.Protocol, "A fragmented datagram from the SOCKS relay was dropped.").WithCode(netx.CodeSOCKSUDPFragment)})
	c.flush([]item{
		{state: &StateEventDTO{ConnID: c.id, RequestID: c.requestID, Address: c.address, State: "open", At: formatTime(at), LocalAddr: "127.0.0.1:50000", RemoteAddr: "10.0.0.1:40000",
			Via: "socks5h://proxy.lan:1080", Proxy: toProxyHopDTO(hop), MaxPayload: 65507 - 17}},
		{datagram: ptr(toDatagramDTO(udpclient.Datagram{Direction: udpclient.Sent, Data: []byte("ping"), Size: 4, Peer: "dns.test:53", At: at}))},
		{datagram: ptr(toDatagramDTO(udpclient.Datagram{Direction: udpclient.Received, Data: []byte{0, 0xff}, Size: 2, Peer: "10.0.0.53:53", At: at}))},
		{datagram: &note},
		{datagram: ptr(toDatagramDTO(udpclient.Datagram{Direction: udpclient.Received, Data: []byte("cut"), Size: 1400, Truncated: true, Peer: "10.0.0.53:53", At: at}))},
		{state: &StateEventDTO{ConnID: c.id, RequestID: c.requestID, Address: c.address, State: "closed", At: formatTime(info.ClosedAt), Close: toCloseInfoDTO(info)}},
	})
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/events", append(data, '\n'))
}

func ptr[T any](v T) *T { return &v }
