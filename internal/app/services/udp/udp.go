// Package udp is the bound service for UDP requests (UDP-01): the UI opens
// a UDP socket to host:port, directly or through a SOCKS5 proxy's UDP
// ASSOCIATE relay (SOCKS-07), sends datagrams as text (with {{variables}}),
// hex or Base64, and receives datagrams with their sender as udp:state
// and udp:data events. The protocol work is
// internal/core/realtime/udpclient; this service owns the open sockets,
// turns their callbacks into batched events and closes them when the app
// quits or the workspace changes. See docs/udp.md.
package udp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/services/websocket"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/hexbytes"
	"github.com/sorrelfox/postsonde/internal/core/realtime/udpclient"
	"github.com/sorrelfox/postsonde/internal/core/stream"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Error codes of the service. Socket errors keep their udpclient.*,
// netx.*, hexbytes.*, ws.invalid_base64 and template.* codes.
const (
	// CodeNotFound: Send to a socket that is closed or never existed
	// (not_found).
	CodeNotFound = "udp.not_found"
	// CodeNotOpen: Send while the socket is still opening
	// (invalid_argument).
	CodeNotOpen = "udp.not_open"
	// CodeInvalidMode: Send with a mode other than text, hex or base64
	// (invalid_argument).
	CodeInvalidMode = "udp.invalid_mode"
	// CodeInvalidIPVersion: an IP version other than "", ipv4 or ipv6
	// (invalid_argument).
	CodeInvalidIPVersion = "udp.invalid_ip_version"
)

// Batching of one socket's events: a flush every 50 ms at most, and at
// most maxItems datagrams (of up to 64 KiB) per flush.
const (
	maxItems = 16
	// shutdownWait bounds how long ServiceShutdown and the workspace hook
	// wait for sockets to close.
	shutdownWait = 2 * time.Second
)

// Scopes returns the variable scopes of udp request requestID, as for an
// HTTP send. It is called on every open and send, so switching the
// environment changes the next one.
type Scopes func(ctx context.Context, requestID string) (template.Chain, error)

// UDPService opens UDP sockets for the UI.
type UDPService struct {
	dialer   netx.Dialer
	emit     func(name string, data any)
	logger   *slog.Logger
	interval time.Duration // batch interval; 0 = stream default
	scopes   Scopes
	// tweak changes the client options of every socket, for tests.
	tweak func(*udpclient.Options)

	mu    sync.Mutex
	conns map[string]*connection
}

// Option configures a UDPService.
type Option func(*UDPService)

// WithScopes renders {{variables}} in the address and in text datagrams
// with the scopes scopes returns. Without it (and for a socket with no
// request ID) no variable is defined.
func WithScopes(scopes Scopes) Option { return func(s *UDPService) { s.scopes = scopes } }

// withInterval sets the event batching interval, for tests.
func withInterval(d time.Duration) Option { return func(s *UDPService) { s.interval = d } }

// withClientOptions changes the client options of every socket, for tests.
func withClientOptions(f func(*udpclient.Options)) Option {
	return func(s *UDPService) { s.tweak = f }
}

// New returns a UDPService that opens sockets through dialer (the app's,
// with the proxy in effect, the guard and the connection log) and sends
// events with emit (nil drops them). A nil logger means slog.Default().
func New(dialer netx.Dialer, emit func(name string, data any), logger *slog.Logger, opts ...Option) *UDPService {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(string, any) {}
	}
	s := &UDPService{dialer: dialer, emit: emit, logger: logger.With("service", Domain), conns: map[string]*connection{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *UDPService) ServiceName() string { return Domain }

// ServiceShutdown closes every socket when the app quits, waiting up to
// two seconds.
func (s *UDPService) ServiceShutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	_, _ = s.CloseAll(ctx)
	return nil
}

