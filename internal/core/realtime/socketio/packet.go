package socketio

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/engineio"
)

// Protocol is the Socket.IO protocol version spoken by Socket.IO v3 and v4.
const Protocol = 5

// PacketType is a Socket.IO packet type (spec "Packet format").
type PacketType byte

// Packet types.
const (
	// Connect: the client asks to join a namespace (payload: optional auth
	// object); the server answers with the socket ID ({"sid":"..."}).
	Connect PacketType = 0
	// Disconnect: leaving a namespace. No payload.
	Disconnect PacketType = 1
	// Event: an event, the payload is [name, ...args].
	Event PacketType = 2
	// Ack: the answer to an event that had an ack ID, the payload is
	// [...args].
	Ack PacketType = 3
	// ConnectError: the server refused the connection to a namespace
	// (payload: {"message": ..., "data": ...}).
	ConnectError PacketType = 4
	// BinaryEvent: an Event with binary attachments.
	BinaryEvent PacketType = 5
	// BinaryAck: an Ack with binary attachments.
	BinaryAck PacketType = 6
)

var typeNames = [...]string{"CONNECT", "DISCONNECT", "EVENT", "ACK", "CONNECT_ERROR", "BINARY_EVENT", "BINARY_ACK"}

// Valid reports whether t is one of the seven packet types.
func (t PacketType) Valid() bool { return int(t) < len(typeNames) }

// String returns the spec's name of the type ("CONNECT", "BINARY_EVENT", ...).
func (t PacketType) String() string {
	if t.Valid() {
		return typeNames[t]
	}
	return "PacketType(" + strconv.Itoa(int(t)) + ")"
}

// IsBinary reports whether packets of type t carry attachments.
func (t PacketType) IsBinary() bool { return t == BinaryEvent || t == BinaryAck }

// ackID reports whether t may carry an ack ID, and whether it must.
func (t PacketType) ackID() (allowed, required bool) {
	switch t {
	case Event, BinaryEvent:
		return true, false
	case Ack, BinaryAck:
		return true, true
	}
	return false, false
}

// MainNamespace is the default namespace, left out of the text form.
const MainNamespace = "/"

// MaxAckID is the largest ack ID: 2^53-1, the largest integer a JavaScript
// number holds exactly.
const MaxAckID = 1<<53 - 1

// Packet is one Socket.IO packet.
type Packet struct {
	Type PacketType
	// Namespace starts with "/"; "" means MainNamespace.
	Namespace string
	// ID is the ack ID when HasID is set.
	ID    uint64
	HasID bool
	// Data is the JSON payload as on the wire, with binary placeholders in
	// place; nil when the packet has none.
	Data json.RawMessage
	// Attachments are the binary attachments of a BinaryEvent or
	// BinaryAck, in placeholder index order.
	Attachments [][]byte
}

// Defaults for Options.
const (
	DefaultMaxPacketSize  = 16 << 20
	DefaultMaxAttachments = 256
)

// Options configures decoding. Attachments arrive as Engine.IO binary
// messages, limited by engineio.Options.MaxSize.
type Options struct {
	// MaxPacketSize limits the text form of a packet, in bytes. 0 means
	// DefaultMaxPacketSize; negative means no limit.
	MaxPacketSize int
	// MaxAttachments limits the attachments of one binary packet. 0 means
	// DefaultMaxAttachments; negative means no limit.
	MaxAttachments int
}

func (o Options) maxPacketSize() int {
	if o.MaxPacketSize == 0 {
		return DefaultMaxPacketSize
	}
	return o.MaxPacketSize
}

func (o Options) maxAttachments() int {
	if o.MaxAttachments == 0 {
		return DefaultMaxAttachments
	}
	return o.MaxAttachments
}

// reservedEvents are the event names socket.io-parser refuses in an EVENT
// (RESERVED_EVENTS): they have a meaning of their own on both sides.
var reservedEvents = []string{"connect", "connect_error", "disconnect", "disconnecting", "newListener", "removeListener"}

// IsReservedEvent reports whether name cannot be used as an event name.
func IsReservedEvent(name string) bool {
	for _, r := range reservedEvents {
		if name == r {
			return true
		}
	}
	return false
}

