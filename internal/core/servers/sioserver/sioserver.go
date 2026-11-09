// Package sioserver is the Socket.IO server type (SSRV-03): Engine.IO v4
// over HTTP long-polling and WebSocket, with the upgrade from one to the
// other and the heartbeat, and Socket.IO v5 packets (the protocol of
// socket.io v3 and v4) on top: namespaces with an auth payload, events,
// acknowledgements in both directions, binary attachments and
// disconnects. It lists the connected sockets, emits to one socket, to a
// room or to every socket, answers events with auto-reply rules, and logs
// every packet to the traffic log. The codecs are the client's
// (realtime/engineio, realtime/socketio), framing is wsframe in the server
// role and the HTTP side is httpbase. See docs/servers.md.
package sioserver

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/engineio"
	"github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/httpbase"
)

// TypeName is the Spec.Type of the Socket.IO server.
const TypeName = "socketio"

// Defaults of Options.
const (
	// DefaultWriteTimeout bounds writing one WebSocket message (or the 101
	// response) to a client that does not read.
	DefaultWriteTimeout = 10 * time.Second
	// DefaultConnectTimeout closes an Engine.IO session that joined no
	// namespace in time, as socket.io's connectTimeout (45 s).
	DefaultConnectTimeout = 45 * time.Second
	// DefaultUpgradeTimeout ends an upgrade whose probe is not followed by
	// the upgrade packet, as engine.io's upgradeTimeout (10 s).
	DefaultUpgradeTimeout = 10 * time.Second
	// upgradeNoopEvery ends a pending poll with a noop while upgrading, so
	// the client can pause polling (engine.io checks every 100 ms).
	upgradeNoopEvery = 100 * time.Millisecond
	// closeGrace is how long a closed long-polling client is kept for the
	// poll that takes its close packet, when none was pending.
	closeGrace = 2 * time.Second
)

// Error codes of the operations.
const (
	CodeSocketNotFound = "sioserver.socket_not_found"
	CodeInvalidEvent   = "sioserver.invalid_event"
	CodeSendFailed     = "sioserver.send_failed"
	CodeInvalidRoom    = "sioserver.invalid_room"
)

// Reasons a client or a socket ended, as socket.io names them
// (servers.SIOInfo.Reason).
const (
	ReasonTransportClose   = "transport close"
	ReasonTransportError   = "transport error"
	ReasonPingTimeout      = "ping timeout"
	ReasonParseError       = "parse error"
	ReasonServerShutdown   = "server shutting down"
	ReasonForcedClose      = "forced close"
	ReasonConnectTimeout   = "connect timeout"
	ReasonClientDisconnect = "client namespace disconnect"
	ReasonServerDisconnect = "server namespace disconnect"
)

// Transport names.
const (
	transportPolling   = "polling"
	transportWebSocket = "websocket"
)

// Options change timeouts, for tests.
type Options struct {
	// WriteTimeout: 0 means DefaultWriteTimeout.
	WriteTimeout time.Duration
	// ConnectTimeout: 0 means DefaultConnectTimeout.
	ConnectTimeout time.Duration
	// UpgradeTimeout: 0 means DefaultUpgradeTimeout.
	UpgradeTimeout time.Duration
	// Now is the clock of the dynamic values of the rules; nil means
	// time.Now.
	Now func() time.Time
}

// Type is the Socket.IO server type.
type Type struct {
	opts Options
}

// New returns the Socket.IO server type.
func New(opts Options) Type {
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = DefaultConnectTimeout
	}
	if opts.UpgradeTimeout <= 0 {
		opts.UpgradeTimeout = DefaultUpgradeTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return Type{opts: opts}
}

func (Type) Name() string  { return TypeName }
func (Type) Label() string { return "Socket.IO" }

// ALPN offers HTTP/1.1 over TLS (SRV-05): the WebSocket upgrade needs it.
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
	return nil, errs.New(errs.Internal, fmt.Sprintf("A Socket.IO server cannot use a %T configuration.", config)).
		WithCode(CodeInvalidConfig)
}

// Start serves Socket.IO on ln.
func (t Type) Start(spec servers.Spec, ln net.Listener, sink servers.Sink) (servers.Instance, error) {
	cfg, err := t.NormalizeConfig(spec.Config)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	inst := &Instance{base: httpbase.New(sink), opts: t.opts, sessions: map[string]*session{}}
	inst.Reconfigure(cfg)
	inst.base.Serve(ln, http.HandlerFunc(inst.serveHTTP))
	return inst, nil
}

// Socket is a snapshot of a connected socket: one client connected to one
// namespace.
type Socket struct {
	// ID identifies the socket within one run of the server: the client's
	// number for the main namespace ("3"), followed by the namespace for
	// another ("3/admin").
	ID string
	// ClientID is the number of its Engine.IO session ("3").
	ClientID string
	// SID is the socket ID sent to the client in the CONNECT packet.
	SID       string
	Namespace string
	// Session is the Engine.IO session ID.
	Session string
	// Transport is the session's transport now: "polling" or "websocket".
	Transport string
	Peer      string
	// Target is the request target of the handshake (path and query).
	Target    string
	Origin    string
	UserAgent string
	// Auth is the CONNECT's auth payload as received, "" when none.
	Auth string
	// Rooms are the rooms the socket is in (besides its own), sorted.
	Rooms       []string
	ConnectedAt time.Time
	// EventsIn and EventsOut count the events and acknowledgements
	// received from and sent to the socket.
	EventsIn  uint64
	EventsOut uint64
}

// Target says where Emit sends: to the socket SocketID, else to every
// socket in Room, else to every socket.
type Target struct {
	SocketID string
	Room     string
}

// EmitResult counts the sockets an emit reached.
type EmitResult struct {
	Sent   int
	Failed int
}

// Hub is the running Socket.IO server's socket operations, implemented by
// *Instance.
type Hub interface {
	Sockets() []Socket
	Emit(to Target, event string, args []any, ack bool) (EmitResult, error)
	Disconnect(socketID string, closeClient bool) error
	SetRooms(socketID string, rooms []string) error
}

var _ Hub = (*Instance)(nil)
var _ servers.Reconfigurable = (*Instance)(nil)

// Instance is one run of a Socket.IO server. It implements
// servers.Instance, servers.Reconfigurable and Hub.
type Instance struct {
	base *httpbase.Instance
	opts Options
	live atomic.Pointer[live]

	mu         sync.Mutex
	sessions   map[string]*session // by Engine.IO session ID
	nextClient uint64
	stopping   bool
	running    sync.WaitGroup // WebSocket readers and heartbeats, added under mu while not stopping

	stopOnce sync.Once
	stopErr  error
}

type live struct {
	cfg   Config
	rules *ruleSet
}

func (i *Instance) Done() <-chan struct{} { return i.base.Done() }
func (i *Instance) Err() error            { return i.base.Err() }

