package websocket

import (
	"encoding/base64"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// ConnectInputDTO is what Connect takes: the handshake of a websocket
// request as the editor holds it, {{variables}} unresolved.
type ConnectInputDTO struct {
	// RequestID is the websocket request the connection belongs to: its
	// scopes render the {{variables}} (WS-11), and it is passed back in the
	// events. It may be "" (then no variable is defined).
	RequestID string `json:"requestId"`
	// URL is the ws:// or wss:// URL, query included.
	URL string `json:"url"`
	// Headers are the handshake header rows; disabled rows are skipped.
	Headers []HeaderDTO `json:"headers"`
	// Subprotocols are offered in Sec-WebSocket-Protocol, in order.
	Subprotocols []string `json:"subprotocols"`
	// ConnectTimeoutMs bounds connect, TLS and the upgrade: null for the
	// default (30 s), 0 for no limit.
	ConnectTimeoutMs *int64 `json:"connectTimeoutMs"`
	// VerifyTLS false accepts any certificate; null means verify.
	VerifyTLS *bool `json:"verifyTls"`
	// SendCookies false leaves the project's cookie jar out; null means
	// use it.
	SendCookies *bool `json:"sendCookies"`
	// PingIntervalMs sends a keepalive ping this often while the
	// connection is open (WS-09); null or 0 sends none. Values under 100
	// are raised to 100.
	PingIntervalMs *int64 `json:"pingIntervalMs,omitempty"`
	// PongTimeoutMs is how long a pong may take after a keepalive ping
	// before the connection is closed (wsclient.pong_timeout): null for
	// the default (10 s), 0 to never give up.
	PongTimeoutMs *int64 `json:"pongTimeoutMs,omitempty"`
	// AutoReconnect connects again, with back-off, when the connection
	// drops or the server closes with 1001, 1011–1014 (WS-10).
	AutoReconnect *bool `json:"autoReconnect,omitempty"`
	// ReconnectMaxAttempts is how many reconnects in a row are tried:
	// null for 5, 0 for no limit.
	ReconnectMaxAttempts *int `json:"reconnectMaxAttempts,omitempty"`
	// ReconnectDelayMs is the wait before the first reconnect, doubled for
	// each further one (null: 1000; at least 100).
	ReconnectDelayMs *int64 `json:"reconnectDelayMs,omitempty"`
	// ReconnectMaxDelayMs caps the wait (null: 30000).
	ReconnectMaxDelayMs *int64 `json:"reconnectMaxDelayMs,omitempty"`
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

// ConnectionDTO identifies a connection.
type ConnectionDTO struct {
	ConnID    string `json:"connId"`
	RequestID string `json:"requestId"`
	// URL is the URL connected to, without userinfo.
	URL string `json:"url"`
}

// SendInputDTO is what Send takes.
type SendInputDTO struct {
	ConnID string `json:"connId"`
	// Text is the message: in the text formats sent as one text message,
	// its {{variables}} rendered (WS-11); in "hex" and "base64" the bytes of
	// one binary message, sent as written (WS-04).
	Text string `json:"text"`
	// Format is "text" (or ""), "json" (sent as text), "hex" or "base64".
	Format string `json:"format,omitempty"`
	// AllowUnresolved sends {{variables}} that no scope defines as written
	// instead of refusing (the "Send anyway" answer of ENV-11).
	AllowUnresolved bool `json:"allowUnresolved,omitempty"`
}

// PingInputDTO is what Ping takes.
type PingInputDTO struct {
	ConnID string `json:"connId"`
	// Payload is the ping's payload, at most 125 bytes: text as written
	// (no {{variables}}), or bytes typed as hex or Base64.
	Payload string `json:"payload"`
	// Format is "text" (or ""), "hex" or "base64".
	Format string `json:"format,omitempty"`
}

// PingResultDTO is what Ping returns.
type PingResultDTO struct {
	// Bytes is the size of the payload.
	Bytes int `json:"bytes"`
}

// SendResultDTO is what Send returns.
type SendResultDTO struct {
	// Bytes is the size of the message in bytes (UTF-8).
	Bytes int `json:"bytes"`
}

// CloseInputDTO is what Close takes.
type CloseInputDTO struct {
	ConnID string `json:"connId"`
	// Code is the close code to send; 0 means 1000 (normal closure).
	Code int `json:"code"`
	// Reason is the close reason, at most 123 bytes of UTF-8.
	Reason string `json:"reason"`
}

// StateEventDTO is the payload of ws:state.
type StateEventDTO struct {
	ConnID    string `json:"connId"`
	RequestID string `json:"requestId"`
	URL       string `json:"url"`
	Seq       int64  `json:"seq"`
	// Attempt numbers the connection's attempts from 1; a reconnect
	// (WS-10) is attempt 2 and up.
	Attempt int `json:"attempt"`
	// State is "connecting", "open", "closing" or "closed".
	State string `json:"state"`
	// At is when the state was reached, RFC 3339 in UTC.
	At string `json:"at"`
	// Handshake is set on "open", and on "closed" when connecting failed
	// (with the server's response, if one arrived).
	Handshake *HandshakeDTO `json:"handshake"`
	// Close is set on "closed".
	Close *CloseInfoDTO `json:"close"`
	// Error is set on "closed" when the connection failed.
	Error *apperr.ErrorDTO `json:"error"`
	// Reconnect is set on "closed" with auto-reconnect on (WS-10): the
	// next attempt and the wait before it, or why there is none. A
	// "closed" event with reconnect.stopped "manual" and no close info
	// says the wait for a reconnect was cancelled.
	Reconnect *ReconnectDTO `json:"reconnect"`
	// Tests are set on the "closed" event that ends the connection for
	// good (no reconnect follows): the results of the request's
	// assertions against the messages received (TEST-08), empty without
	// any. Null on the other events.
	Tests []streamcheck.TestResultDTO `json:"tests"`
}

// ReconnectDTO says what auto-reconnect does after an attempt ended.
type ReconnectDTO struct {
	// Attempt is the number of the reconnect that follows, counted from 1
	// since the connection was last open; with Stopped, the number of
	// reconnects made.
	Attempt int `json:"attempt"`
	// MaxAttempts is the limit of reconnects in a row; 0 is none.
	MaxAttempts int `json:"maxAttempts"`
	// DelayMs is the wait before the next attempt.
	DelayMs int64 `json:"delayMs"`
	// Stopped is "" when an attempt follows, else why not: "manual",
	// "close_code", "error" or "max_attempts".
	Stopped string `json:"stopped"`
}

// HandshakeDTO summarizes the upgrade exchange.
type HandshakeDTO struct {
	// URL is the URL connected to, without userinfo.
	URL string `json:"url"`
	// RequestHeaders are the headers sent, in order, as spelled.
	RequestHeaders []HeaderDTO `json:"requestHeaders"`
	// Response is the server's answer; null when none arrived.
	Response *HandshakeResponseDTO `json:"response"`
	// Subprotocol is the subprotocol the server selected, or "".
	Subprotocol string `json:"subprotocol"`
	// LocalAddr and RemoteAddr are the socket's addresses, "" when the
	// connection was not made.
	LocalAddr  string `json:"localAddr"`
	RemoteAddr string `json:"remoteAddr"`
	// TLSVersion is "TLS 1.2" or "TLS 1.3" for wss, else "".
	TLSVersion string `json:"tlsVersion"`
	// DurationMs is how long connecting took.
	DurationMs int64 `json:"durationMs"`
}

// HandshakeResponseDTO is the answer to the upgrade request.
type HandshakeResponseDTO struct {
	StatusCode int `json:"statusCode"`
	// Status is the status line's text, e.g. "101 Switching Protocols".
	Status  string      `json:"status"`
	Headers []HeaderDTO `json:"headers"`
	// Body is the body of a rejected upgrade as text (invalid UTF-8
	// replaced), cut at 1 MiB (BodyTruncated).
	Body          string `json:"body"`
	BodyTruncated bool   `json:"bodyTruncated"`
}

// CloseInfoDTO says how a connection ended.
type CloseInfoDTO struct {
	// Code and Reason are the server's close frame (1005: no status), or
	// 1006 when none arrived.
	Code   int    `json:"code"`
	Reason string `json:"reason"`
	// WasClean is true when close frames went both ways.
	WasClean bool `json:"wasClean"`
	// InitiatedBy is "local", "remote" or "none" (a failure, or the
	// handshake failed).
	InitiatedBy string `json:"initiatedBy"`
	// Sent and Received are the close frames sent and received, null if
	// none.
	Sent     *CloseFrameDTO `json:"sent"`
	Received *CloseFrameDTO `json:"received"`
	// Message and byte counts of the text and binary messages.
	MessagesSent     int64 `json:"messagesSent"`
	MessagesReceived int64 `json:"messagesReceived"`
	BytesSent        int64 `json:"bytesSent"`
	BytesReceived    int64 `json:"bytesReceived"`
	// DurationMs is how long the connection was open; 0 when it never
	// opened.
	DurationMs int64 `json:"durationMs"`
}

// CloseFrameDTO is the code and reason of one close frame.
type CloseFrameDTO struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

// MessageEventDTO is the payload of ws:message.
type MessageEventDTO struct {
	ConnID    string       `json:"connId"`
	RequestID string       `json:"requestId"`
	Seq       int64        `json:"seq"`
	Messages  []MessageDTO `json:"messages"`
}

// MessageDTO is one text or binary message, or ping or pong (WS-09), sent
// or received.
type MessageDTO struct {
	// Direction is "sent" or "received".
	Direction string `json:"direction"`
	// Opcode is "text", "binary", "ping" or "pong".
	Opcode string `json:"opcode"`
	// At is when the message was handed to the socket or completely
	// received, RFC 3339 in UTC.
	At string `json:"at"`
	// Text is a text message's content ("" for binary), or a ping's or
	// pong's payload when it is UTF-8.
	Text string `json:"text"`
	// Data is a binary message's bytes, or a ping's or pong's payload,
	// base64 (standard alphabet, padded); "" for text.
	Data string `json:"data"`
	// Size is the message's size in bytes.
	Size int `json:"size"`
	// Keepalive marks a keepalive ping and the pong that answered it.
	Keepalive bool `json:"keepalive,omitempty"`
	// RTTMs is set on a pong that answers a ping sent here: the round
	// trip in milliseconds (fractions kept).
	RTTMs *float64 `json:"rttMs,omitempty"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toMessageDTO(dir string, op wsframe.Opcode, data []byte, at time.Time) MessageDTO {
	m := MessageDTO{Direction: dir, At: formatTime(at), Size: len(data)}
	if op == wsframe.OpText {
		m.Opcode = "text"
		m.Text = string(data)
	} else {
		m.Opcode = "binary"
		m.Data = base64.StdEncoding.EncodeToString(data)
	}
	return m
}

// toPingPongDTO is a ping or pong as a message of ws:message.
func toPingPongDTO(p wsclient.PingPong) MessageDTO {
	m := MessageDTO{
		Direction: "received",
		Opcode:    "ping",
		At:        formatTime(p.At),
		Data:      base64.StdEncoding.EncodeToString(p.Payload),
		Size:      len(p.Payload),
		Keepalive: p.Keepalive,
	}
	if p.Direction == wsframe.Sent {
		m.Direction = "sent"
	}
	if p.Opcode == wsframe.OpPong {
		m.Opcode = "pong"
	}
	if utf8.Valid(p.Payload) {
		m.Text = string(p.Payload)
	}
	if p.Answered {
		m.RTTMs = new(float64(p.RTT.Microseconds()) / 1000)
	}
	return m
}

func toHeaderDTOs(rows []httpclient.Header) []HeaderDTO {
	out := make([]HeaderDTO, len(rows))
	for i, h := range rows {
		out[i] = HeaderDTO{Name: h.Name, Value: h.Value, Enabled: h.Enabled}
	}
	return out
}

func toHeaders(rows []HeaderDTO) []httpclient.Header {
	out := make([]httpclient.Header, len(rows))
	for i, h := range rows {
		out[i] = httpclient.Header{Name: h.Name, Value: h.Value, Enabled: h.Enabled}
	}
	return out
}

func toHandshakeDTO(hs *wsclient.Handshake) *HandshakeDTO {
	if hs == nil {
		return nil
	}
	dto := &HandshakeDTO{
		URL:            hs.URL,
		RequestHeaders: toHeaderDTOs(hs.RequestHeaders),
		Subprotocol:    hs.Subprotocol,
		LocalAddr:      hs.Conn.LocalAddr,
		RemoteAddr:     hs.Conn.RemoteAddr,
		DurationMs:     hs.Timing.Total.Milliseconds(),
	}
	if hs.Conn.TLS != nil {
		dto.TLSVersion = hs.Conn.TLS.Version
	}
	if r := hs.Response; r != nil {
		dto.Response = &HandshakeResponseDTO{
			StatusCode:    r.StatusCode,
			Status:        r.Status,
			Headers:       toHeaderDTOs(r.Headers),
			Body:          toValidUTF8(r.Body),
			BodyTruncated: r.BodyTruncated,
		}
	}
	return dto
}

// maskHandshake hides the auth's secrets (m, nil for none) in the URL
// and the request headers of dto, which is changed and returned.
func maskHandshake(dto *HandshakeDTO, m *httpclient.SecretMasker) *HandshakeDTO {
	if dto == nil || m == nil {
		return dto
	}
	dto.URL = m.MaskText(dto.URL)
	for i, h := range dto.RequestHeaders {
		if masked := m.MaskText(h.Value); masked != h.Value {
			dto.RequestHeaders[i].Value = m.MaskHeader(h.Name, h.Value)
		}
	}
	return dto
}

func toValidUTF8(b []byte) string {
	return strings.ToValidUTF8(string(b), string(utf8.RuneError))
}

func toCloseFrameDTO(f *wsclient.CloseFrame) *CloseFrameDTO {
	if f == nil {
		return nil
	}
	return &CloseFrameDTO{Code: int(f.Code), Reason: f.Reason}
}
