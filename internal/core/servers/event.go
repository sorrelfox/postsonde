package servers

import (
	"time"
	"unicode/utf8"
)

// Direction says who produced an Event.
type Direction string

const (
	// In: the peer sent it to the server (a request, a message).
	In Direction = "in"
	// Out: the server sent it to the peer (a response, a reply).
	Out Direction = "out"
	// System: the server itself (started, stopped, a connection opened or
	// closed, an error).
	System Direction = "system"
)

// EventKind is what an Event is.
type EventKind string

const (
	KindRequest         EventKind = "request"
	KindResponse        EventKind = "response"
	KindMessage         EventKind = "message"
	KindConnectionOpen  EventKind = "connection_open"
	KindConnectionClose EventKind = "connection_close"
	// KindStarted and KindStopped are the Manager's own system events.
	KindStarted EventKind = "started"
	KindStopped EventKind = "stopped"
	KindError   EventKind = "error"
)

// Header is one HTTP header line. Headers are kept as a list, in a stable
// order, so repeated names survive.
type Header struct {
	Name  string
	Value string
}

// HTTPInfo is the HTTP detail of a request or response event.
type HTTPInfo struct {
	// Method and Target (the request target, path and query) of the
	// request; also set on its response.
	Method string
	Target string
	Proto  string
	// Status is the response status code, 0 on a request.
	Status  int
	Headers []Header
	// Route is the mock route that answered the request (HTTP mock), set on
	// its request and response events; nil otherwise.
	Route *RouteMatch
	// Delay is how long the response was held back (HTTP mock), set on a
	// response event.
	Delay time.Duration
}

// RouteMatch says which mock route a request matched.
type RouteMatch struct {
	ID string
	// Method and Pattern are the route's, as defined ("ANY", "/users/:id").
	Method  string
	Pattern string
	// Params are the path parameters in the order of the pattern, with
	// decoded values.
	Params []Header
	// Wildcard is what a catch-all matched, decoded.
	Wildcard string
	// Response is the name of the response that answered, on a response
	// event of a route with several responses or a request with
	// X-PostSonde-Response (MOCK-05, MOCK-07): "default", a response's
	// name, or "status 500" for a status no response has.
	Response string
}

// Event is one entry of a server's traffic log.
type Event struct {
	// ServerID and Seq are set by the Manager: Seq counts 1, 2, 3, … per
	// server and is never reused while the server exists.
	ServerID string
	Seq      uint64
	// Time is when it happened; the Manager sets it when zero.
	Time time.Time
	// Peer is the remote address ("127.0.0.1:53122"), empty for events of
	// the server itself.
	Peer string
	// ConnID identifies the peer's connection within one run of the
	// server (1, 2, …); 0 when not tied to a connection.
	ConnID    uint64
	Direction Direction
	Kind      EventKind
	// Summary is a one-line description ("GET /users?id=1", "200 OK").
	Summary string
	// Content is the payload (a body, a message). The Manager caps it at
	// Options.ContentLimit bytes: Size keeps the full length and Truncated
	// is set. A type that reads a stream may pass a prefix and set Size
	// and Truncated itself.
	Content   []byte
	Size      int
	Truncated bool
	// Binary is set by the Manager when Content is not valid UTF-8.
	Binary bool
	HTTP   *HTTPInfo
	// WS is the WebSocket detail of an event of the WebSocket server; nil
	// otherwise.
	WS *WSInfo
	// TCP is set on every connection event of the TCP server (nil
	// otherwise), with how a connection ended on connection_close.
	TCP *TCPInfo
	// SSE is set on every subscriber and event of the SSE server (nil
	// otherwise).
	SSE *SSEInfo
	// UDP is set on every datagram event of the UDP server (nil
	// otherwise).
	UDP *UDPInfo
	// GRPC is set on every event of a call of the gRPC mock server (nil
	// otherwise).
	GRPC *GRPCInfo
	// SIO is set on every event of a client of the Socket.IO server (nil
	// otherwise).
	SIO *SIOInfo
	// GraphQL is set on the request and response events of a GraphQL
	// operation sent to the GraphQL mock server (nil otherwise).
	GraphQL *GraphQLInfo
	// Capture is the whole request of a request event, byte for byte, from
	// a type that keeps its requests for replay (the webhook receiver). The
	// Manager moves it into the server's captures (Manager.Capture) and
	// sets Captured: events passed to OnTraffic or returned by Traffic
	// never carry it.
	Capture *Capture
	// Captured is set by the Manager on an event whose Capture it kept;
	// the capture is found by the event's Seq while it is kept.
	Captured bool
}

