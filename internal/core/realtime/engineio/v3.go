package engineio

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Engine.IO protocol 3, spoken by engine.io 3.x (socket.io v2) and by
// socket.io v3/v4 servers with allowEIO3. The packet types and the text
// form of a packet are those of protocol 4; what differs is how binary
// data and long-polling payloads are written (engine.io-parser 2.x):
//
//   - WebSocket: a binary message starts with the type byte 4, then the
//     data.
//   - Long-polling, text payload: every packet as "<length>:<packet>",
//     the length in UTF-16 code units (JavaScript's String.length); a
//     binary packet as "b4" and standard Base64.
//   - Long-polling, binary payload (XHR2, when the client did not ask for
//     b64=1): every packet as a marker byte (0 text, 1 binary), its length
//     as decimal digits of one byte each (0-9), the byte 255, then the
//     packet (a binary one with its type byte first).

// ProtocolV3 is the EIO query value of Engine.IO protocol 3.
const ProtocolV3 = 3

// EncodePacketV3 encodes p as one WebSocket message of protocol 3: a text
// packet as for protocol 4, a binary message as the type byte 4 and the
// data.
func EncodePacketV3(p Packet) (data []byte, binary bool, err error) {
	if err := p.validate(); err != nil {
		return nil, false, err
	}
	if p.Binary {
		out := make([]byte, 0, 1+len(p.Data))
		out = append(out, byte(Message))
		return append(out, p.Data...), true, nil
	}
	return appendText(make([]byte, 0, 1+len(p.Data)), p), false, nil
}

// DecodePacketV3 decodes one WebSocket message of protocol 3.
func DecodePacketV3(data []byte, binary bool, opts Options) (Packet, error) {
	if limit := opts.maxSize(); limit >= 0 && len(data) > limit {
		return Packet{}, tooBig(limit)
	}
	if binary {
		if len(data) == 0 {
			return Packet{}, protocolError(CodeEmptyPacket, "The Engine.IO binary message is empty.")
		}
		if data[0] != byte(Message) {
			return Packet{}, protocolError(CodeUnknownType, "The Engine.IO 3 binary message starts with the type "+strconv.Itoa(int(data[0]))+", not 4 (message).")
		}
		return BinaryMessage(bytes.Clone(data[1:])), nil
	}
	p, err := decodeTextV3(data)
	if err != nil {
		return Packet{}, err
	}
	return p, nil
}

// decodeTextV3 decodes a text packet; "b4" and Base64 is a binary message.
func decodeTextV3(data []byte) (Packet, *errs.Error) {
	if len(data) > 0 && data[0] == 'b' {
		if len(data) < 2 || data[1] != '0'+byte(Message) {
			return Packet{}, protocolError(CodeUnknownType, "An Engine.IO 3 Base64 packet must be a message (b4).")
		}
		return decodeText(append([]byte{'b'}, data[2:]...))
	}
	return decodeText(data)
}

// textV3 is the text form of a packet in a protocol 3 payload.
func textV3(p Packet) string {
	if p.Binary {
		return "b4" + base64.StdEncoding.EncodeToString(p.Data)
	}
	return string(appendText(nil, p))
}

// utf16Len is the length of s in UTF-16 code units.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// EncodePayloadV3 encodes packets as one protocol 3 long-polling body in
// the text form: "<length>:<packet>" each, binary messages as "b4" and
// Base64.
func EncodePayloadV3(packets []Packet) ([]byte, error) {
	if len(packets) == 0 {
		return nil, invalidArgument("A long-polling payload needs at least one packet.")
	}
	var out []byte
	for i, p := range packets {
		if err := p.validate(); err != nil {
			return nil, err.WithDetail(DetailIndex, strconv.Itoa(i))
		}
		s := textV3(p)
		out = strconv.AppendInt(out, int64(utf16Len(s)), 10)
		out = append(out, ':')
		out = append(out, s...)
	}
	return out, nil
}

// EncodeBinaryPayloadV3 encodes packets as one protocol 3 long-polling
// body in the binary (XHR2) form.
func EncodeBinaryPayloadV3(packets []Packet) ([]byte, error) {
	if len(packets) == 0 {
		return nil, invalidArgument("A long-polling payload needs at least one packet.")
	}
	var out []byte
	for i, p := range packets {
		if err := p.validate(); err != nil {
			return nil, err.WithDetail(DetailIndex, strconv.Itoa(i))
		}
		var body []byte
		if p.Binary {
			out = append(out, 1)
			body = append([]byte{byte(Message)}, p.Data...)
		} else {
			out = append(out, 0)
			body = appendText(nil, p)
		}
		for _, d := range strconv.Itoa(len(body)) {
			out = append(out, byte(d-'0'))
		}
		out = append(out, 0xff)
		out = append(out, body...)
	}
	return out, nil
}

