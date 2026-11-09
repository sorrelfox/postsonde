// Package tcp is the bound service for raw TCP connections (TCP-01): the
// UI connects to host:port, optionally over TLS with the app's trust and
// client certificates (TCP-02) and with a message framing (TCP-03), sends
// text or hex, and receives what the server sends as tcp:state and
// tcp:data events. The protocol work is
// internal/core/realtime/tcpclient; this service owns the open
// connections, turns their callbacks into batched events and closes them
// when the app quits or the workspace changes. See docs/tcp.md.
package tcp

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/hexbytes"
	"github.com/sorrelfox/postsonde/internal/core/realtime/tcpclient"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/stream"
)

// Error codes of the service. Connection errors keep their tcpclient.*,
// netx.* and hexbytes.* codes.
const (
	// CodeNotFound: Send to a connection that is closed or never existed
	// (not_found).
	CodeNotFound = "tcp.not_found"
	// CodeNotOpen: Send while the connection is still connecting
	// (invalid_argument).
	CodeNotOpen = "tcp.not_open"
	// CodeInvalidMode, CodeInvalidLineEnding: Send with a mode other than
	// text or hex, or an unknown line ending (invalid_argument).
	CodeInvalidMode       = "tcp.invalid_mode"
	CodeInvalidLineEnding = "tcp.invalid_line_ending"
	// CodeEmptyMessage: Send with nothing to send (invalid_argument).
	CodeEmptyMessage = "tcp.empty_message"
	// CodeVariablesUnsupported: an address with {{variables}}, which are
	// not resolved yet (unsupported).
	CodeVariablesUnsupported = "tcp.variables_unsupported"
)

// Batching of the events of one connection: at most one flush every 50 ms,
// and at most maxChunks items (chunks of up to 64 KiB) per flush, so one
// tcp:data event stays around a megabyte.
const (
	maxChunks = 16
	// shutdownWait bounds how long ServiceShutdown and the workspace hook
	// wait for connections to close.
	shutdownWait = 2 * time.Second
)

// TCPService opens raw TCP connections for the UI.
type TCPService struct {
	dialer   netx.Dialer
	emit     func(name string, data any)
	logger   *slog.Logger
	interval time.Duration // batch interval; 0 = stream default
	roots    RootsFunc
	// clientCerts picks the client certificate of a TLS connection
	// (WithClientCerts); nil sends none.
	clientCerts httpclient.ClientCertFunc
	// requireTLS refuses TLS connects that turn certificate verification
	// off (WithRequireTLSVerification).
	requireTLS bool
	// tweak changes the client options of every connection, for tests.
	tweak func(*tcpclient.Options)

	mu    sync.Mutex
	conns map[string]*connection
}

// Option configures a TCPService.
type Option func(*TCPService)

// withInterval sets the event batching interval, for tests.
func withInterval(d time.Duration) Option { return func(s *TCPService) { s.interval = d } }

// withClientOptions changes the client options of every connection, for
// tests (a read buffer size).
func withClientOptions(f func(*tcpclient.Options)) Option {
	return func(s *TCPService) { s.tweak = f }
}

// RootsFunc returns the certificate authorities trusted for TLS
// connections: nil for the system's, or the system's plus the custom CA
// bundles (NET-04, tlsconf.Trust.Roots). An error means a configured
// bundle cannot be used.
type RootsFunc func() (*x509.CertPool, error)

// WithTrust gives the CA pool verified TLS connections use, read on every
// connect. Without it, the system roots only.
func WithTrust(roots RootsFunc) Option {
	return func(s *TCPService) { s.roots = roots }
}

// WithClientCerts gives the per-host client certificates (AUTH-30): a TLS
// connection sends the certificate pick returns for its host and port when
// the server asks for one. An error from pick fails Connect.
func WithClientCerts(pick httpclient.ClientCertFunc) Option {
	return func(s *TCPService) { s.clientCerts = pick }
}

// WithRequireTLSVerification refuses a TLS connect that turns certificate
// verification off (settings.tls_verification_required): the admin
// policy's requireTlsVerification (SET-02). Plain TCP is not affected.
func WithRequireTLSVerification(require bool) Option {
	return func(s *TCPService) { s.requireTLS = require }
}

