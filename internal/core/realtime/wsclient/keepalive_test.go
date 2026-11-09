package wsclient

import (
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/testutil/fakeclock"
)

// pingPongs records OnPingPong calls.
type pingPongs struct {
	mu  sync.Mutex
	all []PingPong
	ch  chan PingPong
}

func newPingPongs() *pingPongs { return &pingPongs{ch: make(chan PingPong, 100)} }

func (p *pingPongs) add(pp PingPong) {
	p.mu.Lock()
	p.all = append(p.all, pp)
	p.mu.Unlock()
	p.ch <- pp
}

func (p *pingPongs) next(t *testing.T) PingPong {
	t.Helper()
	select {
	case pp := <-p.ch:
		return pp
	case <-time.After(5 * time.Second):
		t.Fatal("no ping or pong")
	}
	return PingPong{}
}

// silentServer upgrades and reads without answering pings (auto pong off)
// until the connection ends.
func silentServer(t *testing.T) (*http.ServeMux, chan net.Conn) {
	conns := make(chan net.Conn, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		nc, br := hijack(t, w)
		resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n"
		if _, err := nc.Write([]byte(resp)); err != nil {
			t.Errorf("write 101: %v", err)
		}
		conns <- nc
		sc := wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleServer, Reader: br, DisableAutoPong: true})
		for {
			if _, _, err := sc.ReadMessage(); err != nil {
				_ = nc.Close()
				return
			}
		}
	})
	return mux, conns
}

func TestPingManualRTT(t *testing.T) {
	result := make(chan error, 1)
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", nil)
		echo(sc, nc, result)
	})
	o, _ := options(srv, "/")
	pps := newPingPongs()
	o.OnPingPong = pps.add
	c := connect(t, o)

	if err := c.Ping([]byte("hello")); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	sent := pps.next(t)
	if sent.Direction != wsframe.Sent || sent.Opcode != wsframe.OpPing || string(sent.Payload) != "hello" || sent.Keepalive {
		t.Fatalf("sent = %+v", sent)
	}
	pong := pps.next(t)
	if pong.Direction != wsframe.Received || pong.Opcode != wsframe.OpPong || string(pong.Payload) != "hello" || !pong.Answered || pong.Keepalive {
		t.Fatalf("pong = %+v", pong)
	}
	if pong.RTT < 0 || pong.RTT != pong.At.Sub(sent.At) {
		t.Errorf("RTT = %v, want %v", pong.RTT, pong.At.Sub(sent.At))
	}

	// Empty payload, and a payload over 125 bytes refused.
	if err := c.Ping(nil); err != nil {
		t.Fatalf("Ping(nil): %v", err)
	}
	if pp := pps.next(t); len(pp.Payload) != 0 || pp.Payload == nil {
		t.Errorf("empty ping payload = %#v", pp.Payload)
	}
	if pp := pps.next(t); !pp.Answered {
		t.Errorf("empty pong = %+v", pp)
	}
	if err := c.Ping(make([]byte, 126)); errCode(err) != wsframe.CodeInvalidArgument {
		t.Errorf("Ping(126 bytes) = %v", err)
	}
}

func TestPingFromServerAndUnsolicitedPong(t *testing.T) {
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", nil)
		defer nc.Close()
		_ = sc.Ping([]byte("srv"))
		_ = sc.Pong([]byte("beat"))
		_, _, _ = sc.ReadMessage()
	})
	o, _ := options(srv, "/")
	pps := newPingPongs()
	o.OnPingPong = pps.add
	connect(t, o)

	ping := pps.next(t)
	if ping.Direction != wsframe.Received || ping.Opcode != wsframe.OpPing || string(ping.Payload) != "srv" {
		t.Errorf("ping = %+v", ping)
	}
	auto := pps.next(t)
	if auto.Direction != wsframe.Sent || auto.Opcode != wsframe.OpPong || string(auto.Payload) != "srv" {
		t.Errorf("automatic pong = %+v", auto)
	}
	pong := pps.next(t)
	if pong.Opcode != wsframe.OpPong || pong.Direction != wsframe.Received || pong.Answered || string(pong.Payload) != "beat" {
		t.Errorf("unsolicited pong = %+v", pong)
	}
}