// Open renders the {{variables}} of in.Address and starts opening a
// socket to it, returning the socket's ID at once. The outcome arrives as
// udp:state events: opening, then open, or closed with the error. It
// fails at once only for input that cannot be used: an undefined variable
// (template.unresolved), an invalid address (udpclient.invalid_address),
// IP version (udp.invalid_ip_version), family or maximum size.
func (s *UDPService) Open(ctx context.Context, in OpenInputDTO) (ConnectionDTO, error) {
	if err := ctx.Err(); err != nil {
		return ConnectionDTO{}, errs.Wrap(err, errs.KindOf(err), "Opening was interrupted.")
	}
	address, err := s.render(ctx, in.RequestID, in.Address)
	if err != nil {
		return ConnectionDTO{}, err
	}
	network := "udp"
	switch in.IPVersion {
	case "":
	case "ipv4":
		network = "udp4"
	case "ipv6":
		network = "udp6"
	default:
		return ConnectionDTO{}, errs.New(errs.InvalidArgument, "The IP version is not one PostSonde knows.").
			WithCode(CodeInvalidIPVersion).
			WithHint(`Use "ipv4", "ipv6" or either.`).
			WithDetail("ipVersion", in.IPVersion)
	}
	o := udpclient.Options{Address: address, Dialer: s.dialer, Network: network, Connected: in.Connected}
	if in.MaxDatagramBytes != nil {
		o.MaxDatagramSize = -1 // out of range unless it fits below
		if n := *in.MaxDatagramBytes; n > 0 && n <= udpclient.MaxDatagramSize {
			o.MaxDatagramSize = int(n)
		}
	}
	if in.OpenTimeoutMs != nil {
		o.OpenTimeout = time.Duration(*in.OpenTimeoutMs) * time.Millisecond
		if *in.OpenTimeoutMs <= 0 {
			o.OpenTimeout = -1
		}
	}
	// Checked here, so input that cannot be used fails Open itself.
	if err := udpclient.Check(o); err != nil {
		if e, ok := errors.AsType[*errs.Error](err); ok && e.Code == udpclient.CodeInvalidOptions && in.MaxDatagramBytes != nil {
			return ConnectionDTO{}, e.WithDetail("value", strconv.FormatInt(*in.MaxDatagramBytes, 10))
		}
		return ConnectionDTO{}, err
	}

	id := newConnID()
	// The socket outlives this call, so it gets a context of its own,
	// marked as the user's open so the dialer allows it (PRV-05).
	cctx, cancel := context.WithCancel(netx.WithUserOp(context.Background(), netx.UserOp{Kind: Domain + ".open", ID: id}))
	c := &connection{
		id:        id,
		requestID: in.RequestID,
		address:   address,
		cancel:    cancel,
		emit:      s.emit,
		done:      make(chan struct{}),
		opened:    make(chan struct{}),
	}
	c.batch = stream.New(context.Background(), stream.Options[item]{Interval: s.interval, MaxItems: maxItems}, c.flush)
	s.mu.Lock()
	s.conns[c.id] = c
	s.mu.Unlock()

	o.OnState, o.OnDatagram, o.OnNote = c.onState, c.onDatagram, c.onNote
	if s.tweak != nil {
		s.tweak(&o)
	}
	go s.run(cctx, c, o)
	return ConnectionDTO{ConnID: c.id, RequestID: c.requestID, Address: c.address}, nil
}

// render renders text with the variables of request requestID.
func (s *UDPService) render(ctx context.Context, requestID, text string) (string, error) {
	var chain template.Chain
	if s.scopes != nil && requestID != "" {
		c, err := s.scopes(ctx, requestID)
		if err != nil {
			return "", err
		}
		chain = c
	}
	res := template.Resolver{Scopes: chain}.Render(text)
	if err := res.Err(); err != nil {
		return "", err
	}
	return res.Text, nil
}

