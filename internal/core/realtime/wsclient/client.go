package wsclient

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// State is the state of a Client's connection.
type State string

// States, in the order a connection goes through them. A connection that
// fails goes to StateClosed from any state, with StateChange.Err set.
const (
	StateIdle       State = "idle"       // created, Connect not called
	StateConnecting State = "connecting" // dialing, TLS, upgrade
	StateOpen       State = "open"       // messages flow
	StateClosing    State = "closing"    // a close frame was sent or received
	StateClosed     State = "closed"     // the transport is closed
)

// Initiator says which side started closing.
type Initiator string

const (
	// InitiatorNone: no close frame was exchanged and the connection was
	// not aborted locally (the handshake failed, the transport dropped).
	InitiatorNone Initiator = "none"
	// InitiatorLocal: this side sent the first close frame (Close, or a
	// protocol error found in what the server sent) or called Abort.
	InitiatorLocal Initiator = "local"
	// InitiatorRemote: the server sent the first close frame.
	InitiatorRemote Initiator = "remote"
)

// CloseFrame is the status code and reason of one close frame. Code is
// wsframe.CloseNoStatus (1005) for a close frame without payload.
type CloseFrame struct {
	Code   wsframe.StatusCode
	Reason string
}

// CloseInfo describes how a connection ended.
type CloseInfo struct {
	// Code and Reason are the server's close frame (1005 when it had no
	// status code), or 1006 (abnormal closure, never sent on the wire)
	// when no close frame arrived from the server.
	Code   wsframe.StatusCode
	Reason string
	// WasClean is true when close frames went both ways.
	WasClean bool
	// InitiatedBy says who started closing.
	InitiatedBy Initiator
	// Sent and Received are the close frames sent and received, nil if
	// none. For a close started by the server, Sent is the echo.
	Sent     *CloseFrame
	Received *CloseFrame
}

// StateChange is one state transition, passed to Options.OnState.
type StateChange struct {
	State State
	// At is when the change happened.
	At time.Time
	// Close is set for StateClosed.
	Close *CloseInfo
	// Err is set for StateClosed when the connection failed: the
	// handshake failed, the transport dropped, the server broke the
	// protocol, or Close timed out. A close handshake (from either side)
	// and Abort are not failures.
	Err error
}

// Message is a text or binary message received.
type Message struct {
	Opcode wsframe.Opcode
	Data   []byte
	// At is when the message was complete.
	At time.Time
}

// Client is one WebSocket connection attempt: New validates the options,
// Connect opens the connection, and it ends in StateClosed. Reconnecting
// means creating a new Client.
//
// Messages arrive through Options.OnMessage on a read goroutine started by
// Connect. Send, Ping, Close and Abort are safe for concurrent use.
type Client struct {
	o       Options
	t       *target
	headers []httpclient.Header
	now     func() time.Time
	clock   Clock

	cbMu sync.Mutex // held while a state change is applied and reported

	mu        sync.Mutex
	state     State
	hs        *Handshake
	conn      *wsframe.Conn
	netConn   net.Conn
	sent      *CloseFrame
	received  *CloseFrame
	initiator Initiator
	aborted   bool
	closeErr  error
	info      *CloseInfo
	err       error
	done      chan struct{}
	// Keepalive (WS-09), under mu: the next tick, the pong deadline, and
	// a generation bumped by every pong so a deadline that fires late is
	// ignored.
	kaTick    func() bool
	kaTimeout func() bool
	kaGen     uint64

	pingMu  sync.Mutex // orders reporting a ping before its pong
	pending []pendingPing
}

// New validates o and returns a Client in StateIdle. It panics if
// o.Dialer is nil.
func New(o Options) (*Client, error) {
	if o.Dialer == nil {
		panic("wsclient: New needs a netx.Dialer")
	}
	t, err := parseURL(o.URL)
	if err != nil {
		return nil, err
	}
	headers, err := checkHeaders(o.Headers, o.AllowHandshakeHeaders)
	if err != nil {
		return nil, err
	}
	if err := checkSubprotocols(o.Subprotocols); err != nil {
		return nil, err
	}
	c := &Client{
		o:         o,
		t:         t,
		headers:   headers,
		now:       o.Now,
		state:     StateIdle,
		initiator: InitiatorNone,
		done:      make(chan struct{}),
	}
	if c.now == nil {
		c.now = time.Now
	}
	c.clock = o.Clock
	if c.clock == nil {
		c.clock = SystemClock()
	}
	return c, nil
}

// URL returns the URL without userinfo.
func (c *Client) URL() string { return c.t.display }

// State returns the current state.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Handshake returns what happened while connecting, nil before Connect
// returned.
func (c *Client) Handshake() *Handshake {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hs
}

// Subprotocol returns the subprotocol the server selected, or "".
func (c *Client) Subprotocol() string {
	if hs := c.Handshake(); hs != nil {
		return hs.Subprotocol
	}
	return ""
}

