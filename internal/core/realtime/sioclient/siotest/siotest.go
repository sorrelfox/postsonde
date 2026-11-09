// Package siotest is an in-process Socket.IO server for tests, built on
// the engineio and socketio codecs: by default a v4 server (Engine.IO v4
// over long-polling and WebSocket, with the upgrade; Socket.IO v5
// packets), with Config.SocketIOv3 a v3 one (its handshake has no
// maxPayload) and with Config.V2 a socket.io v2 server (Engine.IO 3,
// Socket.IO 4; SIO-02), written from the protocol documents of both
// versions. It is small and strict rather than complete: one Engine.IO
// session per client, the behaviour of the reference server where the
// tests look (handshake, heartbeat, probe and noop of the upgrade,
// CONNECT_ERROR for unknown namespaces and refused auth,
// acknowledgements) and hooks for the rest.
package siotest

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/realtime/engineio"
	"github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// Config configures a Server. Zero values are the reference server's
// defaults where it has them.
type Config struct {
	// Path is the Engine.IO path; "" means "/socket.io/".
	Path string
	// PingInterval and PingTimeout are sent in the handshake; zero means
	// 25 s and 20 s.
	PingInterval, PingTimeout time.Duration
	// NoPings stops the server from sending pings (a dead server, for
	// ping timeouts).
	NoPings bool
	// NoWebSocket and NoPolling refuse a transport ("Transport unknown").
	NoWebSocket, NoPolling bool
	// NoUpgrade leaves "websocket" out of the handshake's upgrades.
	NoUpgrade bool
	// IgnoreProbe makes the server never answer the upgrade probe.
	IgnoreProbe bool
	// Namespaces are accepted besides "/".
	Namespaces []string
	// Authorize checks a CONNECT's auth payload (nil when there is none);
	// an error refuses the namespace with CONNECT_ERROR {"message": err,
	// "data": {"reason": "auth"}}.
	Authorize func(nsp string, auth json.RawMessage) error
	// SocketIOv3 leaves maxPayload out of the handshake, as socket.io v3
	// (engine.io 4) does.
	SocketIOv3 bool
	// V2 makes a socket.io v2 server (engine.io 3, socket.io-parser 3):
	// the EIO query is not checked, as engine.io 3 does not; long-polling
	// payloads are "<length>:<packet>" with binary as "b4" and Base64, or
	// the XHR2 binary form when the client did not send b64=1; binary
	// WebSocket messages start with the type byte 4; the client pings and
	// the server answers (NoPings: it does not); the main namespace is
	// joined at once with a CONNECT without payload, another one when the
	// client sends CONNECT ("/admin?query"); refusals are CONNECT_ERROR
	// with a string. Authorize gets the query of the handshake (for "/")
	// or of the CONNECT as a JSON object of strings.
	V2 bool
	// OnEvent is called for every EVENT and BINARY_EVENT after the
	// default handling: "echo" is emitted back with the same arguments,
	// "bye" makes the server send DISCONNECT, "close" closes the Engine.IO
	// session, and an ack ID is answered with the event's arguments unless
	// the event is "noack".
	OnEvent func(s *Session, p *socketio.Packet)
}

// Server is a running test server.
type Server struct {
	// URL is the server's http://127.0.0.1:port.
	URL string
	cfg Config
	srv *httptest.Server

	mu       sync.Mutex
	sessions map[string]*Session
	received []socketio.Packet
	requests []*http.Request
	posts    [][]byte
	wake     chan struct{}
}

// Start starts a server; it is closed when the test ends (cleanup).
func Start(cfg Config, cleanup func(func())) *Server {
	if cfg.Path == "" {
		cfg.Path = "/socket.io/"
	}
	if cfg.PingInterval == 0 {
		cfg.PingInterval = 25 * time.Second
	}
	if cfg.PingTimeout == 0 {
		cfg.PingTimeout = 20 * time.Second
	}
	s := &Server{cfg: cfg, sessions: map[string]*Session{}, wake: make(chan struct{}, 1)}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL
	cleanup(s.Close)
	return s
}

// Close stops the server and ends every session.
func (s *Server) Close() {
	s.mu.Lock()
	all := make([]*Session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		all = append(all, ss)
	}
	s.mu.Unlock()
	for _, ss := range all {
		ss.end()
	}
	s.srv.CloseClientConnections()
	s.srv.Close()
}

// Received returns the Socket.IO packets received so far, of every
// session.
func (s *Server) Received() []socketio.Packet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]socketio.Packet(nil), s.received...)
}

