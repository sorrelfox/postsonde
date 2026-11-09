package wsclient

import (
	"bytes"
	"strconv"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// Clock supplies the keepalive timers (WS-09). It has the method set of
// autosave.Clock, so tests can pass internal/testutil/fakeclock.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine after d and returns a
	// function that stops the timer, as time.AfterFunc does.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

// SystemClock returns the real clock.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// PingPong is a ping or pong frame sent or received, passed to
// Options.OnPingPong (WS-09).
type PingPong struct {
	Direction wsframe.Direction
	// Opcode is wsframe.OpPing or wsframe.OpPong.
	Opcode wsframe.Opcode
	// Payload is a copy of the frame's payload (at most 125 bytes).
	Payload []byte
	// At is when the ping was about to be written, or the frame was sent
	// or read.
	At time.Time
	// Keepalive marks a ping sent by the keepalive timer
	// (Options.PingInterval) and the pong that answered it.
	Keepalive bool
	// Answered is set on a received pong whose payload equals that of a
	// ping this client sent and that no pong answered yet; RTT is then the
	// time since that ping.
	Answered bool
	RTT      time.Duration
}

// pendingPing is a ping sent and not answered yet.
type pendingPing struct {
	payload   []byte
	at        time.Time
	keepalive bool
}

// maxPendingPings bounds the pings kept for matching pongs; older ones
// are forgotten when a server never answers.
const maxPendingPings = 64

// keepaliveTimeoutError is the error a connection fails with when no pong
// arrived within Options.PongTimeout of a keepalive ping.
func keepaliveTimeoutError(timeout time.Duration) error {
	return errs.New(errs.Timeout, "The server did not answer a keepalive ping within "+formatDuration(timeout)+"; the connection was closed.").
		WithCode(CodePongTimeout).
		WithHint("The server or the network may be down. Raise the pong timeout if the server is just slow.").
		WithDetail("timeoutMs", strconv.FormatInt(timeout.Milliseconds(), 10))
}

func formatDuration(d time.Duration) string {
	if d%time.Second == 0 {
		return strconv.FormatInt(int64(d/time.Second), 10) + " s"
	}
	return strconv.FormatInt(d.Milliseconds(), 10) + " ms"
}

// ping records a ping as pending, reports it and writes it. It is reported
// before it is written, so its pong can never be reported first.
func (c *Client) ping(conn *wsframe.Conn, payload []byte, keepalive bool) error {
	if len(payload) > wsframe.MaxControlPayload {
		return conn.Ping(payload) // the wsframe.invalid_argument error
	}
	p := bytes.Clone(payload)
	if p == nil {
		p = []byte{}
	}
	c.pingMu.Lock()
	at := c.now()
	if len(c.pending) == maxPendingPings {
		c.pending = c.pending[1:]
	}
	c.pending = append(c.pending, pendingPing{payload: p, at: at, keepalive: keepalive})
	if c.o.OnPingPong != nil {
		c.o.OnPingPong(PingPong{Direction: wsframe.Sent, Opcode: wsframe.OpPing, Payload: bytes.Clone(p), At: at, Keepalive: keepalive})
	}
	c.pingMu.Unlock()
	return conn.Ping(payload)
}

// onPingPongFrame reports control frames other than the pings this client
// writes (reported by ping), matches pongs to pings, and clears the
// keepalive timeout on any pong.
func (c *Client) onPingPongFrame(dir wsframe.Direction, f wsframe.Frame) {
	switch {
	case f.Opcode == wsframe.OpPong && dir == wsframe.Received:
		c.mu.Lock()
		if c.kaTimeout != nil {
			c.kaTimeout()
			c.kaTimeout = nil
		}
		c.kaGen++
		c.mu.Unlock()

		c.pingMu.Lock()
		defer c.pingMu.Unlock()
		pp := PingPong{Direction: dir, Opcode: wsframe.OpPong, Payload: bytes.Clone(f.Payload), At: c.now()}
		if pp.Payload == nil {
			pp.Payload = []byte{}
		}
		for i, p := range c.pending {
			if bytes.Equal(p.payload, f.Payload) {
				pp.Answered, pp.RTT, pp.Keepalive = true, pp.At.Sub(p.at), p.keepalive
				// A pong answers the pings before it too (a server may
				// answer only the most recent one, RFC 6455 §5.5.3).
				c.pending = c.pending[i+1:]
				break
			}
		}
		if c.o.OnPingPong != nil {
			c.o.OnPingPong(pp)
		}
	case f.Opcode == wsframe.OpPing && dir == wsframe.Received,
		f.Opcode == wsframe.OpPong && dir == wsframe.Sent:
		if c.o.OnPingPong != nil {
			p := bytes.Clone(f.Payload)
			if p == nil {
				p = []byte{}
			}
			c.o.OnPingPong(PingPong{Direction: dir, Opcode: f.Opcode, Payload: p, At: c.now()})
		}
	}
}

// startKeepalive schedules the first keepalive ping, if PingInterval is set.
func (c *Client) startKeepalive() {
	if c.o.PingInterval <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateOpen {
		c.kaTick = c.clock.AfterFunc(c.o.PingInterval, c.keepaliveTick)
	}
}

// keepaliveTick sends a keepalive ping and schedules the next one. The
// pong timeout starts with the first unanswered ping: pings sent while one
// is outstanding do not push the deadline back.
func (c *Client) keepaliveTick() {
	c.mu.Lock()
	if c.state != StateOpen {
		c.mu.Unlock()
		return
	}
	conn := c.conn
	if c.o.PongTimeout > 0 && c.kaTimeout == nil {
		gen := c.kaGen
		c.kaTimeout = c.clock.AfterFunc(c.o.PongTimeout, func() { c.pongTimedOut(gen) })
	}
	c.kaTick = c.clock.AfterFunc(c.o.PingInterval, c.keepaliveTick)
	c.mu.Unlock()
	_ = c.ping(conn, nil, true) // a failed write fails the connection
}

// pongTimedOut closes the transport when no pong arrived since the
// keepalive ping of generation gen; the connection fails with
// CodePongTimeout.
func (c *Client) pongTimedOut(gen uint64) {
	c.mu.Lock()
	if gen != c.kaGen || c.state != StateOpen || c.netConn == nil {
		c.mu.Unlock()
		return
	}
	c.kaTimeout = nil
	c.closeErr = keepaliveTimeoutError(c.o.PongTimeout)
	c.aborted = true
	nc := c.netConn
	c.mu.Unlock()
	_ = nc.Close()
}

// stopKeepalive stops the keepalive timers.
func (c *Client) stopKeepalive() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kaTick != nil {
		c.kaTick()
		c.kaTick = nil
	}
	if c.kaTimeout != nil {
		c.kaTimeout()
		c.kaTimeout = nil
	}
	c.kaGen++
}
