// Package socketio is the bound service for Socket.IO connections (SIO-01,
// SIO-03…SIO-07): Connect runs a socketio request's connection
// (internal/core/realtime/sioclient) as an operation whose items
// (sio:op:data) are the handshake, the events and acknowledgements sent
// and received, the heartbeat and the end; Emit sends an event on it;
// OpsService.Cancel disconnects. {{variables}} in the URL, headers, path,
// namespace, auth payload, event names and arguments are rendered here,
// on every connect and emit. See docs/socketio.md.
package socketio

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/sioclient"
	coresio "github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
	"github.com/sorrelfox/postsonde/internal/core/request"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Error codes of the service. Connection errors keep their sioclient.*,
// wsclient.*, engineio.*, socketio.* and netx.* codes.
const (
	// CodeUnavailable: the service was built without an operation
	// registry (kind unsupported).
	CodeUnavailable = "sio.unavailable"
	// CodeNotFound: Emit on a connection that ended or never existed
	// (not_found).
	CodeNotFound = "sio.not_found"
	// CodeInvalidEvent: Emit without an event name (invalid_argument).
	CodeInvalidEvent = "sio.invalid_event"
	// CodeInvalidArgs: the arguments are not JSON, or a binary marker does
	// not spell bytes (invalid_argument). Details: offset.
	CodeInvalidArgs = "sio.invalid_args"
	// CodeInvalidAuthIn: Connect with an authIn other than "handshake"
	// and "payload" (unsupported). Details: field "authIn".
	CodeInvalidAuthIn = "sio.invalid_auth_in"
)

const (
	// maxItems is the largest number of items in one sio:op:data event.
	maxItems = 200
	// disconnectWait bounds the close handshake after a cancel.
	disconnectWait = 2 * time.Second
)

// CookieJars supplies the cookie jar of the open project
// (websocket.WorkspaceJars).
type CookieJars interface {
	Jar() (httpclient.CookieJar, error)
}

// RootsFunc returns the certificate authorities trusted for https and
// wss: nil for the system's, or the system's plus the custom CA bundles
// (NET-04).
type RootsFunc func() (*x509.CertPool, error)

// SocketIOService opens Socket.IO connections for the UI.
type SocketIOService struct {
	ops         *operation.Registry
	dialer      netx.Dialer
	jars        CookieJars
	logger      *slog.Logger
	scopes      Scopes
	roots       RootsFunc
	clientCerts httpclient.ClientCertFunc
	requireTLS  bool
	interval    time.Duration
	tweak       func(*sioclient.Options)
	// auth builds the auth of the handshake (WithAuth); nil sends none.
	auth request.HandshakeAuthFunc
	// proxies gives the request's own proxy (WithProxy); nil inherits.
	proxies ProxySource

	mu    sync.Mutex
	conns map[string]*conn

	// checks loads the assertions of a request (WithChecks, TEST-08);
	// ended keeps the last connections that closed, for CheckAssertions.
	checks streamcheck.Loader
	ended  streamcheck.Recent[*conn]
}

// Option configures a SocketIOService.
type Option func(*SocketIOService)

// WithRequireTLSVerification refuses a connect that turns certificate
// verification off (settings.tls_verification_required): the admin
// policy's requireTlsVerification (SET-02).
func WithRequireTLSVerification(require bool) Option {
	return func(s *SocketIOService) { s.requireTLS = require }
}

// WithTrust gives the CA pool verified connections use, read on every
// connect.
func WithTrust(roots RootsFunc) Option {
	return func(s *SocketIOService) { s.roots = roots }
}

// WithClientCerts gives the per-host client certificates (AUTH-30).
func WithClientCerts(pick httpclient.ClientCertFunc) Option {
	return func(s *SocketIOService) { s.clientCerts = pick }
}

// WithAuth sends the auth of the connection's request with the handshake
// (AUTH-31), built by auth on every connect with the request's scopes:
// with ConnectInputDTO.AuthIn "handshake" (or "") its headers go with
// every transport request after the header rows (an enabled row of the
// same name wins) and its query parameters in the URL's query; with
// "payload" its credentials become members of the CONNECT packet's auth
// payload (request.WithPayloadCredentials). Without it no auth is sent.
func WithAuth(auth request.HandshakeAuthFunc) Option {
	return func(s *SocketIOService) { s.auth = auth }
}

