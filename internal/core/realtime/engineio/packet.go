package engineio

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Protocol is the Engine.IO protocol version, sent as the EIO query
// parameter (spec "Transports > HTTP long-polling > Request path").
const Protocol = 4

// PacketType is an Engine.IO packet type (spec "Protocol > Packet
// encoding").
type PacketType byte

// Packet types.
const (
	// Open: sent by the server once the transport is open; the data is
	// the handshake JSON.
	Open PacketType = 0
	// Close: the transport can be closed.
	Close PacketType = 1
	// Ping: the heartbeat (sent by the server in v4) and, with the data
	// "probe", the upgrade probe sent by the client.
	Ping PacketType = 2
	// Pong: the answer to a ping, with the same data.
	Pong PacketType = 3
	// Message: application data, text or binary.
	Message PacketType = 4
	// Upgrade: sent by the client to finish the upgrade to WebSocket.
	Upgrade PacketType = 5
	// Noop: sent by the server to end a pending poll during an upgrade.
	Noop PacketType = 6
)

var typeNames = [...]string{"open", "close", "ping", "pong", "message", "upgrade", "noop"}

// Valid reports whether t is one of the seven packet types.
func (t PacketType) Valid() bool { return int(t) < len(typeNames) }

// String returns the spec's name of the type ("open", "message", ...).
func (t PacketType) String() string {
	if t.Valid() {
		return typeNames[t]
	}
	return "PacketType(" + strconv.Itoa(int(t)) + ")"
}

// Packet is one Engine.IO packet.
type Packet struct {
	Type PacketType
	// Data is the packet's data: the text after the type digit for a text
	// packet, the raw bytes for a binary message.
	Data []byte
	// Binary marks a binary message packet. Only Message packets can be
	// binary.
	Binary bool
}

// Text returns a text packet of type t with data s.
func Text(t PacketType, s string) Packet { return Packet{Type: t, Data: []byte(s)} }

// BinaryMessage returns a binary message packet.
func BinaryMessage(b []byte) Packet { return Packet{Type: Message, Data: b, Binary: true} }

// DefaultMaxSize is the default Options.MaxSize: 16 MiB.
const DefaultMaxSize = 16 << 20

// Options configures decoding.
type Options struct {
	// MaxSize limits a WebSocket message (DecodePacket) and a long-polling
	// body (DecodePayload), in bytes. 0 means DefaultMaxSize; negative means
	// no limit.
	MaxSize int
}

func (o Options) maxSize() int {
	if o.MaxSize == 0 {
		return DefaultMaxSize
	}
	return o.MaxSize
}

// validate checks a packet for encoding.
func (p Packet) validate() *errs.Error {
	if !p.Type.Valid() {
		return invalidArgument("The Engine.IO packet type " + strconv.Itoa(int(p.Type)) + " does not exist.")
	}
	if p.Binary {
		if p.Type != Message {
			return invalidArgument("Only message packets can carry binary data.")
		}
		return nil
	}
	if !utf8.Valid(p.Data) {
		return invalidArgument("The data of an Engine.IO text packet must be valid UTF-8.")
	}
	if len(p.Data) > 0 && noData(p.Type) {
		return invalidArgument("A " + p.Type.String() + " packet cannot carry data.")
	}
	return nil
}

func noData(t PacketType) bool { return t == Close || t == Upgrade || t == Noop }

func appendText(dst []byte, p Packet) []byte {
	dst = append(dst, '0'+byte(p.Type))
	return append(dst, p.Data...)
}

// EncodePacket encodes p as one WebSocket message (spec "WebSocket > Packet
// encoding"): a text packet as its type digit and data, sent as a text
// message (binary false); a binary message as its raw bytes, sent as a
// binary message (binary true).
func EncodePacket(p Packet) (data []byte, binary bool, err error) {
	if err := p.validate(); err != nil {
		return nil, false, err
	}
	if p.Binary {
		return bytes.Clone(p.Data), true, nil
	}
	return appendText(make([]byte, 0, 1+len(p.Data)), p), false, nil
}

