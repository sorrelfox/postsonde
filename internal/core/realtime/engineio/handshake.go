package engineio

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"time"
)

// Handshake is the data of the open packet (spec "Handshake"), for example
//
//	0{"sid":"lv_VI97HAXpY6yYWAAAC","upgrades":["websocket"],"pingInterval":25000,"pingTimeout":5000,"maxPayload":1000000}
type Handshake struct {
	// SID is the session ID, sent back as the sid query parameter.
	SID string `json:"sid"`
	// Upgrades lists the transports the connection can be upgraded to
	// ("websocket"); empty when the connection is already a WebSocket.
	Upgrades []string `json:"upgrades"`
	// PingInterval and PingTimeout are the heartbeat settings in
	// milliseconds (spec "Heartbeat").
	PingInterval int `json:"pingInterval"`
	PingTimeout  int `json:"pingTimeout"`
	// MaxPayload is the largest long-polling body the server accepts, in
	// bytes; 0 when the server did not send it.
	MaxPayload int `json:"maxPayload,omitempty"`
}

// PingIntervalDuration returns PingInterval as a duration.
func (h Handshake) PingIntervalDuration() time.Duration {
	return time.Duration(h.PingInterval) * time.Millisecond
}

// PingTimeoutDuration returns PingTimeout as a duration.
func (h Handshake) PingTimeoutDuration() time.Duration {
	return time.Duration(h.PingTimeout) * time.Millisecond
}

// maxMillis bounds the heartbeat values: a JavaScript timer holds at most
// 2^31-1 ms.
const maxMillis = 1<<31 - 1

// ParseHandshake parses and validates the data of an open packet: a JSON
// object with a non-empty string "sid", an array of strings "upgrades",
// positive integers "pingInterval" and "pingTimeout" (at most 2^31-1 ms)
// and an optional non-negative integer "maxPayload". Other fields are
// ignored.
func ParseHandshake(data []byte) (Handshake, error) {
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return Handshake{}, handshakeError("", "The Engine.IO handshake is not a JSON object.")
	}
	if _, err := dec.Token(); err != io.EOF {
		return Handshake{}, handshakeError("", "The Engine.IO handshake has data after the JSON object.")
	}
	var h Handshake
	raw, ok := fields["sid"]
	if !ok || json.Unmarshal(raw, &h.SID) != nil || h.SID == "" {
		return Handshake{}, handshakeError("sid", "The Engine.IO handshake has no session ID (sid).")
	}
	raw, ok = fields["upgrades"]
	if !ok || json.Unmarshal(raw, &h.Upgrades) != nil || h.Upgrades == nil {
		return Handshake{}, handshakeError("upgrades", "The Engine.IO handshake's upgrades is not an array of strings.")
	}
	var err error
	if h.PingInterval, err = intField(fields, "pingInterval", true, 1, maxMillis); err != nil {
		return Handshake{}, err
	}
	if h.PingTimeout, err = intField(fields, "pingTimeout", true, 1, maxMillis); err != nil {
		return Handshake{}, err
	}
	if h.MaxPayload, err = intField(fields, "maxPayload", false, 0, 1<<53-1); err != nil {
		return Handshake{}, err
	}
	return h, nil
}

func intField(fields map[string]json.RawMessage, name string, required bool, lo, hi int64) (int, error) {
	raw, ok := fields[name]
	if !ok {
		if required {
			return 0, handshakeError(name, "The Engine.IO handshake has no "+name+".")
		}
		return 0, nil
	}
	var n json.Number
	// json.Number also accepts a quoted number; the handshake has bare ones.
	if len(raw) == 0 || raw[0] == '"' || json.Unmarshal(raw, &n) != nil {
		return 0, handshakeError(name, "The Engine.IO handshake's "+name+" is not a number.")
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil || v < lo || v > hi {
		return 0, handshakeError(name, "The Engine.IO handshake's "+name+" is not an integer from "+
			strconv.FormatInt(lo, 10)+" to "+strconv.FormatInt(hi, 10)+".")
	}
	return int(v), nil
}

// Packet returns the open packet carrying h, with the fields in the order
// the reference server writes them.
func (h Handshake) Packet() (Packet, error) {
	if h.SID == "" {
		return Packet{}, invalidArgument("The handshake needs a session ID.")
	}
	if h.PingInterval <= 0 || h.PingTimeout <= 0 || h.PingInterval > maxMillis || h.PingTimeout > maxMillis || h.MaxPayload < 0 {
		return Packet{}, invalidArgument("The handshake's ping interval and timeout must be positive and the max payload not negative.")
	}
	if h.Upgrades == nil {
		h.Upgrades = []string{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(h); err != nil {
		return Packet{}, invalidArgument("The handshake cannot be encoded as JSON.")
	}
	return Packet{Type: Open, Data: bytes.TrimSuffix(buf.Bytes(), []byte("\n"))}, nil
}
