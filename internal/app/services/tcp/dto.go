package tcp

import (
	"encoding/base64"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/framing"
	"github.com/sorrelfox/postsonde/internal/core/realtime/tcpclient"
)

// ConnectInputDTO is what Connect takes.
type ConnectInputDTO struct {
	// RequestID is the tcp request the connection belongs to; it is only
	// passed back in the events. It may be "".
	RequestID string `json:"requestId"`
	// Address is host:port.
	Address string `json:"address"`
	// ConnectTimeoutMs bounds connecting and the TLS handshake: null for
	// the default (10 s), 0 for no limit.
	ConnectTimeoutMs *int64 `json:"connectTimeoutMs"`
	// TLS connects with TLS (TCP-02): the app's trusted CAs (system and
	// custom bundles) and the client certificate set for this host and
	// port.
	TLS bool `json:"tls"`
	// VerifyTLS false accepts any server certificate; null verifies.
	VerifyTLS *bool `json:"verifyTls"`
	// Framing splits received bytes into messages and frames sent ones
	// (TCP-03); null is no framing.
	Framing *FramingDTO `json:"framing"`
}

// FramingDTO is a connection's message framing, as a tcp request stores
// it (docs/tcp.md#framing).
type FramingDTO struct {
	// Mode is "none" (or ""), "newline" or "length".
	Mode string `json:"mode"`
	// Delimiter is appended to sent messages by newline framing: "lf" (or
	// "") or "crlf". Received lines may end in either.
	Delimiter string `json:"delimiter"`
	// PrefixBytes is the length prefix's size: 1, 2, 4 or 8 (0: 4).
	PrefixBytes int `json:"prefixBytes"`
	// ByteOrder of the length prefix: "big" (or "") or "little".
	ByteOrder string `json:"byteOrder"`
	// MaxMessageBytes limits a received message: null for 1 MiB, at most
	// 32 MiB.
	MaxMessageBytes *int64 `json:"maxMessageBytes"`
}

// ConnectionDTO identifies a connection.
type ConnectionDTO struct {
	ConnID    string `json:"connId"`
	RequestID string `json:"requestId"`
	Address   string `json:"address"`
}

// SendInputDTO is what Send takes.
type SendInputDTO struct {
	ConnID string `json:"connId"`
	// Mode is "text" (Text as UTF-8, plus LineEnding) or "hex" (Text parsed
	// as hex bytes; LineEnding is ignored).
	Mode string `json:"mode"`
	Text string `json:"text"`
	// LineEnding is "none" (or ""), "lf" or "crlf". It is not appended
	// when the connection has a framing, which frames the message itself.
	LineEnding string `json:"lineEnding"`
}

// SendResultDTO is what Send returns.
type SendResultDTO struct {
	// Bytes is the size of the message (without a framing's prefix or
	// delimiter).
	Bytes int `json:"bytes"`
}

// StateEventDTO is the payload of tcp:state.
type StateEventDTO struct {
	ConnID    string `json:"connId"`
	RequestID string `json:"requestId"`
	Address   string `json:"address"`
	Seq       int64  `json:"seq"`
	// State is "connecting", "open", "closing" or "closed".
	State string `json:"state"`
	// At is when the state was reached, RFC 3339 in UTC.
	At string `json:"at"`
	// LocalAddr and RemoteAddr are the socket's addresses once open, else "".
	LocalAddr  string `json:"localAddr"`
	RemoteAddr string `json:"remoteAddr"`
	// TLS describes the TLS session from "open" on; null without TLS.
	TLS *TLSDTO `json:"tls"`
	// Close is set on "closed".
	Close *CloseInfoDTO `json:"close"`
	// Error is set on "closed" when the connection failed (also when
	// connecting failed).
	Error *apperr.ErrorDTO `json:"error"`
}

// TLSDTO describes a connection's TLS session.
type TLSDTO struct {
	// Version is "TLS 1.2" or "TLS 1.3".
	Version string `json:"version"`
	// CipherSuite is the IANA name, e.g. "TLS_AES_128_GCM_SHA256".
	CipherSuite string `json:"cipherSuite"`
	// ServerName is the SNI sent, "" for an IP address.
	ServerName string `json:"serverName"`
	// PeerSubject is the subject of the server's certificate.
	PeerSubject string `json:"peerSubject"`
}