// Reconfigure applies config (a normalized Config): the path, transports,
// CORS and heartbeat to new clients, the namespaces, auth and replies to
// connected clients too.
func (i *Instance) Reconfigure(config any) {
	c, ok := config.(Config)
	if !ok {
		return
	}
	i.live.Store(&live{cfg: c, rules: compileRules(c.Rules)})
}

// Stop refuses new clients, closes every client (an Engine.IO close
// packet, then the transport), waits for their goroutines and stops the
// HTTP server. The clients close concurrently; when ctx ends first, their
// connections are cut so that a client that does not read cannot hold
// Stop. Every close is logged before it returns unless ctx ended.
func (i *Instance) Stop(ctx context.Context) error {
	i.stopOnce.Do(func() {
		i.mu.Lock()
		i.stopping = true
		list := i.sortedSessions()
		i.mu.Unlock()
		var closing sync.WaitGroup
		for _, s := range list {
			closing.Go(func() { s.close(ReasonServerShutdown, "server", true) })
		}
		if !waitCtx(ctx, closing.Wait) {
			for _, s := range list {
				s.abort()
			}
		}
		// Long-polling clients get their close packet with their next poll.
		for _, s := range list {
			select {
			case <-s.forgotten:
			case <-ctx.Done():
			}
		}
		waitCtx(ctx, i.running.Wait)
		i.stopErr = i.base.Stop(ctx)
	})
	return i.stopErr
}

// waitCtx runs wait and reports whether it returned before ctx ended.
func waitCtx(ctx context.Context, wait func()) bool {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (i *Instance) sortedSessions() []*session {
	list := make([]*session, 0, len(i.sessions))
	for _, s := range i.sessions {
		list = append(list, s)
	}
	slices.SortFunc(list, func(a, b *session) int { return cmp.Compare(a.num, b.num) })
	return list
}

// Sockets returns the connected sockets, by client and then namespace.
func (i *Instance) Sockets() []Socket {
	i.mu.Lock()
	list := i.sortedSessions()
	i.mu.Unlock()
	var out []Socket
	for _, s := range list {
		out = append(out, s.snapshot()...)
	}
	if out == nil {
		out = []Socket{}
	}
	return out
}

// socket finds a connected socket by its ID.
func (i *Instance) socket(id string) (*socket, error) {
	num, nsp, _ := strings.Cut(id, "/")
	nsp = "/" + nsp
	i.mu.Lock()
	var s *session
	for _, x := range i.sessions {
		if x.id == num {
			s = x
			break
		}
	}
	i.mu.Unlock()
	if s != nil {
		s.mu.Lock()
		so := s.sockets[nsp]
		s.mu.Unlock()
		if so != nil {
			return so, nil
		}
	}
	return nil, errs.New(errs.NotFound, "This socket is no longer connected.").
		WithCode(CodeSocketNotFound).WithDetail("socketId", id)
}

// Emit sends the event with args ([]byte anywhere in them is a binary
// attachment; json.RawMessage is sent as it is) to the sockets of to, each
// in its own namespace. With ack, each packet asks for an acknowledgement,
// whose arrival is logged with its round trip. Each packet is logged
// before it is written.
func (i *Instance) Emit(to Target, event string, args []any, ack bool) (EmitResult, error) {
	if err := checkEventName(event, false); err != nil {
		return EmitResult{}, err
	}
	var targets []*socket
	switch {
	case to.SocketID != "":
		so, err := i.socket(to.SocketID)
		if err != nil {
			return EmitResult{}, err
		}
		targets = []*socket{so}
	default:
		i.mu.Lock()
		list := i.sortedSessions()
		i.mu.Unlock()
		for _, s := range list {
			for _, so := range s.sortedSockets() {
				if to.Room == "" || so.inRoom(to.Room) {
					targets = append(targets, so)
				}
			}
		}
	}
	// Check the arguments once, before anything is sent.
	if _, err := socketio.NewEvent("", event, args...); err != nil {
		return EmitResult{}, err
	}
	var res EmitResult
	for _, so := range targets {
		if err := so.emit(event, args, ack, ""); err != nil {
			if to.SocketID != "" {
				return EmitResult{}, err
			}
			res.Failed++
			continue
		}
		res.Sent++
	}
	return res, nil
}

// Disconnect sends DISCONNECT to the socket (socket.disconnect() in
// socket.io); with closeClient the client's whole Engine.IO session is
// closed too (socket.disconnect(true)).
func (i *Instance) Disconnect(socketID string, closeClient bool) error {
	so, err := i.socket(socketID)
	if err != nil {
		return err
	}
	if closeClient {
		so.s.close(ReasonForcedClose, "server", true)
		return nil
	}
	so.s.serverDisconnect(so)
	return nil
}

// SetRooms puts the socket in exactly these rooms (besides its own).
func (i *Instance) SetRooms(socketID string, rooms []string) error {
	so, err := i.socket(socketID)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	for _, r := range rooms {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if len(r) > 256 || strings.ContainsAny(r, ",\r\n") {
			return errs.New(errs.InvalidArgument, fmt.Sprintf("%q is not a room name: at most 256 bytes, no commas or line breaks.", r)).WithCode(CodeInvalidRoom)
		}
		set[r] = true
	}
	so.s.mu.Lock()
	so.rooms = set
	so.s.mu.Unlock()
	so.s.inst.base.Emit(servers.Event{
		Peer: so.s.peer, ConnID: so.s.currentConn(), Direction: servers.System, Kind: servers.KindMessage,
		Summary: fmt.Sprintf("Socket %s is in the rooms: %s", so.id, roomList(so.roomsSorted())),
		SIO:     so.info(""),
	})
	return nil
}

func roomList(rooms []string) string {
	if len(rooms) == 0 {
		return "(none)"
	}
	return strings.Join(rooms, ", ")
}

// ---- HTTP ----

// engineError is an Engine.IO error answer ({"code":…,"message":…}).
type engineError struct {
	code int
	msg  string
}

var (
	errTransportUnknown = engineError{0, "Transport unknown"}
	errSessionUnknown   = engineError{1, "Session ID unknown"}
	errBadHandshake     = engineError{2, "Bad handshake method"}
	errBadRequest       = engineError{3, "Bad request"}
	errUnsupported      = engineError{5, "Unsupported protocol version"}
)

// serveHTTP answers the Engine.IO requests: the handshake over either
// transport, polls and posts of a session, and the WebSocket upgrade.
func (i *Instance) serveHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := &i.live.Load().cfg
	if !cfg.matchesPath(r.URL.Path) {
		i.reject(w, r, cfg, http.StatusNotFound, "no Socket.IO endpoint on this path", "text/plain; charset=utf-8",
			fmt.Sprintf("No Socket.IO endpoint at %s. This server accepts Socket.IO clients at %s.\n", r.URL.Path, cfg.Path))
		return
	}
	if r.Method == http.MethodOptions {
		i.preflight(w, r, cfg)
		return
	}
	q := r.URL.Query()
	if q.Get("EIO") != "4" {
		i.rejectEngine(w, r, cfg, errUnsupported, "Engine.IO "+strconv.Quote(q.Get("EIO"))+" is not spoken; this server speaks Engine.IO 4 (Socket.IO v3 and v4 clients)")
		return
	}
	transport := q.Get("transport")
	if (transport != transportPolling && transport != transportWebSocket) || !cfg.allows(transport) {
		i.rejectEngine(w, r, cfg, errTransportUnknown, "transport "+strconv.Quote(transport)+" is not accepted")
		return
	}
	sid := q.Get("sid")
	if sid == "" {
		i.mu.Lock()
		stopping := i.stopping
		i.mu.Unlock()
		if stopping {
			i.reject(w, r, cfg, http.StatusServiceUnavailable, "the server is stopping", "text/plain; charset=utf-8", "The server is stopping.\n")
			return
		}
		if transport == transportWebSocket {
			i.openWebSocket(w, r, cfg)
			return
		}
		if r.Method != http.MethodGet {
			i.rejectEngine(w, r, cfg, errBadHandshake, "the handshake must be a GET request")
			return
		}
		i.openPolling(w, r, cfg)
		return
	}
	i.mu.Lock()
	s := i.sessions[sid]
	i.mu.Unlock()
	if s == nil {
		i.rejectEngine(w, r, cfg, errSessionUnknown, "no client has this session ID")
		return
	}
	switch {
	case transport == transportWebSocket:
		i.upgrade(w, r, cfg, s)
	case r.Method == http.MethodGet:
		s.poll(w, r, cfg)
	case r.Method == http.MethodPost:
		s.post(w, r, cfg)
	default:
		i.rejectEngine(w, r, cfg, errBadRequest, "a long-polling request must be a GET or a POST")
	}
}

