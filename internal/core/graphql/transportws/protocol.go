package transportws

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// Subprotocol is the WebSocket subprotocol of the protocol.
const Subprotocol = "graphql-transport-ws"

// LegacySubprotocol is the WebSocket subprotocol of the legacy
// subscriptions-transport-ws protocol (GQL-31).
const LegacySubprotocol = "graphql-ws"

// Protocol is the wire protocol a Session speaks.
type Protocol string

// Protocols.
const (
	// ProtocolTransportWS is graphql-transport-ws, the protocol of the
	// graphql-ws project (the default).
	ProtocolTransportWS Protocol = "graphql-transport-ws"
	// ProtocolLegacy is Apollo's subscriptions-transport-ws, subprotocol
	// graphql-ws: start/data/stop instead of subscribe/next/complete, ka
	// keep-alives from the server, connection_error and
	// connection_terminate, no ping/pong.
	ProtocolLegacy Protocol = "subscriptions-transport-ws"
)

// legacy reports whether p is the legacy protocol.
func (p Protocol) legacy() bool { return p == ProtocolLegacy }

// Subprotocol is the WebSocket subprotocol of p.
func (p Protocol) Subprotocol() string {
	if p.legacy() {
		return LegacySubprotocol
	}
	return Subprotocol
}

// subscribeType and stopType are the messages that start and stop an
// operation.
func (p Protocol) subscribeType() MessageType {
	if p.legacy() {
		return TypeStart
	}
	return TypeSubscribe
}

func (p Protocol) stopType() MessageType {
	if p.legacy() {
		return TypeStop
	}
	return TypeComplete
}

// MessageType is the "type" of a protocol message.
type MessageType string

// Message types of graphql-transport-ws.
const (
	TypeConnectionInit MessageType = "connection_init" // client → server
	TypeConnectionAck  MessageType = "connection_ack"  // server → client
	TypePing           MessageType = "ping"            // both ways
	TypePong           MessageType = "pong"            // both ways
	TypeSubscribe      MessageType = "subscribe"       // client → server
	TypeNext           MessageType = "next"            // server → client
	TypeError          MessageType = "error"           // server → client
	TypeComplete       MessageType = "complete"        // both ways
)

// Message types of subscriptions-transport-ws besides connection_init,
// connection_ack, error (server → client, payload an error object or
// array) and complete (server → client only).
const (
	TypeStart               MessageType = "start"                // client → server
	TypeStop                MessageType = "stop"                 // client → server
	TypeData                MessageType = "data"                 // server → client
	TypeKeepAlive           MessageType = "ka"                   // server → client
	TypeConnectionError     MessageType = "connection_error"     // server → client
	TypeConnectionTerminate MessageType = "connection_terminate" // client → server
)

// Close codes of the protocol. The 44xx codes and 4500 are sent by servers
// (4400 and 4408 by this client too); 4004, 4005, 4499 and 4504 are used
// by the reference client and may show up when testing servers against it.
const (
	CloseBadRequest               wsframe.StatusCode = 4400
	CloseUnauthorized             wsframe.StatusCode = 4401
	CloseForbidden                wsframe.StatusCode = 4403
	CloseSubprotocolNotAcceptable wsframe.StatusCode = 4406
	CloseConnectionInitTimeout    wsframe.StatusCode = 4408
	CloseSubscriberAlreadyExists  wsframe.StatusCode = 4409
	CloseTooManyInitRequests      wsframe.StatusCode = 4429
	CloseInternalServerError      wsframe.StatusCode = 4500
	CloseBadResponse              wsframe.StatusCode = 4004
	CloseInternalClientError      wsframe.StatusCode = 4005
	CloseTerminated               wsframe.StatusCode = 4499
	CloseConnectionAckTimeout     wsframe.StatusCode = 4504
)

// CloseCodeName returns what a close code means in this protocol, the
// WebSocket name of a 1xxx code, or "" if the code has no known meaning.
func CloseCodeName(code wsframe.StatusCode) string {
	switch code {
	case CloseBadRequest:
		return "invalid message"
	case CloseUnauthorized:
		return "unauthorized"
	case CloseForbidden:
		return "forbidden"
	case CloseSubprotocolNotAcceptable:
		return "subprotocol not acceptable"
	case CloseConnectionInitTimeout:
		return "connection initialisation timeout"
	case CloseSubscriberAlreadyExists:
		return "subscriber already exists"
	case CloseTooManyInitRequests:
		return "too many initialisation requests"
	case CloseInternalServerError:
		return "internal server error"
	case CloseBadResponse:
		return "bad response"
	case CloseInternalClientError:
		return "internal client error"
	case CloseTerminated:
		return "terminated"
	case CloseConnectionAckTimeout:
		return "connection acknowledgement timeout"
	}
	s := code.String()
	if i := strings.IndexByte(s, '('); i >= 0 {
		return s[i+1 : len(s)-1]
	}
	return ""
}