// Requests returns the HTTP requests received so far (long-polling and
// WebSocket handshakes).
func (s *Server) Requests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.requests...)
}

// Posts returns the bodies of the long-polling POST requests received so
// far, in order.
func (s *Server) Posts() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.posts...)
}

// WaitReceived waits until cond holds for the received packets, up to 5 s.
func (s *Server) WaitReceived(cond func([]socketio.Packet) bool) bool {
	deadline := time.After(5 * time.Second)
	for {
		if cond(s.Received()) {
			return true
		}
		select {
		case <-s.wake:
		case <-deadline:
			return false
		}
	}
}

// Sessions returns the open sessions.
func (s *Server) Sessions() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		out = append(out, ss)
	}
	return out
}

func (s *Server) record(p socketio.Packet) {
	s.mu.Lock()
	s.received = append(s.received, p)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func jsonError(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg})
}

func newID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Clone(r.Context()))
	s.mu.Unlock()
	if r.URL.Path != s.cfg.Path {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if !s.cfg.V2 && q.Get("EIO") != "4" {
		jsonError(w, http.StatusBadRequest, 5, "Unsupported protocol version")
		return
	}
	transport := q.Get("transport")
	if (transport != "polling" && transport != "websocket") ||
		(transport == "polling" && s.cfg.NoPolling) || (transport == "websocket" && s.cfg.NoWebSocket) {
		jsonError(w, http.StatusBadRequest, 0, "Transport unknown")
		return
	}
	sid := q.Get("sid")
	if sid == "" {
		if transport == "websocket" {
			s.openWebSocket(w, r)
			return
		}
		if r.Method != http.MethodGet {
			jsonError(w, http.StatusBadRequest, 2, "Bad handshake method")
			return
		}
		ss := s.newSession("polling", r)
		ss.b64 = q.Get("b64") != ""
		open := ss.openPacket()
		ss.mu.Lock()
		out := append([]engineio.Packet{open}, ss.outbox...)
		ss.outbox = nil
		ss.mu.Unlock()
		ss.writePayload(w, out)
		return
	}
	s.mu.Lock()
	ss := s.sessions[sid]
	s.mu.Unlock()
	if ss == nil {
		jsonError(w, http.StatusBadRequest, 1, "Session ID unknown")
		return
	}
	switch {
	case transport == "websocket":
		ss.probe(w, r)
	case r.Method == http.MethodGet:
		ss.poll(w, r)
	case r.Method == http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.posts = append(s.posts, body)
		s.mu.Unlock()
		decode := engineio.DecodePayload
		if s.cfg.V2 {
			decode = engineio.DecodePayloadV3
		}
		pkts, err := decode(body, engineio.Options{})
		if err != nil {
			jsonError(w, http.StatusBadRequest, 3, "Bad request")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("ok"))
		for _, p := range pkts {
			ss.onPacket(p)
		}
	default:
		jsonError(w, http.StatusBadRequest, 2, "Bad request")
	}
}

// Session is one Engine.IO session.
type Session struct {
	SID string
	s   *Server

	mu        sync.Mutex
	transport string
	outbox    []engineio.Packet
	wake      chan struct{}
	polling   bool
	ws        *wsframe.Conn
	wsConn    net.Conn
	upgrading bool
	ended     bool
	pong      chan struct{}
	done      chan struct{}
	sockets   map[string]string // namespace -> socket ID
	dec       *socketio.Decoder
	query     map[string]string // the handshake's query (V2)
	b64       bool              // the client asked for text payloads (V2)
}