// setCORS adds the CORS headers of cfg for the request r.
func setCORS(h http.Header, r *http.Request, cfg *Config) {
	switch cfg.AllowOrigin {
	case "":
		return
	case OriginAny:
		if origin := r.Header.Get("Origin"); origin != "" {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
		} else {
			h.Set("Access-Control-Allow-Origin", "*")
		}
	default:
		h.Set("Access-Control-Allow-Origin", cfg.AllowOrigin)
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	h.Add("Vary", "Origin")
}

// preflight answers a CORS preflight request: 204, with the CORS headers
// when the server allows other origins.
func (i *Instance) preflight(w http.ResponseWriter, r *http.Request, cfg *Config) {
	peer, connID := i.base.ReadRequest(r, nil)
	h := w.Header()
	why := "CORS preflight; other origins are not allowed"
	if cfg.AllowOrigin != "" {
		setCORS(h, r, cfg)
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
			h.Set("Access-Control-Allow-Headers", req)
		}
		h.Set("Access-Control-Max-Age", "600")
		why = "CORS preflight"
	}
	w.WriteHeader(http.StatusNoContent)
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: fmt.Sprintf("204 No Content (%s)", why),
		HTTP:    &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Status: http.StatusNoContent, Headers: httpbase.HeaderList(h)},
	})
}

// rejectEngine answers 400 with an Engine.IO error, as engine.io does.
func (i *Instance) rejectEngine(w http.ResponseWriter, r *http.Request, cfg *Config, e engineError, why string) {
	body, _ := json.Marshal(map[string]any{"code": e.code, "message": e.msg})
	i.reject(w, r, cfg, http.StatusBadRequest, why, "application/json", string(body))
}

// reject answers r with status and a body and logs both.
func (i *Instance) reject(w http.ResponseWriter, r *http.Request, cfg *Config, status int, why, contentType, body string) {
	peer, connID := i.base.ReadRequest(r, nil)
	h := w.Header()
	setCORS(h, r, cfg)
	h.Set("Content-Type", contentType)
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

// newSession registers a session for the handshake request r, or returns
// nil when the server is stopping. A WebSocket session gets its conn, and
// its reader is counted in running (the caller runs readWS).
func (i *Instance) newSession(r *http.Request, cfg *Config, transport string, connID uint64, conn *wsframe.Conn, nc net.Conn) *session {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.stopping {
		return nil
	}
	i.nextClient++
	s := &session{
		inst:        i,
		num:         i.nextClient,
		id:          strconv.FormatUint(i.nextClient, 10),
		sid:         newID(),
		peer:        r.RemoteAddr,
		target:      r.RequestURI,
		origin:      r.Header.Get("Origin"),
		userAgent:   r.Header.Get("User-Agent"),
		connectedAt: time.Now(),
		transport:   transport,
		connID:      connID,
		pingEvery:   time.Duration(cfg.PingIntervalMs) * time.Millisecond,
		pingWait:    time.Duration(cfg.PingTimeoutMs) * time.Millisecond,
		maxPayload:  cfg.MaxPayload,
		wake:        make(chan struct{}, 1),
		pong:        make(chan struct{}, 1),
		done:        make(chan struct{}),
		forgotten:   make(chan struct{}),
		sockets:     map[string]*socket{},
		upgrades:    map[net.Conn]bool{},
		dec:         socketio.NewDecoder(socketio.Options{MaxPacketSize: cfg.MaxPayload}),
		ws:          conn,
		nc:          nc,
	}
	s.connectTimer = time.AfterFunc(i.opts.ConnectTimeout, s.connectTimedOut)
	i.sessions[s.sid] = s
	i.running.Add(1)
	if conn != nil {
		i.running.Add(1)
	}
	go s.heartbeat()
	return s
}

// handshake is the open packet of s.
func (s *session) handshake(cfg *Config) engineio.Handshake {
	upgrades := []string{}
	if s.transport == transportPolling && cfg.allows(transportWebSocket) {
		upgrades = []string{transportWebSocket}
	}
	return engineio.Handshake{
		SID: s.sid, Upgrades: upgrades, PingInterval: cfg.PingIntervalMs, PingTimeout: cfg.PingTimeoutMs, MaxPayload: cfg.MaxPayload,
	}
}

// openPolling runs the handshake of a long-polling client.
func (i *Instance) openPolling(w http.ResponseWriter, r *http.Request, cfg *Config) {
	peer, connID := i.base.ReadRequest(r, nil)
	s := i.newSession(r, cfg, transportPolling, connID, nil, nil)
	if s == nil {
		i.reject(w, r, cfg, http.StatusServiceUnavailable, "the server is stopping", "text/plain; charset=utf-8", "The server is stopping.\n")
		return
	}
	hs := s.handshake(cfg)
	open, _ := hs.Packet()
	body, _ := engineio.EncodePayload([]engineio.Packet{open})
	h := w.Header()
	setCORS(h, r, cfg)
	h.Set("Content-Type", "text/plain; charset=UTF-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: fmt.Sprintf("200 OK: client %s connected over long-polling (session %s)", s.id, s.sid),
		Content: open.Data,
		HTTP:    &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Status: http.StatusOK, Headers: httpbase.HeaderList(h)},
		SIO:     s.info(),
	})
}

