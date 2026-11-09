// Package websocket is the bound service for WebSocket connections (WS-01,
// WS-02, WS-03, WS-04, WS-08, WS-09, WS-10, WS-11): the UI connects with a
// request's handshake options, sends text messages and binary ones typed as
// hex or Base64, pings, and receives what the server sends as ws:state and
// ws:message events. Connections can send keepalive pings and reconnect
// with back-off when they drop. {{variables}} in the URL, headers,
// subprotocols and messages are rendered here, on every connect and send. The protocol work is
// internal/core/realtime/wsclient; this service owns the open connections,
// supplies the project's cookie jar, turns the client's callbacks into
// batched events and closes the connections when the app quits or the
// workspace changes. See docs/websocket.md.
package websocket

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/request"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/stream"
)

// Error codes of the service. Connection errors keep their wsclient.*,
// wsframe.* and netx.* codes.
const (
	// CodeNotFound: Send to a connection that is closed or never existed
	// (not_found).
	CodeNotFound = "ws.not_found"
	// CodeNotOpen: Send while the connection is still connecting or
	// already closing (invalid_argument).
	CodeNotOpen = "ws.not_open"
	// CodePingTooLong: a ping payload over 125 bytes (invalid_argument,
	// details.bytes).
	CodePingTooLong = "ws.ping_too_long"
)

const (
	// maxMessages is the largest number of messages in one ws:message
	// event.
	maxMessages = 64
	// closeWait bounds how long Close waits for the server's close frame
	// before the transport is closed (wsclient.close_timeout).
	closeWait = 5 * time.Second
	// shutdownWait bounds how long ServiceShutdown and the workspace hook
	// wait for connections to close.
	shutdownWait = 2 * time.Second
)

// CookieJars supplies the cookie jar of the open project.
type CookieJars interface {
	// Jar returns the jar a connection opened now uses.
	Jar() (httpclient.CookieJar, error)
	// CloseAll saves and forgets the open jars, when the project changes
	// and on shutdown.
	CloseAll() error
}

// JarSource is the app's shared cookie jars (*appcookies.Jars of
// internal/app/services/cookies): the open project's jar, or its active
// environment's when the project keeps separate cookies per environment.
type JarSource interface {
	Jar() (*cookies.Jar, error)
	CloseAll() error
}

// WorkspaceJars returns CookieJars over src, so handshakes use the same
// jar as HTTP sends and the cookie manager (docs/cookies.md). The jar is
// looked up by Connect: the connection and its reconnects keep that jar
// when the environment changes later.
func WorkspaceJars(src JarSource) CookieJars {
	return workspaceJars{src: src}
}

type workspaceJars struct{ src JarSource }

func (j workspaceJars) Jar() (httpclient.CookieJar, error) {
	jar, err := j.src.Jar()
	if err != nil {
		return nil, err
	}
	return jar, nil
}

func (j workspaceJars) CloseAll() error { return j.src.CloseAll() }

// WebSocketService opens WebSocket connections for the UI.
type WebSocketService struct {
	dialer   netx.Dialer
	jars     CookieJars
	emit     func(name string, data any)
	logger   *slog.Logger
	interval time.Duration // batch interval; 0 = stream default
	tweak    func(*wsclient.Options)
	scopes   Scopes
	roots    RootsFunc
	// clientCerts picks the client certificate of a wss connection
	// (WithClientCerts); nil sends none.
	clientCerts httpclient.ClientCertFunc
	// requireTLS refuses connects that turn certificate verification off
	// (WithRequireTLSVerification).
	requireTLS bool
	// clock runs the keepalive timers and the reconnect back-off; nil is
	// the real clock.
	clock wsclient.Clock
	// auth builds the auth of the upgrade request (WithAuth); nil sends
	// none.
	auth request.HandshakeAuthFunc
	// proxies gives the request's own proxy (WithProxy); nil inherits.
	proxies ProxySource

	mu    sync.Mutex
	conns map[string]*connection

	// checks loads the assertions of a request (WithChecks, TEST-08);
	// ended keeps the last connections that closed, for CheckAssertions.
	checks streamcheck.Loader
	ended  streamcheck.Recent[*connection]
}