func TestKeepaliveAnswered(t *testing.T) {
	result := make(chan error, 1)
	srv := newServer(t, false, func(w http.ResponseWriter, r *http.Request) {
		sc, nc := upgrade(t, w, r, "", nil)
		echo(sc, nc, result)
	})
	clock := fakeclock.New(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	o, _ := options(srv, "/")
	pps := newPingPongs()
	o.OnPingPong = pps.add
	o.PingInterval = 10 * time.Second
	o.PongTimeout = 3 * time.Second
	o.Clock = clock
	o.Now = clock.Now
	c := connect(t, o)

	for i := range 3 {
		if clock.Pending() != 1 {
			t.Fatalf("round %d: %d timers pending, want the next tick", i, clock.Pending())
		}
		clock.Advance(10 * time.Second)
		ping := pps.next(t)
		if ping.Opcode != wsframe.OpPing || !ping.Keepalive || len(ping.Payload) != 0 {
			t.Fatalf("round %d: ping = %+v", i, ping)
		}
		pong := pps.next(t)
		if pong.Opcode != wsframe.OpPong || !pong.Keepalive || !pong.Answered || pong.RTT != 0 {
			t.Fatalf("round %d: pong = %+v", i, pong)
		}
		// The pong cleared the deadline; only the next tick is left.
		waitFor(t, func() bool { return clock.Pending() == 1 })
	}
	clock.Advance(5 * time.Second) // past where the deadline would have been
	if c.State() != StateOpen {
		t.Fatalf("state %s after answered pings", c.State())
	}
	if err := c.Close(t.Context(), wsframe.CloseNormalClosure, ""); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if clock.Pending() != 0 {
		t.Errorf("%d timers left after close", clock.Pending())
	}
}

func TestKeepalivePongTimeout(t *testing.T) {
	mux, conns := silentServer(t)
	srv := newServer(t, false, mux.ServeHTTP)
	clock := fakeclock.New(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	o, st := options(srv, "/")
	pps := newPingPongs()
	o.OnPingPong = pps.add
	o.PingInterval = 2 * time.Second
	o.PongTimeout = 5 * time.Second
	o.Clock = clock
	c := connect(t, o)
	<-conns

	clock.Advance(2 * time.Second) // first ping, deadline at 7 s
	if pp := pps.next(t); pp.Opcode != wsframe.OpPing || !pp.Keepalive {
		t.Fatalf("ping = %+v", pp)
	}
	clock.Advance(4 * time.Second) // pings at 4 s and 6 s keep the 7 s deadline
	pps.next(t)
	pps.next(t)
	if c.State() != StateOpen {
		t.Fatalf("closed before the deadline: %s", c.State())
	}
	clock.Advance(1 * time.Second)
	waitDone(t, c)
	if got := errCode(c.Err()); got != CodePongTimeout {
		t.Fatalf("Err = %v, want %s", c.Err(), CodePongTimeout)
	}
	info := c.CloseInfo()
	if info.Code != wsframe.CloseAbnormalClosure || info.InitiatedBy != InitiatorLocal || info.WasClean {
		t.Errorf("CloseInfo = %+v", info)
	}
	if got := st.names(); got != "connecting,open,closed" {
		t.Errorf("states = %s", got)
	}
	if clock.Pending() != 0 {
		t.Errorf("%d timers left after the timeout", clock.Pending())
	}
}

func TestKeepaliveWithoutTimeoutNeverGivesUp(t *testing.T) {
	mux, conns := silentServer(t)
	srv := newServer(t, false, mux.ServeHTTP)
	clock := fakeclock.New(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	o, _ := options(srv, "/")
	pps := newPingPongs()
	o.OnPingPong = pps.add
	o.PingInterval = time.Second
	o.Clock = clock
	c := connect(t, o)
	<-conns
	for range 5 {
		clock.Advance(time.Second)
		pps.next(t)
	}
	if c.State() != StateOpen || clock.Pending() != 1 {
		t.Errorf("state %s, %d timers", c.State(), clock.Pending())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}