// Capture is an HTTP request as received, kept whole so it can be
// inspected and replayed unchanged (MOCK-08).
type Capture struct {
	// Seq is the Seq of its request event, set by the Manager; Time and
	// Peer are the event's.
	Seq  uint64
	Time time.Time
	Peer string
	// Method, Target (the request target: path and query as sent), Proto
	// and Host (the Host header) of the request.
	Method string
	Target string
	Proto  string
	Host   string
	// Headers are the request's headers without Host, sorted by name,
	// repeated names in the order received.
	Headers []Header
	// Body is the body as received. Size is its full length: when the body
	// was longer than the type keeps, Body is its first bytes and Truncated
	// is set.
	Body      []byte
	Size      int64
	Truncated bool
}

// bytes is what a capture counts against Options.CaptureBytes.
func (c *Capture) bytes() int {
	n := len(c.Body) + len(c.Method) + len(c.Target) + len(c.Proto) + len(c.Host) + len(c.Peer)
	for _, h := range c.Headers {
		n += len(h.Name) + len(h.Value)
	}
	return n
}

// GRPCInfo is the gRPC mock server's detail of an event of one call.
type GRPCInfo struct {
	// Call numbers the calls of one run of the server (1, 2, …).
	Call uint64
	// Method is the call's path, "/pkg.Service/Method".
	Method string
	// Kind is the method's kind: "unary", "server_stream",
	// "client_stream" or "bidi"; "" for a method the server does not
	// know.
	Kind string
	// Web is set for a gRPC-Web call (Content-Type application/grpc-web
	// or application/grpc-web-text).
	Web bool
	// Type is the full name of a message's type, on a message event.
	Type string
	// Index counts the messages of a call in each direction from 0, on a
	// message event.
	Index int
	// Code, CodeName ("NOT_FOUND") and Message are the status the call
	// ended with, on its response event.
	Code     int
	CodeName string
	Message  string
	// Trailers are the trailer metadata sent with the status, on the
	// response event (the response headers are in HTTPInfo.Headers).
	Trailers []Header
}

// GraphQLInfo is the GraphQL mock server's detail of a request and its
// response.
type GraphQLInfo struct {
	// Request numbers the GraphQL requests of one run of the server (1,
	// 2, …).
	Request uint64
	// Operation is the operation's name ("" for an anonymous one) and
	// OperationType "query", "mutation" or "subscription"; both "" when
	// the document does not parse or names no operation it has.
	Operation     string
	OperationType string
	// Variables are the request's variables as JSON text, "" when none.
	Variables string
	// Introspection is set when the operation selects __schema or __type.
	Introspection bool
	// Errors is the number of errors in the response, and HasData whether
	// it has data (false for a request error), on the response event.
	Errors  int
	HasData bool
}

// SIOInfo is the Socket.IO server's detail of an event.
type SIOInfo struct {
	// ClientID is the number of the client's Engine.IO session ("3"),
	// Session its session ID and Transport its transport ("polling" or
	// "websocket") when the event happened.
	ClientID  string
	Session   string
	Transport string
	// SocketID ("3", "3/admin") and Namespace are the socket a packet
	// belongs to.
	SocketID  string
	Namespace string
	// Packet is the Socket.IO packet type of a message event ("EVENT",
	// "BINARY_ACK", "CONNECT", …).
	Packet string
	// Event is an event's name, or on an acknowledgement from the client
	// the name of the event it answers.
	Event string
	// AckID is the ack ID of an event that asks for one, or of an
	// acknowledgement (HasAck).
	AckID  uint64
	HasAck bool
	// Attachments is the number of binary attachments.
	Attachments int
	// RTT is the round trip of an acknowledgement of an event the server
	// sent.
	RTT time.Duration
	// Source says what sent a packet: "" (the app), "echo", "auto-ack" or
	// "auto-reply rule 2".
	Source string
	// Reason is why a client or socket ended ("transport close", "ping
	// timeout", "client namespace disconnect", …).
	Reason string
	// InitiatedBy is who ended it: "client", "server" or "" (a failure).
	InitiatedBy string
}