// writePayload answers a poll: Engine.IO 4 payloads, or with V2 the text
// form of Engine.IO 3 (the binary XHR2 form when binary packets go to a
// client that did not ask for b64).
func (ss *Session) writePayload(w http.ResponseWriter, pkts []engineio.Packet) {
	if !ss.s.cfg.V2 {
		body, err := engineio.EncodePayload(pkts)
		if err != nil {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		_, _ = w.Write(body)
		return
	}
	binary := false
	for _, p := range pkts {
		binary = binary || p.Binary
	}
	ss.mu.Lock()
	b64 := ss.b64
	ss.mu.Unlock()
	if binary && !b64 {
		body, err := engineio.EncodeBinaryPayloadV3(pkts)
		if err != nil {
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
		return
	}
	body, err := engineio.EncodePayloadV3(pkts)
	if err != nil {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	_, _ = w.Write(body)
}

func (ss *Session) encodeWS(p engineio.Packet) ([]byte, bool, error) {
	if ss.s.cfg.V2 {
		return engineio.EncodePacketV3(p)
	}
	return engineio.EncodePacket(p)
}

func (ss *Session) decodeWS(data []byte, binary bool) (engineio.Packet, error) {
	if ss.s.cfg.V2 {
		return engineio.DecodePacketV3(data, binary, engineio.Options{})
	}
	return engineio.DecodePacket(data, binary, engineio.Options{})
}

func (s *Server) newSession(transport string, r *http.Request) *Session {
	ss := &Session{
		SID:       newID(),
		s:         s,
		transport: transport,
		wake:      make(chan struct{}, 1),
		pong:      make(chan struct{}, 1),
		done:      make(chan struct{}),
		sockets:   map[string]string{},
		dec:       socketio.NewDecoder(socketio.Options{}),
		query:     map[string]string{},
	}
	for k, v := range r.URL.Query() {
		if k != "EIO" && k != "transport" && k != "b64" && k != "sid" && k != "t" && len(v) > 0 {
			ss.query[k] = v[0]
		}
	}
	s.mu.Lock()
	s.sessions[ss.SID] = ss
	s.mu.Unlock()
	if !s.cfg.NoPings && !s.cfg.V2 {
		go ss.heartbeat()
	}
	if s.cfg.V2 {
		// socket.io 2 joins the main namespace at once.
		ss.connectV2("/", ss.query)
	}
	return ss
}

func (ss *Session) openPacket() engineio.Packet {
	upgrades := []string{}
	ss.mu.Lock()
	polling := ss.transport == "polling"
	ss.mu.Unlock()
	if polling && !ss.s.cfg.NoWebSocket && !ss.s.cfg.NoUpgrade {
		upgrades = []string{"websocket"}
	}
	maxPayload := 1000000
	if ss.s.cfg.SocketIOv3 || ss.s.cfg.V2 {
		maxPayload = 0 // left out
	}
	p, _ := engineio.Handshake{
		SID:          ss.SID,
		Upgrades:     upgrades,
		PingInterval: int(ss.s.cfg.PingInterval / time.Millisecond),
		PingTimeout:  int(ss.s.cfg.PingTimeout / time.Millisecond),
		MaxPayload:   maxPayload,
	}.Packet()
	return p
}

// Transport returns "polling" or "websocket".
func (ss *Session) Transport() string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.transport
}

// Done is closed when the session ended.
func (ss *Session) Done() <-chan struct{} { return ss.done }

func (ss *Session) heartbeat() {
	for {
		select {
		case <-time.After(ss.s.cfg.PingInterval):
		case <-ss.done:
			return
		}
		ss.send(engineio.Packet{Type: engineio.Ping})
		select {
		case <-ss.pong:
		case <-time.After(ss.s.cfg.PingTimeout):
			ss.end()
			return
		case <-ss.done:
			return
		}
	}
}

// send queues or writes Engine.IO packets.
func (ss *Session) send(pkts ...engineio.Packet) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.ended {
		return
	}
	if ss.transport == "websocket" && !ss.upgrading && ss.ws != nil {
		for _, p := range pkts {
			data, binary, err := ss.encodeWS(p)
			if err != nil {
				continue
			}
			op := wsframe.OpText
			if binary {
				op = wsframe.OpBinary
			}
			_ = ss.ws.WriteMessage(op, data)
		}
		return
	}
	ss.outbox = append(ss.outbox, pkts...)
	select {
	case ss.wake <- struct{}{}:
	default:
	}
}

// Emit sends an event to the socket of namespace nsp.
func (ss *Session) Emit(nsp, event string, args ...any) {
	p, err := socketio.NewEvent(nsp, event, args...)
	if err != nil {
		return
	}
	ss.sendPacket(p)
}

// Disconnect sends a DISCONNECT for nsp.
func (ss *Session) Disconnect(nsp string) {
	p, _ := socketio.NewDisconnect(nsp)
	ss.sendPacket(p)
}

func (ss *Session) sendPacket(p socketio.Packet) {
	msgs, err := socketio.EncodeMessages(p)
	if err == nil {
		ss.send(msgs...)
	}
}

// CloseEngine sends an Engine.IO close packet and ends the session.
func (ss *Session) CloseEngine() {
	ss.send(engineio.Packet{Type: engineio.Close})
	time.AfterFunc(50*time.Millisecond, ss.end)
}

// Kill drops the transport without a close packet.
func (ss *Session) Kill() { ss.end() }

func (ss *Session) end() {
	ss.mu.Lock()
	if ss.ended {
		ss.mu.Unlock()
		return
	}
	ss.ended = true
	nc := ss.wsConn
	ss.mu.Unlock()
	close(ss.done)
	if nc != nil {
		_ = nc.Close()
	}
	ss.s.mu.Lock()
	delete(ss.s.sessions, ss.SID)
	ss.s.mu.Unlock()
}

// poll answers a long-polling GET with the queued packets, waiting for
// some.
func (ss *Session) poll(w http.ResponseWriter, r *http.Request) {
	ss.mu.Lock()
	if ss.polling || ss.transport != "polling" {
		ss.mu.Unlock()
		jsonError(w, http.StatusBadRequest, 3, "Bad request")
		return
	}
	ss.polling = true
	ss.mu.Unlock()
	defer func() {
		ss.mu.Lock()
		ss.polling = false
		ss.mu.Unlock()
	}()
	for {
		ss.mu.Lock()
		out := ss.outbox
		ss.outbox = nil
		ended := ss.ended
		upgrading := ss.upgrading
		ss.mu.Unlock()
		if len(out) == 0 && ended {
			out = []engineio.Packet{{Type: engineio.Noop}}
		}
		if len(out) > 0 {
			ss.writePayload(w, out)
			return
		}
		// While upgrading, polls are ended with a noop now and then, as
		// the reference server does, so the client can pause.
		var tick <-chan time.Time
		if upgrading {
			tick = time.After(50 * time.Millisecond)
		}
		select {
		case <-ss.wake:
		case <-tick:
			ss.send(engineio.Packet{Type: engineio.Noop})
		case <-ss.done:
		case <-r.Context().Done():
			return
		}
	}
}

func upgradeConn(w http.ResponseWriter, r *http.Request) (*wsframe.Conn, net.Conn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		jsonError(w, http.StatusBadRequest, 3, "Bad request")
		return nil, nil, errors.New("not an upgrade")
	}
	nc, brw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsframe.ComputeAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n"
	if _, err := nc.Write([]byte(resp)); err != nil {
		_ = nc.Close()
		return nil, nil, err
	}
	var br *bufio.Reader = brw.Reader
	return wsframe.NewConn(nc, wsframe.Options{Role: wsframe.RoleServer, Reader: br}), nc, nil
}

