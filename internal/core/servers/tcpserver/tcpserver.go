// Package tcpserver is the raw TCP server type (SSRV-05): it accepts TCP
// connections, keeps the list of open ones, logs the bytes each sends as
// they arrive, sends bytes to one connection and closes a connection. There
// is no framing: a received message is whatever one read returned, like the
// raw TCP client (tcpclient). See docs/servers.md.
package tcpserver

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers"
)

// TypeName is the Spec.Type of the TCP server.
const TypeName = "tcp"

// Defaults of Options.
const (
	// DefaultWriteTimeout bounds one Send to a peer that does not read.
	DefaultWriteTimeout = 10 * time.Second
	// DefaultReadBufferSize is the most bytes one read (one message event)
	// returns; it is lowered to the sink's content limit, so a received
	// message is never cut.
	DefaultReadBufferSize = 32 << 10
)

// Who closed a connection (servers.TCPInfo.InitiatedBy).
const (
	InitiatedByClient = "client"
	InitiatedByServer = "server"
)

// Error codes of the connection operations.
const (
	CodeConnectionNotFound = "tcpserver.connection_not_found"
	CodeSendFailed         = "tcpserver.send_failed"
	CodeEmptyMessage       = "tcpserver.empty_message"
)

// Options change limits, for tests.
type Options struct {
	// WriteTimeout: 0 means DefaultWriteTimeout.
	WriteTimeout time.Duration
	// ReadBufferSize: 0 means DefaultReadBufferSize.
	ReadBufferSize int
}

// Type is the TCP server type.
type Type struct {
	opts Options
}

// New returns the TCP server type.
func New(opts Options) Type {
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}
	if opts.ReadBufferSize <= 0 {
		opts.ReadBufferSize = DefaultReadBufferSize
	}
	return Type{opts: opts}
}

func (Type) Name() string  { return TypeName }
func (Type) Label() string { return "TCP" }

// Start accepts TCP connections on ln.
func (t Type) Start(_ servers.Spec, ln net.Listener, sink servers.Sink) (servers.Instance, error) {
	bufSize := t.opts.ReadBufferSize
	if limit := sink.ContentLimit(); limit > 0 && limit < bufSize {
		bufSize = limit
	}
	inst := &Instance{
		ln:      ln,
		sink:    sink,
		opts:    t.opts,
		bufSize: bufSize,
		conns:   map[string]*conn{},
		done:    make(chan struct{}),
	}
	go inst.accept()
	return inst, nil
}

// Connection is a snapshot of an open connection.
type Connection struct {
	// ID identifies the connection within one run of the server: its
	// connection ID ("1", "2", …), as in the traffic.
	ID     string
	ConnID uint64
	// Peer is the client's address, Local the server's end.
	Peer        string
	Local       string
	ConnectedAt time.Time
	// Bytes and messages (reads, sends) received from and sent to the
	// client.
	BytesIn     uint64
	BytesOut    uint64
	MessagesIn  uint64
	MessagesOut uint64
}

// Hub is the running TCP server's connection operations, implemented by
// *Instance.
type Hub interface {
	Connections() []Connection
	Send(connectionID string, data []byte) error
	Close(connectionID string) error
}

var _ Hub = (*Instance)(nil)

// Instance is one run of a TCP server. It implements servers.Instance and
// Hub.
type Instance struct {
	ln      net.Listener
	sink    servers.Sink
	opts    Options
	bufSize int

	mu       sync.Mutex
	conns    map[string]*conn
	nextID   uint64
	stopping bool
	running  sync.WaitGroup // connection goroutines, added under mu while not stopping

	done chan struct{} // closed when the accept loop ends
	err  error         // why accepting ended on its own; set before done closes

	stopOnce sync.Once
}

// conn is one open connection.
type conn struct {
	info Connection // fixed fields
	nc   net.Conn

	bytesIn, bytesOut, messagesIn, messagesOut atomic.Uint64

	// sendMu keeps one write (and its event) at a time.
	sendMu sync.Mutex
	// closedBy is who closed the connection on the server side:
	// closedByServer (Close) or closedByStop; 0 when the server did not.
	closedBy atomic.Int32

	done chan struct{} // closed once the connection is gone and logged
}

const (
	closedByServer int32 = iota + 1
	closedByStop
)