// CloseInfo returns how the connection ended, nil until StateClosed.
func (c *Client) CloseInfo() *CloseInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// Err returns the error the connection failed with, nil if it did not
// fail (or has not ended).
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Done is closed once the client reached StateClosed and OnState returned.
func (c *Client) Done() <-chan struct{} { return c.done }

// setState moves to s and reports it, unless the client is already in s
// or closed. update runs under the lock before the change is reported.
func (c *Client) setState(s State, update func(*StateChange)) bool {
	c.cbMu.Lock()
	defer c.cbMu.Unlock()
	c.mu.Lock()
	if c.state == s || c.state == StateClosed || (s == StateConnecting && c.state != StateIdle) {
		c.mu.Unlock()
		return false
	}
	c.state = s
	ch := StateChange{State: s, At: c.now()}
	if update != nil {
		update(&ch)
	}
	c.mu.Unlock()
	if c.o.OnState != nil {
		c.o.OnState(ch)
	}
	if s == StateClosed {
		close(c.done)
	}
	return true
}

// Connect dials, runs the upgrade handshake and, on success, moves to
// StateOpen and starts reading. ctx bounds the handshake only. On failure
// the client moves to StateClosed with the error, and Handshake still
// describes the attempt (with the server's response, if any). Connect can
// be called once.
func (c *Client) Connect(ctx context.Context) error {
	if !c.setState(StateConnecting, nil) {
		return invalid(CodeAlreadyConnected, "This WebSocket client was already connected.").
			WithHint("Create a new client to connect again.")
	}

	hs := &Handshake{}
	netConn, br, err := c.handshake(ctx, hs)
	c.mu.Lock()
	c.hs = hs
	c.mu.Unlock()
	if err != nil {
		c.setState(StateClosed, func(ch *StateChange) {
			c.info = &CloseInfo{Code: wsframe.CloseAbnormalClosure, InitiatedBy: InitiatorNone}
			c.err = err
			ch.Close, ch.Err = c.info, err
		})
		return err
	}

	conn := wsframe.NewConn(netConn, wsframe.Options{
		Role:            wsframe.RoleClient,
		Limits:          c.o.Limits,
		MaxMessageSize:  c.o.MaxMessageSize,
		DisableAutoPong: c.o.DisableAutoPong,
		OnFrame:         c.onFrame,
		Rand:            c.o.Rand,
		Reader:          br,
	})
	c.mu.Lock()
	c.conn, c.netConn = conn, netConn
	c.mu.Unlock()
	c.setState(StateOpen, nil)
	c.startKeepalive()
	go c.readLoop(conn, netConn, br)
	return nil
}

// onFrame records close frames and passes every frame on.
func (c *Client) onFrame(dir wsframe.Direction, f wsframe.Frame) {
	if f.Opcode == wsframe.OpClose {
		code, reason, _ := wsframe.ParseClosePayload(f.Payload)
		cf := &CloseFrame{Code: code, Reason: reason}
		c.mu.Lock()
		switch {
		case dir == wsframe.Sent && c.sent == nil:
			c.sent = cf
			if c.received == nil && c.initiator == InitiatorNone {
				c.initiator = InitiatorLocal
			}
		case dir == wsframe.Received && c.received == nil:
			c.received = cf
			if c.sent == nil && c.initiator == InitiatorNone {
				c.initiator = InitiatorRemote
			}
		}
		c.mu.Unlock()
	}
	if f.Opcode == wsframe.OpPing || f.Opcode == wsframe.OpPong {
		c.onPingPongFrame(dir, f)
	}
	if c.o.OnFrame != nil {
		c.o.OnFrame(dir, f)
	}
}

func (c *Client) readLoop(conn *wsframe.Conn, netConn net.Conn, br *bufio.Reader) {
	for {
		op, data, err := conn.ReadMessage()
		if err != nil {
			c.finish(err, netConn, br)
			return
		}
		if c.o.OnMessage != nil {
			c.o.OnMessage(Message{Opcode: op, Data: data, At: c.now()})
		}
	}
}

