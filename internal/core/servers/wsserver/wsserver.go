// Package wsserver is the WebSocket server type (SSRV-01): it accepts
// WebSocket connections on a path, keeps the list of connected clients,
// sends text or binary messages to one client or to all, and disconnects a
// client with a chosen close code. Every upgrade, message and close goes to
// the traffic log. The HTTP side (connections, stop) is httpbase; framing is
// wsframe in the server role, shared with the WebSocket client. See
// docs/servers.md.
package wsserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/httpbase"
)

// TypeName is the Spec.Type of the WebSocket server.
const TypeName = "websocket"

// Defaults of Options.
const (
	// DefaultCloseTimeout is how long Disconnect and Stop wait for a
	// client's close frame before closing its connection.
	DefaultCloseTimeout = 2 * time.Second
	// DefaultWriteTimeout bounds writing one message (or the 101
	// response) to a client that does not read.
	DefaultWriteTimeout = 10 * time.Second
)

// StopReason is the close reason sent with 1001 when the server stops.
const StopReason = "server stopping"

// Error codes of the client operations.
const (
	CodeClientNotFound = "wsserver.client_not_found"
	CodeClientClosing  = "wsserver.client_closing"
	CodeSendFailed     = "wsserver.send_failed"
	CodeInvalidMessage = "wsserver.invalid_message"
)

// Options change timeouts, for tests.
type Options struct {
	// CloseTimeout: 0 means DefaultCloseTimeout.
	CloseTimeout time.Duration
	// WriteTimeout: 0 means DefaultWriteTimeout.
	WriteTimeout time.Duration
}

// Type is the WebSocket server type.
type Type struct {
	opts Options
}

// New returns the WebSocket server type.
func New(opts Options) Type {
	if opts.CloseTimeout <= 0 {
		opts.CloseTimeout = DefaultCloseTimeout
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}
	return Type{opts: opts}
}

func (Type) Name() string  { return TypeName }
func (Type) Label() string { return "WebSocket" }

// ALPN offers HTTP/1.1 over TLS (SRV-05); HTTP/2 is not served.
func (Type) ALPN() []string { return httpbase.ALPN() }

// NormalizeConfig takes a Config, a *Config or nil (DefaultConfig) and
// returns a normalized Config (Normalize).
func (Type) NormalizeConfig(config any) (any, error) {
	switch c := config.(type) {
	case nil:
		return DefaultConfig(), nil
	case Config:
		return Normalize(c)
	case *Config:
		if c == nil {
			return DefaultConfig(), nil
		}
		return Normalize(*c)
	}
	return nil, errs.New(errs.Internal, fmt.Sprintf("A WebSocket server cannot use a %T configuration.", config)).
		WithCode(CodeInvalidConfig)
}

// Start accepts WebSocket connections on ln.
func (t Type) Start(spec servers.Spec, ln net.Listener, sink servers.Sink) (servers.Instance, error) {
	cfg, err := t.NormalizeConfig(spec.Config)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	inst := &Instance{base: httpbase.New(sink), opts: t.opts, clients: map[string]*client{}}
	inst.Reconfigure(cfg)
	inst.base.Serve(ln, http.HandlerFunc(inst.serveHTTP))
	return inst, nil
}

// Client is a snapshot of a connected client.
type Client struct {
	// ID identifies the client within one run of the server: its
	// connection ID ("1", "2", …), as in the traffic.
	ID     string
	ConnID uint64
	// Peer is the client's address.
	Peer string
	// Target is the request target of the upgrade (path and query).
	Target      string
	Subprotocol string
	ConnectedAt time.Time
	// Origin and UserAgent are the handshake headers, "" when not sent.
	Origin    string
	UserAgent string
	// Messages and bytes of data messages received from and sent to the
	// client.
	MessagesIn  uint64
	MessagesOut uint64
	BytesIn     uint64
	BytesOut    uint64
}

// BroadcastResult counts the clients a broadcast reached.
type BroadcastResult struct {
	Sent   int
	Failed int
}