// Option configures a WebSocketService.
type Option func(*WebSocketService)

// withInterval sets the event batching interval, for tests.
func withInterval(d time.Duration) Option {
	return func(s *WebSocketService) { s.interval = d }
}

// withClientOptions changes the client options of every connection, for
// tests (trusted certificates, short linger).
func withClientOptions(f func(*wsclient.Options)) Option {
	return func(s *WebSocketService) { s.tweak = f }
}

// withClock sets the clock of keepalive pings and reconnect waits, for
// tests.
func withClock(c wsclient.Clock) Option {
	return func(s *WebSocketService) { s.clock = c }
}

// RootsFunc returns the certificate authorities trusted for wss URLs: nil
// for the system's, or the system's plus the custom CA bundles (NET-04,
// tlsconf.Trust.Roots). An error means a configured bundle cannot be used.
type RootsFunc func() (*x509.CertPool, error)

// WithTrust gives the CA pool verified wss connections use, read on every
// connect. Without it, the system roots only.
func WithTrust(roots RootsFunc) Option {
	return func(s *WebSocketService) { s.roots = roots }
}

// WithClientCerts gives the per-host client certificates (AUTH-30): a wss
// connection sends the certificate pick returns for its host and port. An
// error from pick fails the connect.
func WithClientCerts(pick httpclient.ClientCertFunc) Option {
	return func(s *WebSocketService) { s.clientCerts = pick }
}

// WithRequireTLSVerification refuses a connect that turns certificate
// verification off (settings.tls_verification_required): the admin
// policy's requireTlsVerification (SET-02).
func WithRequireTLSVerification(require bool) Option {
	return func(s *WebSocketService) { s.requireTLS = require }
}

// WithAuth sends the auth of the connection's request with the upgrade
// request (AUTH-31): its own or its folder's, built by auth on every
// attempt, its fields rendered with the request's scopes. Its headers go
// after the header rows (an enabled row of the same name wins), its query
// parameters at the end of the URL. Without it no auth is sent.
func WithAuth(auth request.HandshakeAuthFunc) Option {
	return func(s *WebSocketService) { s.auth = auth }
}

// New returns a WebSocketService that connects through dialer, takes
// cookies from jars (nil: no cookies) and sends events with emit (nil
// drops them). A nil logger means slog.Default().
func New(dialer netx.Dialer, jars CookieJars, emit func(name string, data any), logger *slog.Logger, opts ...Option) *WebSocketService {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(string, any) {}
	}
	s := &WebSocketService{dialer: dialer, jars: jars, emit: emit, logger: logger.With("service", Domain), conns: map[string]*connection{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// isWSS reports whether url uses TLS.
func isWSS(url string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(url)), "wss:")
}

// ServiceName names the service in Wails logs.
func (s *WebSocketService) ServiceName() string { return Domain }

// ServiceShutdown closes every connection when the app quits (waiting up
// to two seconds) and saves the cookie jars.
func (s *WebSocketService) ServiceShutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	_, _ = s.CloseAll(ctx)
	s.closeJars()
	return nil
}

// Connect starts connecting and returns the connection's ID at once. The
// outcome arrives as ws:state events: connecting, then open with the
// handshake, or closed with the error (and the handshake as far as it
// got).
//
// {{variables}} in the URL, the enabled header rows and the subprotocols
// are rendered first with the scopes of in.RequestID (WS-11). Undefined
// names of all of them are reported together as one template.unresolved
// error (details.names), before any other, so the UI can ask whether to
// connect anyway (ENV-11); with in.AllowUnresolved they are used as
// written. Other template problems always refuse.
//
// With in.PingIntervalMs the connection sends keepalive pings and fails
// when a pong is late (WS-09). With in.AutoReconnect a connection that
// drops is connected again with back-off under the same ID (WS-10), its
// {{variables}} rendered anew for every attempt.
//
// It fails at once only for input that cannot be used: those variables,
// an invalid URL, header or subprotocol (wsclient.*), or a request proxy
// that cannot be used (netx.proxy_invalid_url, request.*; WithProxy).
func (s *WebSocketService) Connect(ctx context.Context, in ConnectInputDTO) (ConnectionDTO, error) {
	if err := ctx.Err(); err != nil {
		return ConnectionDTO{}, errs.Wrap(err, errs.KindOf(err), "Connecting was interrupted.")
	}
	c := &connection{
		id:        newConnID(),
		requestID: in.RequestID,
		input:     in,
		policy:    newReconnectPolicy(in),
		emit:      s.emit,
		done:      make(chan struct{}),
		wake:      make(chan struct{}),
		checks:    s.checks,
		log:       streamcheck.NewLog(time.Now()),
	}
	client, err := s.newClient(ctx, c, in)
	if err != nil {
		return ConnectionDTO{}, err
	}
	c.batch = stream.New(context.Background(), stream.Options[item]{Interval: s.interval, MaxItems: maxMessages}, c.flush)
	s.mu.Lock()
	s.conns[c.id] = c
	s.mu.Unlock()

	go s.run(c, client)
	return ConnectionDTO{ConnID: c.id, RequestID: c.requestID, URL: c.masker.MaskText(client.URL())}, nil
}