func (c *conn) snapshot() Connection {
	s := c.info
	s.BytesIn = c.bytesIn.Load()
	s.BytesOut = c.bytesOut.Load()
	s.MessagesIn = c.messagesIn.Load()
	s.MessagesOut = c.messagesOut.Load()
	return s
}

func (i *Instance) Done() <-chan struct{} { return i.done }
func (i *Instance) Err() error            { return i.err }

// Stop stops accepting and closes every connection, then returns once each
// is logged as closed. Raw TCP has no work in progress to wait for, so ctx
// is not needed. Safe to call more than once.
func (i *Instance) Stop(context.Context) error {
	i.stopOnce.Do(func() {
		i.mu.Lock()
		i.stopping = true
		list := i.sortedConns()
		i.mu.Unlock()
		_ = i.ln.Close()
		<-i.done
		for _, c := range list {
			c.closedBy.CompareAndSwap(0, closedByStop)
			_ = c.nc.Close()
		}
		i.running.Wait()
	})
	return nil
}

// accept takes connections until the listener is closed.
func (i *Instance) accept() {
	defer close(i.done)
	for {
		nc, err := i.ln.Accept()
		if err != nil {
			i.mu.Lock()
			stopping := i.stopping
			i.mu.Unlock()
			if !stopping && !errors.Is(err, net.ErrClosed) {
				i.err = err
			}
			return
		}
		i.mu.Lock()
		if i.stopping {
			i.mu.Unlock()
			_ = nc.Close()
			return
		}
		i.nextID++
		id := strconv.FormatUint(i.nextID, 10)
		c := &conn{
			info: Connection{
				ID: id, ConnID: i.nextID, Peer: nc.RemoteAddr().String(), Local: nc.LocalAddr().String(),
				ConnectedAt: time.Now(),
			},
			nc:   nc,
			done: make(chan struct{}),
		}
		i.conns[id] = c
		i.running.Add(1)
		// Logged under mu, so it comes before anything of the connection.
		i.sink.Emit(servers.Event{
			Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.System, Kind: servers.KindConnectionOpen,
			Summary: "Connection " + id + " opened", TCP: &servers.TCPInfo{},
		})
		i.mu.Unlock()
		go i.read(c)
	}
}

// sortedConns lists the connections by ID; the caller holds mu.
func (i *Instance) sortedConns() []*conn {
	list := make([]*conn, 0, len(i.conns))
	for _, c := range i.conns {
		list = append(list, c)
	}
	slices.SortFunc(list, func(a, b *conn) int { return cmp.Compare(a.info.ConnID, b.info.ConnID) })
	return list
}

// Connections returns the open connections, oldest first.
func (i *Instance) Connections() []Connection {
	i.mu.Lock()
	list := i.sortedConns()
	i.mu.Unlock()
	out := make([]Connection, 0, len(list))
	for _, c := range list {
		out = append(out, c.snapshot())
	}
	return out
}

func (i *Instance) conn(id string) (*conn, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	c, ok := i.conns[id]
	if !ok {
		return nil, notFound(id)
	}
	return c, nil
}

func notFound(id string) *errs.Error {
	return errs.New(errs.NotFound, "This connection is closed.").
		WithCode(CodeConnectionNotFound).WithDetail("connectionId", id)
}

