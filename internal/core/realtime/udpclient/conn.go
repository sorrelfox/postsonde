package udpclient

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Defaults and limits of Options.
const (
	// DefaultOpenTimeout bounds DNS and, through a proxy, the UDP
	// association when Options.OpenTimeout is zero.
	DefaultOpenTimeout = 10 * time.Second
	// MaxDatagramSize is the largest datagram there can be (the UDP
	// length field), and the largest Options.MaxDatagramSize.
	MaxDatagramSize = 65535
)

// State is where a connection is in its life.
type State string

// The states, in order. An open that fails goes from opening straight to
// closed; a connection that fails goes from open straight to closed.
const (
	StateOpening State = "opening" // resolving, associating with the proxy
	StateOpen    State = "open"    // datagrams flow
	StateClosing State = "closing" // Close was called
	StateClosed  State = "closed"  // the socket is closed
)

// Initiator says who ended a connection.
type Initiator string

const (
	// InitiatorNone: nobody chose to; opening or the socket failed (a
	// proxy that ended the association).
	InitiatorNone Initiator = "none"
	// InitiatorLocal: Close was called, or opening was cancelled.
	InitiatorLocal Initiator = "local"
)

// Direction says which way a datagram went.
type Direction string

const (
	Sent     Direction = "sent"
	Received Direction = "received"
)

// Datagram is one datagram sent or received.
type Datagram struct {
	Direction Direction
	// Data is the payload, owned by the receiver of the datagram. A
	// received datagram longer than Options.MaxDatagramSize is cut to it.
	Data []byte
	// Size is the datagram's size: len(Data), or more when Truncated.
	Size      int
	Truncated bool
	// Peer is the sender of a received datagram (ip:port, or host:port
	// when a proxy reports a name), the target of a sent one.
	Peer string
	At   time.Time
}

// Note is something that happened to a datagram that does not end the
// connection: a datagram the proxy relay sent fragmented or malformed
// (netx.socks_udp_*), or an ICMP error a connected socket got for an
// earlier datagram (netx.connection_refused: no one listens on the port).
type Note struct {
	At  time.Time
	Err error
}

// CloseInfo describes how a connection ended.
type CloseInfo struct {
	InitiatedBy Initiator
	// Err is the failure, or nil when the connection was closed by Close.
	Err error
	// BytesSent and BytesReceived count payload bytes (not the SOCKS
	// header); DatagramsSent and DatagramsReceived the datagrams; Notes
	// the notes (dropped datagrams, ICMP errors).
	BytesSent, BytesReceived         int64
	DatagramsSent, DatagramsReceived int64
	Notes                            int64
	// OpenedAt is zero when the connection never opened.
	OpenedAt, ClosedAt time.Time
}

// StateChange is passed to Options.OnState.
type StateChange struct {
	State State
	At    time.Time
	// LocalAddr and RemoteAddr are the socket's addresses from StateOpen
	// on; through a proxy RemoteAddr is its relay.
	LocalAddr, RemoteAddr string
	// Via names the proxy ("socks5h://proxy:1080"), "" when direct; Proxy
	// is the hop through it (UDP ASSOCIATE, the relay), from StateOpen on
	// and on a failed open as far as it got.
	Via   string
	Proxy *netx.ProxyHop
	// Connected and MaxPayload (the largest datagram Send takes) are set
	// from StateOpen on.
	Connected  bool
	MaxPayload int
	// Close is set on StateClosed; Err when the connection failed (the
	// same as Close.Err).
	Close *CloseInfo
	Err   error
}

