package sioclient

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/engineio"
	"github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
)

// State is the state of a Client.
type State string

// States, in order. A failed connection goes to StateClosed from any state.
const (
	StateIdle       State = "idle"
	StateConnecting State = "connecting" // Engine.IO handshake, upgrade, namespace CONNECT
	StateOpen       State = "open"       // connected to the namespace
	StateClosed     State = "closed"
)

// Reasons a connection ended, as socket.io-client names them
// (Trace.Reason of TraceClosed, Client.Reason).
const (
	ReasonClientDisconnect = "io client disconnect" // Disconnect
	ReasonServerDisconnect = "io server disconnect" // a DISCONNECT packet
	ReasonTransportClose   = "transport close"      // an Engine.IO close packet, or the server closed the WebSocket
	ReasonTransportError   = "transport error"      // the transport failed (Err set)
	ReasonPingTimeout      = "ping timeout"         // no ping in time (Err set)
	ReasonParseError       = "parse error"          // the server sent a packet that cannot be decoded (Err set)
	ReasonConnectFailed    = "connect failed"       // Connect failed (Err set)
)

// TraceKind says what a Trace reports.
type TraceKind string

// Trace kinds.
const (
	// TraceOpen: the Engine.IO handshake is done. Transport, Handshake.
	TraceOpen TraceKind = "open"
	// TraceFallback: the WebSocket connection of TransportAuto failed
	// (Err); long-polling is tried next.
	TraceFallback TraceKind = "fallback"
	// TraceUpgrade: the connection moved to Transport "websocket".
	TraceUpgrade TraceKind = "upgrade"
	// TraceUpgradeFailed: the upgrade probe failed (Err); the connection
	// stays on long-polling.
	TraceUpgradeFailed TraceKind = "upgrade_failed"
	// TracePing: the server's heartbeat ping arrived (Socket.IO v3/v4),
	// or the client sent one (Sent, Socket.IO v2).
	TracePing TraceKind = "ping"
	// TracePong: the answer to it was sent (Sent), or the server's answer
	// arrived (Socket.IO v2).
	TracePong TraceKind = "pong"
	// TracePacket: a Socket.IO packet was sent (Sent) or received: Packet,
	// Size. For an ACK that answers an emit, AckEvent and RTT are set and
	// Matched is true.
	TracePacket TraceKind = "packet"
	// TraceAckTimeout: no acknowledgement arrived for emit AckID
	// (AckEvent) within Options.AckTimeout.
	TraceAckTimeout TraceKind = "ack_timeout"
	// TraceConnected: the namespace accepted the connection. SocketID.
	TraceConnected TraceKind = "connected"
	// TraceClosed: the connection ended. Reason, and Err for a failure.
	TraceClosed TraceKind = "closed"
)

// Trace is one thing that happened on a connection (Options.OnTrace).
// Kind says which fields are set.
type Trace struct {
	Kind TraceKind
	At   time.Time
	// Sent is true for what this client sent.
	Sent bool
	// Transport is "websocket" or "polling".
	Transport string
	Handshake *engineio.Handshake
	Packet    *socketio.Packet
	// Size is the packet's size on the wire: the text form plus the
	// attachments, in bytes.
	Size     int
	AckID    uint64
	AckEvent string
	RTT      time.Duration
	Matched  bool
	SocketID string
	Reason   string
	Err      error
}

// ConnectInfo describes an open connection.
type ConnectInfo struct {
	// URL is the server as connected to: scheme, host and path.
	URL string
	// Namespace is the namespace connected to.
	Namespace string
	// Transport is the transport in use once connected.
	Transport string
	// Handshake is the server's Engine.IO handshake.
	Handshake engineio.Handshake
	// SocketID is the Socket.IO session ID of the namespace (the sid of the
	// server's CONNECT).
	SocketID string
	// Duration is how long connecting took.
	Duration time.Duration
}

// EmitResult says what Emit sent.
type EmitResult struct {
	// AckID is the acknowledgement ID, when one was asked for (HasAck).
	AckID  uint64
	HasAck bool
	// Size is the packet's size on the wire, attachments included.
	Size int
	// Attachments is the number of binary attachments.
	Attachments int
}