// wsAccept checks a WebSocket upgrade request and completes it, logging
// the request and the 101 response. It returns nil when the request was
// answered with an error.
func (i *Instance) wsAccept(w http.ResponseWriter, r *http.Request, cfg *Config, what string, sio func() *servers.SIOInfo) (*wsframe.Conn, net.Conn, uint64) {
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if r.Method != http.MethodGet || !headerHasToken(r.Header, "Upgrade", "websocket") || !headerHasToken(r.Header, "Connection", "upgrade") ||
		strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" || !validKey(key) {
		i.rejectEngine(w, r, cfg, errBadRequest, "not a valid WebSocket upgrade")
		return nil, nil, 0
	}
	connID := i.base.ConnID(r) // before Hijack, which forgets the connection
	peer := r.RemoteAddr
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.In, Kind: servers.KindRequest,
		Summary: r.Method + " " + r.RequestURI,
		HTTP:    &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: r.Proto, Headers: httpbase.RequestHeaders(r)},
		SIO:     sio(),
	})
	nc, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		i.base.Emit(servers.Event{
			Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindError,
			Summary: "The WebSocket upgrade failed: " + err.Error(), SIO: sio(),
		})
		return nil, nil, 0
	}
	_ = nc.SetDeadline(time.Time{})
	resp := []servers.Header{
		{Name: "Upgrade", Value: "websocket"},
		{Name: "Connection", Value: "Upgrade"},
		{Name: "Sec-WebSocket-Accept", Value: wsframe.ComputeAccept(key)},
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
			Summary: "The WebSocket upgrade response could not be sent: " + err.Error(), SIO: sio(),
		})
		return nil, nil, 0
	}
	limit := int64(cfg.MaxPayload)
	conn := wsframe.NewConn(nc, wsframe.Options{
		Role: wsframe.RoleServer, Limits: wsframe.Limits{MaxPayload: limit}, MaxMessageSize: limit, Reader: brw.Reader,
	})
	i.base.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: "101 Switching Protocols: " + what,
		HTTP:    &servers.HTTPInfo{Method: r.Method, Target: r.RequestURI, Proto: "HTTP/1.1", Status: http.StatusSwitchingProtocols, Headers: resp},
		SIO:     sio(),
	})
	return conn, nc, connID
}

// openWebSocket runs the handshake of a client that starts over
// WebSocket, then reads its messages.
func (i *Instance) openWebSocket(w http.ResponseWriter, r *http.Request, cfg *Config) {
	conn, nc, connID := i.wsAccept(w, r, cfg, "a client connects over WebSocket", func() *servers.SIOInfo {
		return &servers.SIOInfo{Transport: transportWebSocket}
	})
	if conn == nil {
		return
	}
	s := i.newSession(r, cfg, transportWebSocket, connID, conn, nc)
	if s == nil {
		_, _ = conn.WriteClose(wsframe.CloseGoingAway, "server stopping")
		_ = nc.Close()
		return
	}
	open, _ := s.handshake(cfg).Packet()
	i.base.Emit(servers.Event{
		Peer: s.peer, ConnID: connID, Direction: servers.Out, Kind: servers.KindConnectionOpen,
		Summary: fmt.Sprintf("Client %s connected over WebSocket (session %s)", s.id, s.sid),
		Content: open.Data, SIO: s.info(),
	})
	_ = s.send(open)
	s.readWS(conn, nc)
}

// upgrade runs the upgrade of a long-polling session to WebSocket: the
// probe (ping "probe", pong "probe"), noops that end the pending poll, and
// the upgrade packet, after which the WebSocket carries everything.
func (i *Instance) upgrade(w http.ResponseWriter, r *http.Request, cfg *Config, s *session) {
	s.mu.Lock()
	ok := s.transport == transportPolling && !s.upgrading && !s.closed
	s.mu.Unlock()
	if !ok {
		i.rejectEngine(w, r, cfg, errBadRequest, "client "+s.id+" cannot be upgraded now")
		return
	}
	conn, nc, connID := i.wsAccept(w, r, cfg, "client "+s.id+" probes WebSocket", func() *servers.SIOInfo { return s.info() })
	if conn == nil {
		return
	}
	i.mu.Lock()
	if i.stopping {
		i.mu.Unlock()
		_ = nc.Close()
		return
	}
	i.running.Add(1)
	i.mu.Unlock()
	// The WebSocket is the session's from here, so that its close closes
	// it, and the whole upgrade, the probe included, has UpgradeTimeout.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = nc.Close()
		i.running.Done()
		return
	}
	s.upgrades[nc] = true
	s.mu.Unlock()
	timer := time.AfterFunc(i.opts.UpgradeTimeout, func() { _ = nc.Close() })
	probed := false
	fail := func(why string) {
		timer.Stop()
		s.mu.Lock()
		if s.probe == nc {
			s.upgrading, s.probe = false, nil
		}
		delete(s.upgrades, nc)
		s.mu.Unlock()
		_ = nc.Close()
		if why != "" {
			i.base.Emit(servers.Event{
				Peer: s.peer, ConnID: connID, Direction: servers.System, Kind: servers.KindError,
				Summary: fmt.Sprintf("The upgrade of client %s failed: %s", s.id, why), SIO: s.info(),
			})
		}
		i.running.Done()
	}
	for {
		op, data, err := conn.ReadMessage()
		if err != nil {
			select {
			case <-s.done:
				fail("")
			default:
				fail("the WebSocket closed before the upgrade")
			}
			return
		}
		p, err := engineio.DecodePacket(data, op == wsframe.OpBinary, engineio.Options{MaxSize: s.maxPayload})
		switch {
		case err != nil:
			fail(messageOf(err))
			return
		case p.Type == engineio.Ping && string(p.Data) == "probe" && !probed:
			s.mu.Lock()
			if s.closed || s.upgrading {
				s.mu.Unlock()
				fail("")
				return
			}
			s.upgrading, s.probe = true, nc
			s.mu.Unlock()
			probed = true
			_ = nc.SetWriteDeadline(time.Now().Add(i.opts.WriteTimeout))
			err := conn.WriteMessage(wsframe.OpText, []byte("3probe"))
			_ = nc.SetWriteDeadline(time.Time{})
			if err != nil {
				fail(messageOf(err))
				return
			}
			timer.Reset(i.opts.UpgradeTimeout)
			s.wakePoll()
		case p.Type == engineio.Upgrade && probed:
			timer.Stop()
			if !s.switchToWebSocket(conn, nc, connID) {
				fail("")
				return
			}
			i.base.Emit(servers.Event{
				Peer: s.peer, ConnID: connID, Direction: servers.System, Kind: servers.KindMessage,
				Summary: fmt.Sprintf("Client %s upgraded to WebSocket", s.id), SIO: s.info(),
			})
			s.readWS(conn, nc)
			return
		default:
			fail("the client sent a " + p.Type.String() + " packet instead of the probe or the upgrade")
			return
		}
	}
}