// Encode returns the text form of p and its attachments (spec "Packet
// encoding"). Send the text as one Engine.IO message packet and each
// attachment as a binary message packet after it (EncodeMessages does
// both).
func Encode(p Packet) (text string, attachments [][]byte, err error) {
	if !p.Type.Valid() {
		return "", nil, invalidArgument("The Socket.IO packet type " + strconv.Itoa(int(p.Type)) + " does not exist.")
	}
	nsp := p.Namespace
	if nsp == "" {
		nsp = MainNamespace
	}
	if !strings.HasPrefix(nsp, "/") || strings.ContainsRune(nsp, ',') {
		return "", nil, invalidArgument("A Socket.IO namespace must start with \"/\" and cannot contain \",\".").
			WithCode(CodeInvalidNamespace)
	}
	if !p.Type.IsBinary() && len(p.Attachments) > 0 {
		return "", nil, invalidArgument("Only BINARY_EVENT and BINARY_ACK packets carry attachments.")
	}
	allowed, required := p.Type.ackID()
	switch {
	case p.HasID && !allowed:
		return "", nil, invalidArgument("A " + p.Type.String() + " packet cannot have an ack ID.").WithCode(CodeInvalidAckID)
	case !p.HasID && required:
		return "", nil, invalidArgument("A " + p.Type.String() + " packet needs an ack ID.").WithCode(CodeInvalidAckID)
	case p.HasID && p.ID > MaxAckID:
		return "", nil, invalidArgument("The ack ID is larger than 2^53-1.").WithCode(CodeInvalidAckID)
	}
	if err := checkPayload(p.Type, p.Data, len(p.Attachments), 0); err != nil {
		return "", nil, &errs.Error{Kind: errs.InvalidArgument, Code: err.Code, Message: err.Message, Details: err.Details}
	}
	var b strings.Builder
	b.Grow(8 + len(nsp) + len(p.Data))
	b.WriteByte('0' + byte(p.Type))
	if p.Type.IsBinary() {
		b.WriteString(strconv.Itoa(len(p.Attachments)))
		b.WriteByte('-')
	}
	if nsp != MainNamespace {
		b.WriteString(nsp)
		b.WriteByte(',')
	}
	if p.HasID {
		b.WriteString(strconv.FormatUint(p.ID, 10))
	}
	b.Write(p.Data)
	return b.String(), p.Attachments, nil
}

// EncodeMessages encodes p as the Engine.IO message packets that carry it:
// a text message followed by one binary message per attachment.
func EncodeMessages(p Packet) ([]engineio.Packet, error) {
	text, atts, err := Encode(p)
	if err != nil {
		return nil, err
	}
	out := make([]engineio.Packet, 0, 1+len(atts))
	out = append(out, engineio.Text(engineio.Message, text))
	for _, a := range atts {
		out = append(out, engineio.BinaryMessage(a))
	}
	return out, nil
}

