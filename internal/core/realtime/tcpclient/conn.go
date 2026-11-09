package tcpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/framing"
)

// Defaults for Options.
const (
	// DefaultConnectTimeout bounds DNS and connect when
	// Options.ConnectTimeout is zero.
	DefaultConnectTimeout = 10 * time.Second
	// DefaultReadBufferSize is the largest chunk one read returns when
	// Options.ReadBufferSize is zero.
	DefaultReadBufferSize = 64 << 10
)

// State is where a connection is in its life.
type State string

// The states, in the order they are reached. A connection that fails to
// connect goes from connecting straight to closed; one the peer closes or
// that fails goes from open straight to closed.
const (
	StateConnecting State = "connecting" // dialing
	StateOpen       State = "open"       // bytes flow
	StateClosing    State = "closing"    // Close was called
	StateClosed     State = "closed"     // the socket is closed
)

// Initiator says who ended a connection.
type Initiator string

const (
	// InitiatorNone: nobody chose to; connecting or the transport failed.
	InitiatorNone Initiator = "none"
	// InitiatorLocal: Close was called, or connecting was cancelled.
	InitiatorLocal Initiator = "local"
	// InitiatorRemote: the peer closed its side (EOF) or reset the
	// connection.
	InitiatorRemote Initiator = "remote"
)

// Direction says which way a chunk went.
type Direction string

const (
	Sent     Direction = "sent"
	Received Direction = "received"
)

// Chunk is bytes sent with one Send, or returned by one read. With a
// framing (Options.Framing) it is one message instead, without its length
// prefix or line delimiter, in both directions.
type Chunk struct {
	Direction Direction
	// Data is owned by the receiver of the chunk.
	Data []byte
	// At is when the read returned, or when the bytes were handed to the
	// socket.
	At time.Time
	// Incomplete marks the bytes of a message the connection ended in the
	// middle of (framing only): received, but never finished.
	Incomplete bool
}

// CloseInfo describes how a connection ended.
type CloseInfo struct {
	InitiatedBy Initiator
	// Err is the failure, or nil when the connection was closed normally
	// (by Close, or by the peer's orderly close).
	Err error
	// Bytes written to and read from the socket (with framing and TLS
	// records' payload, not their overhead), and the chunks (messages with
	// a framing) passed to OnData.
	BytesSent, BytesReceived   int64
	ChunksSent, ChunksReceived int64
	// OpenedAt is zero when the connection never opened.
	OpenedAt, ClosedAt time.Time
}

// StateChange is passed to Options.OnState.
type StateChange struct {
	State State
	At    time.Time
	// LocalAddr and RemoteAddr are set from StateOpen on.
	LocalAddr, RemoteAddr string
	// TLS describes the TLS session from StateOpen on, nil without TLS.
	TLS *httpclient.TLSInfo
	// Close is set on StateClosed.
	Close *CloseInfo
	// Err is set on StateClosed when the connection failed; the same as
	// Close.Err.
	Err error
}

// Options configure Connect.
type Options struct {
	// Address is host:port; see ParseAddress.
	Address string
	// Dialer opens the connection. Required.
	Dialer netx.Dialer
	// ConnectTimeout bounds the dial and the TLS handshake: zero means
	// DefaultConnectTimeout, negative means no limit besides the context.
	ConnectTimeout time.Duration
	// TLS, when set, runs a TLS handshake over the dialed connection
	// before it opens (TCP-02).
	TLS *TLSOptions
	// Framing splits received bytes into messages and frames sent ones
	// (TCP-03). The zero value is no framing: chunks as read.
	Framing framing.Options
	// ReadBufferSize is the largest chunk a read returns: zero means
	// DefaultReadBufferSize.
	ReadBufferSize int
	// OnState is called with every state change, in order. OnData is
	// called with every chunk: a sent chunk just before its bytes are
	// written, so it always comes before any reply to it, and a received
	// chunk after each read. Calls to both are serialized: one at a time,
	// in the order of the events. Reading waits for OnData (backpressure).
	// They must not call Send, CloseWrite or Close.
	OnState func(StateChange)
	OnData  func(Chunk)
	// Now is the clock for timestamps. Default time.Now.
	Now func() time.Time
}

// TLSOptions configure TLS over TCP. The zero value verifies the server
// against the system's certificate authorities, sends the host of the
// address as SNI (none for an IP address) and sends no client
// certificate.
type TLSOptions struct {
	// ServerName is the name sent in SNI and checked against the server's
	// certificate; empty means the host of Options.Address.
	ServerName string
	// RootCAs are the trusted authorities; nil means the system's.
	RootCAs *x509.CertPool
	// InsecureSkipVerify accepts any server certificate.
	InsecureSkipVerify bool
	// ClientCertificate is sent when the server asks for one (mTLS); nil
	// sends none.
	ClientCertificate *httpclient.ClientCert
}

