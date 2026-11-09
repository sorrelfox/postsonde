package tcpserver_test

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	"github.com/sorrelfox/postsonde/internal/core/servers/tcpserver"
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

func (e *events) kinds() string {
	var parts []string
	for _, ev := range e.all() {
		parts = append(parts, string(ev.Direction)+":"+string(ev.Kind))
	}
	return strings.Join(parts, ",")
}

type fixture struct {
	m    *servers.Manager
	id   string
	port int
	rec  *events
}

func start(t *testing.T, opts servers.Options) *fixture {
	t.Helper()
	rec := &events{}
	if opts.Types == nil {
		opts.Types = []servers.Type{tcpserver.New(tcpserver.Options{})}
	}
	opts.OnTraffic = rec.add
	opts.StopTimeout = 2 * time.Second
	opts.Logger = slog.New(slog.DiscardHandler)
	m := servers.NewManager(opts)
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: tcpserver.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if s.Spec.Name != "TCP" || s.Spec.Config != nil {
		t.Errorf("defaults = %+v", s.Spec)
	}
	s, err = m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Scope != "loopback" {
		t.Errorf("scope = %q, want loopback by default", s.Scope)
	}
	return &fixture{m: m, id: s.ID, port: s.Port(), rec: rec}
}

func (f *fixture) hub(t *testing.T) tcpserver.Hub {
	t.Helper()
	inst, err := f.m.Running(f.id)
	if err != nil {
		t.Fatal(err)
	}
	return inst.(tcpserver.Hub)
}