// parseText parses the text form of a packet. It returns the packet
// without attachments and how many attachments follow.
func parseText(s string, opts Options) (Packet, int, *errs.Error) {
	if limit := opts.maxPacketSize(); limit >= 0 && len(s) > limit {
		return Packet{}, 0, tooBig("packet", limit)
	}
	if s == "" {
		return Packet{}, 0, decodeError(CodeEmptyPacket, 0, "The Socket.IO packet is empty.")
	}
	if s[0] < '0' || s[0] > '6' {
		return Packet{}, 0, decodeError(CodeUnknownType, 0, "The Socket.IO packet type "+strconv.Quote(s[:1])+" does not exist.").
			WithHint("Socket.IO v3 and v4 packets start with a digit 0 to 6. The server may speak another protocol version.")
	}
	p := Packet{Type: PacketType(s[0] - '0'), Namespace: MainNamespace}
	i := 1
	count := 0
	if p.Type.IsBinary() {
		j := digits(s, i)
		if j == i || j == len(s) || s[j] != '-' || (j-i > 1 && s[i] == '0') {
			return Packet{}, 0, decodeError(CodeInvalidAttachments, i, "The attachment count of the "+p.Type.String()+" packet is missing or malformed.")
		}
		limit := opts.maxAttachments()
		n, err := strconv.Atoi(s[i:j])
		if err != nil || (limit >= 0 && n > limit) {
			return Packet{}, 0, decodeError(CodeTooManyAttachments, i, "The "+p.Type.String()+" packet has more attachments than the limit.").
				WithDetail(DetailLimit, strconv.Itoa(limit))
		}
		count = n
		i = j + 1
	}
	if i < len(s) && s[i] == '/' {
		// The namespace runs to the first ",", or to the end
		// (socket.io-parser accepts both).
		j := strings.IndexByte(s[i:], ',')
		if j < 0 {
			p.Namespace = s[i:]
			i = len(s)
		} else {
			p.Namespace = s[i : i+j]
			i += j + 1
		}
	}
	if j := digits(s, i); j > i {
		if (j-i > 1 && s[i] == '0') || j-i > 16 {
			return Packet{}, 0, decodeError(CodeInvalidAckID, i, "The ack ID of the Socket.IO packet is malformed.")
		}
		id, _ := strconv.ParseUint(s[i:j], 10, 64)
		if id > MaxAckID {
			return Packet{}, 0, decodeError(CodeInvalidAckID, i, "The ack ID of the Socket.IO packet is larger than 2^53-1.")
		}
		allowed, _ := p.Type.ackID()
		if !allowed {
			return Packet{}, 0, decodeError(CodeInvalidAckID, i, "A "+p.Type.String()+" packet cannot have an ack ID.")
		}
		p.ID, p.HasID = id, true
		i = j
	} else if _, required := p.Type.ackID(); required {
		return Packet{}, 0, decodeError(CodeInvalidAckID, i, "The "+p.Type.String()+" packet has no ack ID.")
	}
	if i < len(s) {
		p.Data = json.RawMessage(s[i:])
	}
	if err := checkPayload(p.Type, p.Data, count, i); err != nil {
		return Packet{}, 0, err
	}
	return p, count, nil
}

// digits returns the end of the run of ASCII digits starting at i.
func digits(s string, i int) int {
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

// parseJSON decodes one JSON value, numbers as json.Number, and fails on
// anything after it but white space.
func parseJSON(data []byte) (any, int, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		// The offset of the bad byte: a syntax error reports how much was
		// read including it; a truncated value fails at the end.
		off := len(data)
		if se, ok := errors.AsType[*json.SyntaxError](err); ok && se.Offset > 0 {
			off = int(se.Offset) - 1
		}
		return nil, off, err
	}
	end := int(dec.InputOffset())
	if _, err := dec.Token(); err != io.EOF {
		return nil, end, errors.New("data after the JSON value")
	}
	return v, 0, nil
}

// checkPayload validates the payload of a packet of type t with n
// attachments (socket.io-parser's isPayloadValid, plus placeholders).
// base is the payload's offset in the text form, for error details.
func checkPayload(t PacketType, data []byte, n int, base int) *errs.Error {
	if len(data) == 0 {
		switch t {
		case Connect, Disconnect:
			return nil
		}
		return decodeError(CodeInvalidPayload, base, "The "+t.String()+" packet has no payload.")
	}
	if t == Disconnect {
		return decodeError(CodeInvalidPayload, base, "A DISCONNECT packet cannot have a payload.")
	}
	v, off, err := parseJSON(data)
	if err != nil {
		return decodeError(CodeInvalidPayload, base+off, "The payload of the "+t.String()+" packet is not valid JSON.")
	}
	switch t {
	case Connect:
		if _, ok := v.(map[string]any); !ok {
			return decodeError(CodeInvalidPayload, base, "The payload of a CONNECT packet must be a JSON object.")
		}
	case ConnectError:
		switch v.(type) {
		case map[string]any, string:
		default:
			return decodeError(CodeInvalidPayload, base, "The payload of a CONNECT_ERROR packet must be a JSON object or string.")
		}
	case Event, BinaryEvent:
		arr, ok := v.([]any)
		if !ok || len(arr) == 0 {
			return decodeError(CodeInvalidPayload, base, "The payload of an event must be a JSON array starting with the event name.")
		}
		switch name := arr[0].(type) {
		case json.Number:
		case string:
			if IsReservedEvent(name) {
				return decodeError(CodeReservedEvent, base, "The event name "+strconv.Quote(name)+" is reserved.")
			}
		default:
			return decodeError(CodeInvalidPayload, base, "The event name must be a string or a number.")
		}
	case Ack, BinaryAck:
		if _, ok := v.([]any); !ok {
			return decodeError(CodeInvalidPayload, base, "The payload of an acknowledgement must be a JSON array.")
		}
	}
	if t.IsBinary() {
		if err := walkPlaceholders(v, n, nil); err != nil {
			return decodeError(CodeInvalidPlaceholder, base, err.Error())
		}
	}
	return nil
}