// TCPInfo is the TCP server's detail of an event.
type TCPInfo struct {
	// InitiatedBy is set on connection_close: "client" (it closed or reset
	// the connection), "server" (closed from the app, or the server
	// stopped) or "" when the connection failed.
	InitiatedBy string
	// BytesIn and BytesOut are the connection's totals, on
	// connection_close.
	BytesIn  uint64
	BytesOut uint64
}

// SSEInfo is the SSE server's detail of an event.
type SSEInfo struct {
	// SubscriberID is the subscriber a request, response, close or error
	// belongs to, or the one an event went to; "" for an event sent to
	// every subscriber.
	SubscriberID string
	// Event, ID and Retry are the fields of an event sent (Event "" is the
	// default type, "message"); Retry in milliseconds, 0 when not sent.
	Event string
	ID    string
	Retry int
	// Recipients is how many subscribers an event was written to.
	Recipients int
	// Source says what sent an event: "" (the user), or "timer 2".
	Source string
	// LastEventID is the Last-Event-ID header a subscriber connected with.
	LastEventID string
	// InitiatedBy is set on connection_close: "client" (it went away) or
	// "server" (disconnected from the app, or the server stopped).
	InitiatedBy string
}

// UDPInfo is the UDP server's detail of a datagram event.
type UDPInfo struct {
	// Local is the server's address the datagram arrived on or left from.
	Local string
	// Reply says how an outgoing datagram was made: "echo", "fixed", or ""
	// for one sent from the app.
	Reply string
}

// WebSocket message opcodes of WSInfo.Opcode.
const (
	OpcodeText   = "text"
	OpcodeBinary = "binary"
)

// WSInfo is the WebSocket detail of an event: which client it belongs to,
// the opcode of a message and how a connection closed.
type WSInfo struct {
	// ClientID identifies the client within one run of the server.
	ClientID string
	// Opcode is OpcodeText or OpcodeBinary on a message event, else "". A
	// binary message is always Binary, even when its bytes are UTF-8.
	Opcode string
	// Subprotocol is the one accepted, on the upgrade response.
	Subprotocol string
	// CloseCode and CloseReason are the close frame on a connection_close
	// event (1005 for an empty one, 1006 when none was exchanged).
	CloseCode   int
	CloseReason string
	// InitiatedBy is "client" or "server" (whose close frame went first),
	// or "" when the connection dropped.
	InitiatedBy string
}

// normalize applies the Manager's rules to an event from a Sink.
func (e *Event) normalize(limit int, now time.Time) {
	if e.Time.IsZero() {
		e.Time = now
	}
	if e.Size < len(e.Content) {
		e.Size = len(e.Content)
	}
	if limit >= 0 && len(e.Content) > limit {
		e.Content = e.Content[:limit:limit]
		e.Truncated = true
	}
	if e.Size > len(e.Content) {
		e.Truncated = true
	}
	e.Binary = !validUTF8Prefix(e.Content, e.Truncated) || (e.WS != nil && e.WS.Opcode == OpcodeBinary)
}

// validUTF8Prefix reports whether b is UTF-8 text. A cut payload may end in
// the middle of a character, which does not make it binary.
func validUTF8Prefix(b []byte, cut bool) bool {
	if utf8.Valid(b) {
		return true
	}
	if !cut {
		return false
	}
	for i := 1; i < utf8.UTFMax && i <= len(b); i++ {
		if utf8.Valid(b[:len(b)-i]) {
			return true
		}
	}
	return false
}

// Sink receives the events of one running server. Emit is safe for
// concurrent use and never blocks for long. Events emitted after the
// instance's Stop has returned are dropped.
type Sink interface {
	Emit(e Event)
	// ContentLimit is the most content bytes an event keeps, so a type
	// reading a stream knows how much to buffer.
	ContentLimit() int
}
