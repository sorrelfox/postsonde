package netlog

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// echoServer accepts connections on loopback and echoes what it reads
// until the test ends.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

// closedPort returns a loopback address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

type events struct {
	mu    sync.Mutex
	conns []Conn
}

func (e *events) add(c Conn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.conns = append(e.conns, c)
}

func (e *events) states() []State {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]State, len(e.conns))
	for i, c := range e.conns {
		out[i] = c.State
	}
	return out
}

func newDialer(l *Log) netx.Dialer {
	return netx.Chain(netx.NewDirect(netx.Options{}), l.Middleware(), netx.Guarded(netx.RequireUserOp(nil)))
}

func userCtx(t *testing.T) context.Context {
	return netx.WithUserOp(t.Context(), netx.UserOp{Kind: "tcp.connect", ID: "c1"})
}

func TestRecordsAConnection(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(time.Second)
		return clock
	}
	l := New(Options{Now: now})
	var ev events
	unsub := l.Subscribe(ev.add)
	addr := echoServer(t)

	conn, err := newDialer(l).DialContext(userCtx(t), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Active(); got != 1 {
		t.Errorf("Active = %d while open", got)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close() // a second Close is not recorded again

	conns := l.Conns()
	if len(conns) != 1 {
		t.Fatalf("Conns = %+v", conns)
	}
	c := conns[0]
	if c.ID != 1 || c.Op != (netx.UserOp{Kind: "tcp.connect", ID: "c1"}) || c.Network != "tcp" || c.Address != addr {
		t.Errorf("conn = %+v", c)
	}
	if c.State != Closed || c.RemoteAddr != addr || c.LocalAddr == "" {
		t.Errorf("state/addresses = %+v", c)
	}
	if c.BytesIn != 5 || c.BytesOut != 5 {
		t.Errorf("bytes in/out = %d/%d, want 5/5", c.BytesIn, c.BytesOut)
	}
	if !c.Started.Before(c.Connected) || !c.Connected.Before(c.Ended) {
		t.Errorf("times = %v %v %v", c.Started, c.Connected, c.Ended)
	}
	if got := l.Active(); got != 0 {
		t.Errorf("Active = %d after close", got)
	}
	if got := ev.states(); len(got) != 3 || got[0] != Connecting || got[1] != Open || got[2] != Closed {
		t.Errorf("events = %v", got)
	}

	unsub()
	conn, err = newDialer(l).DialContext(userCtx(t), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if n := len(ev.states()); n != 3 {
		t.Errorf("%d events after unsubscribing", n-3)
	}
}

func TestRecordsRefusedAndFailedDials(t *testing.T) {
	t.Parallel()
	l := New(Options{})
	d := newDialer(l)

	// No user operation: the guard refuses it; nothing is dialed.
	if _, err := d.DialContext(t.Context(), "tcp", echoServer(t)); err == nil {
		t.Fatal("unmarked dial succeeded")
	}
	if _, err := d.DialContext(userCtx(t), "tcp", closedPort(t)); err == nil {
		t.Fatal("dial to a closed port succeeded")
	}

	conns := l.Conns()
	if len(conns) != 2 {
		t.Fatalf("Conns = %+v", conns)
	}
	if c := conns[0]; c.State != Refused || c.ErrCode != netx.CodeNotUserInitiated || c.Op.Kind != "" || c.Err == "" || !c.Connected.IsZero() || c.Ended.IsZero() {
		t.Errorf("refused = %+v", c)
	}
	if c := conns[1]; c.State != Failed || c.ErrCode != netx.CodeConnectionRefused || c.Op.Kind != "tcp.connect" || c.RemoteAddr != "" {
		t.Errorf("failed = %+v", c)
	}
	if got := l.Active(); got != 0 {
		t.Errorf("Active = %d", got)
	}
}

func TestSizeLimitKeepsActive(t *testing.T) {
	t.Parallel()
	l := New(Options{Size: 3})
	d := newDialer(l)
	addr := echoServer(t)

	open, err := d.DialContext(userCtx(t), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	for range 4 {
		_, _ = d.DialContext(t.Context(), "tcp", addr) // refused
	}
	conns := l.Conns()
	if len(conns) != 3 || conns[0].ID != 1 || conns[1].ID != 4 || conns[2].ID != 5 {
		t.Fatalf("Conns = %+v", conns)
	}
	if active := l.ActiveConns(); len(active) != 1 || active[0].ID != 1 {
		t.Errorf("ActiveConns = %+v", active)
	}
	if l.Capacity() != 3 {
		t.Errorf("Capacity = %d", l.Capacity())
	}

	l.Clear()
	if conns := l.Conns(); len(conns) != 1 || conns[0].ID != 1 {
		t.Errorf("after Clear: %+v", conns)
	}
	if c, ok := l.Get(1); !ok || c.State != Open {
		t.Errorf("Get(1) = %+v, %v", c, ok)
	}
	if _, ok := l.Get(2); ok {
		t.Error("Get(2) found a dropped connection")
	}
	open.Close()
	if l.Active() != 0 {
		t.Errorf("Active = %d", l.Active())
	}
}

func TestKeepsHalfClose(t *testing.T) {
	t.Parallel()
	l := New(Options{})
	conn, err := newDialer(l).DialContext(userCtx(t), "tcp", echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cw, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("the wrapped TCP connection lost CloseWrite")
	}
	if _, err := conn.Write([]byte("bye")); err != nil {
		t.Fatal(err)
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(conn) // the echo server sees EOF, echoes and closes
	if err != nil || string(got) != "bye" {
		t.Errorf("read %q, %v", got, err)
	}
	if u, ok := conn.(interface{ Unwrap() net.Conn }); !ok || u.Unwrap() == nil {
		t.Error("no Unwrap")
	}

	// A connection without half-close is not given one.
	p1, p2 := net.Pipe()
	defer p2.Close()
	if _, ok := wrap(p1, l, &record{}).(interface{ CloseWrite() error }); ok {
		t.Error("net.Pipe gained CloseWrite")
	}
	p1.Close()
}

func TestConcurrentDials(t *testing.T) {
	t.Parallel()
	l := New(Options{Size: 50})
	l.Subscribe(func(Conn) {})
	d := newDialer(l)
	addr := echoServer(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			c, err := d.DialContext(userCtx(t), "tcp", addr)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = c.Write([]byte("x"))
			_ = l.Conns()
			c.Close()
		})
	}
	wg.Wait()
	if l.Active() != 0 || len(l.Conns()) != 20 {
		t.Errorf("Active = %d, %d conns", l.Active(), len(l.Conns()))
	}
}

func TestNilLogMiddleware(t *testing.T) {
	t.Parallel()
	var l *Log
	if l.Middleware() != nil {
		t.Error("nil Log gave a middleware")
	}
}