// newClient renders in with the scopes of its request as they are now and
// returns the client of the connection's next attempt.
func (s *WebSocketService) newClient(ctx context.Context, c *connection, in ConnectInputDTO) (*wsclient.Client, error) {
	if s.requireTLS && in.VerifyTLS != nil && !*in.VerifyTLS {
		return nil, coresettings.TLSVerificationRequiredError("WebSocket")
	}
	r, err := s.rendering(ctx, in.RequestID, in.AllowUnresolved)
	if err != nil {
		return nil, err
	}
	in, err = r.renderConnect(in)
	if err != nil {
		return nil, err
	}
	headers := toHeaders(in.Headers)
	a := &attempt{}
	if a.proxy, err = s.requestProxy(ctx, in.RequestID, r); err != nil {
		return nil, err
	}
	var secrets []string
	if s.auth != nil && in.RequestID != "" {
		h, err := s.auth(ctx, request.HandshakeInput{
			RequestID: in.RequestID, Protocol: request.HandshakeWebSocket, URL: in.URL,
			Expand: r.render, Ready: r.unresolvedErr, Proxy: a.proxy,
		})
		if err != nil {
			if uerr := r.unresolvedErr(); uerr != nil {
				return nil, uerr
			}
			return nil, err
		}
		headers = append(headers, request.HandshakeHeaders(headers, h.Headers, false)...)
		in.URL = request.WithAuthQuery(in.URL, h.Query)
		secrets = h.Secrets
	}
	if pw := a.proxy.Secret(); pw != "" {
		secrets = append(secrets, pw)
	}
	if len(secrets) > 0 {
		a.masker = httpclient.NewSecretMasker(secrets)
	}
	if err := r.unresolvedErr(); err != nil {
		return nil, err
	}
	timeout := time.Duration(0) // wsclient.DefaultHandshakeTimeout
	if in.ConnectTimeoutMs != nil {
		timeout = time.Duration(*in.ConnectTimeoutMs) * time.Millisecond
		if *in.ConnectTimeoutMs <= 0 {
			timeout = -1
		}
	}
	o := wsclient.Options{
		URL:                in.URL,
		Headers:            headers,
		Subprotocols:       in.Subprotocols,
		Dialer:             s.dialer,
		InsecureSkipVerify: in.VerifyTLS != nil && !*in.VerifyTLS,
		HandshakeTimeout:   timeout,
		OnState:            func(ch wsclient.StateChange) { c.onState(a, ch) },
		OnMessage:          c.onMessage,
		OnPingPong:         c.onPingPong,
		Clock:              s.clock,
	}
	keepalive(&o, in)
	if !o.InsecureSkipVerify && s.roots != nil {
		// Only wss uses the CA bundles, so a broken one does not stop a
		// ws connection (NET-04).
		roots, err := s.roots()
		switch {
		case err == nil:
			o.RootCAs = roots
		case isWSS(in.URL):
			return nil, err
		default:
			s.logger.Warn("custom CA certificates not used", "err", err)
		}
	}
	cert, err := httpclient.ClientCertForURL(s.clientCerts, in.URL)
	if err != nil {
		return nil, err
	}
	o.ClientCertificate = cert
	if s.jars != nil && (in.SendCookies == nil || *in.SendCookies) {
		if jar, err := s.jars.Jar(); err != nil {
			s.logger.Warn("websocket connects without cookies", "err", err)
		} else {
			o.Jar = jar
		}
	}
	if s.tweak != nil {
		s.tweak(&o)
	}
	client, err := wsclient.New(o)
	if err != nil {
		return nil, err
	}
	a.client = client
	c.mu.Lock()
	c.masker, c.proxy = a.masker, a.proxy
	c.mu.Unlock()
	return client, nil
}