// ---- Sessions ----

// session is one Engine.IO session: one client.
type session struct {
	inst        *Instance
	num         uint64
	id          string
	sid         string
	peer        string
	target      string
	origin      string
	userAgent   string
	connectedAt time.Time
	pingEvery   time.Duration
	pingWait    time.Duration
	maxPayload  int

	// sendMu serializes writes and the switch of an upgrade, so packets
	// keep their order across it.
	sendMu sync.Mutex

	mu           sync.Mutex
	transport    string
	connID       uint64
	outbox       []engineio.Packet
	polling      bool // a GET is pending
	upgrading    bool
	probe        net.Conn          // the WebSocket of the upgrade in progress
	upgrades     map[net.Conn]bool // the WebSockets of the upgrades not done, probed or not
	ws           *wsframe.Conn
	nc           net.Conn
	closed       bool
	sockets      map[string]*socket // by namespace
	connectTimer *time.Timer

	wake chan struct{}
	pong chan struct{}
	done chan struct{}
	// forgotten is closed once the session left inst.sessions: at close,
	// or for a long-polling client once a poll took its close packet.
	forgotten  chan struct{}
	forgetOnce sync.Once

	// procMu serializes the packets received (the decoder is stateful).
	procMu sync.Mutex
	dec    *socketio.Decoder
}

func (s *session) info() *servers.SIOInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &servers.SIOInfo{ClientID: s.id, Session: s.sid, Transport: s.transport}
}

func (s *session) currentConn() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connID
}

func (s *session) sortedSockets() []*socket {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]*socket, 0, len(s.sockets))
	for _, so := range s.sockets {
		list = append(list, so)
	}
	slices.SortFunc(list, func(a, b *socket) int {
		if a.nsp == socketio.MainNamespace {
			return -1
		}
		if b.nsp == socketio.MainNamespace {
			return 1
		}
		return strings.Compare(a.nsp, b.nsp)
	})
	return list
}

func (s *session) snapshot() []Socket {
	list := s.sortedSockets()
	s.mu.Lock()
	transport := s.transport
	s.mu.Unlock()
	out := make([]Socket, 0, len(list))
	for _, so := range list {
		out = append(out, Socket{
			ID: so.id, ClientID: s.id, SID: so.sid, Namespace: so.nsp, Session: s.sid, Transport: transport,
			Peer: s.peer, Target: s.target, Origin: s.origin, UserAgent: s.userAgent, Auth: so.auth,
			Rooms: so.roomsSorted(), ConnectedAt: so.connectedAt, EventsIn: so.eventsIn.Load(), EventsOut: so.eventsOut.Load(),
		})
	}
	return out
}

// heartbeat pings the client every interval and closes the session when
// no pong comes within the timeout.
func (s *session) heartbeat() {
	defer s.inst.running.Done()
	for {
		t := time.NewTimer(s.pingEvery)
		select {
		case <-t.C:
		case <-s.done:
			t.Stop()
			return
		}
		select {
		case <-s.pong:
		default:
		}
		if s.send(engineio.Packet{Type: engineio.Ping}) != nil {
			return
		}
		t = time.NewTimer(s.pingWait)
		select {
		case <-s.pong:
			t.Stop()
		case <-t.C:
			s.close(ReasonPingTimeout, "", false)
			return
		case <-s.done:
			t.Stop()
			return
		}
	}
}

func (s *session) connectTimedOut() {
	s.mu.Lock()
	idle := len(s.sockets) == 0
	s.mu.Unlock()
	if idle {
		s.close(ReasonConnectTimeout, "server", true)
	}
}

func (s *session) wakePoll() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

var errClosed = errs.New(errs.NotFound, "The client is no longer connected.").WithCode(CodeSocketNotFound)

// send writes Engine.IO packets on the WebSocket, or queues them for the
// next poll.
func (s *session) send(pkts ...engineio.Packet) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errClosed
	}
	if s.transport != transportWebSocket {
		s.outbox = append(s.outbox, pkts...)
		s.mu.Unlock()
		s.wakePoll()
		return nil
	}
	conn, nc := s.ws, s.nc
	s.mu.Unlock()
	if err := s.writeWS(conn, nc, pkts); err != nil {
		go s.close(ReasonTransportError, "", false)
		return err
	}
	return nil
}

// writeWS writes packets as WebSocket messages; the caller holds sendMu.
func (s *session) writeWS(conn *wsframe.Conn, nc net.Conn, pkts []engineio.Packet) error {
	for _, p := range pkts {
		data, binary, err := engineio.EncodePacket(p)
		if err != nil {
			return err
		}
		op := wsframe.OpText
		if binary {
			op = wsframe.OpBinary
		}
		_ = nc.SetWriteDeadline(time.Now().Add(s.inst.opts.WriteTimeout))
		err = conn.WriteMessage(op, data)
		_ = nc.SetWriteDeadline(time.Time{})
		if err != nil {
			return err
		}
	}
	return nil
}

// switchToWebSocket makes the probed WebSocket the session's transport
// and sends it what was queued meanwhile (noops left out).
func (s *session) switchToWebSocket(conn *wsframe.Conn, nc net.Conn, connID uint64) bool {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	if s.closed || s.probe != nc {
		s.mu.Unlock()
		return false
	}
	s.transport, s.upgrading, s.probe = transportWebSocket, false, nil
	delete(s.upgrades, nc)
	s.ws, s.nc, s.connID = conn, nc, connID
	var rest []engineio.Packet
	for _, p := range s.outbox {
		if p.Type != engineio.Noop {
			rest = append(rest, p)
		}
	}
	s.outbox = nil
	s.mu.Unlock()
	s.wakePoll() // a poll still pending ends with a noop
	if len(rest) > 0 {
		if err := s.writeWS(conn, nc, rest); err != nil {
			go s.close(ReasonTransportError, "", false)
		}
	}
	return true
}