// New returns a TCPService that connects through dialer and sends events
// with emit (nil drops them). A nil logger means slog.Default().
func New(dialer netx.Dialer, emit func(name string, data any), logger *slog.Logger, opts ...Option) *TCPService {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(string, any) {}
	}
	s := &TCPService{dialer: dialer, emit: emit, logger: logger.With("service", Domain), conns: map[string]*connection{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *TCPService) ServiceName() string { return Domain }

// ServiceShutdown closes every connection when the app quits, waiting up
// to two seconds.
func (s *TCPService) ServiceShutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	_, _ = s.CloseAll(ctx)
	return nil
}

// Connect starts connecting to in.Address, with TLS when in.TLS, and
// returns the connection's ID at once. The outcome arrives as tcp:state
// events: connecting, then open, or closed with the error. It fails at
// once only for input that cannot be used: an invalid address
// (tcpclient.invalid_address) or one with {{variables}}
// (tcp.variables_unsupported), an invalid framing
// (framing.invalid_options), and for TLS a custom CA bundle or a client
// certificate that cannot be loaded (tlsconf.*).
func (s *TCPService) Connect(ctx context.Context, in ConnectInputDTO) (ConnectionDTO, error) {
	if err := ctx.Err(); err != nil {
		return ConnectionDTO{}, errs.Wrap(err, errs.KindOf(err), "Connecting was interrupted.")
	}
	if strings.Contains(in.Address, "{{") {
		return ConnectionDTO{}, errs.New(errs.Unsupported, "Variables in the address are not supported yet.").
			WithCode(CodeVariablesUnsupported).
			WithHint("Write the host and port directly, e.g. localhost:7000.")
	}
	if _, _, err := tcpclient.ParseAddress(in.Address); err != nil {
		return ConnectionDTO{}, err
	}
	frame, err := toFramingOptions(in.Framing).Normalize()
	if err != nil {
		return ConnectionDTO{}, err
	}
	var tlsOpts *tcpclient.TLSOptions
	if in.TLS {
		if tlsOpts, err = s.tlsOptions(in); err != nil {
			return ConnectionDTO{}, err
		}
	}
	timeout := time.Duration(0) // tcpclient.DefaultConnectTimeout
	if in.ConnectTimeoutMs != nil {
		timeout = time.Duration(*in.ConnectTimeoutMs) * time.Millisecond
		if *in.ConnectTimeoutMs <= 0 {
			timeout = -1
		}
	}

	id := newConnID()
	// The connection outlives this call, so it gets a context of its own,
	// marked as the user's connect so the dialer allows it (PRV-05).
	cctx, cancel := context.WithCancel(netx.WithUserOp(context.Background(), netx.UserOp{Kind: Domain + ".connect", ID: id}))
	c := &connection{
		id:        id,
		requestID: in.RequestID,
		address:   in.Address,
		cancel:    cancel,
		framed:    frame.Framed(),
		emit:      s.emit,
		done:      make(chan struct{}),
		connected: make(chan struct{}),
	}
	c.batch = stream.New(context.Background(), stream.Options[item]{Interval: s.interval, MaxItems: maxChunks}, c.flush)
	s.mu.Lock()
	s.conns[c.id] = c
	s.mu.Unlock()

	o := tcpclient.Options{
		Address:        in.Address,
		Dialer:         s.dialer,
		ConnectTimeout: timeout,
		TLS:            tlsOpts,
		Framing:        frame,
		OnState:        c.onState,
		OnData:         c.onData,
	}
	if s.tweak != nil {
		s.tweak(&o)
	}
	go s.run(cctx, c, o)
	return ConnectionDTO{ConnID: c.id, RequestID: c.requestID, Address: c.address}, nil
}