// placeholderIndex reports whether v is a placeholder object (one whose
// "_placeholder" is true) and returns its index, checked against n.
func placeholderIndex(m map[string]any, n int) (int, bool, error) {
	if b, ok := m["_placeholder"].(bool); !ok || !b {
		return 0, false, nil
	}
	num, ok := m["num"].(json.Number)
	if !ok {
		return 0, true, errors.New("A binary placeholder has no numeric \"num\".")
	}
	idx, err := strconv.Atoi(num.String())
	if err != nil || idx < 0 || idx >= n {
		return 0, true, errors.New("The binary placeholder " + num.String() + " does not refer to one of the " + strconv.Itoa(n) + " attachments.")
	}
	return idx, true, nil
}

// walkPlaceholders checks every placeholder in v; with atts it replaces
// them by the attachments and returns the new value.
func walkPlaceholders(v any, n int, atts [][]byte) error {
	_, err := reconstruct(v, n, atts)
	return err
}

func reconstruct(v any, n int, atts [][]byte) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		idx, ok, err := placeholderIndex(x, n)
		if err != nil {
			return nil, err
		}
		if ok {
			if atts == nil {
				return nil, nil
			}
			return atts[idx], nil
		}
		for k, e := range x {
			r, err := reconstruct(e, n, atts)
			if err != nil {
				return nil, err
			}
			if atts != nil {
				x[k] = r
			}
		}
	case []any:
		for i, e := range x {
			r, err := reconstruct(e, n, atts)
			if err != nil {
				return nil, err
			}
			if atts != nil {
				x[i] = r
			}
		}
	}
	return v, nil
}

// Value decodes the payload: objects as map[string]any, arrays as []any,
// numbers as json.Number, and in a BinaryEvent or BinaryAck every
// placeholder replaced by its attachment ([]byte). It is nil for a packet
// without payload.
func (p Packet) Value() (any, error) {
	if len(p.Data) == 0 {
		return nil, nil
	}
	v, _, err := parseJSON(p.Data)
	if err != nil {
		return nil, invalidArgument("The packet's payload is not valid JSON.").WithCode(CodeInvalidPayload)
	}
	if !p.Type.IsBinary() {
		return v, nil
	}
	atts := p.Attachments
	if atts == nil {
		atts = [][]byte{}
	}
	v, err = reconstruct(v, len(atts), atts)
	if err != nil {
		return nil, invalidArgument(err.Error()).WithCode(CodeInvalidPlaceholder)
	}
	return v, nil
}

// Args returns the payload array of an event or acknowledgement, with
// attachments in place. For an event the first element is the name.
func (p Packet) Args() ([]any, error) {
	v, err := p.Value()
	if err != nil {
		return nil, err
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, invalidArgument("The " + p.Type.String() + " packet's payload is not an array.").WithCode(CodeInvalidPayload)
	}
	return arr, nil
}

// EventName returns the name of an event: the first payload element when
// it is a string or a number, else "".
func (p Packet) EventName() string {
	if p.Type != Event && p.Type != BinaryEvent {
		return ""
	}
	arr, err := p.Args()
	if err != nil || len(arr) == 0 {
		return ""
	}
	switch x := arr[0].(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}
