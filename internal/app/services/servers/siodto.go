package servers

import (
	"slices"

	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/sioserver"
)

// The DTOs of the Socket.IO server (SSRV-03): its configuration, its
// sockets and the emit operations. See docs/servers.md.

// SocketIOConfigDTO is the configuration of a socketio server.
type SocketIOConfigDTO struct {
	// Path is the Engine.IO path ("/socket.io/").
	Path string `json:"path"`
	// Namespaces are accepted besides "/"; [] or null accepts any.
	Namespaces []string `json:"namespaces"`
	// Auth is a JSON object whose members a CONNECT's auth payload must
	// have, or "" for none.
	Auth string `json:"auth"`
	// Transports is "" (both), "websocket" or "polling".
	Transports string `json:"transports"`
	// AllowOrigin is the CORS origin of the long-polling responses: "",
	// "*" or one origin.
	AllowOrigin string `json:"allowOrigin"`
	// PingIntervalMs and PingTimeoutMs are the heartbeat; 0 takes the
	// defaults (25000, 20000).
	PingIntervalMs int `json:"pingIntervalMs"`
	PingTimeoutMs  int `json:"pingTimeoutMs"`
	// MaxPayload is the largest payload accepted in bytes; 0 means
	// 1000000.
	MaxPayload int `json:"maxPayload"`
	// Echo emits events no rule answers back to their socket.
	Echo bool `json:"echo"`
	// AckMode is "echo" ("" too), "empty" or "none".
	AckMode string `json:"ackMode"`
	// Rules are the auto-reply rules; null is none.
	Rules []SIORuleDTO `json:"rules"`
}

// SIORuleDTO is an auto-reply rule: an event whose name matches pattern
// gets the ack arguments and the event emit with emitArgs (JSON text,
// dynamic values allowed), after delayMs.
type SIORuleDTO struct {
	Enabled bool `json:"enabled"`
	// Match is "exact", "contains" or "regex".
	Match    string `json:"match"`
	Pattern  string `json:"pattern"`
	Ack      string `json:"ack"`
	Emit     string `json:"emit"`
	EmitArgs string `json:"emitArgs"`
	DelayMs  int    `json:"delayMs"`
}

// SIOSocketDTO is a socket connected to a Socket.IO server: one client in
// one namespace.
type SIOSocketDTO struct {
	// ID is "3" for client 3 in "/", "3/admin" in another namespace.
	ID       string `json:"id"`
	ClientID string `json:"clientId"`
	// SID is the socket ID the client got in its CONNECT.
	SID       string `json:"sid"`
	Namespace string `json:"namespace"`
	// Session is the Engine.IO session ID; Transport "polling" or
	// "websocket".
	Session   string `json:"session"`
	Transport string `json:"transport"`
	Peer      string `json:"peer"`
	Target    string `json:"target"`
	Origin    string `json:"origin"`
	UserAgent string `json:"userAgent"`
	// Auth is the auth payload of its CONNECT, "" when none.
	Auth  string   `json:"auth"`
	Rooms []string `json:"rooms"`
	// ConnectedAt is RFC 3339 in UTC.
	ConnectedAt string `json:"connectedAt"`
	EventsIn    int64  `json:"eventsIn"`
	EventsOut   int64  `json:"eventsOut"`
}

// SocketsEventDTO is the payload of servers:sockets: the sockets of one
// Socket.IO server now ([] once it stopped).
type SocketsEventDTO struct {
	ServerID string         `json:"serverId"`
	Sockets  []SIOSocketDTO `json:"sockets"`
}

// SIOEmitInputDTO emits an event to the socket socketId, else to every
// socket in room, else to every socket.
type SIOEmitInputDTO struct {
	ServerID string `json:"serverId"`
	SocketID string `json:"socketId"`
	Room     string `json:"room"`
	Event    string `json:"event"`
	// Args is the arguments as JSON text: an array holds one argument per
	// element, another value is one argument, "" none; {"$base64": "…"}
	// or {"$hex": "…"} is binary; dynamic values are rendered.
	Args string `json:"args"`
	// Ack asks each socket for an acknowledgement.
	Ack bool `json:"ack"`
}

// SIOEmitResultDTO counts the sockets an emit reached.
type SIOEmitResultDTO struct {
	Sent   int `json:"sent"`
	Failed int `json:"failed"`
}

// SIODisconnectInputDTO sends DISCONNECT to a socket; closeClient closes
// its client's whole connection too.
type SIODisconnectInputDTO struct {
	ServerID    string `json:"serverId"`
	SocketID    string `json:"socketId"`
	CloseClient bool   `json:"closeClient"`
}