// withInterval sets the batching interval, for tests.
func withInterval(d time.Duration) Option {
	return func(s *SocketIOService) { s.interval = d }
}

// withClientOptions changes the client options of every connection, for
// tests.
func withClientOptions(f func(*sioclient.Options)) Option {
	return func(s *SocketIOService) { s.tweak = f }
}

// New returns a SocketIOService that runs connections as operations in
// ops, dials through dialer and takes cookies from jars (nil: none). A
// nil logger means slog.Default().
func New(ops *operation.Registry, dialer netx.Dialer, jars CookieJars, logger *slog.Logger, opts ...Option) *SocketIOService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &SocketIOService{ops: ops, dialer: dialer, jars: jars, logger: logger.With("service", Domain), conns: map[string]*conn{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *SocketIOService) ServiceName() string { return Domain }

// Connect starts connecting and returns the operation's ID at once. The
// connection arrives as sio:op:data items: open (the Engine.IO
// handshake), maybe fallback, upgrade or upgrade_failed, the CONNECT
// packets, connected, then events, acks and the heartbeat, and closed.
// sio:op:done follows: "cancelled" after OpsService.Cancel (Disconnect,
// or the tab closed), "error" with the error when connecting failed or
// the connection broke (a refused namespace is sioclient.connect_error),
// "ok" when the server ended it. The client never reconnects.
//
// {{variables}} in the URL, the enabled header rows, the path, the
// namespace and the auth payload are rendered with the scopes of
// in.RequestID; undefined names are reported together as one
// template.unresolved error (details.names) unless in.AllowUnresolved
// (ENV-11). It fails at once for those, for options that cannot be used
// (sioclient.invalid_*) and for a request proxy that cannot be used
// (netx.proxy_invalid_url, request.*; WithProxy).
func (s *SocketIOService) Connect(ctx context.Context, in ConnectInputDTO) (operation.StartedDTO, error) {
	if err := ctx.Err(); err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.KindOf(err), "Connecting was interrupted.")
	}
	if s.ops == nil || s.dialer == nil {
		return operation.StartedDTO{}, errs.New(errs.Unsupported, "Socket.IO connections are not available.").WithCode(CodeUnavailable)
	}
	if s.requireTLS && in.VerifyTLS != nil && !*in.VerifyTLS {
		return operation.StartedDTO{}, coresettings.TLSVerificationRequiredError("Socket.IO")
	}
	r, err := s.rendering(ctx, in.RequestID, in.AllowUnresolved)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	in, err = r.renderConnect(in)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	c := &conn{s: s, requestID: in.RequestID, checks: s.checks, log: streamcheck.NewLog(time.Now())}
	if c.proxy, err = s.requestProxy(ctx, in.RequestID, r); err != nil {
		return operation.StartedDTO{}, err
	}
	headers := toHeaders(in.Headers)
	if err := s.addAuth(ctx, r, &in, &headers, c); err != nil {
		if uerr := r.unresolvedErr(); uerr != nil {
			return operation.StartedDTO{}, uerr
		}
		return operation.StartedDTO{}, err
	}
	if c.masker == nil {
		c.masker = secretMasker(nil, c.proxy)
	}
	if err := r.unresolvedErr(); err != nil {
		return operation.StartedDTO{}, err
	}
	o := sioclient.Options{
		URL:                in.URL,
		Path:               in.Path,
		Namespace:          in.Namespace,
		Auth:               json.RawMessage(in.AuthPayload),
		Headers:            headers,
		Transport:          sioclient.Transport(in.Transport),
		Version:            sioclient.Version(in.Version),
		Dialer:             s.dialer,
		InsecureSkipVerify: in.VerifyTLS != nil && !*in.VerifyTLS,
		OnTrace:            c.onTrace,
	}
	if in.ConnectTimeoutMs != nil {
		o.HandshakeTimeout = time.Duration(*in.ConnectTimeoutMs) * time.Millisecond
		if *in.ConnectTimeoutMs <= 0 {
			o.HandshakeTimeout = -1
		}
	}
	if in.AckTimeoutMs != nil && *in.AckTimeoutMs > 0 {
		o.AckTimeout = time.Duration(*in.AckTimeoutMs) * time.Millisecond
	}
	secure := isSecure(in.URL)
	if !o.InsecureSkipVerify && s.roots != nil {
		// Only TLS uses the CA bundles, so a broken one does not stop a
		// plain connection (NET-04).
		roots, err := s.roots()
		switch {
		case err == nil:
			o.RootCAs = roots
		case secure:
			return operation.StartedDTO{}, err
		default:
			s.logger.Warn("custom CA certificates not used", "err", err)
		}
	}
	cert, err := httpclient.ClientCertForURL(s.clientCerts, in.URL)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	o.ClientCertificate = cert
	if s.jars != nil && (in.SendCookies == nil || *in.SendCookies) {
		if jar, err := s.jars.Jar(); err != nil {
			s.logger.Warn("socket.io connects without cookies", "err", err)
		} else {
			o.Jar = jar
		}
	}
	if s.tweak != nil {
		s.tweak(&o)
	}
	client, err := sioclient.New(o)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	c.client = client
	spec := operation.Spec{Domain: Domain, Kind: "connect", Interval: s.interval, MaxItems: maxItems}
	started, err := operation.Start(s.ops, spec, c.run)
	if err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.Internal, "The connection could not be started.")
	}
	s.mu.Lock()
	c.id = started.OpID
	if !c.finished {
		s.conns[c.id] = c
	}
	s.mu.Unlock()
	s.logger.Info("socket.io connecting", "opId", started.OpID, "requestId", in.RequestID, "url", logURL(client.URL()),
		"namespace", client.Namespace(), "transport", in.Transport)
	return started, nil
}