// poll answers a long-polling GET with the queued packets, waiting for
// some.
func (s *session) poll(w http.ResponseWriter, r *http.Request, cfg *Config) {
	s.mu.Lock()
	if s.polling {
		s.mu.Unlock()
		s.inst.rejectEngine(w, r, cfg, errBadRequest, "client "+s.id+" sent a second poll while one is pending")
		s.close(ReasonTransportError, "", false)
		return
	}
	if s.transport != transportPolling {
		s.mu.Unlock()
		s.inst.rejectEngine(w, r, cfg, errBadRequest, "client "+s.id+" is on WebSocket")
		return
	}
	s.polling = true
	s.mu.Unlock()
	connID := s.inst.base.ConnID(r)
	s.mu.Lock()
	s.connID = connID
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.polling = false
		s.mu.Unlock()
	}()
	for {
		s.mu.Lock()
		out := s.outbox
		s.outbox = nil
		closed, upgrading, transport := s.closed, s.upgrading, s.transport
		s.mu.Unlock()
		switch {
		case len(out) > 0:
		case closed:
			out = []engineio.Packet{{Type: engineio.Close}}
		case transport != transportPolling:
			out = []engineio.Packet{{Type: engineio.Noop}}
		}
		if len(out) > 0 {
			if closed {
				defer s.forget()
			}
			body, err := engineio.EncodePayload(out)
			if err != nil {
				return
			}
			h := w.Header()
			setCORS(h, r, cfg)
			h.Set("Content-Type", "text/plain; charset=UTF-8")
			h.Set("Content-Length", strconv.Itoa(len(body)))
			h.Set("Cache-Control", "no-store")
			_, _ = w.Write(body)
			return
		}
		var tick <-chan time.Time
		if upgrading {
			tick = time.After(upgradeNoopEvery)
		}
		select {
		case <-s.wake:
		case <-tick:
			s.mu.Lock()
			if len(s.outbox) == 0 {
				s.outbox = []engineio.Packet{{Type: engineio.Noop}}
			}
			s.mu.Unlock()
		case <-s.done:
		case <-r.Context().Done():
			return
		}
	}
}

// post handles a long-polling POST: the packets of its payload.
func (s *session) post(w http.ResponseWriter, r *http.Request, cfg *Config) {
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(s.maxPayload)+1))
	if err != nil {
		return
	}
	if len(body) > s.maxPayload {
		s.inst.reject(w, r, cfg, http.StatusRequestEntityTooLarge, fmt.Sprintf("client %s sent more than %d bytes", s.id, s.maxPayload),
			"text/plain; charset=utf-8", "Payload Too Large\n")
		s.close(ReasonTransportError, "", false)
		return
	}
	pkts, err := engineio.DecodePayload(body, engineio.Options{MaxSize: s.maxPayload})
	if err != nil {
		s.inst.rejectEngine(w, r, cfg, errBadRequest, "client "+s.id+" sent a payload that is not Engine.IO: "+messageOf(err))
		s.parseError(err)
		return
	}
	connID := s.inst.base.ConnID(r)
	s.mu.Lock()
	s.connID = connID
	s.mu.Unlock()
	h := w.Header()
	setCORS(h, r, cfg)
	h.Set("Content-Type", "text/html")
	h.Set("Content-Length", "2")
	_, _ = w.Write([]byte("ok"))
	for _, p := range pkts {
		s.onPacket(p)
	}
}

// readWS reads the session's WebSocket until it ends; the caller added
// to inst.running.
func (s *session) readWS(conn *wsframe.Conn, nc net.Conn) {
	defer s.inst.running.Done()
	defer nc.Close()
	for {
		op, data, err := conn.ReadMessage()
		if err != nil {
			if conn.PeerClose() != nil || errors.Is(err, io.EOF) {
				s.close(ReasonTransportClose, "client", false)
			} else {
				s.close(ReasonTransportError, "", false)
			}
			return
		}
		p, err := engineio.DecodePacket(data, op == wsframe.OpBinary, engineio.Options{MaxSize: s.maxPayload})
		if err != nil {
			s.parseError(err)
			return
		}
		s.onPacket(p)
		if p.Type == engineio.Close {
			return
		}
	}
}

// onPacket handles one Engine.IO packet from the client.
func (s *session) onPacket(p engineio.Packet) {
	s.procMu.Lock()
	defer s.procMu.Unlock()
	select {
	case <-s.done:
		return
	default:
	}
	switch p.Type {
	case engineio.Pong:
		select {
		case s.pong <- struct{}{}:
		default:
		}
	case engineio.Ping:
		_ = s.send(engineio.Packet{Type: engineio.Pong, Data: p.Data})
	case engineio.Close:
		s.close(ReasonTransportClose, "client", false)
	case engineio.Message:
		sp, err := s.dec.Feed(p)
		if err != nil {
			s.parseError(err)
			return
		}
		if sp != nil {
			s.handle(sp)
		}
	}
}