// finish ends the connection after ReadMessage failed with err.
func (c *Client) finish(err error, netConn net.Conn, br *bufio.Reader) {
	c.stopKeepalive()
	peerClosed := false
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Code == wsframe.CodePeerClosed {
		peerClosed = true
	}
	if peerClosed {
		c.setState(StateClosing, nil)
		// The server closes the TCP connection first (RFC 6455 §7.1.1);
		// wait a little for it, then close it here.
		linger := c.o.CloseLinger
		if linger == 0 {
			linger = DefaultCloseLinger
		}
		if linger > 0 {
			_ = netConn.SetReadDeadline(c.now().Add(linger))
			_, _ = io.Copy(io.Discard, io.LimitReader(br, 1<<20))
		}
	}
	_ = netConn.Close()

	c.setState(StateClosed, func(ch *StateChange) {
		info := &CloseInfo{
			Code:        wsframe.CloseAbnormalClosure,
			InitiatedBy: c.initiator,
			Sent:        c.sent,
			Received:    c.received,
		}
		if info.InitiatedBy == InitiatorNone && c.aborted {
			info.InitiatedBy = InitiatorLocal
		}
		switch {
		case peerClosed:
			info.Code, info.Reason = c.received.Code, c.received.Reason
			info.WasClean = c.sent != nil
		case c.closeErr != nil:
			c.err = c.closeErr
		case c.aborted:
		default:
			c.err = err
		}
		c.info = info
		ch.Close, ch.Err = info, c.err
	})
}

func (c *Client) openConn() (*wsframe.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateOpen {
		return nil, invalid(CodeNotOpen, "The WebSocket connection is not open.").
			WithDetail("state", string(c.state))
	}
	return c.conn, nil
}

// Send sends a single-frame text or binary message. Text must be UTF-8.
// It blocks while the transport does; Abort releases it.
func (c *Client) Send(op wsframe.Opcode, data []byte) error {
	conn, err := c.openConn()
	if err != nil {
		return err
	}
	return conn.WriteMessage(op, data)
}

// SendText sends a text message.
func (c *Client) SendText(s string) error { return c.Send(wsframe.OpText, []byte(s)) }

// SendBinary sends a binary message.
func (c *Client) SendBinary(b []byte) error { return c.Send(wsframe.OpBinary, b) }

// Ping sends a ping with a payload of at most 125 bytes. It is reported
// to Options.OnPingPong before it is written, and the pong that answers it
// is reported with the round-trip time (WS-09). Frames are also seen
// through Options.OnFrame.
func (c *Client) Ping(payload []byte) error {
	conn, err := c.openConn()
	if err != nil {
		return err
	}
	return c.ping(conn, payload, false)
}

// Close starts the close handshake with code and reason (validated as in
// wsframe: a code that may be sent, a UTF-8 reason of at most 123 bytes;
// wsframe.CloseNoStatus with no reason sends an empty close frame) and
// waits until the connection is closed. If the server does not answer
// before ctx ends, Close closes the transport, the connection fails with
// CodeCloseTimeout and Close returns that error.
//
// Close on a closed client returns nil; before Connect succeeded it fails
// with CodeNotOpen. If a close frame was already sent (a concurrent Close,
// the echo of the server's close), no second one is sent.
func (c *Client) Close(ctx context.Context, code wsframe.StatusCode, reason string) error {
	if _, err := wsframe.AppendClosePayload(nil, code, reason); err != nil {
		return err
	}
	c.mu.Lock()
	state, conn, netConn := c.state, c.conn, c.netConn
	c.mu.Unlock()
	switch state {
	case StateIdle, StateConnecting:
		return invalid(CodeNotOpen, "The WebSocket connection is not open.").WithDetail("state", string(state))
	case StateClosed:
		return nil
	}
	c.setState(StateClosing, nil)

	// The close is ours once we decide to send it, unless the server's
	// close frame was already read: the Sent hook runs only after the
	// write returned, and by then the server's echo may have been read
	// and recorded as the first close frame.
	c.mu.Lock()
	if c.received == nil && c.initiator == InitiatorNone {
		c.initiator = InitiatorLocal
	}
	c.mu.Unlock()

	written := make(chan error, 1)
	go func() {
		_, err := conn.WriteClose(code, reason)
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			_ = netConn.Close()
			<-c.done
			if c.Err() != nil {
				return c.Err()
			}
			return err
		}
	case <-ctx.Done():
		return c.closeTimedOut(ctx, netConn)
	}
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return c.closeTimedOut(ctx, netConn)
	}
}

func (c *Client) closeTimedOut(ctx context.Context, netConn net.Conn) error {
	select {
	case <-c.done:
		return nil
	default:
	}
	e := errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "The server did not complete the WebSocket close handshake in time.").
		WithCode(CodeCloseTimeout).
		WithHint("The connection was closed without waiting for the server.")
	c.mu.Lock()
	if c.state != StateClosed {
		c.closeErr = e
	}
	c.mu.Unlock()
	_ = netConn.Close()
	<-c.done
	if err := c.Err(); err != nil {
		return err
	}
	return nil
}

// Abort closes the transport without a close handshake. The connection
// ends with 1006 and no error. It does nothing before the connection is
// open or after it closed; cancel the context of Connect to stop
// connecting.
func (c *Client) Abort() {
	c.mu.Lock()
	nc := c.netConn
	if nc == nil || c.state == StateClosed || c.aborted {
		c.mu.Unlock()
		return
	}
	c.aborted = true
	c.mu.Unlock()
	_ = nc.Close()
}