// Hub is the running WebSocket server's client operations, implemented by
// *Instance.
type Hub interface {
	Clients() []Client
	Send(clientID string, binary bool, data []byte) error
	Broadcast(binary bool, data []byte) BroadcastResult
	Disconnect(ctx context.Context, clientID string, code int, reason string) error
}

var _ Hub = (*Instance)(nil)
var _ servers.Reconfigurable = (*Instance)(nil)

// Instance is one run of a WebSocket server. It implements
// servers.Instance, servers.Reconfigurable and Hub.
type Instance struct {
	base *httpbase.Instance
	opts Options
	live atomic.Pointer[live]

	mu       sync.Mutex
	clients  map[string]*client
	stopping bool
	running  sync.WaitGroup // client goroutines, added under mu while not stopping

	stopOnce sync.Once
	stopErr  error
}

// live is the configuration in use and its compiled rules.
type live struct {
	cfg   Config
	rules *RuleSet
}

// client is one connected client.
type client struct {
	info Client // fixed fields
	nc   net.Conn
	conn *wsframe.Conn

	messagesIn, messagesOut, bytesIn, bytesOut atomic.Uint64

	// sendMu keeps one message write (and its event) at a time.
	sendMu sync.Mutex
	// closeMu guards serverClose: the close frame the server sent first.
	closeMu     sync.Mutex
	serverClose *closeFrame
	stopClose   bool // serverClose was sent because the server stops

	done chan struct{} // closed once the client is gone and logged
}

type closeFrame struct {
	code   wsframe.StatusCode
	reason string
}

func (c *client) snapshot() Client {
	s := c.info
	s.MessagesIn = c.messagesIn.Load()
	s.MessagesOut = c.messagesOut.Load()
	s.BytesIn = c.bytesIn.Load()
	s.BytesOut = c.bytesOut.Load()
	return s
}

func (i *Instance) Done() <-chan struct{} { return i.base.Done() }
func (i *Instance) Err() error            { return i.base.Err() }

// Reconfigure applies config (a normalized Config) to new connections;
// echo and the auto-reply rules apply to connected clients too.
func (i *Instance) Reconfigure(config any) {
	c, ok := config.(Config)
	if !ok {
		return
	}
	rules, err := CompileRules(c.Rules)
	if err != nil {
		rules = nil // Normalize checked them; never here
	}
	i.live.Store(&live{cfg: c, rules: rules})
}

// Stop refuses new connections, closes every client with 1001 (waiting up
// to the close timeout, or until ctx ends, for their close frames), then
// stops the HTTP server.
func (i *Instance) Stop(ctx context.Context) error {
	i.stopOnce.Do(func() {
		i.mu.Lock()
		i.stopping = true
		list := i.sortedClients()
		i.mu.Unlock()
		var wg sync.WaitGroup
		for _, c := range list {
			wg.Go(func() { i.disconnect(ctx, c, wsframe.CloseGoingAway, StopReason, true) })
		}
		wg.Wait()
		i.running.Wait()
		i.stopErr = i.base.Stop(ctx)
	})
	return i.stopErr
}

// sortedClients lists the clients by connection ID; the caller holds mu.
func (i *Instance) sortedClients() []*client {
	list := make([]*client, 0, len(i.clients))
	for _, c := range i.clients {
		list = append(list, c)
	}
	slices.SortFunc(list, func(a, b *client) int { return cmp.Compare(a.info.ConnID, b.info.ConnID) })
	return list
}

// Clients returns the connected clients, oldest first.
func (i *Instance) Clients() []Client {
	i.mu.Lock()
	list := i.sortedClients()
	i.mu.Unlock()
	out := make([]Client, 0, len(list))
	for _, c := range list {
		out = append(out, c.snapshot())
	}
	return out
}

func (i *Instance) client(id string) (*client, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	c, ok := i.clients[id]
	if !ok {
		return nil, errs.New(errs.NotFound, "This client is no longer connected.").
			WithCode(CodeClientNotFound).WithDetail("clientId", id)
	}
	return c, nil
}