// transport is one Engine.IO transport.
type transport interface {
	name() string
	send(pkts []engineio.Packet) error
	// close ends the transport gracefully where it can (a WebSocket close
	// handshake), within ctx.
	close(ctx context.Context)
	// abort ends it at once. It may be called more than once.
	abort()
}

type pendingAck struct {
	event string
	sent  time.Time
	timer *time.Timer
}

type connectResult struct {
	sid string
	err error
}

// Client is one Socket.IO connection. Emit, Disconnect and the getters are
// safe for concurrent use.
type Client struct {
	o       Options
	t       *target
	nsp     string
	auth    json.RawMessage
	headers []httpclient.Header
	tport   Transport
	// v2 speaks Engine.IO 3 and Socket.IO 4 (VersionV2).
	v2  bool
	now func() time.Time

	// sendMu serializes writes (so traces are in wire order) and the
	// switch of an upgrade.
	sendMu sync.Mutex
	// dispatchMu serializes received packets (the decoder is stateful).
	dispatchMu sync.Mutex
	dec        *socketio.Decoder

	mu            sync.Mutex
	state         State
	tr            transport
	hs            engineio.Handshake
	sid           string
	cancelConnect context.CancelFunc
	connectWait   chan connectResult
	disconnecting bool
	nextAck       uint64
	acks          map[uint64]*pendingAck
	ping          *time.Timer
	pinger        *time.Timer // Socket.IO v2: the client's next ping
	pongSeen      bool        // Socket.IO v4: a ping arrived
	reason        string
	err           error
	done          chan struct{}
}