// addAuth adds the auth of in's request to the handshake (WithAuth):
// to headers and in.URL, or to in.AuthPayload, and keeps its secrets in
// c's masker, with the password of c's proxy.
func (s *SocketIOService) addAuth(ctx context.Context, r *rendering, in *ConnectInputDTO, headers *[]httpclient.Header, c *conn) error {
	switch in.AuthIn {
	case "", projectfile.SocketIOAuthHandshake, projectfile.SocketIOAuthPayload:
	default:
		return errs.New(errs.Unsupported, "This version of PostSonde cannot send the auth to "+in.AuthIn+".").
			WithCode(CodeInvalidAuthIn).
			WithHint("Send it in the handshake or in the auth payload (the Auth tab).").
			WithDetail("field", "authIn")
	}
	if s.auth == nil || in.RequestID == "" {
		return nil
	}
	h, err := s.auth(ctx, request.HandshakeInput{
		RequestID: in.RequestID, Protocol: request.HandshakeSocketIO, URL: in.URL,
		Expand: r.render, Ready: r.unresolvedErr, Proxy: c.proxy,
	})
	if err != nil {
		return err
	}
	c.masker = secretMasker(h.Secrets, c.proxy)
	if in.AuthIn == projectfile.SocketIOAuthPayload && sioclient.Version(in.Version) == sioclient.VersionV2 {
		// Socket.IO v2 has no auth payload: its servers read the
		// credentials from socket.handshake.query, the URL's query that
		// goes with the handshake and the namespace's CONNECT.
		in.URL = request.WithAuthQuery(in.URL, request.PayloadQuery(h.Credentials))
		return nil
	}
	if in.AuthIn == projectfile.SocketIOAuthPayload {
		payload, err := request.WithPayloadCredentials(in.AuthPayload, h.Credentials)
		if err != nil {
			return err
		}
		in.AuthPayload = payload
		return nil
	}
	*headers = append(*headers, request.HandshakeHeaders(*headers, h.Headers, false)...)
	in.URL = request.WithAuthQuery(in.URL, h.Query)
	return nil
}

// secretMasker masks secrets and the password of proxy; nil when there is
// nothing to mask.
func secretMasker(secrets []string, proxy request.Proxy) *httpclient.SecretMasker {
	if pw := proxy.Secret(); pw != "" {
		secrets = append(append([]string(nil), secrets...), pw)
	}
	if len(secrets) == 0 {
		return nil
	}
	return httpclient.NewSecretMasker(secrets)
}

// logURL is the part of a connection URL the app log may hold: scheme and
// host. The path and query may carry a secret variable's value (a token
// in the query is common with Socket.IO), and the log is not masked with
// them (docs/privacy.md); userinfo is dropped too.
func logURL(url string) string {
	scheme, rest, ok := strings.Cut(url, "://")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, "?")
	host, _, _ = strings.Cut(host, "#")
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	return scheme + "://" + host
}