// run makes the connection's attempts one after the other until one ends
// without a reconnect, sends the last events and forgets the connection.
func (s *WebSocketService) run(c *connection, client *wsclient.Client) {
	defer func() {
		c.batch.Close()
		s.ended.Put(c.id, c)
		s.mu.Lock()
		delete(s.conns, c.id)
		s.mu.Unlock()
		close(c.done)
	}()
	for {
		s.runAttempt(c, client)
		c.mu.Lock()
		delay, again := c.nextDelay, c.reconnecting
		c.reconnecting = false
		c.mu.Unlock()
		if !again {
			return
		}
		if !s.wait(c, delay) {
			c.stopWaiting()
			return
		}
		var err error
		client, err = s.newClient(context.Background(), c, c.input)
		if err != nil {
			s.logger.Info("websocket reconnect stopped", "conn", c.id, "err", err)
			c.failAttempt(err)
			return
		}
	}
}

// runAttempt connects client, waits for the connection to end and cleans
// up after it. A close asked for while the attempt starts stops it.
func (s *WebSocketService) runAttempt(c *connection, client *wsclient.Client) {
	kind := Domain + ".connect"
	c.mu.Lock()
	c.attempts++
	if c.attempts > 1 {
		kind = Domain + ".reconnect"
	}
	// The connection outlives Connect, so it gets a context of its own,
	// marked as the user's connect so the dialer allows it (PRV-05), with
	// the request's own proxy as this attempt rendered it.
	ctx, cancel := context.WithCancel(c.proxy.Context(netx.WithUserOp(context.Background(), netx.UserOp{Kind: kind, ID: c.id})))
	c.client, c.url, c.cancel, c.opened = client, client.URL(), cancel, false
	if c.closing {
		cancel() // closed while waiting to reconnect: Connect fails at once
	}
	c.mu.Unlock()
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		s.logger.Info("websocket connect failed", "conn", c.id, "url", redact(client.URL()), "attempt", c.attemptNumber(), "err", err)
		return
	}
	s.logger.Info("websocket connected", "conn", c.id, "url", redact(client.URL()), "subprotocol", client.Subprotocol(), "attempt", c.attemptNumber())
	c.mu.Lock()
	c.opened = true
	closing, code, reason := c.closing, c.closeCode, c.closeReason
	c.mu.Unlock()
	if closing {
		go closeOpen(client, code, reason)
	}
	<-client.Done()
	info := client.CloseInfo()
	s.logger.Info("websocket closed", "conn", c.id, "code", info.Code, "initiatedBy", info.InitiatedBy,
		"clean", info.WasClean, "err", client.Err())
}

// wait waits d before a reconnect; false when the connection was closed
// meanwhile.
func (s *WebSocketService) wait(c *connection, d time.Duration) bool {
	fired := make(chan struct{})
	clock := s.clock
	if clock == nil {
		clock = wsclient.SystemClock()
	}
	stop := clock.AfterFunc(d, func() { close(fired) })
	select {
	case <-c.wake:
		stop()
		return false
	case <-fired:
		select {
		case <-c.wake:
			return false
		default:
			return true
		}
	}
}

// redact keeps the scheme and host of a URL for logs: paths and queries
// may hold tokens.
func redact(url string) string {
	scheme, rest, ok := strings.Cut(url, "://")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, "?")
	return scheme + "://" + host
}