// Options configure Open.
type Options struct {
	// Address is host:port; see ParseAddress.
	Address string
	// Dialer opens the socket. Required.
	Dialer netx.Dialer
	// Network is "udp" (the default for ""): IPv4 or IPv6, as the name
	// resolves; "udp4" or "udp6" for one IP version.
	Network string
	// Connected receives only from the target and reports ICMP errors as
	// notes; otherwise datagrams from any sender are received.
	Connected bool
	// OpenTimeout bounds DNS and the proxy's UDP association: zero means
	// DefaultOpenTimeout, negative no limit besides the context.
	OpenTimeout time.Duration
	// MaxDatagramSize limits datagrams: Send refuses larger ones, and a
	// larger received one is cut to it (Datagram.Truncated). Zero means
	// MaxDatagramSize; at most MaxDatagramSize.
	MaxDatagramSize int
	// OnState is called with every state change, OnDatagram with every
	// datagram (a sent one just before it is written), OnNote with every
	// note. Calls are serialized, in order. They must not call Send or
	// Close.
	OnState    func(StateChange)
	OnDatagram func(Datagram)
	OnNote     func(Note)
	// Now is the clock for timestamps. Default time.Now.
	Now func() time.Time
}

// Conn is an open, or once open, UDP socket. Its methods are safe for
// concurrent use.
type Conn struct {
	o     Options
	nc    net.Conn
	dc    netx.DatagramConn // nc when it reports senders
	max   int               // the largest payload Send takes
	peer  string            // the target, for sent datagrams
	now   func() time.Time
	done  chan struct{}
	limit int

	writeMu sync.Mutex
	cbMu    sync.Mutex // serializes callbacks and state changes

	mu     sync.Mutex
	state  State
	reason *closeReason
	info   *CloseInfo
	stats  CloseInfo
	open   StateChange
}

type closeReason struct {
	initiator Initiator
	err       error
}

// Open opens a UDP socket to o.Address through o.Dialer and returns it,
// reading at once. ctx bounds opening only. OnState gets opening, then
// open, or closed when opening fails (then Open returns the error too).
//
// Invalid options fail before anything is reported: CodeInvalidAddress,
// CodeInvalidOptions, CodeFamilyMismatch. Dial errors keep their netx.*
// codes (netx.dial_timeout, netx.dns_not_found, netx.socks_*: a proxy
// without UDP ASSOCIATE answers netx.socks_command_not_supported, SOCKS4
// and HTTP proxies netx.socks_unsupported_network and
// netx.http_proxy_unsupported_network).
func Open(ctx context.Context, o Options) (*Conn, error) {
	if o.Dialer == nil {
		panic("udpclient: Options.Dialer is nil")
	}
	if err := check(&o); err != nil {
		return nil, err
	}
	c := &Conn{o: o, now: o.Now, done: make(chan struct{}), state: StateOpening, limit: o.MaxDatagramSize}
	if c.now == nil {
		c.now = time.Now
	}
	c.emitState(StateChange{State: StateOpening, At: c.now()})

	dctx, cancel := ctx, context.CancelFunc(func() {})
	switch {
	case o.OpenTimeout == 0:
		dctx, cancel = context.WithTimeout(ctx, DefaultOpenTimeout)
	case o.OpenTimeout > 0:
		dctx, cancel = context.WithTimeout(ctx, o.OpenTimeout)
	}
	if !o.Connected {
		dctx = netx.UnconnectedUDP(dctx)
	}
	var hop *netx.ProxyHop
	dctx = netx.WithTrace(dctx, &netx.Trace{DialDone: func(i netx.DialDoneInfo) {
		if i.Proxy != nil && i.Address == o.Address {
			h := *i.Proxy
			hop = &h // the dial runs on this goroutine
		}
	}})
	nc, err := o.Dialer.DialContext(dctx, o.Network, o.Address)
	if err == nil && nc == nil {
		err = errs.New(errs.Internal, "The dialer returned no connection.")
	}
	if err != nil {
		err = netx.WrapDialError(err, o.Network, o.Address)
	}
	cancel()
	if err != nil {
		initiator := InitiatorNone
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			initiator = InitiatorLocal
		}
		c.cbMu.Lock()
		c.mu.Lock()
		c.state = StateClosed
		c.info = &CloseInfo{InitiatedBy: initiator, Err: err, ClosedAt: c.now()}
		info := *c.info
		c.mu.Unlock()
		c.callState(StateChange{State: StateClosed, At: info.ClosedAt, Proxy: hop, Close: &info, Err: err})
		close(c.done)
		c.cbMu.Unlock()
		return nil, err
	}

	c.nc = nc
	c.dc, _ = nc.(netx.DatagramConn)
	c.max = netx.MaxDatagramPayload(nc)
	if c.max <= 0 {
		c.max = netx.MaxUDPPayloadIPv4
	}
	c.max = min(c.max, c.limit)
	c.peer = o.Address
	c.cbMu.Lock()
	c.mu.Lock()
	c.state = StateOpen
	c.stats.OpenedAt = c.now()
	ch := StateChange{
		State: StateOpen, At: c.stats.OpenedAt,
		Via: netx.ViaOf(nc), Proxy: hop,
		Connected: netx.IsConnectedUDP(nc), MaxPayload: c.max,
	}
	if a := nc.LocalAddr(); a != nil {
		ch.LocalAddr = a.String()
	}
	if a := nc.RemoteAddr(); a != nil {
		ch.RemoteAddr = a.String()
		if ch.Via == "" {
			c.peer = a.String() // the resolved target
		}
	}
	c.open = ch
	c.mu.Unlock()
	c.callState(ch)
	c.cbMu.Unlock()

	go c.readLoop()
	return c, nil
}

