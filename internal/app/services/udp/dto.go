package udp

import (
	"encoding/base64"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/udpclient"
)

// OpenInputDTO is what Open takes: a udp request's address and settings
// as stored (docs/file-format.md#udp-request-kind-udp).
type OpenInputDTO struct {
	// RequestID is the udp request the socket belongs to: its variables
	// render the address, and it is passed back in the events. It may be
	// "" (no variables).
	RequestID string `json:"requestId"`
	// Address is host:port, with {{variables}}.
	Address string `json:"address"`
	// IPVersion is "" (either), "ipv4" or "ipv6".
	IPVersion string `json:"ipVersion"`
	// Connected receives only from the target; false from any sender.
	Connected bool `json:"connected"`
	// MaxDatagramBytes limits datagrams both ways: null for 65,535.
	MaxDatagramBytes *int64 `json:"maxDatagramBytes"`
	// OpenTimeoutMs bounds DNS and the proxy's UDP association: null for
	// 10 s, 0 for no limit.
	OpenTimeoutMs *int64 `json:"openTimeoutMs"`
}

// ConnectionDTO identifies a socket.
type ConnectionDTO struct {
	ConnID    string `json:"connId"`
	RequestID string `json:"requestId"`
	// Address is the address with its variables rendered.
	Address string `json:"address"`
}

// SendInputDTO is what Send takes.
type SendInputDTO struct {
	ConnID string `json:"connId"`
	// Mode is "text" (Text as UTF-8, {{variables}} rendered), "hex" or
	// "base64" (the bytes Text spells, as written).
	Mode string `json:"mode"`
	Text string `json:"text"`
}

// SendResultDTO is what Send returns.
type SendResultDTO struct {
	// Bytes is the size of the datagram.
	Bytes int `json:"bytes"`
}

// StateEventDTO is the payload of udp:state.
type StateEventDTO struct {
	ConnID    string `json:"connId"`
	RequestID string `json:"requestId"`
	Address   string `json:"address"`
	Seq       int64  `json:"seq"`
	// State is "opening", "open", "closing" or "closed".
	State string `json:"state"`
	// At is when the state was reached, RFC 3339 in UTC.
	At string `json:"at"`
	// LocalAddr and RemoteAddr are the socket's addresses once open (the
	// remote one is the proxy's relay through a proxy), else "".
	LocalAddr  string `json:"localAddr"`
	RemoteAddr string `json:"remoteAddr"`
	// Via names the proxy ("socks5h://proxy:1080"), "" when direct.
	Via string `json:"via"`
	// Proxy is the hop through the proxy (SOCKS-12, SOCKS-07): on "open",
	// and on "closed" when opening failed at the proxy; else null.
	Proxy *ProxyHopDTO `json:"proxy"`
	// Connected and MaxPayload (the largest datagram that can be sent)
	// are set from "open" on.
	Connected  bool `json:"connected"`
	MaxPayload int  `json:"maxPayload"`
	// Close is set on "closed".
	Close *CloseInfoDTO `json:"close"`
	// Error is set on "closed" when the socket failed (also when opening
	// failed).
	Error *apperr.ErrorDTO `json:"error"`
}

// ProxyHopDTO is how the socket went through a proxy (netx.ProxyHop). It
// holds no credential.
type ProxyHopDTO struct {
	// Scheme is socks5 or socks5h; Version "SOCKS5".
	Scheme  string `json:"scheme"`
	Version string `json:"version"`
	// Proxy is the proxy's host:port as configured, ProxyAddr the address
	// connected to.
	Proxy     string `json:"proxy"`
	ProxyAddr string `json:"proxyAddr"`
	// Command is "UDP ASSOCIATE"; RelayAddr the relay datagrams go to.
	Command   string `json:"command"`
	RelayAddr string `json:"relayAddr"`
	// Auth is "none" or "username/password"; "" when not chosen.
	Auth string `json:"auth"`
	// RemoteDNS is set when the proxy resolves the target's host name.
	RemoteDNS bool `json:"remoteDns"`
	// Reply is the proxy's reply code, null when none came; ReplyText its
	// meaning.
	Reply     *int   `json:"reply"`
	ReplyText string `json:"replyText"`
	// HandshakeMs is the greeting, authentication and UDP ASSOCIATE.
	HandshakeMs float64 `json:"handshakeMs"`
}