// Send sends in.Text as one message. In the text formats ("", "text",
// "json") it is a text message, its {{variables}} rendered with the scopes
// of the connection's request as they are now (WS-11): the active
// environment is read on every send. Undefined names refuse with
// template.unresolved (details.names) unless in.AllowUnresolved, which
// sends them as written (ENV-11). In "hex" and "base64" in.Text spells the
// bytes of one binary message (WS-04), sent as written with no variables;
// text that spells no bytes refuses with a hexbytes.* or ws.invalid_base64
// error carrying the offending character's offset. The message, as sent,
// also arrives as a sent message in ws:message, before any reply. A failed
// write fails the connection with that error.
func (s *WebSocketService) Send(ctx context.Context, in SendInputDTO) (SendResultDTO, error) {
	if err := ctx.Err(); err != nil {
		return SendResultDTO{}, errs.Wrap(err, errs.KindOf(err), "Sending was interrupted.")
	}
	if err := checkFormat(in.Format); err != nil {
		return SendResultDTO{}, err
	}
	c, client, err := s.openClient(in.ConnID, "Connect again to send.")
	if err != nil {
		return SendResultDTO{}, err
	}
	op, data, err := s.payload(ctx, c.requestID, in)
	if err != nil {
		return SendResultDTO{}, err
	}
	// Reported before writing, under the send lock, so the log shows the
	// message before any reply and sends in the order they were made.
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.count(true, len(data))
	c.add(item{message: new(toMessageDTO("sent", op, data, time.Now()))})
	if err := client.Send(op, data); err != nil {
		return SendResultDTO{}, err
	}
	return SendResultDTO{Bytes: len(data)}, nil
}

// openClient returns connection id and its client if it is open:
// ws.not_found for an unknown or closed connection, ws.not_open while it
// connects, closes or waits to reconnect.
func (s *WebSocketService) openClient(id, hint string) (*connection, *wsclient.Client, error) {
	c := s.get(id)
	if c == nil {
		return nil, nil, errs.New(errs.NotFound, "The connection is closed.").
			WithCode(CodeNotFound).
			WithHint(hint).
			WithDetail("connId", id)
	}
	client := c.current()
	if st := client.State(); st != wsclient.StateOpen {
		return nil, nil, errs.New(errs.InvalidArgument, "The connection is not open.").
			WithCode(CodeNotOpen).
			WithHint("Wait until it is connected.").
			WithDetail("state", string(st))
	}
	return c, client, nil
}

// payload is the opcode and bytes of in: the bytes hex or Base64 spells,
// or the text with its {{variables}} rendered.
func (s *WebSocketService) payload(ctx context.Context, requestID string, in SendInputDTO) (wsframe.Opcode, []byte, error) {
	if isBinaryFormat(in.Format) {
		data, err := binaryPayload(in.Format, in.Text)
		return wsframe.OpBinary, data, err
	}
	r, err := s.rendering(ctx, requestID, in.AllowUnresolved)
	if err != nil {
		return 0, nil, err
	}
	text, err := r.render(in.Text)
	if err != nil {
		return 0, nil, err
	}
	if err := r.unresolvedErr(); err != nil {
		return 0, nil, err
	}
	return wsframe.OpText, []byte(text), nil
}

// Ping sends a ping (WS-09) whose payload is in.Payload: text as written
// (format "" or "text"; no {{variables}}), or the bytes hex or Base64
// spells, at most 125 bytes (ws.ping_too_long). The ping and the pong that
// answers it arrive in ws:message, the pong with the round-trip time.
func (s *WebSocketService) Ping(ctx context.Context, in PingInputDTO) (PingResultDTO, error) {
	if err := ctx.Err(); err != nil {
		return PingResultDTO{}, errs.Wrap(err, errs.KindOf(err), "Sending the ping was interrupted.")
	}
	if err := checkFormat(in.Format); err != nil {
		return PingResultDTO{}, err
	}
	data := []byte(in.Payload)
	if isBinaryFormat(in.Format) {
		var err error
		if data, err = binaryPayload(in.Format, in.Payload); err != nil {
			return PingResultDTO{}, err
		}
	}
	if len(data) > wsframe.MaxControlPayload {
		return PingResultDTO{}, errs.New(errs.InvalidArgument, "A ping payload can be at most 125 bytes.").
			WithCode(CodePingTooLong).
			WithHint("Shorten the payload; pings are meant to be small.").
			WithDetail("bytes", strconv.Itoa(len(data)))
	}
	_, client, err := s.openClient(in.ConnID, "Connect again to ping.")
	if err != nil {
		return PingResultDTO{}, err
	}
	if err := client.Ping(data); err != nil {
		return PingResultDTO{}, err
	}
	return PingResultDTO{Bytes: len(data)}, nil
}