// Check reports whether Open would refuse o before reporting anything:
// CodeInvalidAddress, CodeInvalidOptions, CodeFamilyMismatch.
func Check(o Options) error { return check(&o) }

// check validates and fills in o.
func check(o *Options) error {
	host, _, err := ParseAddress(o.Address)
	if err != nil {
		return err
	}
	switch o.Network {
	case "":
		o.Network = "udp"
	case "udp", "udp4", "udp6":
	default:
		return invalidOptions("network", o.Network, `The network must be "udp", "udp4" or "udp6".`)
	}
	if err := checkFamily(o.Network, host, o.Address); err != nil {
		return err
	}
	switch {
	case o.MaxDatagramSize == 0:
		o.MaxDatagramSize = MaxDatagramSize
	case o.MaxDatagramSize < 0 || o.MaxDatagramSize > MaxDatagramSize:
		return invalidOptions("maxDatagramSize", itoa(o.MaxDatagramSize), "The maximum datagram size must be from 1 to 65,535 bytes.")
	}
	return nil
}

// Address returns the address the socket sends to.
func (c *Conn) Address() string { return c.o.Address }

// MaxPayload returns the largest datagram Send takes.
func (c *Conn) MaxPayload() int { return c.max }

// State returns the current state.
func (c *Conn) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Done is closed once the connection is closed and OnState returned.
func (c *Conn) Done() <-chan struct{} { return c.done }

// CloseInfo returns how the connection ended, or nil while it is not
// closed.
func (c *Conn) CloseInfo() *CloseInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.info == nil {
		return nil
	}
	info := *c.info
	return &info
}

// Err returns the failure the connection ended with, or nil.
func (c *Conn) Err() error {
	if info := c.CloseInfo(); info != nil {
		return info.Err
	}
	return nil
}

// Send sends b as one datagram (an empty b is an empty datagram). A
// datagram over MaxPayload fails with CodeDatagramTooLarge. OnDatagram
// gets it just before it is written. A failed write is returned and the
// socket stays open: UDP failures concern one datagram.
func (c *Conn) Send(b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	if c.state != StateOpen {
		c.mu.Unlock()
		return notOpen()
	}
	c.mu.Unlock()
	if len(b) > c.max {
		return tooLarge(len(b), c.max, c.limit < MaxDatagramSize && c.max == c.limit)
	}
	data := append([]byte(nil), b...)
	c.cbMu.Lock()
	if c.o.OnDatagram != nil {
		c.o.OnDatagram(Datagram{Direction: Sent, Data: data, Size: len(data), Peer: c.peer, At: c.now()})
	}
	c.cbMu.Unlock()

	n, err := c.nc.Write(b)
	if err != nil {
		return writeError(err, c.o.Address, len(b), c.max)
	}
	c.mu.Lock()
	c.stats.BytesSent += int64(n)
	c.stats.DatagramsSent++
	c.mu.Unlock()
	return nil
}