// SIORoomsInputDTO puts a socket in exactly these rooms.
type SIORoomsInputDTO struct {
	ServerID string   `json:"serverId"`
	SocketID string   `json:"socketId"`
	Rooms    []string `json:"rooms"`
}

// SIODTO is the Socket.IO server's detail of a traffic event.
type SIODTO struct {
	ClientID  string `json:"clientId"`
	Session   string `json:"session"`
	Transport string `json:"transport"`
	SocketID  string `json:"socketId"`
	Namespace string `json:"namespace"`
	// Packet is the Socket.IO packet type ("EVENT", "BINARY_ACK", …).
	Packet string `json:"packet"`
	Event  string `json:"event"`
	// AckID is set when HasAck is.
	AckID       int64 `json:"ackId"`
	HasAck      bool  `json:"hasAck"`
	Attachments int   `json:"attachments"`
	// RTTMs is the round trip of an acknowledgement of the server's event.
	RTTMs int64 `json:"rttMs"`
	// Source is "", "echo", "auto-ack" or "auto-reply rule 2".
	Source string `json:"source"`
	// Reason and InitiatedBy say how a client or socket ended.
	Reason      string `json:"reason"`
	InitiatedBy string `json:"initiatedBy"`
}

func (c SocketIOConfigDTO) toCore() sioserver.Config {
	out := sioserver.Config{
		Path: c.Path, Namespaces: slices.Clone(c.Namespaces), Auth: c.Auth, Transports: c.Transports, AllowOrigin: c.AllowOrigin,
		PingIntervalMs: c.PingIntervalMs, PingTimeoutMs: c.PingTimeoutMs, MaxPayload: c.MaxPayload, Echo: c.Echo, AckMode: c.AckMode,
		Rules: make([]sioserver.Rule, 0, len(c.Rules)),
	}
	for _, r := range c.Rules {
		out.Rules = append(out.Rules, sioserver.Rule{
			Enabled: r.Enabled, Match: r.Match, Pattern: r.Pattern, Ack: r.Ack, Emit: r.Emit, EmitArgs: r.EmitArgs, DelayMs: r.DelayMs,
		})
	}
	return out
}

func toSocketIOConfigDTO(c sioserver.Config) *SocketIOConfigDTO {
	out := &SocketIOConfigDTO{
		Path: c.Path, Namespaces: slices.Clone(c.Namespaces), Auth: c.Auth, Transports: c.Transports, AllowOrigin: c.AllowOrigin,
		PingIntervalMs: c.PingIntervalMs, PingTimeoutMs: c.PingTimeoutMs, MaxPayload: c.MaxPayload, Echo: c.Echo, AckMode: c.AckMode,
		Rules: make([]SIORuleDTO, 0, len(c.Rules)),
	}
	if out.Namespaces == nil {
		out.Namespaces = []string{}
	}
	for _, r := range c.Rules {
		out.Rules = append(out.Rules, SIORuleDTO{
			Enabled: r.Enabled, Match: r.Match, Pattern: r.Pattern, Ack: r.Ack, Emit: r.Emit, EmitArgs: r.EmitArgs, DelayMs: r.DelayMs,
		})
	}
	return out
}

func toSIOSocketDTOs(list []sioserver.Socket) []SIOSocketDTO {
	out := make([]SIOSocketDTO, 0, len(list))
	for _, s := range list {
		rooms := slices.Clone(s.Rooms)
		if rooms == nil {
			rooms = []string{}
		}
		out = append(out, SIOSocketDTO{
			ID: s.ID, ClientID: s.ClientID, SID: s.SID, Namespace: s.Namespace, Session: s.Session, Transport: s.Transport,
			Peer: s.Peer, Target: s.Target, Origin: s.Origin, UserAgent: s.UserAgent, Auth: s.Auth, Rooms: rooms,
			ConnectedAt: formatTime(s.ConnectedAt), EventsIn: int64(s.EventsIn), EventsOut: int64(s.EventsOut),
		})
	}
	return out
}

func toSIODTO(s *core.SIOInfo) *SIODTO {
	if s == nil {
		return nil
	}
	return &SIODTO{
		ClientID: s.ClientID, Session: s.Session, Transport: s.Transport, SocketID: s.SocketID, Namespace: s.Namespace,
		Packet: s.Packet, Event: s.Event, AckID: int64(s.AckID), HasAck: s.HasAck, Attachments: s.Attachments,
		RTTMs: s.RTT.Milliseconds(), Source: s.Source, Reason: s.Reason, InitiatedBy: s.InitiatedBy,
	}
}