// New validates o and returns a Client in StateIdle. It panics if o.Dialer
// is nil.
func New(o Options) (*Client, error) {
	if o.Dialer == nil {
		panic("sioclient: New needs a netx.Dialer")
	}
	t, err := parseTarget(o.URL, o.Path)
	if err != nil {
		return nil, err
	}
	nsp, err := namespaceOf(o.Namespace, t.urlPath)
	if err != nil {
		return nil, err
	}
	auth, err := checkAuth(o.Auth)
	if err != nil {
		return nil, err
	}
	headers, err := checkHeaders(o.Headers)
	if err != nil {
		return nil, err
	}
	tport, err := checkTransport(o.Transport)
	if err != nil {
		return nil, err
	}
	version, err := checkVersion(o.Version)
	if err != nil {
		return nil, err
	}
	if version == VersionV2 && len(auth) > 0 {
		return nil, invalid(CodeInvalidAuth, "Socket.IO v2 has no auth payload.").
			WithHint("Remove the auth payload, and put the token in the URL's query: v2 sends it with the handshake and the namespace's CONNECT.")
	}
	if t.auth != "" && !hasHeader(headers, "Authorization") {
		headers = append(headers, httpclient.Header{Name: "Authorization", Value: t.auth, Enabled: true})
	}
	c := &Client{
		o:       o,
		t:       t,
		nsp:     nsp,
		auth:    auth,
		headers: headers,
		tport:   tport,
		v2:      version == VersionV2,
		now:     o.Now,
		dec:     socketio.NewDecoder(socketio.Options{MaxPacketSize: o.MaxMessageSize}),
		state:   StateIdle,
		acks:    map[uint64]*pendingAck{},
		done:    make(chan struct{}),
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

func hasHeader(rows []httpclient.Header, name string) bool {
	for _, h := range rows {
		if strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}

// URL returns the server as shown to the user: scheme, host and path.
func (c *Client) URL() string { return c.t.display() }

// Namespace returns the namespace the client connects to.
func (c *Client) Namespace() string { return c.nsp }

// State returns the current state.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Transport returns the transport in use, "" before the handshake.
func (c *Client) Transport() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tr == nil {
		return ""
	}
	return c.tr.name()
}

// Done is closed once the client reached StateClosed.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns the error the connection failed with, nil if it ended
// without one (or has not ended).
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Reason returns why the connection ended (Reason*), "" until it did.
func (c *Client) Reason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

func (c *Client) trace(t Trace) {
	if c.o.OnTrace == nil {
		return
	}
	if t.At.IsZero() {
		t.At = c.now()
	}
	c.o.OnTrace(t)
}

// Connect runs the Engine.IO handshake over the chosen transport, upgrades
// or falls back as it says, and connects to the namespace with the auth
// payload. It returns once the server accepted the namespace, or with the
// error: a refused namespace is CodeConnectError with the server's message
// and data. ctx and Options.HandshakeTimeout bound connecting; ctx's
// values (the user-operation mark) are kept for every later dial.
// Connect can be called once.
func (c *Client) Connect(ctx context.Context) (*ConnectInfo, error) {
	c.mu.Lock()
	if c.state != StateIdle {
		c.mu.Unlock()
		return nil, invalid(CodeAlreadyConnected, "This Socket.IO client was already connected.").
			WithHint("Create a new client to connect again.")
	}
	c.state = StateConnecting
	var hctx context.Context
	var cancel context.CancelFunc
	switch d := c.o.HandshakeTimeout; {
	case d < 0:
		hctx, cancel = context.WithCancel(ctx)
	case d == 0:
		hctx, cancel = context.WithTimeoutCause(ctx, DefaultHandshakeTimeout, errHandshakeTimeout)
	default:
		hctx, cancel = context.WithTimeoutCause(ctx, d, errHandshakeTimeout)
	}
	c.cancelConnect = cancel
	c.mu.Unlock()
	defer cancel()

	start := c.now()
	info, err := c.connect(hctx)
	c.mu.Lock()
	c.cancelConnect = nil
	if err == nil && c.state != StateConnecting {
		err = c.err
		if err == nil {
			err = errs.New(errs.Cancelled, "Connecting was cancelled.").WithCode(CodeCancelled)
		}
	}
	if err == nil {
		c.state = StateOpen
	}
	c.mu.Unlock()
	if err != nil {
		if e := c.Err(); e != nil {
			// The connection already failed (the transport, a ping
			// timeout): that error says more than the cancelled wait.
			err = e
		} else {
			err = connectError(hctx, err, c.t.authority)
		}
		c.finish(ReasonConnectFailed, err)
		return nil, err
	}
	info.Duration = c.now().Sub(start)
	c.trace(Trace{Kind: TraceConnected, SocketID: info.SocketID, Transport: info.Transport})
	return info, nil
}

func (c *Client) connect(ctx context.Context) (*ConnectInfo, error) {
	var (
		tr      transport
		hs      engineio.Handshake
		pending []engineio.Packet
		err     error
	)
	switch c.tport {
	case TransportWebSocket:
		tr, hs, err = c.openWebSocket(ctx)
	case TransportPolling, TransportUpgrade:
		tr, hs, pending, err = c.openPolling(ctx)
	default:
		tr, hs, err = c.openWebSocket(ctx)
		if err != nil && ctx.Err() == nil {
			c.trace(Trace{Kind: TraceFallback, Transport: "polling", Err: transportError(err, c.t.authority)})
			tr, hs, pending, err = c.openPolling(ctx)
		}
	}
	if err != nil {
		return nil, err
	}

	wait := make(chan connectResult, 1)
	c.mu.Lock()
	if c.state != StateConnecting {
		c.mu.Unlock()
		tr.abort()
		return nil, ctx.Err()
	}
	c.tr, c.hs, c.connectWait = tr, hs, wait
	c.mu.Unlock()
	c.trace(Trace{Kind: TraceOpen, Transport: tr.name(), Handshake: &hs})
	c.resetPing()
	if c.v2 {
		c.schedulePing()
	}
	switch t := tr.(type) {
	case *wsTransport:
		t.attach(c)
	case *pollTransport:
		t.attach(c, pending)
		if c.tport == TransportUpgrade && contains(hs.Upgrades, "websocket") {
			if err := c.upgrade(ctx, t); err != nil {
				return nil, err
			}
		}
	}

	if err := c.sendConnect(); err != nil {
		return nil, err
	}
	select {
	case r := <-wait:
		if r.err != nil {
			// Tell the server the Engine.IO session is over; the
			// transport's end is not the error.
			c.mu.Lock()
			c.disconnecting = true
			c.mu.Unlock()
			_ = c.send(engineio.Packet{Type: engineio.Close})
			return nil, r.err
		}
		c.mu.Lock()
		c.sid = r.sid
		name := c.tr.name()
		c.mu.Unlock()
		return &ConnectInfo{URL: c.t.display(), Namespace: c.nsp, Transport: name, Handshake: hs, SocketID: r.sid}, nil
	case <-c.done:
		return nil, errs.New(errs.Network, "The connection ended while connecting to the namespace.").WithCode(CodeTransportError)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// sendConnect joins the namespace: CONNECT with the auth payload. With
// Socket.IO v2 the main namespace needs none (the server joins it at
// once) and another one carries the URL's query instead of an auth
// payload, as socket.io-client 2 sends it ("0/admin?token=abc,").
func (c *Client) sendConnect() error {
	if c.v2 {
		if c.nsp == socketio.MainNamespace {
			return nil
		}
		nsp := c.nsp
		if c.t.query != "" {
			nsp += "?" + strings.ReplaceAll(c.t.query, ",", "%2C")
		}
		return c.sendPacket(socketio.Packet{Type: socketio.Connect, Namespace: nsp})
	}
	var authArg any
	if len(c.auth) > 0 {
		authArg = c.auth
	}
	p, err := socketio.NewConnect(c.nsp, authArg)
	if err != nil {
		return err
	}
	if len(c.auth) > 0 {
		// Keep the auth as typed (compacted), not re-encoded.
		p.Data = c.auth
	}
	return c.sendPacket(p)
}

// eio is the Engine.IO protocol version spoken.
func (c *Client) eio() int {
	if c.v2 {
		return engineio.ProtocolV3
	}
	return engineio.Protocol
}

// schedulePing sends the client's next heartbeat ping (Socket.IO v2)
// after the ping interval.
func (c *Client) schedulePing() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateClosed {
		return
	}
	d := c.hs.PingIntervalDuration()
	if c.pinger == nil {
		c.pinger = time.AfterFunc(d, c.sendPing)
		return
	}
	c.pinger.Reset(d)
}

func (c *Client) sendPing() {
	if c.State() == StateClosed {
		return
	}
	if err := c.send(engineio.Packet{Type: engineio.Ping}); err == nil {
		c.trace(Trace{Kind: TracePing, Sent: true})
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// upgrade moves a long-polling connection to WebSocket (Engine.IO
// "Upgrade"): probe with ping "probe", pause polling, send the upgrade
// packet. A failed probe is reported and keeps polling; a failure after
// polling paused fails the connection.
func (c *Client) upgrade(ctx context.Context, poll *pollTransport) error {
	// The probe gets the server's ping timeout: a server that never
	// answers it must not hold up connecting (polling keeps working).
	c.mu.Lock()
	probeTimeout := c.hs.PingTimeoutDuration()
	c.mu.Unlock()
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	w, err := c.dialWebSocket(pctx, poll.sid)
	if err == nil {
		err = w.send([]engineio.Packet{engineio.Text(engineio.Ping, "probe")})
		if err == nil {
			var p engineio.Packet
			p, err = w.next(pctx)
			if err == nil && (p.Type != engineio.Pong || string(p.Data) != "probe") {
				err = errs.New(errs.Protocol, "The server answered the upgrade probe with a "+p.Type.String()+" packet.").
					WithCode(CodeUpgradeFailed)
			}
		}
		if err != nil {
			w.abort()
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var e error
		if pctx.Err() != nil {
			e = errs.Wrap(pctx.Err(), errs.Timeout, "The server did not answer the WebSocket upgrade probe within its ping timeout.").
				WithCode(CodeUpgradeFailed)
		} else {
			e = transportError(err, c.t.authority)
		}
		c.trace(Trace{Kind: TraceUpgradeFailed, Transport: "polling", Err: e})
		return nil
	}
	if err := poll.pause(ctx); err != nil {
		w.abort()
		return err
	}
	c.sendMu.Lock()
	err = w.send([]engineio.Packet{{Type: engineio.Upgrade}})
	if err == nil {
		c.mu.Lock()
		c.tr = w
		c.mu.Unlock()
	}
	c.sendMu.Unlock()
	if err != nil {
		w.abort()
		return err
	}
	poll.abort()
	w.attach(c)
	c.trace(Trace{Kind: TraceUpgrade, Transport: w.name()})
	return nil
}

// resetPing (re)starts the heartbeat timer: the server pings every
// pingInterval and the client gives up pingTimeout later. With Socket.IO
// v2 the client pings, and every packet received restarts the timer, as
// engine.io-client 3 does.
func (c *Client) resetPing() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateClosed {
		return
	}
	d := c.hs.PingIntervalDuration() + c.hs.PingTimeoutDuration()
	if c.ping == nil {
		c.ping = time.AfterFunc(d, c.pingTimedOut)
		return
	}
	c.ping.Reset(d)
}

func (c *Client) pingTimedOut() {
	c.mu.Lock()
	hs, seen := c.hs, c.pongSeen
	c.mu.Unlock()
	ms := strconv.Itoa(hs.PingInterval + hs.PingTimeout)
	if c.v2 {
		c.finish(ReasonPingTimeout, errs.New(errs.Timeout, "The server did not answer the heartbeat ping: nothing arrived for "+
			ms+" ms (ping interval plus ping timeout).").
			WithCode(CodePingTimeout).
			WithHint("The server or the network stopped responding."))
		return
	}
	hint := "The server or the network stopped responding."
	if !seen {
		hint += " A Socket.IO v2 server never pings: if this is one, choose Socket.IO v2 in the Options."
	}
	c.finish(ReasonPingTimeout, errs.New(errs.Timeout, "The server sent no heartbeat ping for "+
		ms+" ms (ping interval plus ping timeout).").
		WithCode(CodePingTimeout).
		WithHint(hint))
}

// send writes Engine.IO packets on the current transport.
func (c *Client) send(pkts ...engineio.Packet) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.sendLocked(pkts)
}

func (c *Client) sendLocked(pkts []engineio.Packet) error {
	c.mu.Lock()
	tr, st := c.tr, c.state
	c.mu.Unlock()
	if tr == nil || st == StateClosed {
		return invalid(CodeNotOpen, "The Socket.IO connection is not open.").WithDetail("state", string(st))
	}
	return tr.send(pkts)
}

// sendPacket traces and writes one Socket.IO packet.
func (c *Client) sendPacket(p socketio.Packet) error {
	msgs, err := socketio.EncodeMessages(p)
	if err != nil {
		return err
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.trace(Trace{Kind: TracePacket, Sent: true, Packet: &p, Size: messagesSize(msgs)})
	return c.sendLocked(msgs)
}

func messagesSize(msgs []engineio.Packet) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Data)
	}
	return n
}

// Emit sends event with args to the namespace (SIO-03): JSON-encodable
// values, with []byte anywhere in them sent as binary attachments. With
// ack, the packet asks for an acknowledgement (SIO-05), reported as the
// matching received ACK trace, or TraceAckTimeout after
// Options.AckTimeout. A write failure fails the connection.
func (c *Client) Emit(event string, args []any, ack bool) (EmitResult, error) {
	p, err := socketio.NewEvent(c.nsp, event, args...)
	if err != nil {
		return EmitResult{}, err
	}
	msgs, err := socketio.EncodeMessages(p)
	if err != nil {
		return EmitResult{}, err
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.Lock()
	if c.state != StateOpen || c.disconnecting {
		st := c.state
		c.mu.Unlock()
		return EmitResult{}, invalid(CodeNotOpen, "The Socket.IO connection is not open.").
			WithHint("Connect to emit events.").WithDetail("state", string(st))
	}
	res := EmitResult{Attachments: len(p.Attachments)}
	if ack {
		id := c.nextAck
		c.nextAck++
		p.ID, p.HasID = id, true
		if msgs, err = socketio.EncodeMessages(p); err != nil {
			c.mu.Unlock()
			return EmitResult{}, err
		}
		timeout := c.o.AckTimeout
		if timeout <= 0 {
			timeout = DefaultAckTimeout
		}
		c.acks[id] = &pendingAck{event: event, sent: c.now(), timer: time.AfterFunc(timeout, func() { c.ackTimedOut(id) })}
		res.AckID, res.HasAck = id, true
	}
	c.mu.Unlock()
	res.Size = messagesSize(msgs)
	c.trace(Trace{Kind: TracePacket, Sent: true, Packet: &p, Size: res.Size})
	if err := c.sendLocked(msgs); err != nil {
		if ack {
			c.mu.Lock()
			if a := c.acks[res.AckID]; a != nil {
				a.timer.Stop()
				delete(c.acks, res.AckID)
			}
			c.mu.Unlock()
		}
		return EmitResult{}, err
	}
	return res, nil
}

func (c *Client) ackTimedOut(id uint64) {
	c.mu.Lock()
	a := c.acks[id]
	delete(c.acks, id)
	c.mu.Unlock()
	if a != nil {
		c.trace(Trace{Kind: TraceAckTimeout, AckID: id, AckEvent: a.event, RTT: c.now().Sub(a.sent)})
	}
}

// Disconnect leaves the namespace (DISCONNECT), closes the Engine.IO
// session and the transport (a WebSocket close handshake, within ctx) and
// waits until the client is closed. While connecting it stops connecting.
// It does nothing on an idle or closed client.
func (c *Client) Disconnect(ctx context.Context) {
	c.mu.Lock()
	st, tr := c.state, c.tr
	switch {
	case st == StateIdle || st == StateClosed || c.disconnecting:
		c.mu.Unlock()
		<-c.doneIfStarted(st)
		return
	case st == StateConnecting:
		cancel := c.cancelConnect
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		c.finish(ReasonClientDisconnect, nil)
		return
	}
	c.disconnecting = true
	c.mu.Unlock()
	if p, err := socketio.NewDisconnect(c.nsp); err == nil {
		if msgs, err := socketio.EncodeMessages(p); err == nil {
			c.sendMu.Lock()
			c.trace(Trace{Kind: TracePacket, Sent: true, Packet: &p, Size: messagesSize(msgs)})
			// The close goes in a request of its own, after the
			// DISCONNECT's, as engine.io-client sends it once its write
			// buffer drained: socket.io handles a DISCONNECT on the next
			// tick, so a close in the same long-polling payload would
			// close the transport first and the server would see
			// "transport close" instead of "client namespace disconnect".
			if tr.send(msgs) == nil {
				_ = tr.send([]engineio.Packet{{Type: engineio.Close}})
			}
			c.sendMu.Unlock()
		}
	}
	tr.close(ctx)
	c.finish(ReasonClientDisconnect, nil)
}

// doneIfStarted is Done for a client past idle, and a closed channel for
// an idle one (Disconnect does not wait for a client never connected).
func (c *Client) doneIfStarted(st State) <-chan struct{} {
	if st == StateIdle {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return c.done
}

// finish ends the connection once, with reason and err.
func (c *Client) finish(reason string, err error) {
	c.mu.Lock()
	if c.state == StateClosed {
		c.mu.Unlock()
		return
	}
	c.state = StateClosed
	c.reason, c.err = reason, err
	tr, ping, pinger, acks, cancel := c.tr, c.ping, c.pinger, c.acks, c.cancelConnect
	c.acks = map[uint64]*pendingAck{}
	c.mu.Unlock()
	if ping != nil {
		ping.Stop()
	}
	if pinger != nil {
		pinger.Stop()
	}
	for _, a := range acks {
		a.timer.Stop()
	}
	if cancel != nil {
		cancel()
	}
	if tr != nil {
		tr.abort()
	}
	c.trace(Trace{Kind: TraceClosed, Reason: reason, Err: err})
	close(c.done)
}

// transportDown is called when transport tr ended: err nil when it closed
// normally. Only the current transport counts, and not while
// disconnecting.
func (c *Client) transportDown(tr transport, err error) {
	c.mu.Lock()
	ignore := c.tr != tr || c.disconnecting || c.state == StateClosed
	c.mu.Unlock()
	if ignore {
		return
	}
	if err == nil {
		c.finish(ReasonTransportClose, nil)
		return
	}
	reason := ReasonTransportError
	if e, ok := errors.AsType[*errs.Error](err); ok && (strings.HasPrefix(e.Code, "engineio.") || strings.HasPrefix(e.Code, "socketio.")) {
		reason = ReasonParseError
	}
	c.finish(reason, transportError(err, c.t.authority))
}

// receive handles one Engine.IO packet of the current transport.
func (c *Client) receive(p engineio.Packet) {
	c.dispatchMu.Lock()
	defer c.dispatchMu.Unlock()
	if c.State() == StateClosed {
		return
	}
	if c.v2 {
		c.resetPing()
	}
	switch p.Type {
	case engineio.Close:
		c.mu.Lock()
		disconnecting := c.disconnecting
		c.mu.Unlock()
		if !disconnecting {
			c.finish(ReasonTransportClose, nil)
		}
	case engineio.Pong:
		if c.v2 {
			c.trace(Trace{Kind: TracePong})
			c.schedulePing()
		}
	case engineio.Ping:
		c.mu.Lock()
		c.pongSeen = true
		c.mu.Unlock()
		c.resetPing()
		c.trace(Trace{Kind: TracePing})
		if err := c.send(engineio.Packet{Type: engineio.Pong, Data: p.Data}); err == nil {
			c.trace(Trace{Kind: TracePong, Sent: true})
		}
	case engineio.Message:
		sp, err := c.dec.Feed(p)
		if err != nil {
			c.finish(ReasonParseError, err)
			return
		}
		if sp != nil {
			c.handle(sp)
		}
	}
}

// handle handles one Socket.IO packet received.
func (c *Client) handle(p *socketio.Packet) {
	text, atts, _ := socketio.Encode(*p)
	size := len(text)
	for _, a := range atts {
		size += len(a)
	}
	tr := Trace{Kind: TracePacket, Packet: p, Size: size}
	if p.Namespace != c.nsp {
		// Another namespace: shown, not acted on.
		c.trace(tr)
		return
	}
	switch p.Type {
	case socketio.Connect:
		var body struct {
			SID string `json:"sid"`
		}
		_ = json.Unmarshal(p.Data, &body)
		if body.SID == "" && c.v2 {
			// Socket.IO v2 sends no socket ID: it is the Engine.IO
			// session ID, after the namespace and "#" for another one.
			c.mu.Lock()
			body.SID = c.hs.SID
			c.mu.Unlock()
			if c.nsp != socketio.MainNamespace {
				body.SID = c.nsp + "#" + body.SID
			}
		}
		c.trace(tr)
		c.resolveConnect(connectResult{sid: body.SID})
	case socketio.ConnectError:
		c.trace(tr)
		c.resolveConnect(connectResult{err: connectErrorOf(c.nsp, p.Data)})
	case socketio.Disconnect:
		c.trace(tr)
		c.finish(ReasonServerDisconnect, nil)
	case socketio.Ack, socketio.BinaryAck:
		c.mu.Lock()
		a := c.acks[p.ID]
		delete(c.acks, p.ID)
		c.mu.Unlock()
		if a != nil {
			a.timer.Stop()
			tr.Matched, tr.AckEvent, tr.RTT = true, a.event, c.now().Sub(a.sent)
		}
		tr.AckID = p.ID
		c.trace(tr)
	default:
		c.trace(tr)
	}
}

func (c *Client) resolveConnect(r connectResult) {
	c.mu.Lock()
	wait := c.connectWait
	c.connectWait = nil
	c.mu.Unlock()
	if wait != nil {
		wait <- r
	}
}

// connectErrorOf is the error of a CONNECT_ERROR payload: {"message",
// "data"} (v4) or a string (v3).
func connectErrorOf(nsp string, data json.RawMessage) error {
	msg := ""
	var detail json.RawMessage
	var obj struct {
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		msg, detail = obj.Message, obj.Data
	} else {
		_ = json.Unmarshal(data, &msg)
	}
	text := "The server refused to connect to the namespace " + nsp
	if msg != "" {
		text += ": " + msg
	} else {
		text += "."
	}
	e := errs.New(errs.PermissionDenied, text).
		WithCode(CodeConnectError).
		WithHint("Check the auth payload, and that the server has this namespace.").
		WithDetail("namespace", nsp)
	if msg != "" {
		e = e.WithDetail("message", msg)
	}
	if len(detail) > 0 && string(detail) != "null" {
		e = e.WithDetail("data", string(detail))
	}
	return e
}