// run opens, waits for the socket to end, sends the last events and
// forgets it.
func (s *UDPService) run(ctx context.Context, c *connection, o udpclient.Options) {
	defer func() {
		c.cancel()
		c.batch.Close()
		s.mu.Lock()
		delete(s.conns, c.id)
		s.mu.Unlock()
		close(c.done)
	}()
	conn, err := udpclient.Open(ctx, o)
	if err != nil {
		close(c.opened)
		s.logger.Info("udp open failed", "conn", c.id, "address", c.address, "err", err)
		return
	}
	s.logger.Info("udp open", "conn", c.id, "address", c.address, "via", c.via)
	c.mu.Lock()
	c.conn = conn
	closing := c.closing
	c.mu.Unlock()
	close(c.opened)
	if closing {
		_ = conn.Close()
	}
	<-conn.Done()
	info := conn.CloseInfo()
	s.logger.Info("udp closed", "conn", c.id, "initiatedBy", info.InitiatedBy, "err", info.Err,
		"datagramsSent", info.DatagramsSent, "datagramsReceived", info.DatagramsReceived)
}

// Send sends in.Text as one datagram: UTF-8 with its {{variables}}
// rendered in text mode, or the bytes it spells in hex or base64 mode
// (hexbytes.* or ws.invalid_base64 errors with the position of a bad
// character). An empty datagram is allowed. The datagram also arrives as
// a sent one in udp:data. A failed write is returned; the socket stays
// open.
func (s *UDPService) Send(ctx context.Context, in SendInputDTO) (SendResultDTO, error) {
	if err := ctx.Err(); err != nil {
		return SendResultDTO{}, errs.Wrap(err, errs.KindOf(err), "Sending was interrupted.")
	}
	c := s.get(in.ConnID)
	if c == nil {
		if _, err := s.encode(ctx, "", in); err != nil {
			return SendResultDTO{}, err
		}
		return SendResultDTO{}, errs.New(errs.NotFound, "The UDP socket is closed.").
			WithCode(CodeNotFound).
			WithHint("Open it again to send.").
			WithDetail("connId", in.ConnID)
	}
	payload, err := s.encode(ctx, c.requestID, in)
	if err != nil {
		return SendResultDTO{}, err
	}
	conn := c.openConn()
	if conn == nil {
		return SendResultDTO{}, errs.New(errs.InvalidArgument, "The UDP socket is not open yet.").
			WithCode(CodeNotOpen).
			WithHint("Wait until it is open.")
	}
	if err := conn.Send(payload); err != nil {
		return SendResultDTO{}, err
	}
	return SendResultDTO{Bytes: len(payload)}, nil
}

// encode turns the composer's text into the datagram.
func (s *UDPService) encode(ctx context.Context, requestID string, in SendInputDTO) ([]byte, error) {
	switch in.Mode {
	case "", "text":
		text, err := s.render(ctx, requestID, in.Text)
		if err != nil {
			return nil, err
		}
		return []byte(text), nil
	case "hex":
		return hexbytes.Parse(in.Text)
	case "base64":
		return websocket.ParseBase64(in.Text)
	}
	return nil, errs.New(errs.InvalidArgument, "The send mode is not one PostSonde knows.").
		WithCode(CodeInvalidMode).
		WithHint(`Use "text", "hex" or "base64".`).
		WithDetail("mode", in.Mode)
}

// Close closes the socket (or stops opening) and returns once it is
// closed and its last events are sent, or when ctx ends. Closing a socket
// that is already closed, or unknown, does nothing.
func (s *UDPService) Close(ctx context.Context, connID string) error {
	c := s.get(connID)
	if c == nil {
		return nil
	}
	c.close()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "Waiting for the UDP socket to close was interrupted.")
	}
}

// CloseAll closes every socket, as Close does, and returns how many there
// were.
func (s *UDPService) CloseAll(ctx context.Context) (int, error) {
	s.mu.Lock()
	all := make([]*connection, 0, len(s.conns))
	for _, c := range s.conns {
		all = append(all, c)
	}
	s.mu.Unlock()
	for _, c := range all {
		c.close()
	}
	for _, c := range all {
		select {
		case <-c.done:
		case <-ctx.Done():
			return len(all), errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "Waiting for the UDP sockets to close was interrupted.")
		}
	}
	return len(all), nil
}

