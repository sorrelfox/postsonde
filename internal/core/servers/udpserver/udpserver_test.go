package udpserver_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
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

func (e *events) messages(dir servers.Direction) []servers.Event {
	var out []servers.Event
	for _, ev := range e.all() {
		if ev.Kind == servers.KindMessage && ev.Direction == dir {
			out = append(out, ev)
		}
	}
	return out
}

// waitMessages polls until n messages in direction dir were recorded.
func (e *events) waitMessages(t *testing.T, dir servers.Direction, n int) []servers.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := e.messages(dir)
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("saw %d %s messages, want %d", len(got), dir, n)
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

func start(t *testing.T, cfg any) *fixture {
	t.Helper()
	rec := &events{}
	m := servers.NewManager(servers.Options{
		Types:     []servers.Type{udpserver.New(udpserver.Options{})},
		OnTraffic: rec.add,
		Logger:    slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: udpserver.TypeName, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	s, err = m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Scope != "loopback" || s.Addrs[0].Addr().String() != "127.0.0.1" {
		t.Errorf("bound %v (%s)", s.Addrs, s.Scope)
	}
	return &fixture{m: m, id: s.ID, port: s.Port(), rec: rec}
}

func (f *fixture) dial(t *testing.T, host string) *net.UDPConn {
	t.Helper()
	raddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(f.port)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (f *fixture) hub(t *testing.T) udpserver.Hub {
	t.Helper()
	inst, err := f.m.Running(f.id)
	if err != nil {
		t.Fatal(err)
	}
	return inst.(udpserver.Hub)
}

func read(t *testing.T, c *net.UDPConn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 70000)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf[:n])
}

// TestEcho: a client made with net.DialUDP gets every datagram back, the
// server logs it in and out with the peer and its size, and binary bytes
// stay bytes.
func TestEcho(t *testing.T) {
	t.Parallel()
	f := start(t, nil) // the default configuration echoes
	c := f.dial(t, "127.0.0.1")
	for _, msg := range []string{"hello", "\x00\xff\x10", strings.Repeat("x", 9000)} {
		if _, err := c.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		if got := read(t, c); got != msg {
			t.Errorf("reply = %d bytes %q…, want %d bytes", len(got), got[:min(len(got), 10)], len(msg))
		}
	}
	in := f.rec.waitMessages(t, servers.In, 3)
	out := f.rec.waitMessages(t, servers.Out, 3)
	peer := c.LocalAddr().String()
	if in[0].Peer != peer || in[0].Summary != "5 bytes from "+peer || string(in[0].Content) != "hello" || in[0].UDP == nil || in[0].UDP.Local == "" {
		t.Errorf("in = %+v", in[0])
	}
	if !in[1].Binary || in[1].Size != 3 {
		t.Errorf("binary datagram = %+v", in[1])
	}
	if out[0].Summary != "5 bytes to "+peer+" (echo)" || out[0].UDP.Reply != udpserver.ReplyEcho {
		t.Errorf("out = %+v", out[0])
	}
	if in[2].Size != 9000 {
		t.Errorf("large datagram size = %d", in[2].Size)
	}
	// The count goes up once the write returned, which may be after the
	// client read the reply.
	var peers []udpserver.Peer
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if peers = f.hub(t).Peers(); len(peers) == 1 && peers[0].DatagramsOut == 3 {
			break
		}
	}
	if len(peers) != 1 || peers[0].Addr != peer || peers[0].DatagramsIn != 3 || peers[0].DatagramsOut != 3 || peers[0].BytesIn != 9008 {
		t.Errorf("peers = %+v", peers)
	}
}

// TestFixedReply: the fixed reply follows the echo when both are on, and a
// hex reply is sent as its bytes; a change applies to the next datagram.
func TestFixedReply(t *testing.T) {
	t.Parallel()
	f := start(t, udpserver.Config{Echo: true, FixedReply: true, Reply: "ack"})
	c := f.dial(t, "127.0.0.1")
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c); got != "ping" {
		t.Errorf("first reply = %q", got)
	}
	if got := read(t, c); got != "ack" {
		t.Errorf("second reply = %q", got)
	}
	if _, err := f.m.UpdateConfig(f.id, udpserver.Config{FixedReply: true, Reply: "DE AD be ef", ReplyFormat: udpserver.FormatHex}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c); got != "\xde\xad\xbe\xef" {
		t.Errorf("hex reply = %q", got)
	}
	out := f.rec.waitMessages(t, servers.Out, 3)
	if out[1].UDP.Reply != udpserver.ReplyFixed || out[2].Summary != "4 bytes to "+c.LocalAddr().String()+" (fixed)" {
		t.Errorf("out = %+v", out)
	}
}