// Send writes data to the connection id as it is. The message is logged
// before it is written, so it comes before the client's answer; writes to
// one connection are serialized and bounded by the write timeout. A failed
// write is logged as an error and closes the connection.
func (i *Instance) Send(connectionID string, data []byte) error {
	if len(data) == 0 {
		return errs.New(errs.InvalidArgument, "There is nothing to send.").
			WithCode(CodeEmptyMessage).WithHint("Type a message first.")
	}
	c, err := i.conn(connectionID)
	if err != nil {
		return err
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	select {
	case <-c.done:
		return notFound(connectionID)
	default:
	}
	i.sink.Emit(servers.Event{
		Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.Out, Kind: servers.KindMessage,
		Summary: fmt.Sprintf("%s to connection %s", byteCount(len(data)), c.info.ID),
		Content: data, TCP: &servers.TCPInfo{},
	})
	_ = c.nc.SetWriteDeadline(time.Now().Add(i.opts.WriteTimeout))
	n, err := c.nc.Write(data)
	_ = c.nc.SetWriteDeadline(time.Time{})
	c.bytesOut.Add(uint64(n))
	if err != nil {
		err = errs.Wrap(err, errs.KindOf(err), fmt.Sprintf("The bytes could not be sent to connection %s.", c.info.ID)).
			WithCode(CodeSendFailed)
		i.sink.Emit(servers.Event{
			Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.System, Kind: servers.KindError,
			Summary: messageOf(err), TCP: &servers.TCPInfo{},
		})
		_ = c.nc.Close() // the reader logs the close
		return err
	}
	c.messagesOut.Add(1)
	return nil
}

// Close closes the connection id and returns once it is logged as closed.
func (i *Instance) Close(connectionID string) error {
	c, err := i.conn(connectionID)
	if err != nil {
		return err
	}
	c.closedBy.CompareAndSwap(0, closedByServer)
	_ = c.nc.Close()
	<-c.done
	return nil
}

// read logs what the client sends until the connection ends.
func (i *Instance) read(c *conn) {
	defer i.running.Done()
	if tc, ok := c.nc.(*tls.Conn); ok {
		// A TLS server (SRV-05): the handshake comes first, so a client
		// that fails it is logged as such.
		_ = tc.SetDeadline(time.Now().Add(HandshakeTimeout))
		if err := tc.Handshake(); err != nil {
			i.finish(c, handshakeError{err})
			return
		}
		_ = tc.SetDeadline(time.Time{})
	}
	buf := make([]byte, i.bufSize)
	for {
		n, err := c.nc.Read(buf)
		if n > 0 {
			c.bytesIn.Add(uint64(n))
			c.messagesIn.Add(1)
			i.sink.Emit(servers.Event{
				Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.In, Kind: servers.KindMessage,
				Summary: fmt.Sprintf("%s from connection %s", byteCount(n), c.info.ID),
				Content: slices.Clone(buf[:n]), TCP: &servers.TCPInfo{},
			})
		}
		if err != nil {
			i.finish(c, err)
			return
		}
	}
}

// finish closes the connection, forgets it and logs how it ended.
func (i *Instance) finish(c *conn, readErr error) {
	_ = c.nc.Close()
	i.mu.Lock()
	delete(i.conns, c.info.ID)
	i.mu.Unlock()

	info := &servers.TCPInfo{BytesIn: c.bytesIn.Load(), BytesOut: c.bytesOut.Load()}
	var summary string
	switch {
	case c.closedBy.Load() == closedByStop:
		info.InitiatedBy = InitiatedByServer
		summary = "Closed: the server is stopping"
	case c.closedBy.Load() == closedByServer:
		info.InitiatedBy = InitiatedByServer
		summary = "Closed by the server"
	case errors.Is(readErr, io.EOF):
		info.InitiatedBy = InitiatedByClient
		summary = "Closed by the client"
	case errors.Is(readErr, syscall.ECONNRESET):
		info.InitiatedBy = InitiatedByClient
		summary = "Reset by the client"
	default:
		i.sink.Emit(servers.Event{
			Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.System, Kind: servers.KindError,
			Summary: fmt.Sprintf("Connection %s failed: %v", c.info.ID, readErr), TCP: &servers.TCPInfo{},
		})
		summary = "Connection lost"
	}
	summary = fmt.Sprintf("%s (%s received, %s sent)", summary, byteCount(int(info.BytesIn)), byteCount(int(info.BytesOut)))
	c.sendMu.Lock() // a send in progress logs before the connection is gone
	i.sink.Emit(servers.Event{
		Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.System, Kind: servers.KindConnectionClose,
		Summary: summary, TCP: info,
	})
	close(c.done)
	c.sendMu.Unlock()
}

// HandshakeTimeout bounds the TLS handshake of a connection to a TLS
// server.
const HandshakeTimeout = 30 * time.Second

// handshakeError is a failed TLS handshake.
type handshakeError struct{ err error }

func (e handshakeError) Error() string { return servers.HandshakeSummary(e.err.Error()) }
func (e handshakeError) Unwrap() error { return e.err }

// byteCount is "1 byte", "12 bytes".
func byteCount(n int) string {
	if n == 1 {
		return "1 byte"
	}
	return strconv.Itoa(n) + " bytes"
}

func messageOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Message != "" {
		if cause := errors.Unwrap(e); cause != nil {
			return e.Message + " " + cause.Error()
		}
		return e.Message
	}
	return err.Error()
}