func (f *fixture) dial(t *testing.T) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.port)), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// waitConns polls until the server lists n connections.
func (f *fixture) waitConns(t *testing.T, n int) []tcpserver.Connection {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := f.hub(t).Connections()
		if len(got) == n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("server lists %d connections, want %d", len(got), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// readN reads exactly n bytes from c.
func readN(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf
}

// received joins the content of the in messages of connection id.
func received(rec *events, connID uint64) []byte {
	var out []byte
	for _, ev := range rec.ofKind(servers.KindMessage) {
		if ev.Direction == servers.In && ev.ConnID == connID {
			out = append(out, ev.Content...)
		}
	}
	return out
}

func waitReceived(t *testing.T, rec *events, connID uint64, want []byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Equal(received(rec, connID), want) {
		if time.Now().After(deadline) {
			t.Fatalf("received %q, want %q", received(rec, connID), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

func TestExchangeTextAndBytes(t *testing.T) {
	t.Parallel()
	f := start(t, servers.Options{})
	c := f.dial(t)

	list := f.waitConns(t, 1)
	conn := list[0]
	if conn.ID != "1" || conn.ConnID != 1 || conn.Peer != c.LocalAddr().String() || conn.Local != c.RemoteAddr().String() || conn.ConnectedAt.IsZero() {
		t.Errorf("connection = %+v", conn)
	}

	// Client to server: text, then bytes that are not UTF-8.
	if _, err := c.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	waitReceived(t, f.rec, 1, []byte("hello\n"))
	binary := []byte{0xde, 0xad, 0xbe, 0xef, 0x00}
	if _, err := c.Write(binary); err != nil {
		t.Fatal(err)
	}
	waitReceived(t, f.rec, 1, append([]byte("hello\n"), binary...))

	// Server to client: text and bytes.
	hub := f.hub(t)
	if err := hub.Send("1", []byte("hi there\r\n")); err != nil {
		t.Fatal(err)
	}
	if got := readN(t, c, 10); string(got) != "hi there\r\n" {
		t.Errorf("client got %q", got)
	}
	if err := hub.Send("1", binary); err != nil {
		t.Fatal(err)
	}
	if got := readN(t, c, len(binary)); !bytes.Equal(got, binary) {
		t.Errorf("client got % x", got)
	}

	conn = f.waitConns(t, 1)[0]
	if conn.BytesIn != 11 || conn.BytesOut != 15 || conn.MessagesOut != 2 || conn.MessagesIn < 1 {
		t.Errorf("counts = %+v", conn)
	}

	for _, ev := range f.rec.ofKind(servers.KindMessage) {
		if ev.TCP == nil || ev.Peer != conn.Peer || ev.ConnID != 1 {
			t.Errorf("message event = %+v", ev)
		}
		if bytes.Equal(ev.Content, binary) && !ev.Binary {
			t.Errorf("bytes not marked binary: %+v", ev)
		}
	}
	out := []servers.Event{}
	for _, ev := range f.rec.ofKind(servers.KindMessage) {
		if ev.Direction == servers.Out {
			out = append(out, ev)
		}
	}
	if len(out) != 2 || out[0].Summary != "10 bytes to connection 1" || string(out[0].Content) != "hi there\r\n" || out[0].Binary || !out[1].Binary {
		t.Errorf("out events = %+v", out)
	}
	if open := f.rec.ofKind(servers.KindConnectionOpen); len(open) != 1 || open[0].Summary != "Connection 1 opened" || open[0].TCP == nil {
		t.Errorf("open events = %+v", open)
	}

	// The client closes.
	_ = c.Close()
	closeEv := f.rec.waitFor(t, servers.KindConnectionClose, 1)[0]
	if closeEv.TCP == nil || closeEv.TCP.InitiatedBy != tcpserver.InitiatedByClient || closeEv.TCP.BytesIn != 11 || closeEv.TCP.BytesOut != 15 {
		t.Errorf("close event = %+v %+v", closeEv, closeEv.TCP)
	}
	if closeEv.Summary != "Closed by the client (11 bytes received, 15 bytes sent)" {
		t.Errorf("close summary = %q", closeEv.Summary)
	}
	f.waitConns(t, 0)
	if err := hub.Send("1", []byte("x")); codeOf(err) != tcpserver.CodeConnectionNotFound {
		t.Errorf("send after close = %v", err)
	}
}

func TestServerClosesConnection(t *testing.T) {
	t.Parallel()
	f := start(t, servers.Options{})
	a := f.dial(t)
	f.waitConns(t, 1)
	b := f.dial(t)
	list := f.waitConns(t, 2)
	if list[0].ID != "1" || list[1].ID != "2" {
		t.Fatalf("connections = %+v", list)
	}

	hub := f.hub(t)
	if err := hub.Close("1"); err != nil {
		t.Fatal(err)
	}
	// Close returns once the close is logged.
	closes := f.rec.ofKind(servers.KindConnectionClose)
	if len(closes) != 1 || closes[0].ConnID != 1 || closes[0].TCP.InitiatedBy != tcpserver.InitiatedByServer || !strings.HasPrefix(closes[0].Summary, "Closed by the server") {
		t.Errorf("close events = %+v", closes)
	}
	_ = a.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := a.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("client a read = %d, %v; want EOF", n, err)
	}
	if list := f.hub(t).Connections(); len(list) != 1 || list[0].ID != "2" {
		t.Errorf("after close = %+v", list)
	}
	if err := hub.Close("1"); codeOf(err) != tcpserver.CodeConnectionNotFound {
		t.Errorf("close again = %v", err)
	}
	if err := hub.Send("2", nil); codeOf(err) != tcpserver.CodeEmptyMessage {
		t.Errorf("empty send = %v", err)
	}
	if err := hub.Send("9", []byte("x")); codeOf(err) != tcpserver.CodeConnectionNotFound {
		t.Errorf("send to unknown = %v", err)
	}

	// b still works.
	if err := hub.Send("2", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if got := readN(t, b, 2); string(got) != "ok" {
		t.Errorf("client b got %q", got)
	}
}

func TestStopClosesConnections(t *testing.T) {
	t.Parallel()
	f := start(t, servers.Options{})
	a := f.dial(t)
	f.dial(t)
	f.waitConns(t, 2)
	if _, err := a.Write([]byte("bye")); err != nil {
		t.Fatal(err)
	}
	waitReceived(t, f.rec, 1, []byte("bye"))

	if _, err := f.m.Stop(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	want := "system:started,system:connection_open,system:connection_open,in:message,system:connection_close,system:connection_close,system:stopped"
	if got := f.rec.kinds(); got != want {
		t.Errorf("events = %s\nwant %s", got, want)
	}
	for _, ev := range f.rec.ofKind(servers.KindConnectionClose) {
		if ev.TCP.InitiatedBy != tcpserver.InitiatedByServer || !strings.HasPrefix(ev.Summary, "Closed: the server is stopping") {
			t.Errorf("close event = %+v", ev)
		}
	}
	_ = a.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := a.Read(make([]byte, 1)); err == nil {
		t.Error("client still connected after stop")
	}
	if _, err := f.m.Running(f.id); codeOf(err) != servers.CodeNotRunning {
		t.Errorf("Running after stop = %v", err)
	}

	// It starts again, with connection IDs from 1.
	s, err := f.m.Start(t.Context(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	f.port = s.Port()
	f.dial(t)
	if list := f.waitConns(t, 1); list[0].ID != "1" {
		t.Errorf("after restart = %+v", list)
	}
}

func TestReadsFitTheContentLimit(t *testing.T) {
	t.Parallel()
	f := start(t, servers.Options{ContentLimit: 16})
	c := f.dial(t)
	f.waitConns(t, 1)
	payload := bytes.Repeat([]byte("0123456789"), 10)
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	waitReceived(t, f.rec, 1, payload)
	for _, ev := range f.rec.ofKind(servers.KindMessage) {
		if len(ev.Content) > 16 || ev.Truncated {
			t.Errorf("event of %d bytes, truncated %v", len(ev.Content), ev.Truncated)
		}
	}
}

func TestManyConnectionsInParallel(t *testing.T) {
	t.Parallel()
	f := start(t, servers.Options{})
	const n = 8
	conns := make([]net.Conn, n)
	for i := range conns {
		conns[i] = f.dial(t)
	}
	list := f.waitConns(t, n)
	hub := f.hub(t)
	var wg sync.WaitGroup
	for i, c := range conns {
		wg.Go(func() {
			msg := []byte("client " + strconv.Itoa(i) + "\n")
			_, _ = c.Write(msg)
		})
	}
	for _, conn := range list {
		wg.Go(func() { _ = hub.Send(conn.ID, []byte("to "+conn.ID)) })
	}
	wg.Wait()
	for _, conn := range list {
		// Each client finds its own reply.
		var c net.Conn
		for _, cc := range conns {
			if cc.LocalAddr().String() == conn.Peer {
				c = cc
			}
		}
		want := "to " + conn.ID
		if got := readN(t, c, len(want)); string(got) != want {
			t.Errorf("connection %s got %q", conn.ID, got)
		}
	}
	f.rec.waitFor(t, servers.KindMessage, 2*n)
}
