package socketio

import (
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
)

// ConnectInputDTO is what Connect takes: the handshake of a socketio
// request as the editor holds it, {{variables}} unresolved.
type ConnectInputDTO struct {
	// RequestID is the socketio request the connection belongs to: its
	// scopes render the {{variables}}. It may be "" (then no variable is
	// defined).
	RequestID string `json:"requestId"`
	// URL is the server's http(s):// or ws(s):// URL, query included. Its
	// path is the namespace when Namespace is "".
	URL string `json:"url"`
	// Headers are sent with every long-polling request and the WebSocket
	// handshake; disabled rows are skipped.
	Headers []HeaderDTO `json:"headers"`
	// Path is the Engine.IO handshake path; "" means "/socket.io/".
	Path string `json:"path"`
	// Namespace is the namespace to connect to; "" means the URL's path or
	// "/".
	Namespace string `json:"namespace"`
	// AuthPayload is the JSON object sent as the CONNECT packet's auth; ""
	// sends none.
	AuthPayload string `json:"authPayload"`
	// AuthIn is where the request's auth goes (AUTH-31): "handshake" (or
	// "") in the headers and query of the transport requests, "payload"
	// as members of AuthPayload.
	AuthIn string `json:"authIn,omitempty"`
	// Transport is "auto" (or ""), "websocket", "polling" or "upgrade".
	Transport string `json:"transport"`
	// Version is the server's Socket.IO version: "" (v3 and v4) or "v2"
	// (SIO-02). With "v2" the auth payload must be empty.
	Version string `json:"version,omitempty"`
	// ConnectTimeoutMs bounds the handshake, the upgrade and the namespace
	// CONNECT: null for the default (20 s), 0 for no limit.
	ConnectTimeoutMs *int64 `json:"connectTimeoutMs"`
	// AckTimeoutMs is how long an emit waits for its acknowledgement: null
	// for the default (10 s).
	AckTimeoutMs *int64 `json:"ackTimeoutMs"`
	// VerifyTLS false accepts any certificate; null means verify.
	VerifyTLS *bool `json:"verifyTls"`
	// SendCookies false leaves the project's cookie jar out; null means use
	// it.
	SendCookies *bool `json:"sendCookies"`
	// AllowUnresolved connects with {{variables}} that no scope defines as
	// written instead of refusing (the "Connect anyway" answer of ENV-11).
	AllowUnresolved bool `json:"allowUnresolved,omitempty"`
}

// HeaderDTO is a header row.
type HeaderDTO struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// EmitInputDTO is what Emit takes.
type EmitInputDTO struct {
	// OpID is the connection's operation.
	OpID string `json:"opId"`
	// Event is the event name, {{variables}} unresolved.
	Event string `json:"event"`
	// Args are the arguments as JSON text, {{variables}} unresolved: an
	// array holds one argument per element, another value is one argument,
	// "" none. {"$base64": "…"} and {"$hex": "…"} objects are binary
	// arguments.
	Args string `json:"args"`
	// Ack asks for an acknowledgement.
	Ack bool `json:"ack"`
	// AllowUnresolved sends undefined {{variables}} as written (ENV-11).
	AllowUnresolved bool `json:"allowUnresolved,omitempty"`
}

// EmitResultDTO is what Emit returns.
type EmitResultDTO struct {
	// AckID is the acknowledgement ID asked for; null without an ack.
	AckID *int64 `json:"ackId"`
	// Size is the packet's size in bytes, attachments included.
	Size int `json:"size"`
	// Attachments is the number of binary arguments.
	Attachments int `json:"attachments"`
}

// Item types, in the order a connection sends them.
const (
	// ItemOpen: the Engine.IO handshake is done (transport, sid, ping
	// interval and timeout, max payload, upgrades, url).
	ItemOpen = "open"
	// ItemFallback: WebSocket failed (error); long-polling is tried.
	ItemFallback = "fallback"
	// ItemUpgrade: the connection moved to WebSocket.
	ItemUpgrade = "upgrade"
	// ItemUpgradeFailed: the upgrade probe failed (error); polling goes on.
	ItemUpgradeFailed = "upgrade_failed"
	// ItemConnected: the namespace accepted the connection (socketId).
	ItemConnected = "connected"
	// ItemEvent: an event sent or received (event, args, ackId).
	ItemEvent = "event"
	// ItemAck: an acknowledgement received (ackId, args; event and
	// elapsedMs when it answers an emit of this connection).
	ItemAck = "ack"
	// ItemPacket: another Socket.IO packet (CONNECT, DISCONNECT,
	// CONNECT_ERROR) sent or received (packetType, data).
	ItemPacket = "packet"
	// ItemPing and ItemPong: the heartbeat.
	ItemPing = "ping"
	ItemPong = "pong"
	// ItemAckTimeout: no acknowledgement came for an emit in time.
	ItemAckTimeout = "ack_timeout"
	// ItemClosed: the connection ended (reason, and error for a failure).
	ItemClosed = "closed"
)

// ItemDTO is one item of a connection's operation (sio:op:data). Type
// says which fields are set; the others are empty.
type ItemDTO struct {
	Type string `json:"type"`
	// At is when it happened, RFC 3339 in UTC.
	At string `json:"at"`
	// Direction is "sent", "received", or "" for the connection's own
	// items.
	Direction string `json:"direction"`
	// Transport is "websocket" or "polling" (open, upgrade, connected).
	Transport string `json:"transport"`
	// URL is the server connected to: scheme, host and path (open).
	URL string `json:"url"`
	// SID, PingIntervalMs, PingTimeoutMs, MaxPayload and Upgrades are the
	// Engine.IO handshake (open).
	SID            string   `json:"sid"`
	PingIntervalMs int      `json:"pingIntervalMs"`
	PingTimeoutMs  int      `json:"pingTimeoutMs"`
	MaxPayload     int      `json:"maxPayload"`
	Upgrades       []string `json:"upgrades"`
	// Namespace is the packet's namespace.
	Namespace string `json:"namespace"`
	// SocketID is the namespace's session ID (connected).
	SocketID string `json:"socketId"`
	// Event is the event name; for an ack, the event of the emit it
	// answers.
	Event string `json:"event"`
	// Args are the arguments as JSON text, binary ones as
	// {"$base64": "…"} (event, ack).
	Args string `json:"args"`
	// AckID is the event's or ack's acknowledgement ID, null for none.
	AckID *int64 `json:"ackId"`
	// ElapsedMs is how long an ack took, or waited before its timeout.
	ElapsedMs int64 `json:"elapsedMs"`
	// PacketType is the Socket.IO packet type of a packet item: CONNECT,
	// DISCONNECT or CONNECT_ERROR.
	PacketType string `json:"packetType"`
	// Data is a packet item's JSON payload ("" for none).
	Data string `json:"data"`
	// Attachments is the number of binary attachments.
	Attachments int `json:"attachments"`
	// Size is the packet's size in bytes, attachments included.
	Size int `json:"size"`
	// Reason is why the connection ended (closed): "io client disconnect",
	// "io server disconnect", "transport close", "transport error", "ping
	// timeout", "parse error" or "connect failed".
	Reason string `json:"reason"`
	// Error is set on fallback, upgrade_failed and a failed closed.
	Error *apperr.ErrorDTO `json:"error"`
	// Tests are the results of the request's assertions against the
	// events received (TEST-08, closed); empty without assertions, null
	// on the other items.
	Tests []streamcheck.TestResultDTO `json:"tests"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