// Close closes the connection with in.Code (0: 1000) and in.Reason, stops
// connecting, or stops waiting to reconnect, and returns once it is closed
// and its last events are sent, or when ctx ends. An invalid code or
// reason fails at once (wsframe.invalid_argument). The server has five
// seconds to answer the close frame before the transport is closed
// (wsclient.close_timeout). Closing a connection that is already closed,
// or unknown, does nothing. A closed connection is not reconnected.
func (s *WebSocketService) Close(ctx context.Context, in CloseInputDTO) error {
	code := wsframe.StatusCode(in.Code)
	if in.Code == 0 {
		code = wsframe.CloseNormalClosure
	}
	if in.Code < 0 || in.Code > 0xffff {
		code = 1 // rejected below
	}
	if _, err := wsframe.AppendClosePayload(nil, code, in.Reason); err != nil {
		return err
	}
	c := s.get(in.ConnID)
	if c == nil {
		return nil
	}
	c.close(code, in.Reason)
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "Waiting for the connection to close was interrupted.")
	}
}

// CloseAll closes every connection with 1001 (going away), as Close does,
// and returns how many there were.
func (s *WebSocketService) CloseAll(ctx context.Context) (int, error) {
	s.mu.Lock()
	all := make([]*connection, 0, len(s.conns))
	for _, c := range s.conns {
		all = append(all, c)
	}
	s.mu.Unlock()
	for _, c := range all {
		c.close(wsframe.CloseGoingAway, "")
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

// OnWorkspaceChanged closes every connection and saves the cookie jars:
// the app calls it when another project is opened or the project is
// closed, since the requests the connections belong to are gone. It is not
// bound (it takes no context), so it is not a method.
func OnWorkspaceChanged(s *WebSocketService) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	if n, err := s.CloseAll(ctx); n > 0 {
		s.logger.Info("websocket connections closed for a workspace change", "count", n, "err", err)
	}
	s.closeJars()
}

func (s *WebSocketService) closeJars() {
	if s.jars == nil {
		return
	}
	if err := s.jars.CloseAll(); err != nil {
		s.logger.Warn("saving the cookie jars failed", "err", err)
	}
}

func (s *WebSocketService) get(id string) *connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[id]
}

func newConnID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ws_" + hex.EncodeToString(b[:])
}

// connection is one connection's state in the service: one or more
// attempts (WS-10) under one ID.
type connection struct {
	id, requestID string
	// input is Connect's input as given, {{variables}} unrendered, for
	// the reconnects.
	input  ConnectInputDTO
	policy reconnectPolicy
	emit   func(string, any)
	batch  *stream.Batcher[item]
	done   chan struct{}
	// wake is closed by the first close, to stop waiting to reconnect.
	wake   chan struct{}
	sendMu sync.Mutex

	mu sync.Mutex
	// The current (or last) attempt.
	client   *wsclient.Client
	url      string
	cancel   context.CancelFunc
	opened   bool // Connect of the current attempt succeeded
	openedAt time.Time
	attempts int // attempts started, the first included
	// retries counts the reconnects since the last attempt that opened.
	retries int
	// reconnecting and nextDelay are set when an attempt ended and
	// another one follows after nextDelay.
	reconnecting           bool
	nextDelay              time.Duration
	closing                bool
	closeCode              wsframe.StatusCode
	closeReason            string
	messagesSent, received int64
	bytesSent, bytesRecv   int64

	// masker and proxy are the latest attempt's (attempt.masker,
	// attempt.proxy).
	masker *httpclient.SecretMasker
	proxy  request.Proxy

	seq int64 // used by flush only (one goroutine)

	// checks and log check the request's assertions against the messages
	// received (TEST-08); hs is the handshake of the attempt that opened
	// last.
	checks streamcheck.Loader
	log    *streamcheck.Log
	hs     *HandshakeDTO
}

