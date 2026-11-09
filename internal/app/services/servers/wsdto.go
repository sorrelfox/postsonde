package servers

import (
	"encoding/base64"
	"slices"

	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
)

// The DTOs of the WebSocket server (SSRV-01): its configuration, its
// clients and the client operations. See docs/servers.md.

// WebSocketConfigDTO is the configuration of a websocket server.
type WebSocketConfigDTO struct {
	// Path is where clients connect ("/", "/socket"), or "*" for any path.
	Path string `json:"path"`
	// Subprotocols are accepted; the first one the client offers that is
	// listed is selected.
	Subprotocols []string `json:"subprotocols"`
	// Echo sends a message back to its client when no rule answers it.
	Echo bool `json:"echo"`
	// MaxMessageSize is the largest message received, in bytes; 0 means
	// 16 MiB.
	MaxMessageSize int64 `json:"maxMessageSize"`
	// Rules are the auto-reply rules (SSRV-02), in order; null is none.
	Rules []WSRuleDTO `json:"rules"`
}

// WSRuleDTO is an auto-reply rule: the first enabled rule whose pattern
// matches a text message sends its reply to the client.
type WSRuleDTO struct {
	Enabled bool `json:"enabled"`
	// Match is "exact", "contains" or "regex" (a Go RE2 regular
	// expression; the reply's $1, ${1}, $name and ${name} are its capture
	// groups, $$ is a $).
	Match   string `json:"match"`
	Pattern string `json:"pattern"`
	Reply   string `json:"reply"`
	// DelayMs holds the reply back, 0 to 60000.
	DelayMs int `json:"delayMs"`
}

// WSClientDTO is a client connected to a WebSocket server.
type WSClientDTO struct {
	// ID identifies the client within one run of the server; it is the
	// connection ID ("3") shown in the traffic.
	ID     string `json:"id"`
	ConnID int64  `json:"connId"`
	Peer   string `json:"peer"`
	// Target is the path and query it connected to.
	Target      string `json:"target"`
	Subprotocol string `json:"subprotocol"`
	// ConnectedAt is RFC 3339 in UTC.
	ConnectedAt string `json:"connectedAt"`
	Origin      string `json:"origin"`
	UserAgent   string `json:"userAgent"`
	MessagesIn  int64  `json:"messagesIn"`
	MessagesOut int64  `json:"messagesOut"`
	BytesIn     int64  `json:"bytesIn"`
	BytesOut    int64  `json:"bytesOut"`
}

// ClientsEventDTO is the payload of servers:clients: the clients of one
// WebSocket server now ([] once it stopped).
type ClientsEventDTO struct {
	ServerID string        `json:"serverId"`
	Clients  []WSClientDTO `json:"clients"`
}

// WSMessageDTO is a message to send: text, or binary with the bytes in
// dataBase64.
type WSMessageDTO struct {
	Binary     bool   `json:"binary"`
	Text       string `json:"text"`
	DataBase64 string `json:"dataBase64"`
}

// WSSendInputDTO sends a message to one client.
type WSSendInputDTO struct {
	ServerID string       `json:"serverId"`
	ClientID string       `json:"clientId"`
	Message  WSMessageDTO `json:"message"`
}

// WSBroadcastInputDTO sends a message to every client of a server.
type WSBroadcastInputDTO struct {
	ServerID string       `json:"serverId"`
	Message  WSMessageDTO `json:"message"`
}

// WSBroadcastResultDTO counts the clients a broadcast reached.
type WSBroadcastResultDTO struct {
	Sent   int `json:"sent"`
	Failed int `json:"failed"`
}

// WSDisconnectInputDTO closes a client's connection.
type WSDisconnectInputDTO struct {
	ServerID string `json:"serverId"`
	ClientID string `json:"clientId"`
	// Code is the close code; 0 means 1000.
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

// WSDTO is the WebSocket detail of a traffic event.
type WSDTO struct {
	ClientID string `json:"clientId"`
	// Opcode is "text" or "binary" on a message, else "".
	Opcode      string `json:"opcode"`
	Subprotocol string `json:"subprotocol"`
	// CloseCode, CloseReason and InitiatedBy ("client", "server" or "")
	// are set on connection_close.
	CloseCode   int    `json:"closeCode"`
	CloseReason string `json:"closeReason"`
	InitiatedBy string `json:"initiatedBy"`
}

func (c WebSocketConfigDTO) toCore() wsserver.Config {
	rules := make([]wsserver.Rule, 0, len(c.Rules))
	for _, r := range c.Rules {
		rules = append(rules, wsserver.Rule{Enabled: r.Enabled, Match: r.Match, Pattern: r.Pattern, Reply: r.Reply, DelayMs: r.DelayMs})
	}
	return wsserver.Config{Path: c.Path, Subprotocols: slices.Clone(c.Subprotocols), Echo: c.Echo, MaxMessageSize: c.MaxMessageSize, Rules: rules}
}

func toWebSocketConfigDTO(c wsserver.Config) *WebSocketConfigDTO {
	subs := slices.Clone(c.Subprotocols)
	if subs == nil {
		subs = []string{}
	}
	rules := make([]WSRuleDTO, 0, len(c.Rules))
	for _, r := range c.Rules {
		rules = append(rules, WSRuleDTO{Enabled: r.Enabled, Match: r.Match, Pattern: r.Pattern, Reply: r.Reply, DelayMs: r.DelayMs})
	}
	return &WebSocketConfigDTO{Path: c.Path, Subprotocols: subs, Echo: c.Echo, MaxMessageSize: c.MaxMessageSize, Rules: rules}
}

func toWSClientDTOs(list []wsserver.Client) []WSClientDTO {
	out := make([]WSClientDTO, 0, len(list))
	for _, c := range list {
		out = append(out, WSClientDTO{
			ID: c.ID, ConnID: int64(c.ConnID), Peer: c.Peer, Target: c.Target, Subprotocol: c.Subprotocol,
			ConnectedAt: formatTime(c.ConnectedAt), Origin: c.Origin, UserAgent: c.UserAgent,
			MessagesIn: int64(c.MessagesIn), MessagesOut: int64(c.MessagesOut), BytesIn: int64(c.BytesIn), BytesOut: int64(c.BytesOut),
		})
	}
	return out
}

func toWSDTO(w *core.WSInfo) *WSDTO {
	if w == nil {
		return nil
	}
	return &WSDTO{ClientID: w.ClientID, Opcode: w.Opcode, Subprotocol: w.Subprotocol, CloseCode: w.CloseCode, CloseReason: w.CloseReason, InitiatedBy: w.InitiatedBy}
}

// payload returns the bytes of m.
func (m WSMessageDTO) payload() ([]byte, error) {
	if !m.Binary {
		return []byte(m.Text), nil
	}
	b, err := base64.StdEncoding.DecodeString(m.DataBase64)
	if err != nil {
		return nil, invalidMessage("The binary message is not valid base64.")
	}
	return b, nil
}