// OnWorkspaceChanged closes every socket: the app calls it when another
// project is opened or the project is closed. It is not bound (it takes no
// context), so it is not a method.
func OnWorkspaceChanged(s *UDPService) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	if n, err := s.CloseAll(ctx); n > 0 {
		s.logger.Info("udp sockets closed for a workspace change", "count", n, "err", err)
	}
}

func (s *UDPService) get(id string) *connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[id]
}

func newConnID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "udp_" + hex.EncodeToString(b[:])
}

// connection is one socket's state in the service.
type connection struct {
	id, requestID, address string
	cancel                 context.CancelFunc
	emit                   func(string, any)
	batch                  *stream.Batcher[item]
	done                   chan struct{}
	via                    string // set when open, read by run's log line
	// opened is closed once udpclient.Open returned, open or not.
	opened chan struct{}

	mu      sync.Mutex
	conn    *udpclient.Conn // set once Open returned it
	open    bool            // the client reported open
	closing bool

	seq int64 // used by flush only (one goroutine)
}

// item is a state change or a datagram, in the order they happened.
type item struct {
	state    *StateEventDTO
	datagram *DatagramDTO
}

// openConn returns the socket once it is open, nil before. The client
// reports open from inside udpclient.Open, and the open event can reach
// the UI before Open returned the socket: a send in between waits for it.
func (c *connection) openConn() *udpclient.Conn {
	c.mu.Lock()
	conn, open := c.conn, c.open
	c.mu.Unlock()
	if conn == nil && open {
		<-c.opened
		c.mu.Lock()
		conn = c.conn
		c.mu.Unlock()
	}
	return conn
}

// close stops opening, or closes the open socket, without waiting.
func (c *connection) close() {
	c.mu.Lock()
	c.closing = true
	conn := c.conn
	c.mu.Unlock()
	c.cancel()
	if conn != nil {
		go func() { _ = conn.Close() }()
	}
}

func (c *connection) onState(ch udpclient.StateChange) {
	if ch.State == udpclient.StateOpen {
		c.via = ch.Via
		c.mu.Lock()
		c.open = true
		c.mu.Unlock()
	}
	ev := StateEventDTO{
		ConnID:     c.id,
		RequestID:  c.requestID,
		Address:    c.address,
		State:      string(ch.State),
		At:         formatTime(ch.At),
		LocalAddr:  ch.LocalAddr,
		RemoteAddr: ch.RemoteAddr,
		Via:        ch.Via,
		Proxy:      toProxyHopDTO(ch.Proxy),
		Connected:  ch.Connected,
		MaxPayload: ch.MaxPayload,
		Close:      toCloseInfoDTO(ch.Close),
	}
	if ch.Err != nil {
		dto := apperr.ToDTO(ch.Err)
		ev.Error = &dto
	}
	_ = c.batch.Add(item{state: &ev})
}

func (c *connection) onDatagram(d udpclient.Datagram) {
	dto := toDatagramDTO(d)
	_ = c.batch.Add(item{datagram: &dto})
}

func (c *connection) onNote(n udpclient.Note) {
	dto := toNoteDTO(n)
	_ = c.batch.Add(item{datagram: &dto})
}

// flush sends a batch: consecutive datagrams as one udp:data event, each
// state change as a udp:state event, in order.
func (c *connection) flush(batch []item) {
	var datagrams []DatagramDTO
	sendData := func() {
		if len(datagrams) == 0 {
			return
		}
		c.seq++
		c.emit(EventData, DataEventDTO{ConnID: c.id, RequestID: c.requestID, Seq: c.seq, Datagrams: datagrams})
		datagrams = nil
	}
	for _, it := range batch {
		if it.datagram != nil {
			datagrams = append(datagrams, *it.datagram)
			continue
		}
		sendData()
		c.seq++
		ev := *it.state
		ev.Seq = c.seq
		c.emit(EventState, ev)
	}
	sendData()
}