// attempt ties the callbacks of one wsclient.Client to it.
type attempt struct {
	client *wsclient.Client
	// masker hides the auth's secrets and the proxy's password in the URL
	// and handshake shown (AUTH-31); nil without any.
	masker *httpclient.SecretMasker
	// proxy is the request's own proxy (WithProxy); the zero Proxy
	// inherits.
	proxy request.Proxy
}

// item is a state change or a message, in the order they happened.
type item struct {
	state   *StateEventDTO
	message *MessageDTO
}

func (c *connection) current() *wsclient.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}

func (c *connection) attemptNumber() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts
}

// close stops connecting or waiting to reconnect, or starts the close
// handshake, without waiting. Only the first call's code is used.
func (c *connection) close(code wsframe.StatusCode, reason string) {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return
	}
	c.closing, c.closeCode, c.closeReason = true, code, reason
	close(c.wake)
	opened, client, cancel := c.opened, c.client, c.cancel
	c.mu.Unlock()
	if opened {
		go closeOpen(client, code, reason)
		return
	}
	// Still connecting: cancel the handshake. If it just succeeded, run
	// sees closing and closes the open connection. Waiting to reconnect:
	// run sees wake.
	if cancel != nil {
		cancel()
	}
}

// closeOpen runs the close handshake of an open connection.
func closeOpen(client *wsclient.Client, code wsframe.StatusCode, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), closeWait)
	defer cancel()
	_ = client.Close(ctx, code, reason)
}

func (c *connection) count(sent bool, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sent {
		c.messagesSent++
		c.bytesSent += int64(n)
	} else {
		c.received++
		c.bytesRecv += int64(n)
	}
}

func (c *connection) onState(a *attempt, ch wsclient.StateChange) {
	c.mu.Lock()
	ev := StateEventDTO{
		ConnID:    c.id,
		RequestID: c.requestID,
		URL:       a.masker.MaskText(a.client.URL()),
		Attempt:   c.attempts,
		State:     string(ch.State),
		At:        formatTime(ch.At),
	}
	switch ch.State {
	case wsclient.StateOpen:
		c.openedAt = ch.At
		c.retries = 0
		ev.Handshake = maskHandshake(toHandshakeDTO(a.client.Handshake()), a.masker)
		c.hs = ev.Handshake
		c.log.SetResponse(handshakeResponse(c.hs, 0))
	case wsclient.StateClosed:
		opened := !c.openedAt.IsZero()
		if !opened {
			ev.Handshake = maskHandshake(toHandshakeDTO(a.client.Handshake()), a.masker)
		}
		if info := ch.Close; info != nil {
			ev.Close = &CloseInfoDTO{
				Code:             int(info.Code),
				Reason:           info.Reason,
				WasClean:         info.WasClean,
				InitiatedBy:      string(info.InitiatedBy),
				Sent:             toCloseFrameDTO(info.Sent),
				Received:         toCloseFrameDTO(info.Received),
				MessagesSent:     c.messagesSent,
				MessagesReceived: c.received,
				BytesSent:        c.bytesSent,
				BytesReceived:    c.bytesRecv,
			}
			if opened {
				ev.Close.DurationMs = ch.At.Sub(c.openedAt).Milliseconds()
			}
			if info.InitiatedBy == wsclient.InitiatorNone && c.closing && ch.Err != nil && isCancelled(ch.Err) {
				// Disconnect while connecting.
				ev.Close.InitiatedBy = string(wsclient.InitiatorLocal)
			}
		}
		ev.Reconnect = c.decide(ch.Close, ch.Err)
		hs, d := c.hs, time.Duration(0)
		if opened {
			d = ch.At.Sub(c.openedAt)
		} else if hs == nil {
			hs = ev.Handshake
		}
		c.log.SetResponse(handshakeResponse(hs, d))
		c.opened, c.openedAt = false, time.Time{}
	}
	c.mu.Unlock()
	if ch.Err != nil {
		dto := apperr.ToDTO(ch.Err)
		ev.Error = &dto
	}
	if ch.State == wsclient.StateClosed && (ev.Reconnect == nil || ev.Reconnect.Stopped != "") {
		// The connection has ended for good: check its messages.
		ev.Tests = c.runChecks(context.Background())
	}
	c.add(item{state: &ev})
}