// DecodePacket decodes one WebSocket message: binary tells whether it came
// in a binary message. A binary message is a binary message packet; a text
// message is a text packet. The returned data does not alias data.
func DecodePacket(data []byte, binary bool, opts Options) (Packet, error) {
	if limit := opts.maxSize(); limit >= 0 && len(data) > limit {
		return Packet{}, tooBig(limit)
	}
	if binary {
		return BinaryMessage(bytes.Clone(data)), nil
	}
	p, err := decodeText(data)
	if err != nil {
		return Packet{}, err
	}
	return p, nil
}

// decodeText decodes a text packet, including the "b" Base64 form that
// engine.io-parser accepts on every transport.
func decodeText(data []byte) (Packet, *errs.Error) {
	if len(data) == 0 {
		return Packet{}, protocolError(CodeEmptyPacket, "The Engine.IO packet is empty.")
	}
	c := data[0]
	if c == 'b' {
		enc := data[1:]
		if bytes.ContainsAny(enc, "\r\n") {
			// encoding/base64 skips line breaks; the spec's Base64 has none.
			return Packet{}, protocolError(CodeInvalidBase64, "The Base64 data of an Engine.IO binary packet contains a line break.")
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(string(enc))
		if err != nil {
			return Packet{}, errs.Wrap(err, errs.Protocol, "The Base64 data of an Engine.IO binary packet is invalid.").
				WithCode(CodeInvalidBase64)
		}
		return BinaryMessage(raw), nil
	}
	if c < '0' || c > '6' {
		return Packet{}, protocolError(CodeUnknownType, "The Engine.IO packet type "+strconv.Quote(string(rune(c)))+" does not exist.").
			WithHint("Engine.IO v4 packets start with a digit 0 to 6. The server may speak another protocol version.")
	}
	p := Packet{Type: PacketType(c - '0'), Data: bytes.Clone(data[1:])}
	if !utf8.Valid(p.Data) {
		return Packet{}, protocolError(CodeInvalidUTF8, "The Engine.IO text packet is not valid UTF-8.")
	}
	if len(p.Data) > 0 && noData(p.Type) {
		return Packet{}, protocolError(CodeUnexpectedData, "The Engine.IO "+p.Type.String()+" packet carries data.")
	}
	return p, nil
}

// Separator is the record separator between packets of a long-polling
// payload.
const Separator = '\x1e'

// EncodePayload encodes packets as one HTTP long-polling body (spec
// "HTTP long-polling > Packet encoding"): each packet in its text form,
// binary messages as "b" and standard Base64, joined by Separator. Text data
// cannot contain Separator.
func EncodePayload(packets []Packet) ([]byte, error) {
	if len(packets) == 0 {
		return nil, invalidArgument("A long-polling payload needs at least one packet.")
	}
	var out []byte
	for i, p := range packets {
		if err := p.validate(); err != nil {
			return nil, err.WithDetail(DetailIndex, strconv.Itoa(i))
		}
		if i > 0 {
			out = append(out, Separator)
		}
		if p.Binary {
			out = append(out, 'b')
			out = base64.StdEncoding.AppendEncode(out, p.Data)
			continue
		}
		if bytes.IndexByte(p.Data, Separator) >= 0 {
			return nil, invalidArgument("Text data sent over long-polling cannot contain the record separator \\x1e.").
				WithDetail(DetailIndex, strconv.Itoa(i))
		}
		out = appendText(out, p)
	}
	return out, nil
}

// DecodePayload decodes an HTTP long-polling body into its packets. Errors
// carry the index and byte offset of the bad packet.
func DecodePayload(body []byte, opts Options) ([]Packet, error) {
	if limit := opts.maxSize(); limit >= 0 && len(body) > limit {
		return nil, tooBig(limit)
	}
	if len(body) == 0 {
		return nil, protocolError(CodeEmptyPacket, "The long-polling payload is empty.")
	}
	var packets []Packet
	offset := 0
	for i := 0; ; i++ {
		rec := body[offset:]
		end := bytes.IndexByte(rec, Separator)
		if end >= 0 {
			rec = rec[:end]
		}
		p, err := decodeText(rec)
		if err != nil {
			return nil, err.WithDetail(DetailIndex, strconv.Itoa(i)).WithDetail(DetailOffset, strconv.Itoa(offset))
		}
		packets = append(packets, p)
		if end < 0 {
			return packets, nil
		}
		offset += end + 1
	}
}