// Close closes the socket (and the proxy association): StateClosing, then
// StateClosed once the read goroutine has stopped. It returns when the
// connection is closed. Closing a closed connection does nothing.
func (c *Conn) Close() error {
	c.cbMu.Lock()
	c.mu.Lock()
	if c.state != StateOpen {
		c.mu.Unlock()
		c.cbMu.Unlock()
		<-c.done
		return nil
	}
	c.state = StateClosing
	c.reason = &closeReason{initiator: InitiatorLocal}
	ch := c.open
	ch.State, ch.At, ch.Proxy = StateClosing, c.now(), nil
	c.mu.Unlock()
	c.callState(ch)
	c.cbMu.Unlock()

	_ = c.nc.Close()
	<-c.done
	return nil
}

func (c *Conn) readLoop() {
	buf := make([]byte, MaxDatagramSize+1)
	for {
		var (
			n    int
			from net.Addr
			err  error
		)
		if c.dc != nil {
			n, from, err = c.dc.ReadFrom(buf)
		} else {
			n, err = c.nc.Read(buf)
			from = c.nc.RemoteAddr()
		}
		if err != nil {
			if note := c.noteOf(err); note != nil {
				c.deliverNote(note)
				continue
			}
			c.finish(err)
			return
		}
		at := c.now()
		size := n
		data := append([]byte(nil), buf[:min(n, c.limit)]...)
		peer := ""
		if from != nil {
			peer = from.String()
		}
		c.mu.Lock()
		c.stats.BytesReceived += int64(n)
		c.stats.DatagramsReceived++
		c.mu.Unlock()
		c.cbMu.Lock()
		if c.o.OnDatagram != nil {
			c.o.OnDatagram(Datagram{Direction: Received, Data: data, Size: size, Truncated: size > len(data), Peer: peer, At: at})
		}
		c.cbMu.Unlock()
	}
}

// noteOf returns the note for a read error that does not end the
// connection, or nil: a dropped datagram, or an ICMP error of a connected
// socket (while open).
func (c *Conn) noteOf(err error) error {
	if netx.IsDroppedDatagram(err) {
		return err
	}
	if c.State() == StateOpen && netx.Classify(err) == netx.FailureRefused {
		return refused(err, c.o.Address)
	}
	return nil
}

func (c *Conn) deliverNote(err error) {
	at := c.now()
	c.mu.Lock()
	c.stats.Notes++
	c.mu.Unlock()
	c.cbMu.Lock()
	defer c.cbMu.Unlock()
	if c.o.OnNote != nil {
		c.o.OnNote(Note{At: at, Err: err})
	}
}

// finish closes the socket after the read loop ended with err and reports
// StateClosed.
func (c *Conn) finish(err error) {
	_ = c.nc.Close()
	c.cbMu.Lock()
	defer c.cbMu.Unlock()
	c.mu.Lock()
	r := c.reason
	if r == nil {
		r = &closeReason{initiator: InitiatorNone, err: readError(err, c.o.Address)}
		c.reason = r
	}
	info := c.stats
	info.InitiatedBy, info.Err, info.ClosedAt = r.initiator, r.err, c.now()
	c.info = &info
	c.state = StateClosed
	ch := c.open
	ch.State, ch.At, ch.Proxy, ch.Close, ch.Err = StateClosed, info.ClosedAt, nil, &info, r.err
	c.mu.Unlock()
	c.callState(ch)
	close(c.done)
}

// emitState reports a change outside a locked section (the first state).
func (c *Conn) emitState(ch StateChange) {
	c.cbMu.Lock()
	defer c.cbMu.Unlock()
	c.callState(ch)
}

// callState calls OnState; c.cbMu must be held.
func (c *Conn) callState(ch StateChange) {
	if c.o.OnState != nil {
		c.o.OnState(ch)
	}
}
