package socketio

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// maxDepth bounds the nesting of values given to Deconstruct, which also
// stops cycles (a map that contains itself).
const maxDepth = 1000

// placeholder is written in the order socket.io-parser writes it.
type placeholder struct {
	Placeholder bool `json:"_placeholder"`
	Num         int  `json:"num"`
}

// Deconstruct encodes v as a JSON payload, replacing every []byte by a
// placeholder and collecting the bytes as attachments, in order (spec
// "Binary", socket.io-parser's deconstructPacket). Binary values are found
// in nil, []any and map[string]any values at any depth; json.RawMessage is
// copied as it is; other values are encoded by encoding/json, where []byte
// would become Base64, not an attachment. Map keys are sorted. HTML
// characters are not escaped, like JSON.stringify.
func Deconstruct(v any) (json.RawMessage, [][]byte, error) {
	var atts [][]byte
	tree, err := deconstruct(v, &atts, 0)
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil, nil, invalidArgument("The value cannot be encoded as JSON: " + err.Error())
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), atts, nil
}

func deconstruct(v any, atts *[][]byte, depth int) (any, error) {
	if depth > maxDepth {
		return nil, invalidArgument("The value is nested more than " + strconv.Itoa(maxDepth) + " levels deep.")
	}
	switch x := v.(type) {
	case []byte:
		*atts = append(*atts, x)
		return placeholder{Placeholder: true, Num: len(*atts) - 1}, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := deconstruct(e, atts, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			r, err := deconstruct(e, atts, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	}
	return v, nil
}

// withPayload fills p.Data and p.Attachments from v, switching an Event or
// Ack to its binary type when v holds binary values, as socket.io-parser's
// Encoder does.
func withPayload(p Packet, v any) (Packet, error) {
	data, atts, err := Deconstruct(v)
	if err != nil {
		return Packet{}, err
	}
	p.Data = data
	if len(atts) > 0 {
		p.Attachments = atts
		switch p.Type {
		case Event:
			p.Type = BinaryEvent
		case Ack:
			p.Type = BinaryAck
		}
	}
	if _, _, err := Encode(p); err != nil {
		return Packet{}, err
	}
	return p, nil
}

// NewEvent returns an EVENT (or BINARY_EVENT when args hold []byte values)
// for the event name with its arguments. Set ID and HasID to ask for an
// acknowledgement.
func NewEvent(namespace, name string, args ...any) (Packet, error) {
	return withPayload(Packet{Type: Event, Namespace: namespace}, append([]any{name}, args...))
}

// NewAck returns the ACK (or BINARY_ACK) answering the event with ack ID id.
func NewAck(namespace string, id uint64, args ...any) (Packet, error) {
	if args == nil {
		args = []any{}
	}
	return withPayload(Packet{Type: Ack, Namespace: namespace, ID: id, HasID: true}, args)
}

// NewConnect returns the CONNECT packet for a namespace, with the auth
// payload when auth is not nil (it must encode as a JSON object).
func NewConnect(namespace string, auth any) (Packet, error) {
	p := Packet{Type: Connect, Namespace: namespace}
	if auth == nil {
		if _, _, err := Encode(p); err != nil {
			return Packet{}, err
		}
		return p, nil
	}
	return withPayload(p, auth)
}

// NewDisconnect returns the DISCONNECT packet for a namespace.
func NewDisconnect(namespace string) (Packet, error) {
	p := Packet{Type: Disconnect, Namespace: namespace}
	if _, _, err := Encode(p); err != nil {
		return Packet{}, err
	}
	return p, nil
}