// closeCodeText is "4401 (unauthorized)" or "4123".
func closeCodeText(code wsframe.StatusCode) string {
	if n := CloseCodeName(code); n != "" {
		return strconv.Itoa(int(code)) + " (" + n + ")"
	}
	return strconv.Itoa(int(code))
}

// outMessage is a message this client sends.
type outMessage struct {
	Type    MessageType     `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type subscribePayload struct {
	Query         string          `json:"query"`
	OperationName string          `json:"operationName,omitempty"`
	Variables     json.RawMessage `json:"variables,omitempty"`
	Extensions    json.RawMessage `json:"extensions,omitempty"`
}

// encode marshals v without HTML escaping, so logs show < > & as typed.
func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// inMessage is a message received from the server, checked by parse.
type inMessage struct {
	Type    MessageType
	ID      string
	Payload json.RawMessage // nil when absent
}

// parse decodes and checks a message from the server of protocol p. On
// failure it returns a short description of what is wrong.
func parse(data []byte, p Protocol) (inMessage, string) {
	if p.legacy() {
		return parseLegacy(data)
	}
	return parseTransportWS(data)
}

// decodeFields decodes the members of a message and its type.
func decodeFields(data []byte) (map[string]json.RawMessage, string, string) {
	trimmed := bytes.TrimSpace(data)
	if !isObject(trimmed) {
		return nil, "", "the message is not a JSON object"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, "", "the message is not valid JSON"
	}
	var typ string
	rawType, ok := fields["type"]
	if !ok {
		return nil, "", "the message has no type"
	}
	if !isString(rawType) || json.Unmarshal(rawType, &typ) != nil {
		return nil, "", "the message type is not a string"
	}
	return fields, typ, ""
}

// stringID returns the message's "id" if it is a non-empty string.
func stringID(fields map[string]json.RawMessage) (string, bool) {
	var id string
	rawID, ok := fields["id"]
	if !ok || !isString(rawID) || json.Unmarshal(rawID, &id) != nil || id == "" {
		return "", false
	}
	return id, true
}

// parseLegacy checks a subscriptions-transport-ws message. The reference
// server sends ids as strings; error may come without an id (a message it
// could not read) and with an error object or an array of them.
func parseLegacy(data []byte) (inMessage, string) {
	var m inMessage
	fields, typ, problem := decodeFields(data)
	if problem != "" {
		return m, problem
	}
	m.Type = MessageType(typ)
	if p, ok := fields["payload"]; ok {
		m.Payload = p
	}
	switch m.Type {
	case TypeConnectionAck:
		if m.Payload != nil && !isObjectOrNull(m.Payload) {
			return m, "the connection_ack payload is not an object or null"
		}
		return m, ""
	case TypeKeepAlive, TypeConnectionError:
		return m, ""
	case TypeData:
		if !isObject(m.Payload) {
			return m, "the data payload is not an object"
		}
	case TypeError:
		if !isErrorObject(m.Payload) && !areGraphQLErrors(m.Payload) {
			return m, "the error payload is not a GraphQL error or a non-empty array of them"
		}
		m.ID, _ = stringID(fields)
		return m, ""
	case TypeComplete:
	case TypeConnectionInit, TypeStart, TypeStop, TypeConnectionTerminate:
		return m, "the server sent " + typ + ", which only clients send"
	default:
		return m, "unknown message type " + strconv.Quote(typ)
	}
	id, ok := stringID(fields)
	if !ok {
		return m, "the " + typ + " message has no id"
	}
	m.ID = id
	return m, ""
}

// isErrorObject reports whether raw is one error object with a message.
func isErrorObject(raw json.RawMessage) bool {
	var e map[string]json.RawMessage
	if !isObject(raw) || json.Unmarshal(raw, &e) != nil {
		return false
	}
	_, ok := e["message"]
	return ok
}

// parseTransportWS checks a graphql-transport-ws message.
func parseTransportWS(data []byte) (inMessage, string) {
	var m inMessage
	trimmed := bytes.TrimSpace(data)
	if !isObject(trimmed) {
		return m, "the message is not a JSON object"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return m, "the message is not valid JSON"
	}
	var typ string
	rawType, ok := fields["type"]
	if !ok {
		return m, "the message has no type"
	}
	if !isString(rawType) || json.Unmarshal(rawType, &typ) != nil {
		return m, "the message type is not a string"
	}
	m.Type = MessageType(typ)
	if p, ok := fields["payload"]; ok {
		m.Payload = p
	}
	needID := false
	switch m.Type {
	case TypeConnectionAck, TypePing, TypePong:
		if m.Payload != nil && !isObjectOrNull(m.Payload) {
			return m, "the " + typ + " payload is not an object or null"
		}
	case TypeNext:
		needID = true
		if !isObject(m.Payload) {
			return m, "the next payload is not an object"
		}
	case TypeError:
		needID = true
		if !areGraphQLErrors(m.Payload) {
			return m, "the error payload is not a non-empty array of GraphQL errors"
		}
	case TypeComplete:
		needID = true
	case TypeConnectionInit, TypeSubscribe:
		return m, "the server sent " + typ + ", which only clients send"
	default:
		return m, "unknown message type " + strconv.Quote(typ)
	}
	if needID {
		var id string
		rawID, ok := fields["id"]
		if !ok || !isString(rawID) || json.Unmarshal(rawID, &id) != nil || id == "" {
			return m, "the " + typ + " message has no id"
		}
		m.ID = id
	}
	return m, ""
}

func isString(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '"'
}

func isObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{'
}

func isObjectOrNull(raw []byte) bool {
	return isObject(raw) || string(bytes.TrimSpace(raw)) == "null"
}

// checkObject validates an optional JSON object argument: empty means
// absent; otherwise it must be a JSON object or null.
func checkObject(raw json.RawMessage) (json.RawMessage, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return nil, true
	}
	if !json.Valid(t) || !isObjectOrNull(t) {
		return nil, false
	}
	return t, true
}

// GraphQLError is the part of a GraphQL error the client decodes. Raw is
// the whole error object as received.
type GraphQLError struct {
	Message    string          `json:"message"`
	Locations  []Location      `json:"locations,omitempty"`
	Path       json.RawMessage `json:"path,omitempty"`
	Extensions json.RawMessage `json:"extensions,omitempty"`
	Raw        json.RawMessage `json:"-"`
}

// Location is a line and column in the operation document.
type Location struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// areGraphQLErrors reports whether raw is a non-empty array of objects
// with a message, as the reference implementation checks.
func areGraphQLErrors(raw json.RawMessage) bool {
	var list []map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &list) != nil || len(list) == 0 {
		return false
	}
	for _, e := range list {
		if _, ok := e["message"]; !ok {
			return false
		}
	}
	return true
}

// decodeErrors decodes an array of GraphQL errors leniently: an entry that
// does not decode keeps only Raw. One error object (the legacy protocol's
// error payload) counts as an array of it.
func decodeErrors(raw json.RawMessage) []GraphQLError {
	var list []json.RawMessage
	if isObject(raw) {
		list = []json.RawMessage{bytes.TrimSpace(raw)}
	} else if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	out := make([]GraphQLError, 0, len(list))
	for _, item := range list {
		var e GraphQLError
		if json.Unmarshal(item, &e) != nil {
			e = GraphQLError{}
		}
		e.Raw = item
		out = append(out, e)
	}
	return out
}

// DecodeErrors decodes a list of GraphQL errors (or one error object)
// leniently, for the errors of other transports.
func DecodeErrors(raw json.RawMessage) []GraphQLError { return decodeErrors(raw) }

// ResultErrors returns the errors of an ExecutionResult, nil if it has
// none (absent, null or empty).
func ResultErrors(payload json.RawMessage) []GraphQLError { return resultErrors(payload) }

// resultErrors returns the errors of an ExecutionResult, nil if it has
// none (absent, null or empty).
func resultErrors(payload json.RawMessage) []GraphQLError {
	var r struct {
		Errors json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(payload, &r) != nil || len(r.Errors) == 0 || isObject(r.Errors) {
		return nil
	}
	if list := decodeErrors(r.Errors); len(list) > 0 {
		return list
	}
	return nil
}