// decide whether another attempt follows the one that just ended (WS-10),
// and how long to wait for it. c.mu is held. Nil without auto-reconnect.
func (c *connection) decide(info *wsclient.CloseInfo, err error) *ReconnectDTO {
	p := c.policy
	if !p.enabled {
		return nil
	}
	r := &ReconnectDTO{Attempt: c.retries, MaxAttempts: p.maxAttempts}
	if c.closing {
		r.Stopped = StoppedManual
		return r
	}
	if ok, why := retryable(info, err); !ok {
		r.Stopped = why
		return r
	}
	if p.maxAttempts > 0 && c.retries >= p.maxAttempts {
		r.Stopped = StoppedMaxAttempts
		return r
	}
	c.retries++
	c.reconnecting, c.nextDelay = true, p.backoff(c.retries)
	r.Attempt, r.DelayMs = c.retries, c.nextDelay.Milliseconds()
	return r
}

// stopWaiting reports that the wait for a reconnect was cancelled: a
// closed event without close info, reconnect.stopped "manual".
func (c *connection) stopWaiting() {
	c.mu.Lock()
	ev := StateEventDTO{
		ConnID: c.id, RequestID: c.requestID, URL: c.url, Attempt: c.attempts,
		State: string(wsclient.StateClosed), At: formatTime(time.Now()),
		Reconnect: &ReconnectDTO{Attempt: c.retries, MaxAttempts: c.policy.maxAttempts, Stopped: StoppedManual},
	}
	c.mu.Unlock()
	ev.Tests = c.runChecks(context.Background())
	c.add(item{state: &ev})
}

// failAttempt reports a reconnect that could not start (its {{variables}}
// or options no longer work): a closed event with the error.
func (c *connection) failAttempt(err error) {
	c.mu.Lock()
	c.attempts++
	ev := StateEventDTO{
		ConnID: c.id, RequestID: c.requestID, URL: c.url, Attempt: c.attempts,
		State: string(wsclient.StateClosed), At: formatTime(time.Now()),
		Close: &CloseInfoDTO{
			Code: int(wsframe.CloseAbnormalClosure), InitiatedBy: string(wsclient.InitiatorNone),
			MessagesSent: c.messagesSent, MessagesReceived: c.received, BytesSent: c.bytesSent, BytesReceived: c.bytesRecv,
		},
		Reconnect: &ReconnectDTO{Attempt: c.retries, MaxAttempts: c.policy.maxAttempts, Stopped: StoppedError},
	}
	c.mu.Unlock()
	dto := apperr.ToDTO(err)
	ev.Error = &dto
	ev.Tests = c.runChecks(context.Background())
	c.add(item{state: &ev})
}

func isCancelled(err error) bool {
	return errs.KindOf(err) == errs.Cancelled
}

func (c *connection) onMessage(m wsclient.Message) {
	c.count(false, len(m.Data))
	c.log.Add(m.Data, messageHeader(m.Opcode == wsframe.OpText), m.At)
	c.add(item{message: new(toMessageDTO("received", m.Opcode, m.Data, m.At))})
}

func (c *connection) onPingPong(p wsclient.PingPong) {
	c.add(item{message: new(toPingPongDTO(p))})
}

// add queues an event. The batcher is closed only after the connection
// ended, so Add cannot fail.
func (c *connection) add(it item) {
	_ = c.batch.Add(it)
}

// flush sends a batch: consecutive messages as one ws:message event, each
// state change as a ws:state event, in order.
func (c *connection) flush(batch []item) {
	var messages []MessageDTO
	sendMessages := func() {
		if len(messages) == 0 {
			return
		}
		c.seq++
		c.emit(EventMessage, MessageEventDTO{ConnID: c.id, RequestID: c.requestID, Seq: c.seq, Messages: messages})
		messages = nil
	}
	for _, it := range batch {
		if it.message != nil {
			messages = append(messages, *it.message)
			continue
		}
		sendMessages()
		c.seq++
		ev := *it.state
		ev.Seq = c.seq
		c.emit(EventState, ev)
	}
	sendMessages()
}