// tlsOptions are the TLS settings of a connection: the trusted CAs
// unless verification is off, and the client certificate for the host and
// port.
func (s *TCPService) tlsOptions(in ConnectInputDTO) (*tcpclient.TLSOptions, error) {
	o := &tcpclient.TLSOptions{InsecureSkipVerify: in.VerifyTLS != nil && !*in.VerifyTLS}
	if o.InsecureSkipVerify && s.requireTLS {
		return nil, coresettings.TLSVerificationRequiredError("TCP")
	}
	if !o.InsecureSkipVerify && s.roots != nil {
		roots, err := s.roots()
		if err != nil {
			return nil, err
		}
		o.RootCAs = roots
	}
	if s.clientCerts != nil {
		cert, err := s.clientCerts(in.Address)
		if err != nil {
			return nil, err
		}
		o.ClientCertificate = cert
	}
	return o, nil
}

// run connects, waits for the connection to end, sends the last events and
// forgets the connection.
func (s *TCPService) run(ctx context.Context, c *connection, o tcpclient.Options) {
	defer func() {
		c.cancel()
		c.batch.Close()
		s.mu.Lock()
		delete(s.conns, c.id)
		s.mu.Unlock()
		close(c.done)
	}()
	conn, err := tcpclient.Connect(ctx, o)
	if err != nil {
		close(c.connected)
		s.logger.Info("tcp connect failed", "conn", c.id, "address", c.address, "err", err)
		return
	}
	s.logger.Info("tcp connected", "conn", c.id, "address", c.address)
	c.mu.Lock()
	c.conn = conn
	closing := c.closing
	c.mu.Unlock()
	close(c.connected)
	if closing {
		_ = conn.Close()
	}
	<-conn.Done()
	info := conn.CloseInfo()
	s.logger.Info("tcp closed", "conn", c.id, "initiatedBy", info.InitiatedBy, "err", info.Err,
		"bytesSent", info.BytesSent, "bytesReceived", info.BytesReceived)
}

// Send sends in.Text on the connection: as UTF-8 with the line ending in
// text mode, or the bytes it spells in hex mode (hexbytes.* errors with the
// position of a bad character). With a framing, the line ending is left
// out and the message is framed instead (a message too long for the
// length prefix fails with framing.message_too_large); an empty message is
// allowed then. The message also arrives as a sent chunk in tcp:data. A
// failed write closes the connection with that error.
func (s *TCPService) Send(ctx context.Context, in SendInputDTO) (SendResultDTO, error) {
	if err := ctx.Err(); err != nil {
		return SendResultDTO{}, errs.Wrap(err, errs.KindOf(err), "Sending was interrupted.")
	}
	c := s.get(in.ConnID)
	framed := c != nil && c.framed
	payload, err := encode(in, framed)
	if err != nil {
		return SendResultDTO{}, err
	}
	if c == nil {
		return SendResultDTO{}, errs.New(errs.NotFound, "The connection is closed.").
			WithCode(CodeNotFound).
			WithHint("Connect again to send.").
			WithDetail("connId", in.ConnID)
	}
	conn := c.openConn()
	if conn == nil {
		return SendResultDTO{}, errs.New(errs.InvalidArgument, "The connection is not open yet.").
			WithCode(CodeNotOpen).
			WithHint("Wait until it is connected.")
	}
	if err := conn.Send(payload); err != nil {
		return SendResultDTO{}, err
	}
	return SendResultDTO{Bytes: len(payload)}, nil
}

// encode turns the composer's text into the bytes to send: the message
// itself when framed (no line ending; empty allowed).
func encode(in SendInputDTO, framed bool) ([]byte, error) {
	var payload []byte
	switch in.Mode {
	case "text":
		ending := ""
		switch in.LineEnding {
		case "", "none":
		case "lf":
			ending = "\n"
		case "crlf":
			ending = "\r\n"
		default:
			return nil, errs.New(errs.InvalidArgument, "The line ending is not one PostSonde knows.").
				WithCode(CodeInvalidLineEnding).
				WithHint(`Use "none", "lf" or "crlf".`).
				WithDetail("lineEnding", in.LineEnding)
		}
		if framed {
			ending = ""
		}
		payload = []byte(in.Text + ending)
	case "hex":
		b, err := hexbytes.Parse(in.Text)
		if err != nil {
			return nil, err
		}
		payload = b
	default:
		return nil, errs.New(errs.InvalidArgument, "The send mode is not one PostSonde knows.").
			WithCode(CodeInvalidMode).
			WithHint(`Use "text" or "hex".`).
			WithDetail("mode", in.Mode)
	}
	if len(payload) == 0 && !framed {
		return nil, errs.New(errs.InvalidArgument, "There is nothing to send.").
			WithCode(CodeEmptyMessage).
			WithHint("Type a message first.")
	}
	return payload, nil
}

