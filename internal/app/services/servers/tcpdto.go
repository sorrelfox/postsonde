package servers

import (
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/hexbytes"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/tcpserver"
)

// The DTOs of the TCP server (SSRV-05): its connections and the connection
// operations. See docs/servers.md.

// CodeInvalidTCPMessage: a send mode or line ending the TCP server does
// not know. Bad hex is a hexbytes.* error.
const CodeInvalidTCPMessage = "tcpserver.invalid_message"

// TCPConnectionDTO is a connection open on a TCP server.
type TCPConnectionDTO struct {
	// ID identifies the connection within one run of the server; it is the
	// connection ID ("3") shown in the traffic.
	ID     string `json:"id"`
	ConnID int64  `json:"connId"`
	// Peer is the client's address, Local the server's end.
	Peer  string `json:"peer"`
	Local string `json:"local"`
	// ConnectedAt is RFC 3339 in UTC.
	ConnectedAt string `json:"connectedAt"`
	BytesIn     int64  `json:"bytesIn"`
	BytesOut    int64  `json:"bytesOut"`
	// MessagesIn counts reads (chunks received), MessagesOut sends.
	MessagesIn  int64 `json:"messagesIn"`
	MessagesOut int64 `json:"messagesOut"`
}

// ConnectionsEventDTO is the payload of servers:connections: the open
// connections of one TCP server now ([] once it stopped).
type ConnectionsEventDTO struct {
	ServerID    string             `json:"serverId"`
	Connections []TCPConnectionDTO `json:"connections"`
}

// TCPSendInputDTO sends bytes to one connection of a TCP server.
type TCPSendInputDTO struct {
	ServerID     string `json:"serverId"`
	ConnectionID string `json:"connectionId"`
	// Mode is "text" (Text as UTF-8, plus LineEnding) or "hex" (Text parsed
	// as hex bytes; LineEnding is ignored).
	Mode string `json:"mode"`
	Text string `json:"text"`
	// LineEnding is "none" (or ""), "lf" or "crlf".
	LineEnding string `json:"lineEnding"`
}

// TCPSendResultDTO says how many bytes were sent.
type TCPSendResultDTO struct {
	Bytes int `json:"bytes"`
}

// TCPCloseInputDTO closes a connection of a TCP server.
type TCPCloseInputDTO struct {
	ServerID     string `json:"serverId"`
	ConnectionID string `json:"connectionId"`
}

// TCPDTO is the TCP server's detail of a traffic event.
type TCPDTO struct {
	// InitiatedBy is set on connection_close: "client", "server" or "" when
	// the connection failed.
	InitiatedBy string `json:"initiatedBy"`
	// BytesIn and BytesOut are the connection's totals on connection_close.
	BytesIn  int64 `json:"bytesIn"`
	BytesOut int64 `json:"bytesOut"`
}

func toTCPConnectionDTOs(list []tcpserver.Connection) []TCPConnectionDTO {
	out := make([]TCPConnectionDTO, 0, len(list))
	for _, c := range list {
		out = append(out, TCPConnectionDTO{
			ID: c.ID, ConnID: int64(c.ConnID), Peer: c.Peer, Local: c.Local, ConnectedAt: formatTime(c.ConnectedAt),
			BytesIn: int64(c.BytesIn), BytesOut: int64(c.BytesOut), MessagesIn: int64(c.MessagesIn), MessagesOut: int64(c.MessagesOut),
		})
	}
	return out
}

func toTCPDTO(t *core.TCPInfo) *TCPDTO {
	if t == nil {
		return nil
	}
	return &TCPDTO{InitiatedBy: t.InitiatedBy, BytesIn: int64(t.BytesIn), BytesOut: int64(t.BytesOut)}
}

// payload returns the bytes in stands for: the text with its line ending,
// or the bytes the hex spells.
func (in TCPSendInputDTO) payload() ([]byte, error) {
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
				WithCode(CodeInvalidTCPMessage).WithHint(`Use "none", "lf" or "crlf".`).WithDetail("lineEnding", in.LineEnding)
		}
		return []byte(in.Text + ending), nil
	case "hex":
		return hexbytes.Parse(in.Text)
	}
	return nil, errs.New(errs.InvalidArgument, "The send mode is not one PostSonde knows.").
		WithCode(CodeInvalidTCPMessage).WithHint(`Use "text" or "hex".`).WithDetail("mode", in.Mode)
}