func isSecure(url string) bool {
	u := strings.ToLower(strings.TrimSpace(url))
	return strings.HasPrefix(u, "https:") || strings.HasPrefix(u, "wss:")
}

func toHeaders(rows []HeaderDTO) []httpclient.Header {
	out := make([]httpclient.Header, len(rows))
	for i, h := range rows {
		out[i] = httpclient.Header{Name: h.Name, Value: h.Value, Enabled: h.Enabled}
	}
	return out
}

// Emit sends one event on connection in.OpID (SIO-03), asking for an
// acknowledgement with in.Ack (SIO-05). The event name and the arguments'
// text have their {{variables}} rendered with the scopes of the
// connection's request as they are now; undefined names refuse with
// template.unresolved unless in.AllowUnresolved. The event, as sent,
// arrives as an event item before any reply; its ack as an ack item, or an
// ack_timeout item.
func (s *SocketIOService) Emit(ctx context.Context, in EmitInputDTO) (EmitResultDTO, error) {
	if err := ctx.Err(); err != nil {
		return EmitResultDTO{}, errs.Wrap(err, errs.KindOf(err), "Emitting was interrupted.")
	}
	s.mu.Lock()
	c := s.conns[in.OpID]
	s.mu.Unlock()
	if c == nil {
		return EmitResultDTO{}, errs.New(errs.NotFound, "The connection is closed.").
			WithCode(CodeNotFound).
			WithHint("Connect again to emit.").
			WithDetail("opId", in.OpID)
	}
	r, err := s.rendering(ctx, c.requestID, in.AllowUnresolved)
	if err != nil {
		return EmitResultDTO{}, err
	}
	event, err := r.render(in.Event)
	if err != nil {
		return EmitResultDTO{}, err
	}
	text, err := r.render(in.Args)
	if err != nil {
		return EmitResultDTO{}, err
	}
	if err := r.unresolvedErr(); err != nil {
		return EmitResultDTO{}, err
	}
	if strings.TrimSpace(event) == "" {
		return EmitResultDTO{}, errs.New(errs.InvalidArgument, "The event has no name.").
			WithCode(CodeInvalidEvent).
			WithHint("Enter the name of the event to emit.")
	}
	args, err := parseArgs(text)
	if err != nil {
		return EmitResultDTO{}, err
	}
	res, err := c.client.Emit(event, args, in.Ack)
	if err != nil {
		return EmitResultDTO{}, err
	}
	out := EmitResultDTO{Size: res.Size, Attachments: res.Attachments}
	if res.HasAck {
		id := int64(res.AckID)
		out.AckID = &id
	}
	return out, nil
}

// Running returns the IDs of the running connections.
func (s *SocketIOService) running() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.conns))
	for id := range s.conns {
		ids = append(ids, id)
	}
	return ids
}

// OnWorkspaceChanged disconnects every connection: the app calls it when
// another project is opened or the project is closed, since the requests
// the connections belong to are gone. It is not bound.
func OnWorkspaceChanged(s *SocketIOService) {
	ids := s.running()
	for _, id := range ids {
		s.ops.Cancel(id)
	}
	if len(ids) > 0 {
		s.logger.Info("socket.io connections closed for a workspace change", "count", len(ids))
	}
}

// conn is one running connection.
type conn struct {
	s         *SocketIOService
	id        string
	requestID string
	client    *sioclient.Client
	finished  bool // run returned (under s.mu)
	// masker hides the auth's secrets and the proxy's password in the
	// items (AUTH-31); nil without any.
	masker *httpclient.SecretMasker
	// proxy is the request's own proxy (WithProxy), put in the context of
	// every dial of the connection; the zero Proxy inherits.
	proxy request.Proxy

	mu   sync.Mutex
	emit *operation.Emitter[ItemDTO]

	// checks and log check the request's assertions against the events
	// received (TEST-08).
	checks streamcheck.Loader
	log    *streamcheck.Log
}

