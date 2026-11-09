package servers

import (
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
)

// The DTOs of the UDP server (SSRV-06): its replies, its recent peers and
// sending a datagram. See docs/servers.md.

// UDPConfigDTO is how a udp server answers datagrams.
type UDPConfigDTO struct {
	// Echo sends every datagram back to its sender.
	Echo bool `json:"echo"`
	// FixedReply sends reply to the sender of every datagram (after the
	// echo when both are on).
	FixedReply bool   `json:"fixedReply"`
	Reply      string `json:"reply"`
	// ReplyFormat is "text" or "hex".
	ReplyFormat string `json:"replyFormat"`
}

// UDPPeerDTO is a peer a UDP server heard from recently.
type UDPPeerDTO struct {
	// Addr is the peer's address, what SendDatagram takes.
	Addr string `json:"addr"`
	// Local is the server's address it sent to.
	Local string `json:"local"`
	// FirstSeen and LastSeen are RFC 3339 in UTC.
	FirstSeen    string `json:"firstSeen"`
	LastSeen     string `json:"lastSeen"`
	DatagramsIn  int64  `json:"datagramsIn"`
	DatagramsOut int64  `json:"datagramsOut"`
	BytesIn      int64  `json:"bytesIn"`
	BytesOut     int64  `json:"bytesOut"`
}

// PeersEventDTO is the payload of servers:peers: the recent peers of one
// UDP server, most recent first ([] once it stopped).
type PeersEventDTO struct {
	ServerID string       `json:"serverId"`
	Peers    []UDPPeerDTO `json:"peers"`
}

// UDPSendInputDTO sends one datagram to a recent peer.
type UDPSendInputDTO struct {
	ServerID string `json:"serverId"`
	Peer     string `json:"peer"`
	// Mode is "text" (Text as UTF-8, plus LineEnding) or "hex".
	Mode string `json:"mode"`
	Text string `json:"text"`
	// LineEnding is "none" (or ""), "lf" or "crlf".
	LineEnding string `json:"lineEnding"`
}

// UDPSendResultDTO says how many bytes the datagram held.
type UDPSendResultDTO struct {
	Bytes int `json:"bytes"`
}

// UDPDTO is the UDP server's detail of a traffic event.
type UDPDTO struct {
	// Local is the server's address the datagram arrived on or left from.
	Local string `json:"local"`
	// Reply is "echo" or "fixed" for an automatic reply, "" otherwise.
	Reply string `json:"reply"`
}

func (c UDPConfigDTO) toCore() udpserver.Config {
	return udpserver.Config{Echo: c.Echo, FixedReply: c.FixedReply, Reply: c.Reply, ReplyFormat: c.ReplyFormat}
}

func toUDPConfigDTO(c udpserver.Config) *UDPConfigDTO {
	return &UDPConfigDTO{Echo: c.Echo, FixedReply: c.FixedReply, Reply: c.Reply, ReplyFormat: c.ReplyFormat}
}

func toUDPPeerDTOs(list []udpserver.Peer) []UDPPeerDTO {
	out := make([]UDPPeerDTO, 0, len(list))
	for _, p := range list {
		out = append(out, UDPPeerDTO{
			Addr: p.Addr, Local: p.Local, FirstSeen: formatTime(p.FirstSeen), LastSeen: formatTime(p.LastSeen),
			DatagramsIn: int64(p.DatagramsIn), DatagramsOut: int64(p.DatagramsOut), BytesIn: int64(p.BytesIn), BytesOut: int64(p.BytesOut),
		})
	}
	return out
}

func toUDPDTO(u *core.UDPInfo) *UDPDTO {
	if u == nil {
		return nil
	}
	return &UDPDTO{Local: u.Local, Reply: u.Reply}
}