// Close closes the connection (or stops connecting) and returns once it is
// closed and its last events are sent, or when ctx ends. Closing a
// connection that is already closed, or unknown, does nothing.
func (s *TCPService) Close(ctx context.Context, connID string) error {
	c := s.get(connID)
	if c == nil {
		return nil
	}
	c.close()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "Waiting for the connection to close was interrupted.")
	}
}

// CloseAll closes every connection, as Close does, and returns how many
// there were.
func (s *TCPService) CloseAll(ctx context.Context) (int, error) {
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
			return len(all), errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "Waiting for the connections to close was interrupted.")
		}
	}
	return len(all), nil
}

// OnWorkspaceChanged closes every connection: the app calls it when
// another project is opened or the project is closed, since the requests
// the connections belong to are gone. It is not bound (it takes no
// context), so it is not a method.
func OnWorkspaceChanged(s *TCPService) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	if n, err := s.CloseAll(ctx); n > 0 {
		s.logger.Info("tcp connections closed for a workspace change", "count", n, "err", err)
	}
}

func (s *TCPService) get(id string) *connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[id]
}

func newConnID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "tcp_" + hex.EncodeToString(b[:])
}

// connection is one connection's state in the service.
type connection struct {
	id, requestID, address string
	cancel                 context.CancelFunc
	framed                 bool // sends messages, not raw bytes
	emit                   func(string, any)
	batch                  *stream.Batcher[item]
	done                   chan struct{}
	// connected is closed once tcpclient.Connect returned, open or not.
	connected chan struct{}

	mu      sync.Mutex
	conn    *tcpclient.Conn // set once Connect returned it
	open    bool            // the client reported open
	closing bool

	seq int64 // used by flush only (one goroutine)
}

// item is a state change or a chunk, in the order they happened.
type item struct {
	state *StateEventDTO
	chunk *ChunkDTO
}

// openConn returns the connection once it is open, nil before. The client
// reports open from inside tcpclient.Connect, and the open event can reach
// the UI before Connect returned the connection: a send in between waits
// for it.
func (c *connection) openConn() *tcpclient.Conn {
	c.mu.Lock()
	conn, open := c.conn, c.open
	c.mu.Unlock()
	if conn == nil && open {
		<-c.connected
		c.mu.Lock()
		conn = c.conn
		c.mu.Unlock()
	}
	return conn
}

// close stops connecting, or closes the open connection, without waiting.
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

func (c *connection) onState(ch tcpclient.StateChange) {
	if ch.State == tcpclient.StateOpen {
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
		TLS:        toTLSDTO(ch.TLS),
		Close:      toCloseInfoDTO(ch.Close),
	}
	if ch.Err != nil {
		dto := apperr.ToDTO(ch.Err)
		ev.Error = &dto
	}
	c.add(item{state: &ev})
}

func (c *connection) onData(ch tcpclient.Chunk) {
	dto := toChunkDTO(ch)
	c.add(item{chunk: &dto})
}

// add queues an event. The batcher is closed only after the connection
// ended, so Add cannot fail.
func (c *connection) add(it item) {
	_ = c.batch.Add(it)
}

// flush sends a batch: consecutive chunks as one tcp:data event, each
// state change as a tcp:state event, in order.
func (c *connection) flush(batch []item) {
	var chunks []ChunkDTO
	sendChunks := func() {
		if len(chunks) == 0 {
			return
		}
		c.seq++
		c.emit(EventData, DataEventDTO{ConnID: c.id, RequestID: c.requestID, Seq: c.seq, Chunks: chunks})
		chunks = nil
	}
	for _, it := range batch {
		if it.chunk != nil {
			chunks = append(chunks, *it.chunk)
			continue
		}
		sendChunks()
		c.seq++
		ev := *it.state
		ev.Seq = c.seq
		c.emit(EventState, ev)
	}
	sendChunks()
}