// Send sends one text (binary false; data must be UTF-8) or binary message
// to the client id. The message is logged before it is written, so it
// comes before the client's answer.
func (i *Instance) Send(clientID string, binary bool, data []byte) error {
	c, err := i.client(clientID)
	if err != nil {
		return err
	}
	return i.send(c, binary, data, "")
}

// Broadcast sends one message to every connected client, in parallel.
func (i *Instance) Broadcast(binary bool, data []byte) BroadcastResult {
	i.mu.Lock()
	list := i.sortedClients()
	i.mu.Unlock()
	var sent, failed atomic.Int64
	var wg sync.WaitGroup
	for _, c := range list {
		wg.Go(func() {
			if i.send(c, binary, data, "") == nil {
				sent.Add(1)
			} else {
				failed.Add(1)
			}
		})
	}
	wg.Wait()
	return BroadcastResult{Sent: int(sent.Load()), Failed: int(failed.Load())}
}

// send writes one message to c. how is "" for a message the user sent,
// "echoed" for an echo, or the auto-reply rule ("auto-reply rule 2"),
// which the summary names.
func (i *Instance) send(c *client, binary bool, data []byte, how string) error {
	op, opcode, kind := wsframe.OpText, servers.OpcodeText, "Text"
	if binary {
		op, opcode, kind = wsframe.OpBinary, servers.OpcodeBinary, "Binary"
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	select {
	case <-c.done:
		return errs.New(errs.NotFound, "This client is no longer connected.").
			WithCode(CodeClientNotFound).WithDetail("clientId", c.info.ID)
	default:
	}
	if !binary && !utf8Valid(data) {
		return errs.New(errs.InvalidArgument, "A text message must be valid UTF-8.").WithCode(CodeInvalidMessage)
	}
	summary := fmt.Sprintf("%s message to client %s", kind, c.info.ID)
	switch how {
	case "":
	case howEcho:
		summary = fmt.Sprintf("%s message echoed to client %s", kind, c.info.ID)
	default:
		summary = fmt.Sprintf("%s message to client %s (%s)", kind, c.info.ID, how)
	}
	i.base.Emit(servers.Event{
		Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.Out, Kind: servers.KindMessage,
		Summary: summary, Content: data, WS: &servers.WSInfo{ClientID: c.info.ID, Opcode: opcode},
	})
	_ = c.nc.SetWriteDeadline(time.Now().Add(i.opts.WriteTimeout))
	err := c.conn.WriteMessage(op, data)
	_ = c.nc.SetWriteDeadline(time.Time{})
	if err != nil {
		if e, ok := errors.AsType[*errs.Error](err); ok && e.Code == wsframe.CodeCloseSent {
			err = errs.Wrap(err, errs.InvalidArgument, "This client is disconnecting; nothing more can be sent.").WithCode(CodeClientClosing)
		} else {
			err = errs.Wrap(err, errs.KindOf(err), fmt.Sprintf("The message could not be sent to client %s.", c.info.ID)).WithCode(CodeSendFailed)
		}
		i.base.Emit(servers.Event{
			Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.System, Kind: servers.KindError,
			Summary: messageOf(err), WS: &servers.WSInfo{ClientID: c.info.ID},
		})
		return err
	}
	c.messagesOut.Add(1)
	c.bytesOut.Add(uint64(len(data)))
	return nil
}

// Disconnect closes the connection of the client id with a close frame
// (code 1000–1003, 1007–1014 or 3000–4999, or 1005 with no reason for an
// empty close frame; the reason at most 123 bytes of UTF-8) and waits for
// the client's close frame up to the close timeout or until ctx ends; then
// the connection is closed anyway. It returns once the client is gone.
func (i *Instance) Disconnect(ctx context.Context, clientID string, code int, reason string) error {
	if code < 0 || code > 0xffff {
		return errs.New(errs.InvalidArgument, fmt.Sprintf("%d is not a close code.", code)).WithCode(wsframe.CodeInvalidArgument)
	}
	if _, err := wsframe.AppendClosePayload(nil, wsframe.StatusCode(code), reason); err != nil {
		return err
	}
	c, err := i.client(clientID)
	if err != nil {
		return err
	}
	i.disconnect(ctx, c, wsframe.StatusCode(code), reason, false)
	return nil
}

func (i *Instance) disconnect(ctx context.Context, c *client, code wsframe.StatusCode, reason string, stopping bool) {
	force := time.AfterFunc(i.opts.CloseTimeout, func() { _ = c.nc.Close() })
	defer force.Stop()
	stopCtx := context.AfterFunc(ctx, func() { _ = c.nc.Close() })
	defer stopCtx()

	c.closeMu.Lock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(i.opts.CloseTimeout))
	sent, err := c.conn.WriteClose(code, reason)
	if sent {
		c.serverClose = &closeFrame{code: code, reason: reason}
		c.stopClose = stopping
	}
	c.closeMu.Unlock()
	if err != nil {
		_ = c.nc.Close()
	}
	<-c.done
}