// abort cuts the session's connections without waiting for writes in
// progress, which then fail, so that a close held by them ends.
func (s *session) abort() {
	s.mu.Lock()
	conns := []net.Conn{}
	if s.nc != nil {
		conns = append(conns, s.nc)
	}
	for u := range s.upgrades {
		conns = append(conns, u)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (s *session) parseError(err error) {
	s.inst.base.Emit(servers.Event{
		Peer: s.peer, ConnID: s.currentConn(), Direction: servers.System, Kind: servers.KindError,
		Summary: fmt.Sprintf("Client %s sent a packet that cannot be decoded: %s", s.id, messageOf(err)), SIO: s.info(),
	})
	s.close(ReasonParseError, "server", false)
}

// close ends the session once: an Engine.IO close packet first when
// sendClose, then the transport. It logs how the client ended.
func (s *session) close(reason, initiatedBy string, sendClose bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	// Closed before the close packet goes out, so that the client's
	// answer to it (closing the transport) finds the session closed and
	// does not log its own reason instead of this one.
	s.closed = true
	transport, conn, nc := s.transport, s.ws, s.nc
	if sendClose && transport != transportWebSocket {
		s.outbox = append(s.outbox, engineio.Packet{Type: engineio.Close})
	}
	nsps := make([]string, 0, len(s.sockets))
	for nsp := range s.sockets {
		nsps = append(nsps, nsp)
	}
	s.sockets = map[string]*socket{}
	if s.connectTimer != nil {
		s.connectTimer.Stop()
	}
	linger := sendClose && transport != transportWebSocket
	upgrades := s.upgrades
	s.upgrades = map[net.Conn]bool{}
	s.mu.Unlock()
	slices.Sort(nsps)
	if linger {
		// Kept for the poll that takes the close packet.
		time.AfterFunc(closeGrace, s.forget)
	} else {
		s.forget()
	}
	close(s.done)
	s.wakePoll()
	for u := range upgrades {
		_ = u.Close()
	}
	if conn != nil {
		s.sendMu.Lock()
		if sendClose {
			_ = s.writeWS(conn, nc, []engineio.Packet{{Type: engineio.Close}})
		}
		_ = nc.SetWriteDeadline(time.Now().Add(time.Second))
		code := wsframe.CloseNormalClosure
		if reason == ReasonServerShutdown {
			code = wsframe.CloseGoingAway
		}
		_, _ = conn.WriteClose(code, "")
		s.sendMu.Unlock()
		_ = nc.Close()
	}
	summary := fmt.Sprintf("Client %s closed: %s", s.id, reason)
	if len(nsps) > 0 {
		summary += " (sockets on " + strings.Join(nsps, ", ") + ")"
	}
	info := s.info()
	info.Reason, info.InitiatedBy = reason, initiatedBy
	s.inst.base.Emit(servers.Event{
		Peer: s.peer, ConnID: s.currentConn(), Direction: servers.System, Kind: servers.KindConnectionClose,
		Summary: summary, SIO: info,
	})
}

// forget removes the session from the server's list.
func (s *session) forget() {
	s.forgetOnce.Do(func() {
		s.inst.mu.Lock()
		if s.inst.sessions[s.sid] == s {
			delete(s.inst.sessions, s.sid)
		}
		s.inst.mu.Unlock()
		close(s.forgotten)
	})
}

// ---- Socket.IO ----

// socket is one namespace of a session.
type socket struct {
	s           *session
	id          string
	nsp         string
	sid         string
	auth        string
	connectedAt time.Time
	rooms       map[string]bool // guarded by s.mu

	eventsIn, eventsOut atomic.Uint64

	ackMu   sync.Mutex
	nextAck uint64
	acks    map[uint64]pendingAck
}

type pendingAck struct {
	event string
	sent  time.Time
}

func (so *socket) inRoom(room string) bool {
	so.s.mu.Lock()
	defer so.s.mu.Unlock()
	return so.rooms[room] || room == so.sid
}

func (so *socket) roomsSorted() []string {
	so.s.mu.Lock()
	defer so.s.mu.Unlock()
	out := make([]string, 0, len(so.rooms))
	for r := range so.rooms {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

func (so *socket) info(packet string) *servers.SIOInfo {
	in := so.s.info()
	in.SocketID, in.Namespace, in.Packet = so.id, so.nsp, packet
	return in
}

// handle handles one Socket.IO packet from the client.
func (s *session) handle(p *socketio.Packet) {
	nsp := p.Namespace
	if nsp == "" {
		nsp = socketio.MainNamespace
	}
	s.mu.Lock()
	so := s.sockets[nsp]
	s.mu.Unlock()
	size := packetSize(p)
	switch p.Type {
	case socketio.Connect:
		s.connect(p, nsp, size)
		return
	case socketio.ConnectError:
		s.logIn(p, nsp, size, fmt.Sprintf("CONNECT_ERROR from client %s (only servers send it)", s.id), string(p.Data), nil)
		return
	}
	if so == nil {
		s.logIn(p, nsp, size, fmt.Sprintf("%s from client %s for %s, which it has not joined (ignored)", p.Type, s.id, nsp), string(p.Data), nil)
		return
	}
	switch p.Type {
	case socketio.Disconnect:
		s.mu.Lock()
		delete(s.sockets, nsp)
		s.mu.Unlock()
		info := so.info(p.Type.String())
		info.Reason, info.InitiatedBy = ReasonClientDisconnect, "client"
		s.inst.base.Emit(servers.Event{
			Peer: s.peer, ConnID: s.currentConn(), Direction: servers.In, Kind: servers.KindMessage,
			Summary: fmt.Sprintf("Socket %s left %s", so.id, nsp), Size: size, SIO: info,
		})
	case socketio.Event, socketio.BinaryEvent:
		so.eventsIn.Add(1)
		name := p.EventName()
		summary := fmt.Sprintf("Event “%s” from socket %s", name, so.id)
		if p.HasID {
			summary += fmt.Sprintf(" (ack %d)", p.ID)
		}
		info := so.info(p.Type.String())
		info.Event, info.AckID, info.HasAck, info.Attachments = name, p.ID, p.HasID, len(p.Attachments)
		s.inst.base.Emit(servers.Event{
			Peer: s.peer, ConnID: s.currentConn(), Direction: servers.In, Kind: servers.KindMessage,
			Summary: summary, Content: []byte(socketio.DisplayArgs(p.Data, p.Attachments, 1)), Size: size, SIO: info,
		})
		so.reply(p)
	case socketio.Ack, socketio.BinaryAck:
		so.eventsIn.Add(1)
		so.ackMu.Lock()
		a, ok := so.acks[p.ID]
		delete(so.acks, p.ID)
		so.ackMu.Unlock()
		info := so.info(p.Type.String())
		info.AckID, info.HasAck, info.Attachments = p.ID, true, len(p.Attachments)
		summary := fmt.Sprintf("Ack %d from socket %s", p.ID, so.id)
		if ok {
			info.Event, info.RTT = a.event, time.Since(a.sent)
			summary += fmt.Sprintf(" (answers “%s” after %d ms)", a.event, info.RTT.Milliseconds())
		} else {
			summary += " (no event of the server waits for it)"
		}
		s.inst.base.Emit(servers.Event{
			Peer: s.peer, ConnID: s.currentConn(), Direction: servers.In, Kind: servers.KindMessage,
			Summary: summary, Content: []byte(socketio.DisplayArgs(p.Data, p.Attachments, 0)), Size: size, SIO: info,
		})
	}
}

func packetSize(p *socketio.Packet) int {
	text, atts, _ := socketio.Encode(*p)
	n := len(text)
	for _, a := range atts {
		n += len(a)
	}
	return n
}

func (s *session) logIn(p *socketio.Packet, nsp string, size int, summary, content string, extra func(*servers.SIOInfo)) {
	info := s.info()
	info.Namespace, info.Packet = nsp, p.Type.String()
	if extra != nil {
		extra(info)
	}
	s.inst.base.Emit(servers.Event{
		Peer: s.peer, ConnID: s.currentConn(), Direction: servers.In, Kind: servers.KindMessage,
		Summary: summary, Content: []byte(content), Size: size, SIO: info,
	})
}

// connect handles a CONNECT: the namespace and the auth are checked, and
// the socket joins, or CONNECT_ERROR says why not.
func (s *session) connect(p *socketio.Packet, nsp string, size int) {
	cfg := &s.inst.live.Load().cfg
	summary := fmt.Sprintf("CONNECT to %s from client %s", nsp, s.id)
	if len(p.Data) == 0 {
		summary += " (no auth)"
	}
	s.logIn(p, nsp, size, summary, string(p.Data), nil)
	refuse := func(message, reason string) {
		data, _ := json.Marshal(map[string]any{"message": message, "data": map[string]any{"reason": reason}})
		reply := socketio.Packet{Type: socketio.ConnectError, Namespace: nsp, Data: data}
		info := s.info()
		info.Namespace, info.Packet = nsp, reply.Type.String()
		_ = s.sendPacket(reply, servers.Event{
			Summary: fmt.Sprintf("CONNECT_ERROR to client %s: %s refused (%s)", s.id, nsp, message), Content: data, SIO: info,
		}, nil)
	}
	if !cfg.acceptsNamespace(nsp) {
		refuse("Invalid namespace", "namespace")
		return
	}
	if !cfg.authorized(p.Data) {
		refuse("Not authorized", "auth")
		return
	}
	s.mu.Lock()
	so := s.sockets[nsp]
	fresh := so == nil
	if fresh {
		id := s.id
		if nsp != socketio.MainNamespace {
			id += nsp
		}
		so = &socket{
			s: s, id: id, nsp: nsp, sid: newID(), auth: string(p.Data), connectedAt: time.Now(),
			rooms: map[string]bool{}, acks: map[uint64]pendingAck{},
		}
		s.sockets[nsp] = so
	}
	s.mu.Unlock()
	data, _ := json.Marshal(map[string]string{"sid": so.sid})
	reply := socketio.Packet{Type: socketio.Connect, Namespace: nsp, Data: data}
	summary = fmt.Sprintf("CONNECT to client %s: joined %s as socket %s", s.id, nsp, so.id)
	if !fresh {
		summary = fmt.Sprintf("CONNECT to client %s: already joined %s as socket %s", s.id, nsp, so.id)
	}
	_ = s.sendPacket(reply, servers.Event{Summary: summary, Content: data, SIO: so.info(reply.Type.String())}, nil)
}

// sendPacket logs ev (direction out, kind message, the packet's size)
// and writes p. counted, when set, counts the packet as sent to it.
func (s *session) sendPacket(p socketio.Packet, ev servers.Event, counted *socket) error {
	msgs, err := socketio.EncodeMessages(p)
	if err != nil {
		return err
	}
	size := 0
	for _, m := range msgs {
		size += len(m.Data)
	}
	select {
	case <-s.done:
		return errClosed
	default:
	}
	ev.Peer, ev.ConnID, ev.Direction, ev.Kind, ev.Size = s.peer, s.currentConn(), servers.Out, servers.KindMessage, size
	s.inst.base.Emit(ev)
	if err := s.send(msgs...); err != nil {
		if errors.Is(err, errClosed) {
			return err
		}
		return errs.Wrap(err, errs.KindOf(err), fmt.Sprintf("The packet could not be sent to client %s.", s.id)).WithCode(CodeSendFailed)
	}
	if counted != nil {
		counted.eventsOut.Add(1)
	}
	return nil
}

// emit sends an event to the socket; source says what sent it ("" for the
// app).
func (so *socket) emit(event string, args []any, ack bool, source string) error {
	p, err := socketio.NewEvent(so.nsp, event, args...)
	if err != nil {
		return err
	}
	info := so.info(p.Type.String())
	info.Event, info.Attachments, info.Source = event, len(p.Attachments), source
	summary := fmt.Sprintf("Event “%s” to socket %s", event, so.id)
	if ack {
		so.ackMu.Lock()
		id := so.nextAck
		so.nextAck++
		so.acks[id] = pendingAck{event: event, sent: time.Now()}
		so.ackMu.Unlock()
		p.ID, p.HasID = id, true
		info.AckID, info.HasAck = id, true
		summary += fmt.Sprintf(" (ack %d)", id)
	}
	if source != "" {
		summary += " (" + source + ")"
	}
	return so.s.sendPacket(p, servers.Event{
		Summary: summary, Content: []byte(socketio.DisplayArgs(p.Data, p.Attachments, 1)), SIO: info,
	}, so)
}

// ack answers the event with ack ID id.
func (so *socket) ack(id uint64, args []any, source string) error {
	p, err := socketio.NewAck(so.nsp, id, args...)
	if err != nil {
		return err
	}
	info := so.info(p.Type.String())
	info.AckID, info.HasAck, info.Attachments, info.Source = id, true, len(p.Attachments), source
	return so.s.sendPacket(p, servers.Event{
		Summary: fmt.Sprintf("Ack %d to socket %s (%s)", id, so.id, source),
		Content: []byte(socketio.DisplayArgs(p.Data, p.Attachments, 0)), SIO: info,
	}, so)
}

// reply answers an event from the client: the first rule that matches its
// name, else echo and the acknowledgement mode.
func (so *socket) reply(p *socketio.Packet) {
	l := so.s.inst.live.Load()
	name := p.EventName()
	args, err := p.Args()
	if err != nil || len(args) == 0 {
		return
	}
	args = args[1:]
	if r, index, ok := l.rules.match(name); ok {
		source := fmt.Sprintf("auto-reply rule %d", index+1)
		answer := func() {
			now := so.s.inst.opts.Now
			if p.HasID {
				if a, err := ruleArgs(r.Ack, now); err == nil {
					_ = so.ack(p.ID, a, source)
				}
			}
			if r.Emit != "" {
				if a, err := ruleArgs(r.EmitArgs, now); err == nil {
					_ = so.emit(r.Emit, a, false, source)
				}
			}
		}
		if r.DelayMs <= 0 {
			answer()
			return
		}
		// A client gone meanwhile is not sent to (sendPacket checks done).
		time.AfterFunc(time.Duration(r.DelayMs)*time.Millisecond, answer)
		return
	}
	if l.cfg.Echo {
		_ = so.emit(name, args, false, "echo")
	}
	if !p.HasID {
		return
	}
	switch l.cfg.AckMode {
	case AckNone:
	case AckEmpty:
		_ = so.ack(p.ID, nil, "auto-ack")
	default:
		_ = so.ack(p.ID, args, "auto-ack")
	}
}

// serverDisconnect sends DISCONNECT to the socket and forgets it.
func (s *session) serverDisconnect(so *socket) {
	s.mu.Lock()
	if s.sockets[so.nsp] != so {
		s.mu.Unlock()
		return
	}
	delete(s.sockets, so.nsp)
	s.mu.Unlock()
	p, _ := socketio.NewDisconnect(so.nsp)
	info := so.info(p.Type.String())
	info.Reason, info.InitiatedBy = ReasonServerDisconnect, "server"
	_ = s.sendPacket(p, servers.Event{Summary: fmt.Sprintf("DISCONNECT to socket %s: it leaves %s", so.id, so.nsp), SIO: info}, nil)
}

// ---- helpers ----

// newID returns 20 characters of URL-safe Base64 from crypto/rand, like
// engine.io's session and socket IDs.
func newID() string {
	var b [15]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// headerHasToken reports whether the header name lists token, in any
// letter case.
func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func validKey(key string) bool {
	if len(key) != 24 {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(b) == 16
}

// messageOf is the user-facing message of err.
func messageOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Message != "" {
		return e.Message
	}
	return err.Error()
}