// TestSendToPeer: the app sends to a recent peer; an unknown peer is
// udpserver.peer_not_found; without replies nothing is sent back.
func TestSendToPeer(t *testing.T) {
	t.Parallel()
	f := start(t, udpserver.Config{})
	c := f.dial(t, "127.0.0.1")
	if _, err := c.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	f.rec.waitMessages(t, servers.In, 1)
	hub := f.hub(t)
	if err := hub.Send(c.LocalAddr().String(), []byte("from the app")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c); got != "from the app" {
		t.Errorf("got %q", got)
	}
	out := f.rec.messages(servers.Out)
	if len(out) != 1 || out[0].UDP.Reply != "" || out[0].Summary != "12 bytes to "+c.LocalAddr().String() {
		t.Errorf("out = %+v", out)
	}
	err := hub.Send("127.0.0.1:1", []byte("x"))
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != udpserver.CodePeerNotFound {
		t.Errorf("unknown peer: %v", err)
	}
	err = hub.Send(c.LocalAddr().String(), make([]byte, udpserver.MaxDatagram+1))
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != udpserver.CodeTooLarge {
		t.Errorf("too large: %v", err)
	}
}

// TestIPv6Loopback: localhost binds ::1 too, on the same port, so a client
// that resolves localhost to ::1 is answered.
func TestIPv6Loopback(t *testing.T) {
	t.Parallel()
	f := start(t, nil)
	s, _ := f.m.Get(f.id)
	if len(s.Addrs) < 2 {
		t.Skip("no IPv6 loopback here")
	}
	c := f.dial(t, "::1")
	if _, err := c.Write([]byte("six")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c); got != "six" {
		t.Errorf("got %q", got)
	}
}

// TestStop: after Stop nothing more is logged and the port is free.
func TestStop(t *testing.T) {
	t.Parallel()
	f := start(t, nil)
	c := f.dial(t, "127.0.0.1")
	if _, err := c.Write([]byte("one")); err != nil {
		t.Fatal(err)
	}
	read(t, c)
	if _, err := f.m.Stop(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	all := f.rec.all()
	if last := all[len(all)-1]; last.Kind != servers.KindStopped {
		t.Errorf("last event = %+v", last)
	}
	if _, err := f.m.Start(t.Context(), f.id); err != nil {
		t.Fatalf("restart: %v", err)
	}
}

// TestConfig: bad configurations and TLS are refused.
func TestConfig(t *testing.T) {
	t.Parallel()
	m := servers.NewManager(servers.Options{Types: []servers.Type{udpserver.New(udpserver.Options{})}, Logger: slog.New(slog.DiscardHandler)})
	for _, tt := range []struct {
		cfg   udpserver.Config
		field string
	}{
		{udpserver.Config{FixedReply: true, Reply: "zz", ReplyFormat: udpserver.FormatHex}, "reply"},
		{udpserver.Config{ReplyFormat: "base64"}, "replyFormat"},
		{udpserver.Config{Reply: "\xff"}, "reply"},
		{udpserver.Config{Reply: strings.Repeat("a", udpserver.MaxDatagram+1)}, "reply"},
	} {
		_, err := m.Add(servers.Spec{Type: udpserver.TypeName, Config: tt.cfg})
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != udpserver.CodeInvalidConfig || e.Details["field"] != tt.field {
			t.Errorf("%+v: %v", tt.cfg, err)
		}
	}
	_, err := m.Add(servers.Spec{Type: udpserver.TypeName, TLS: &servers.TLS{Certificate: servers.CertGenerated}})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != servers.CodeTLSNotSupported {
		t.Errorf("TLS: %v", err)
	}
	s, err := m.Add(servers.Spec{Type: udpserver.TypeName})
	if err != nil || s.Spec.Config != udpserver.DefaultConfig() || s.Spec.Name != "UDP" {
		t.Errorf("default = %+v, %v", s.Spec, err)
	}
}