// serveHTTP checks the upgrade request (RFC 6455 §4.2.1) and answers
// anything else with an HTTP error, logged as a request and a response.
func (i *Instance) serveHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := &i.live.Load().cfg
	h := w.Header()
	if !cfg.matchesPath(r.URL.Path) {
		i.reject(w, r, http.StatusNotFound, "no WebSocket endpoint on this path",
			fmt.Sprintf("No WebSocket endpoint at %s. This server accepts WebSocket connections at %s.\n", r.URL.Path, cfg.Path))
		return
	}
	if !headerHasToken(r.Header, "Upgrade", "websocket") {
		h.Set("Upgrade", "websocket")
		h.Set("Connection", "Upgrade")
		h.Set("Sec-WebSocket-Version", "13")
		i.reject(w, r, http.StatusUpgradeRequired, "not a WebSocket upgrade",
			"This is a PostSonde WebSocket server. Connect to it with a WebSocket client.\n")
		return
	}
	if r.Method != http.MethodGet {
		h.Set("Allow", http.MethodGet)
		i.reject(w, r, http.StatusMethodNotAllowed, "the upgrade must be a GET request", "A WebSocket upgrade must be a GET request.\n")
		return
	}
	if !r.ProtoAtLeast(1, 1) {
		i.reject(w, r, http.StatusBadRequest, "the upgrade needs HTTP/1.1", "A WebSocket upgrade needs HTTP/1.1.\n")
		return
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") {
		i.reject(w, r, http.StatusBadRequest, "Connection: Upgrade is missing", "The Connection header must contain Upgrade.\n")
		return
	}
	if v := r.Header.Values("Sec-WebSocket-Version"); len(v) != 1 || textTrim(v[0]) != "13" {
		h.Set("Sec-WebSocket-Version", "13")
		i.reject(w, r, http.StatusUpgradeRequired, "unsupported Sec-WebSocket-Version", "This server speaks WebSocket version 13 only.\n")
		return
	}
	keys := r.Header.Values("Sec-WebSocket-Key")
	if len(keys) != 1 || !validKey(textTrim(keys[0])) {
		i.reject(w, r, http.StatusBadRequest, "invalid Sec-WebSocket-Key", "Sec-WebSocket-Key must be one base64 value of 16 bytes.\n")
		return
	}
	i.mu.Lock()
	stopping := i.stopping
	i.mu.Unlock()
	if stopping {
		i.reject(w, r, http.StatusServiceUnavailable, "the server is stopping", "The server is stopping.\n")
		return
	}
	i.accept(w, r, cfg, textTrim(keys[0]))
}

// reject answers r with status and a text body and logs both.
func (i *Instance) reject(w http.ResponseWriter, r *http.Request, status int, why, body string) {
	peer, connID := i.base.ReadRequest(r, nil)
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	var content []byte
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
		content = []byte(body)
	}
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: fmt.Sprintf("%d %s (%s)", status, http.StatusText(status), why),
		Content: content,
		HTTP: &servers.HTTPInfo{
			Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Status: status, Headers: httpbase.HeaderList(h),
		},
	})
}