// openWebSocket is a session that starts over WebSocket.
func (s *Server) openWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, nc, err := upgradeConn(w, r)
	if err != nil {
		return
	}
	ss := s.newSession("websocket", r)
	open := ss.openPacket()
	ss.mu.Lock()
	ss.ws, ss.wsConn = conn, nc
	queued := ss.outbox
	ss.outbox = nil
	ss.mu.Unlock()
	ss.send(append([]engineio.Packet{open}, queued...)...)
	ss.readWS(conn)
}

// probe runs the upgrade of a polling session.
func (ss *Session) probe(w http.ResponseWriter, r *http.Request) {
	conn, nc, err := upgradeConn(w, r)
	if err != nil {
		return
	}
	ss.mu.Lock()
	if ss.transport != "polling" || ss.upgrading {
		ss.mu.Unlock()
		_ = nc.Close()
		return
	}
	ss.upgrading = true
	ss.mu.Unlock()
	for {
		op, data, err := conn.ReadMessage()
		if err != nil {
			ss.mu.Lock()
			ss.upgrading = false
			ss.mu.Unlock()
			_ = nc.Close()
			return
		}
		p, err := ss.decodeWS(data, op == wsframe.OpBinary)
		if err != nil {
			_ = nc.Close()
			return
		}
		switch {
		case p.Type == engineio.Ping && string(p.Data) == "probe":
			if ss.s.cfg.IgnoreProbe {
				continue
			}
			_ = conn.WriteMessage(wsframe.OpText, []byte("3probe"))
			// End the pending poll so the client can pause.
			ss.mu.Lock()
			ss.outbox = append(ss.outbox, engineio.Packet{Type: engineio.Noop})
			ss.mu.Unlock()
			select {
			case ss.wake <- struct{}{}:
			default:
			}
		case p.Type == engineio.Upgrade:
			ss.mu.Lock()
			ss.transport = "websocket"
			ss.upgrading = false
			ss.ws, ss.wsConn = conn, nc
			out := ss.outbox
			ss.outbox = nil
			ss.mu.Unlock()
			var rest []engineio.Packet
			for _, q := range out {
				if q.Type != engineio.Noop {
					rest = append(rest, q)
				}
			}
			if len(rest) > 0 {
				ss.send(rest...)
			}
			ss.readWS(conn)
			return
		default:
			_ = nc.Close()
			return
		}
	}
}