// DecodePayloadV3 decodes a protocol 3 long-polling body, in the text form
// or, when it starts with the byte 0 or 1, the binary (XHR2) form.
func DecodePayloadV3(body []byte, opts Options) ([]Packet, error) {
	if limit := opts.maxSize(); limit >= 0 && len(body) > limit {
		return nil, tooBig(limit)
	}
	if len(body) == 0 {
		return nil, protocolError(CodeEmptyPacket, "The long-polling payload is empty.")
	}
	if body[0] == 0 || body[0] == 1 {
		return decodeBinaryPayloadV3(body)
	}
	if !utf8.Valid(body) {
		return nil, protocolError(CodeInvalidUTF8, "The Engine.IO 3 long-polling payload is not valid UTF-8.")
	}
	s := string(body)
	var packets []Packet
	for off := 0; off < len(s); {
		colon := off
		for colon < len(s) && s[colon] >= '0' && s[colon] <= '9' {
			colon++
		}
		if colon == off || colon == len(s) || s[colon] != ':' || colon-off > 10 {
			return nil, payloadError(len(packets), off, "The Engine.IO 3 payload has no packet length (\"<length>:\") here.")
		}
		n, _ := strconv.Atoi(s[off:colon])
		// Take n UTF-16 code units.
		end := colon + 1
		for units := 0; units < n; {
			if end >= len(s) {
				return nil, payloadError(len(packets), off, "The Engine.IO 3 payload ends inside a packet.")
			}
			r, size := utf8.DecodeRuneInString(s[end:])
			units += utf16.RuneLen(r)
			end += size
		}
		p, err := decodeTextV3([]byte(s[colon+1 : end]))
		if err != nil {
			return nil, err.WithDetail(DetailIndex, strconv.Itoa(len(packets))).WithDetail(DetailOffset, strconv.Itoa(off))
		}
		packets = append(packets, p)
		off = end
	}
	return packets, nil
}

func decodeBinaryPayloadV3(body []byte) ([]Packet, error) {
	var packets []Packet
	for off := 0; off < len(body); {
		kind := body[off]
		if kind > 1 {
			return nil, payloadError(len(packets), off, "The Engine.IO 3 binary payload has an unknown packet marker.")
		}
		i := off + 1
		n := 0
		for ; i < len(body) && body[i] != 0xff; i++ {
			if body[i] > 9 || i-off > 10 {
				return nil, payloadError(len(packets), off, "The Engine.IO 3 binary payload has a malformed packet length.")
			}
			n = n*10 + int(body[i])
		}
		if i == off+1 || i >= len(body) || i+1+n > len(body) {
			return nil, payloadError(len(packets), off, "The Engine.IO 3 binary payload ends inside a packet.")
		}
		data := body[i+1 : i+1+n]
		var p Packet
		var err *errs.Error
		if kind == 1 {
			if len(data) == 0 || data[0] != byte(Message) {
				return nil, payloadError(len(packets), off, "An Engine.IO 3 binary packet must be a message.")
			}
			p = BinaryMessage(bytes.Clone(data[1:]))
		} else if p, err = decodeText(data); err != nil {
			return nil, err.WithDetail(DetailIndex, strconv.Itoa(len(packets))).WithDetail(DetailOffset, strconv.Itoa(off))
		}
		packets = append(packets, p)
		off = i + 1 + n
	}
	return packets, nil
}

func payloadError(index, offset int, msg string) *errs.Error {
	return protocolError(CodeInvalidPayload, msg).
		WithDetail(DetailIndex, strconv.Itoa(index)).WithDetail(DetailOffset, strconv.Itoa(offset))
}

// LooksLikeV3Payload reports whether a long-polling body is in the
// protocol 3 text form ("<length>:<packet>…"), which a protocol 4 decoder
// refuses: the answer of an Engine.IO 3 server (socket.io v2).
func LooksLikeV3Payload(body []byte) bool {
	i := 0
	for i < len(body) && i < 11 && body[i] >= '0' && body[i] <= '9' {
		i++
	}
	return i > 0 && i < len(body) && body[i] == ':'
}