// CloseInfoDTO says how a socket ended.
type CloseInfoDTO struct {
	// InitiatedBy is "local" (Close, or opening cancelled) or "none" (a
	// failure).
	InitiatedBy       string `json:"initiatedBy"`
	BytesSent         int64  `json:"bytesSent"`
	BytesReceived     int64  `json:"bytesReceived"`
	DatagramsSent     int64  `json:"datagramsSent"`
	DatagramsReceived int64  `json:"datagramsReceived"`
	// Notes counts the dropped datagrams and ICMP errors.
	Notes int64 `json:"notes"`
	// DurationMs is how long the socket was open; 0 when it never opened.
	DurationMs int64 `json:"durationMs"`
}

// DataEventDTO is the payload of udp:data.
type DataEventDTO struct {
	ConnID    string        `json:"connId"`
	RequestID string        `json:"requestId"`
	Seq       int64         `json:"seq"`
	Datagrams []DatagramDTO `json:"datagrams"`
}

// DatagramDTO is a datagram sent or received, or a note.
type DatagramDTO struct {
	// Direction is "sent", "received" or "note" (a datagram the proxy
	// relay sent fragmented or malformed and was dropped, or an ICMP
	// error; Error says which).
	Direction string `json:"direction"`
	// At is RFC 3339 in UTC.
	At string `json:"at"`
	// Data is the payload, base64-encoded; "" for a note.
	Data string `json:"data"`
	// Size is the datagram's size; more than Data holds when Truncated.
	Size      int  `json:"size"`
	Truncated bool `json:"truncated"`
	// Peer is the sender of a received datagram, the target of a sent
	// one.
	Peer string `json:"peer"`
	// Error is a note's.
	Error *apperr.ErrorDTO `json:"error"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toDatagramDTO(d udpclient.Datagram) DatagramDTO {
	return DatagramDTO{
		Direction: string(d.Direction),
		At:        formatTime(d.At),
		Data:      base64.StdEncoding.EncodeToString(d.Data),
		Size:      d.Size,
		Truncated: d.Truncated,
		Peer:      d.Peer,
	}
}

func toNoteDTO(n udpclient.Note) DatagramDTO {
	dto := apperr.ToDTO(n.Err)
	return DatagramDTO{Direction: "note", At: formatTime(n.At), Error: &dto}
}

func toProxyHopDTO(h *netx.ProxyHop) *ProxyHopDTO {
	if h == nil {
		return nil
	}
	d := &ProxyHopDTO{
		Scheme: h.Scheme, Version: h.Version, Proxy: h.Proxy, ProxyAddr: h.ProxyAddr,
		Command: h.Command, RelayAddr: h.RelayAddr, Auth: h.Auth, RemoteDNS: h.RemoteDNS,
		ReplyText: h.ReplyText, HandshakeMs: float64(h.Handshake.Microseconds()) / 1000,
	}
	if h.Replied {
		r := h.Reply
		d.Reply = &r
	}
	return d
}

func toCloseInfoDTO(info *udpclient.CloseInfo) *CloseInfoDTO {
	if info == nil {
		return nil
	}
	dto := &CloseInfoDTO{
		InitiatedBy:       string(info.InitiatedBy),
		BytesSent:         info.BytesSent,
		BytesReceived:     info.BytesReceived,
		DatagramsSent:     info.DatagramsSent,
		DatagramsReceived: info.DatagramsReceived,
		Notes:             info.Notes,
	}
	if !info.OpenedAt.IsZero() && !info.ClosedAt.IsZero() {
		dto.DurationMs = info.ClosedAt.Sub(info.OpenedAt).Milliseconds()
	}
	return dto
}