// Conn is an open, or once open, TCP connection. Its methods are safe for
// concurrent use.
type Conn struct {
	o       Options
	nc      net.Conn // the TLS connection with TLS
	raw     net.Conn // the dialed connection
	tls     *httpclient.TLSInfo
	now     func() time.Time
	done    chan struct{}
	writeMu sync.Mutex // serializes Send: chunk reported, then written

	// certRequested: the server asked for a client certificate in the
	// TLS handshake, so a TLS alert before the first byte arrives is its
	// verdict on the certificate (TLS 1.3 sends it after the handshake).
	// Set before the read goroutine starts.
	certRequested, certSent bool

	cbMu sync.Mutex // serializes callbacks and state changes

	mu          sync.Mutex
	state       State
	writeClosed bool
	// reason is set once, before the socket is closed by this side.
	reason *closeReason
	info   *CloseInfo
	stats  CloseInfo // counters and OpenedAt
	local  string
	remote string
}

type closeReason struct {
	initiator Initiator
	err       error
}

// Connect dials o.Address through o.Dialer and returns the open connection,
// which starts reading at once. ctx bounds connecting only; cancel it to
// abort. OnState gets connecting, then open, or closed when connecting
// fails (then Connect returns the error too).
//
// Invalid options fail before anything is reported: CodeInvalidAddress,
// framing.invalid_options. Dial and TLS errors are the netx errors
// (netx.dial_timeout for ConnectTimeout, netx.dial_cancelled for ctx,
// netx.connection_refused, netx.tls_unknown_authority, …).
func Connect(ctx context.Context, o Options) (*Conn, error) {
	if o.Dialer == nil {
		panic("tcpclient: Options.Dialer is nil")
	}
	host, _, err := ParseAddress(o.Address)
	if err != nil {
		return nil, err
	}
	if o.Framing, err = o.Framing.Normalize(); err != nil {
		return nil, err
	}
	c := &Conn{o: o, now: o.Now, done: make(chan struct{}), state: StateConnecting}
	if c.now == nil {
		c.now = time.Now
	}
	c.emitState(StateChange{State: StateConnecting, At: c.now()})

	dctx, cancel := ctx, context.CancelFunc(func() {})
	switch {
	case o.ConnectTimeout == 0:
		dctx, cancel = context.WithTimeout(ctx, DefaultConnectTimeout)
	case o.ConnectTimeout > 0:
		dctx, cancel = context.WithTimeout(ctx, o.ConnectTimeout)
	}
	nc, err := o.Dialer.DialContext(dctx, "tcp", o.Address)
	if err == nil && nc == nil {
		err = errs.New(errs.Internal, "The dialer returned no connection.")
	}
	if err != nil {
		err = netx.WrapDialError(err, "tcp", o.Address)
	}
	c.raw = nc
	if err == nil && o.TLS != nil {
		nc, err = c.handshake(dctx, nc, host)
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
		c.callState(StateChange{State: StateClosed, At: info.ClosedAt, Close: &info, Err: err})
		close(c.done)
		c.cbMu.Unlock()
		return nil, err
	}

	c.cbMu.Lock()
	c.mu.Lock()
	c.nc = nc
	c.state = StateOpen
	c.stats.OpenedAt = c.now()
	if a := nc.LocalAddr(); a != nil {
		c.local = a.String()
	}
	if a := nc.RemoteAddr(); a != nil {
		c.remote = a.String()
	}
	ch := StateChange{State: StateOpen, At: c.stats.OpenedAt, LocalAddr: c.local, RemoteAddr: c.remote, TLS: c.tls}
	c.mu.Unlock()
	c.callState(ch)
	c.cbMu.Unlock()

	go c.readLoop()
	return c, nil
}

// Address returns the address the connection was made to.
func (c *Conn) Address() string { return c.o.Address }

// TLS describes the TLS session, nil without TLS.
func (c *Conn) TLS() *httpclient.TLSInfo { return c.tls }

// LocalAddr and RemoteAddr return the socket's addresses.
func (c *Conn) LocalAddr() string  { return c.local }
func (c *Conn) RemoteAddr() string { return c.remote }

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

// Send writes b to the connection, framed as Options.Framing says (a
// message too long for its length prefix fails with
// framing.message_too_large and leaves the connection open). An empty b
// writes nothing without a framing, and an empty message with one. OnData
// gets the chunk (the message, without framing) just before it is written.
// A failed write fails the connection
// (it closes with that error) and is returned. A write blocked on a peer
// that does not read is released by Close.
func (c *Conn) Send(b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	switch {
	case c.state != StateOpen:
		c.mu.Unlock()
		return notOpen()
	case c.writeClosed:
		c.mu.Unlock()
		return errs.New(errs.InvalidArgument, "Sending was closed on this TCP connection.").
			WithCode(CodeWriteClosed).
			WithHint("Reconnect to send more.")
	}
	c.mu.Unlock()
	if len(b) == 0 && !c.o.Framing.Framed() {
		return nil
	}
	wire, err := framing.Encode(c.o.Framing, b)
	if err != nil {
		return err
	}

	data := append([]byte(nil), b...)
	c.cbMu.Lock()
	if c.o.OnData != nil {
		c.o.OnData(Chunk{Direction: Sent, Data: data, At: c.now()})
	}
	c.cbMu.Unlock()

	n, err := c.nc.Write(wire)
	c.mu.Lock()
	c.stats.BytesSent += int64(n)
	c.stats.ChunksSent++
	c.mu.Unlock()
	if err != nil {
		mapped := writeError(err, c.o.Address)
		if c.fail(&closeReason{initiator: initiatorOf(mapped), err: mapped}) {
			return mapped
		}
		// Closed by this side or the peer meanwhile: that is the reason.
		return notOpen()
	}
	return nil
}