func (ss *Session) readWS(conn *wsframe.Conn) {
	defer ss.end()
	for {
		op, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		p, err := ss.decodeWS(data, op == wsframe.OpBinary)
		if err != nil {
			return
		}
		ss.onPacket(p)
		if p.Type == engineio.Close {
			return
		}
	}
}

// onPacket handles one Engine.IO packet from the client.
func (ss *Session) onPacket(p engineio.Packet) {
	switch p.Type {
	case engineio.Pong:
		select {
		case ss.pong <- struct{}{}:
		default:
		}
	case engineio.Ping:
		// Engine.IO 3: the client pings and the server answers.
		if ss.s.cfg.V2 && !ss.s.cfg.NoPings {
			ss.send(engineio.Packet{Type: engineio.Pong, Data: p.Data})
		}
	case engineio.Close:
		ss.end()
	case engineio.Message:
		sp, err := ss.dec.Feed(p)
		if err != nil || sp == nil {
			return
		}
		ss.s.record(*sp)
		ss.onSocketPacket(sp)
	}
}

func (ss *Session) onSocketPacket(p *socketio.Packet) {
	cfg := ss.s.cfg
	switch p.Type {
	case socketio.Connect:
		if cfg.V2 {
			nsp, rawQuery, _ := strings.Cut(p.Namespace, "?")
			query := map[string]string{}
			if values, err := url.ParseQuery(rawQuery); err == nil {
				for k, v := range values {
					query[k] = v[0]
				}
			}
			ss.connectV2(nsp, query)
			return
		}
		ok := p.Namespace == "/"
		for _, n := range cfg.Namespaces {
			ok = ok || n == p.Namespace
		}
		if !ok {
			ss.sendPacket(socketio.Packet{Type: socketio.ConnectError, Namespace: p.Namespace, Data: json.RawMessage(`{"message":"Invalid namespace"}`)})
			return
		}
		if cfg.Authorize != nil {
			if err := cfg.Authorize(p.Namespace, p.Data); err != nil {
				data, _ := json.Marshal(map[string]any{"message": err.Error(), "data": map[string]any{"reason": "auth"}})
				ss.sendPacket(socketio.Packet{Type: socketio.ConnectError, Namespace: p.Namespace, Data: data})
				return
			}
		}
		id := newID()
		ss.mu.Lock()
		ss.sockets[p.Namespace] = id
		ss.mu.Unlock()
		data, _ := json.Marshal(map[string]string{"sid": id})
		ss.sendPacket(socketio.Packet{Type: socketio.Connect, Namespace: p.Namespace, Data: data})
	case socketio.Disconnect:
		ss.mu.Lock()
		delete(ss.sockets, p.Namespace)
		ss.mu.Unlock()
	case socketio.Event, socketio.BinaryEvent:
		args, _ := p.Args()
		name := p.EventName()
		switch name {
		case "echo":
			ss.Emit(p.Namespace, "echo", args[1:]...)
		case "bye":
			ss.Disconnect(p.Namespace)
		case "close":
			ss.CloseEngine()
		}
		if p.HasID && name != "noack" {
			if a, err := socketio.NewAck(p.Namespace, p.ID, args[1:]...); err == nil {
				ss.sendPacket(a)
			}
		}
		if cfg.OnEvent != nil {
			cfg.OnEvent(ss, p)
		}
	}
}

// connectV2 joins nsp as socket.io 2 does: CONNECT without payload, or
// CONNECT_ERROR with a string.
func (ss *Session) connectV2(nsp string, query map[string]string) {
	cfg := ss.s.cfg
	ok := nsp == "/"
	for _, n := range cfg.Namespaces {
		ok = ok || n == nsp
	}
	refuse := func(msg string) {
		data, _ := json.Marshal(msg)
		ss.sendPacket(socketio.Packet{Type: socketio.ConnectError, Namespace: nsp, Data: data})
	}
	if !ok {
		refuse("Invalid namespace")
		return
	}
	if cfg.Authorize != nil {
		auth, _ := json.Marshal(query)
		if err := cfg.Authorize(nsp, auth); err != nil {
			refuse(err.Error())
			return
		}
	}
	ss.mu.Lock()
	ss.sockets[nsp] = ss.SID
	ss.mu.Unlock()
	ss.sendPacket(socketio.Packet{Type: socketio.Connect, Namespace: nsp})
}

// SetB64 changes whether the session answers polls with text payloads
// (V2): false sends binary packets in the XHR2 binary form.
func (ss *Session) SetB64(on bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.b64 = on
}