// accept completes the upgrade and serves the client until it is gone.
func (i *Instance) accept(w http.ResponseWriter, r *http.Request, cfg *Config, key string) {
	connID := i.base.ConnID(r) // before Hijack, which forgets the connection
	id := strconv.FormatUint(connID, 10)
	peer := r.RemoteAddr
	ws := func() *servers.WSInfo { return &servers.WSInfo{ClientID: id} }
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.In, Kind: servers.KindRequest,
		Summary: r.Method + " " + r.RequestURI,
		HTTP:    &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Headers: httpbase.RequestHeaders(r)},
		WS:      ws(),
	})

	subprotocol := cfg.selectSubprotocol(headerTokens(r.Header, "Sec-WebSocket-Protocol"))
	nc, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		i.base.Emit(servers.Event{
			Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindError,
			Summary: "The WebSocket upgrade failed: " + err.Error(), WS: ws(),
		})
		return
	}
	// net/http may leave its header read deadline set.
	_ = nc.SetDeadline(time.Time{})

	resp := []servers.Header{
		{Name: "Upgrade", Value: "websocket"},
		{Name: "Connection", Value: "Upgrade"},
		{Name: "Sec-WebSocket-Accept", Value: wsframe.ComputeAccept(key)},
	}
	if subprotocol != "" {
		resp = append(resp, servers.Header{Name: "Sec-WebSocket-Protocol", Value: subprotocol})
	}
	raw := "HTTP/1.1 101 Switching Protocols\r\n"
	for _, hd := range resp {
		raw += hd.Name + ": " + hd.Value + "\r\n"
	}
	raw += "\r\n"
	_ = nc.SetWriteDeadline(time.Now().Add(i.opts.WriteTimeout))
	_, err = nc.Write([]byte(raw))
	_ = nc.SetWriteDeadline(time.Time{})
	if err != nil {
		_ = nc.Close()
		i.base.Emit(servers.Event{
			Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindError,
			Summary: "The WebSocket upgrade response could not be sent: " + err.Error(), WS: ws(),
		})
		i.base.Emit(servers.Event{
			Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindConnectionClose,
			Summary: "Connection closed", WS: &servers.WSInfo{ClientID: id, CloseCode: int(wsframe.CloseAbnormalClosure)},
		})
		return
	}

	c := &client{
		info: Client{
			ID: id, ConnID: connID, Peer: peer, Target: r.RequestURI, Subprotocol: subprotocol,
			ConnectedAt: time.Now(), Origin: r.Header.Get("Origin"), UserAgent: r.Header.Get("User-Agent"),
		},
		nc:   nc,
		done: make(chan struct{}),
	}
	limit := cfg.maxMessageSize()
	c.conn = wsframe.NewConn(nc, wsframe.Options{
		Role:           wsframe.RoleServer,
		Limits:         wsframe.Limits{MaxPayload: limit},
		MaxMessageSize: limit,
		Reader:         brw.Reader,
	})

	i.mu.Lock()
	if i.stopping {
		i.mu.Unlock()
		_, _ = c.conn.WriteClose(wsframe.CloseGoingAway, StopReason)
		_ = nc.Close()
		return // the server's events are over
	}
	i.clients[id] = c
	i.running.Add(1)
	i.mu.Unlock()

	summary := "101 Switching Protocols"
	if subprotocol != "" {
		summary += " (subprotocol " + subprotocol + ")"
	}
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: summary,
		HTTP:    &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: "HTTP/1.1", Status: http.StatusSwitchingProtocols, Headers: resp},
		WS:      &servers.WSInfo{ClientID: id, Subprotocol: subprotocol},
	})
	i.read(c)
}

// read receives the client's messages until the connection ends.
func (i *Instance) read(c *client) {
	defer i.running.Done()
	for {
		op, data, err := c.conn.ReadMessage()
		if err != nil {
			i.finish(c, err)
			return
		}
		opcode, kind := servers.OpcodeText, "Text"
		if op == wsframe.OpBinary {
			opcode, kind = servers.OpcodeBinary, "Binary"
		}
		c.messagesIn.Add(1)
		c.bytesIn.Add(uint64(len(data)))
		i.base.Emit(servers.Event{
			Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.In, Kind: servers.KindMessage,
			Summary: fmt.Sprintf("%s message from client %s", kind, c.info.ID),
			Content: data, WS: &servers.WSInfo{ClientID: c.info.ID, Opcode: opcode},
		})
		i.autoReply(c, op == wsframe.OpBinary, data)
	}
}