// CloseWrite shuts down the sending side (a TCP FIN, after a TLS
// close_notify with TLS): the peer reads EOF and the connection stays open
// for reading until the peer closes too. Send fails afterwards with
// CodeWriteClosed.
func (c *Conn) CloseWrite() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateOpen {
		return notOpen()
	}
	if c.writeClosed {
		return nil
	}
	hc, ok := c.raw.(interface{ CloseWrite() error })
	if !ok {
		return errs.New(errs.Unsupported, "This connection cannot close only its sending side.").
			WithCode(CodeHalfCloseUnsupported).
			WithHint("Close the whole connection instead.")
	}
	if tc, ok := c.nc.(*tls.Conn); ok {
		// close_notify first, so the peer sees the TLS stream end, then
		// the TCP FIN.
		if err := tc.CloseWrite(); err != nil {
			return writeError(err, c.o.Address)
		}
	}
	if err := hc.CloseWrite(); err != nil {
		return writeError(err, c.o.Address)
	}
	c.writeClosed = true
	return nil
}

// Close closes the connection: StateClosing, then StateClosed once the
// read goroutine has stopped. It returns when the connection is closed.
// Closing a closed connection does nothing.
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
	ch := StateChange{State: StateClosing, At: c.now(), LocalAddr: c.local, RemoteAddr: c.remote}
	c.mu.Unlock()
	c.callState(ch)
	c.cbMu.Unlock()

	_ = c.nc.Close()
	<-c.done
	return nil
}

// fail records r as the reason the connection ends, unless one was
// recorded, and closes the socket so the read goroutine finishes. It
// reports whether r was recorded.
func (c *Conn) fail(r *closeReason) bool {
	c.mu.Lock()
	if c.reason != nil || c.state != StateOpen {
		c.mu.Unlock()
		return false
	}
	c.reason = r
	c.mu.Unlock()
	_ = c.nc.Close()
	return true
}

func (c *Conn) readLoop() {
	size := c.o.ReadBufferSize
	if size <= 0 {
		size = DefaultReadBufferSize
	}
	buf := make([]byte, size)
	split := framing.NewSplitter(c.o.Framing)
	for {
		n, err := c.nc.Read(buf)
		if n > 0 {
			at := c.now()
			msgs, ferr := split.Feed(buf[:n])
			c.mu.Lock()
			c.stats.BytesReceived += int64(n)
			c.mu.Unlock()
			c.deliver(msgs, at, false)
			if ferr != nil {
				// The stream cannot be split any further: the connection
				// fails with the framing error.
				c.fail(&closeReason{initiator: InitiatorNone, err: ferr})
				c.finish(ferr)
				return
			}
		}
		if err != nil {
			if rest := split.Rest(); rest != nil {
				c.deliver([][]byte{rest}, c.now(), true)
			}
			c.finish(err)
			return
		}
	}
}

// deliver passes received messages (or chunks) to OnData.
func (c *Conn) deliver(msgs [][]byte, at time.Time, incomplete bool) {
	if len(msgs) == 0 {
		return
	}
	c.mu.Lock()
	c.stats.ChunksReceived += int64(len(msgs))
	c.mu.Unlock()
	c.cbMu.Lock()
	defer c.cbMu.Unlock()
	if c.o.OnData == nil {
		return
	}
	for _, m := range msgs {
		c.o.OnData(Chunk{Direction: Received, Data: m, At: at, Incomplete: incomplete})
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
		switch {
		case isEOF(err):
			r = &closeReason{initiator: InitiatorRemote}
		default:
			mapped := c.readError(err)
			r = &closeReason{initiator: initiatorOf(mapped), err: mapped}
		}
		c.reason = r
	}
	info := c.stats
	info.InitiatedBy, info.Err, info.ClosedAt = r.initiator, r.err, c.now()
	c.info = &info
	c.state = StateClosed
	ch := StateChange{State: StateClosed, At: info.ClosedAt, LocalAddr: c.local, RemoteAddr: c.remote, Close: &info, Err: r.err}
	c.mu.Unlock()
	c.callState(ch)
	close(c.done)
}

// initiatorOf is remote for a reset by the peer, else none.
func initiatorOf(err *errs.Error) Initiator {
	if err.Code == CodeConnectionReset {
		return InitiatorRemote
	}
	return InitiatorNone
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