// run is the operation: connect, then wait until the connection ends or
// the operation is cancelled (a graceful disconnect).
func (c *conn) run(ctx context.Context, emit *operation.Emitter[ItemDTO]) error {
	c.mu.Lock()
	c.emit = emit
	c.mu.Unlock()
	// The transports keep the values of this context for every request
	// and dial they make, the upgrade's included.
	ctx = c.proxy.Context(ctx)
	defer func() {
		c.s.mu.Lock()
		c.finished = true
		if c.id != "" {
			delete(c.s.conns, c.id)
			c.s.ended.Put(c.id, c)
		}
		c.s.mu.Unlock()
	}()
	info, err := c.client.Connect(ctx)
	if err != nil {
		c.s.logger.Info("socket.io connect failed", "requestId", c.requestID, "err", err)
		return err
	}
	c.s.logger.Info("socket.io connected", "requestId", c.requestID, "transport", info.Transport, "namespace", info.Namespace)
	select {
	case <-c.client.Done():
	case <-ctx.Done():
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), disconnectWait)
		c.client.Disconnect(dctx)
		cancel()
	}
	c.s.logger.Info("socket.io closed", "requestId", c.requestID, "reason", c.client.Reason(), "err", c.client.Err())
	return c.client.Err()
}

func (c *conn) send(it ItemDTO) {
	c.mu.Lock()
	e := c.emit
	c.mu.Unlock()
	if e != nil {
		_ = e.Send(it)
	}
}

// onTrace turns a client trace into an item.
func (c *conn) onTrace(t sioclient.Trace) {
	it := ItemDTO{At: formatTime(t.At), Transport: t.Transport}
	if t.Err != nil {
		dto := apperr.ToDTO(t.Err)
		it.Error = &dto
	}
	switch t.Kind {
	case sioclient.TraceOpen:
		it.Type = ItemOpen
		it.URL = c.masker.MaskText(c.client.URL())
		if h := t.Handshake; h != nil {
			it.SID, it.PingIntervalMs, it.PingTimeoutMs, it.MaxPayload = h.SID, h.PingInterval, h.PingTimeout, h.MaxPayload
			it.Upgrades = append([]string{}, h.Upgrades...)
		}
	case sioclient.TraceFallback:
		it.Type = ItemFallback
	case sioclient.TraceUpgrade:
		it.Type = ItemUpgrade
	case sioclient.TraceUpgradeFailed:
		it.Type = ItemUpgradeFailed
	case sioclient.TraceConnected:
		it.Type = ItemConnected
		it.SocketID = t.SocketID
		it.Namespace = c.client.Namespace()
	case sioclient.TracePing:
		// The server pings (v3/v4); the client does with v2 (Sent).
		it.Type, it.Direction = ItemPing, direction(t.Sent)
	case sioclient.TracePong:
		it.Type, it.Direction = ItemPong, direction(t.Sent)
	case sioclient.TraceAckTimeout:
		it.Type = ItemAckTimeout
		it.Event = t.AckEvent
		it.AckID = ackID(t.AckID, true)
		it.ElapsedMs = t.RTT.Milliseconds()
	case sioclient.TraceClosed:
		it.Type = ItemClosed
		it.Reason = t.Reason
		it.Tests = c.runChecks(context.Background())
	case sioclient.TracePacket:
		p := t.Packet
		it.Direction = "received"
		if t.Sent {
			it.Direction = "sent"
		}
		// A v2 CONNECT's namespace carries the URL's query, which may hold
		// the auth's credentials.
		it.Namespace = c.masker.MaskText(p.Namespace)
		it.Size = t.Size
		it.Attachments = len(p.Attachments)
		switch p.Type {
		case coresio.Event, coresio.BinaryEvent:
			it.Type = ItemEvent
			it.Event = p.EventName()
			it.Args = displayArgs(p.Data, p.Attachments, 1)
			it.AckID = ackID(p.ID, p.HasID)
			if !t.Sent {
				c.log.Add([]byte(it.Args), eventHeader(it.Event), t.At)
			}
		case coresio.Ack, coresio.BinaryAck:
			it.Type = ItemAck
			it.Args = displayArgs(p.Data, p.Attachments, 0)
			it.AckID = ackID(p.ID, true)
			if t.Matched {
				it.Event = t.AckEvent
				it.ElapsedMs = t.RTT.Milliseconds()
			}
		default:
			it.Type = ItemPacket
			it.PacketType = p.Type.String()
			// A CONNECT packet's auth payload may hold the auth's
			// credentials.
			it.Data = c.masker.MaskText(string(p.Data))
		}
	default:
		return
	}
	c.send(it)
}

func direction(sent bool) string {
	if sent {
		return "sent"
	}
	return "received"
}

func ackID(id uint64, ok bool) *int64 {
	if !ok {
		return nil
	}
	v := int64(id)
	return &v
}