const howEcho = "echoed"

// autoReply answers a message received from c: the first auto-reply rule
// that matches a text message sends its reply (after its delay, without
// holding up the next message); otherwise echo, when on, sends the
// message back.
func (i *Instance) autoReply(c *client, binary bool, data []byte) {
	l := i.live.Load()
	if !binary {
		if r, ok := l.rules.Match(data); ok {
			how := fmt.Sprintf("auto-reply rule %d", r.Rule+1)
			if r.Delay <= 0 {
				_ = i.send(c, false, r.Data, how)
				return
			}
			// A client gone meanwhile is not sent to (send checks done).
			time.AfterFunc(r.Delay, func() { _ = i.send(c, false, r.Data, how) })
			return
		}
	}
	if l.cfg.Echo {
		_ = i.send(c, binary, data, howEcho)
	}
}

// finish closes the client's connection (the server closes TCP first,
// RFC 6455 §7.1.1), forgets the client and logs how it ended.
func (i *Instance) finish(c *client, readErr error) {
	_ = c.nc.Close()
	i.mu.Lock()
	delete(i.clients, c.info.ID)
	i.mu.Unlock()

	c.closeMu.Lock()
	server, stopping := c.serverClose, c.stopClose
	c.closeMu.Unlock()
	peer := c.conn.PeerClose()

	ws := &servers.WSInfo{ClientID: c.info.ID}
	var summary string
	switch {
	case server != nil:
		ws.InitiatedBy, ws.CloseCode, ws.CloseReason = "server", int(server.code), server.reason
		switch {
		case stopping:
			summary = "Closed: the server is stopping (" + closeText(server.code, server.reason) + ")"
		default:
			summary = "Disconnected by the server: " + closeText(server.code, server.reason)
		}
		if peer == nil {
			summary += "; the client did not answer"
		}
	case peer != nil:
		ws.InitiatedBy, ws.CloseCode, ws.CloseReason = "client", int(peer.Code), peer.Reason
		summary = "Closed by the client: " + closeText(peer.Code, peer.Reason)
	default:
		if code, ok := protocolFailure(readErr); ok {
			ws.InitiatedBy, ws.CloseCode = "server", int(code)
			i.base.Emit(servers.Event{
				Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.System, Kind: servers.KindError,
				Summary: fmt.Sprintf("Client %s: %s", c.info.ID, messageOf(readErr)), WS: &servers.WSInfo{ClientID: c.info.ID},
			})
			summary = "Closed by the server after a protocol error: " + code.String()
		} else {
			ws.CloseCode = int(wsframe.CloseAbnormalClosure)
			summary = "Connection lost without a close frame (1006)"
		}
	}
	i.base.Emit(servers.Event{
		Peer: c.info.Peer, ConnID: c.info.ConnID, Direction: servers.System, Kind: servers.KindConnectionClose,
		Summary: summary, WS: ws,
	})
	c.sendMu.Lock() // a send in progress logs before the client is gone
	close(c.done)
	c.sendMu.Unlock()
}

// protocolFailure reports whether err is a violation the server answered
// with a close frame, and its code.
func protocolFailure(err error) (wsframe.StatusCode, bool) {
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		return 0, false
	}
	switch e.Code {
	case wsframe.CodeProtocolError, wsframe.CodeInvalidClosePayload, wsframe.CodeInvalidUTF8, wsframe.CodeTooBig:
		return wsframe.CloseCodeOf(e), true
	}
	return 0, false
}

// closeText is "1000 (normal closure) “bye”".
func closeText(code wsframe.StatusCode, reason string) string {
	if reason == "" {
		return code.String()
	}
	return code.String() + " “" + reason + "”"
}