// CloseInfoDTO says how a connection ended.
type CloseInfoDTO struct {
	// InitiatedBy is "local" (Disconnect, or connecting cancelled), "remote"
	// (the server closed or reset the connection) or "none" (a failure).
	InitiatedBy string `json:"initiatedBy"`
	// BytesSent and BytesReceived count the bytes on the connection (with
	// framing; TLS overhead not counted); ChunksSent and ChunksReceived
	// the chunks, or messages with a framing.
	BytesSent      int64 `json:"bytesSent"`
	BytesReceived  int64 `json:"bytesReceived"`
	ChunksSent     int64 `json:"chunksSent"`
	ChunksReceived int64 `json:"chunksReceived"`
	// DurationMs is how long the connection was open; 0 when it never
	// opened.
	DurationMs int64 `json:"durationMs"`
}

// DataEventDTO is the payload of tcp:data.
type DataEventDTO struct {
	ConnID    string     `json:"connId"`
	RequestID string     `json:"requestId"`
	Seq       int64      `json:"seq"`
	Chunks    []ChunkDTO `json:"chunks"`
}

// ChunkDTO is bytes sent with one Send or returned by one read; with a
// framing, one message without its prefix or delimiter.
type ChunkDTO struct {
	// Direction is "sent" or "received".
	Direction string `json:"direction"`
	// At is when the bytes were read or handed to the socket, RFC 3339 in
	// UTC.
	At string `json:"at"`
	// Data is the bytes, base64-encoded (standard alphabet, padded).
	Data string `json:"data"`
	// Size is the number of bytes.
	Size int `json:"size"`
	// Incomplete marks the start of a message the connection ended in
	// the middle of (framing only).
	Incomplete bool `json:"incomplete"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toChunkDTO(c tcpclient.Chunk) ChunkDTO {
	return ChunkDTO{
		Direction:  string(c.Direction),
		At:         formatTime(c.At),
		Data:       base64.StdEncoding.EncodeToString(c.Data),
		Size:       len(c.Data),
		Incomplete: c.Incomplete,
	}
}

func toTLSDTO(info *httpclient.TLSInfo) *TLSDTO {
	if info == nil {
		return nil
	}
	dto := &TLSDTO{Version: info.Version, CipherSuite: info.CipherSuite, ServerName: info.ServerName}
	if len(info.PeerCertificates) > 0 {
		dto.PeerSubject = info.PeerCertificates[0].Subject
	}
	return dto
}

// toFramingOptions maps the framing of Connect; nil is no framing. The
// options are checked when connecting.
func toFramingOptions(f *FramingDTO) framing.Options {
	if f == nil {
		return framing.Options{}
	}
	o := framing.Options{
		Mode:       framing.Mode(f.Mode),
		Delimiter:  f.Delimiter,
		PrefixSize: f.PrefixBytes,
		ByteOrder:  f.ByteOrder,
	}
	if f.MaxMessageBytes != nil {
		o.MaxMessageSize = -1 // out of range unless it fits below
		if n := *f.MaxMessageBytes; n > 0 && n <= framing.LimitMaxMessageSize {
			o.MaxMessageSize = int(n)
		}
	}
	return o
}

func toCloseInfoDTO(info *tcpclient.CloseInfo) *CloseInfoDTO {
	if info == nil {
		return nil
	}
	dto := &CloseInfoDTO{
		InitiatedBy:    string(info.InitiatedBy),
		BytesSent:      info.BytesSent,
		BytesReceived:  info.BytesReceived,
		ChunksSent:     info.ChunksSent,
		ChunksReceived: info.ChunksReceived,
	}
	if !info.OpenedAt.IsZero() && !info.ClosedAt.IsZero() {
		dto.DurationMs = info.ClosedAt.Sub(info.OpenedAt).Milliseconds()
	}
	return dto
}
