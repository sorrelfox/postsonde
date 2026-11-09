package graphql

import "github.com/sorrelfox/postsonde/internal/app/apperr"

// SubscribeInputDTO names the GraphQL request to run as a subscription.
type SubscribeInputDTO struct {
	RequestID string `json:"requestId"`
	// AllowUnresolved uses {{variables}} that no scope defines as written
	// instead of refusing (the "Subscribe anyway" answer of ENV-11).
	AllowUnresolved bool `json:"allowUnresolved,omitempty"`
}

// StopSubscriptionInputDTO names the subscription to stop.
type StopSubscriptionInputDTO struct {
	OpID string `json:"opId"`
}

// Kinds of SubscriptionEventDTO.
const (
	// EventKindConnecting: the WebSocket handshake or the SSE request
	// starts (URL, Transport, OperationType; Method for SSE).
	EventKindConnecting = "connecting"
	// EventKindOpen: the handshake succeeded (Subprotocol), or the SSE
	// response's head arrived (Status, ContentType).
	EventKindOpen = "open"
	// EventKindMessage: a protocol message was sent or received.
	EventKindMessage = "message"
	// EventKindResult: the merged result of a query or mutation (GQL-35),
	// after the server completed it (Payload, ErrorCount).
	EventKindResult = "result"
	// EventKindClosed: the connection ended (Close, Error).
	EventKindClosed = "closed"
)

// SubscriptionEventDTO is one entry of a subscription's event log
// (GQL-34): a protocol message sent or received, a change of the
// connection, or a query's result.
type SubscriptionEventDTO struct {
	// At is when it happened, RFC 3339 in UTC with fractional seconds.
	At string `json:"at"`
	// Kind is "connecting", "open", "message", "result" or "closed".
	Kind string `json:"kind"`
	// Direction is "sent" or "received" for a message, "" otherwise.
	Direction string `json:"direction"`
	// Type is the message's protocol type. graphql-transport-ws:
	// connection_init, connection_ack, ping, pong, subscribe, next, error
	// or complete. graphql-ws (legacy): connection_init, connection_ack,
	// connection_error, ka, start, data, error, complete, stop,
	// connection_terminate. SSE: request (sent), next, complete,
	// keep-alive (a comment), response (a body that is not an event
	// stream) or the name of an ignored event. "" for a received message
	// that could not be parsed, and for the other kinds.
	Type string `json:"type"`
	// ID is the message's operation id ("" for connection messages and
	// over SSE).
	ID string `json:"id"`
	// Payload is the message's payload as JSON text as on the wire ("" when
	// it has none): the ExecutionResult of next and data, the errors of
	// error, the connection params of connection_init, the body of an SSE
	// request; an SSE event's data or comment. For a message that could
	// not be parsed it is the whole message. For "result", the merged
	// result, indented.
	Payload string `json:"payload"`
	// Size is the message's size in bytes.
	Size int `json:"size"`
	// Binary is set for a binary message, which the protocol does not
	// allow.
	Binary bool `json:"binary"`
	// ErrorCount is the number of GraphQL errors of a next, data, error
	// or response message, or of a result.
	ErrorCount int `json:"errorCount"`
	// Note is "unknown_id" for a message for no running operation,
	// "ignored" for one received while the connection was closing after a
	// protocol violation (or an SSE event of another type), or
	// "not_event_stream" for an SSE response that is not a stream; ""
	// otherwise.
	Note string `json:"note"`
	// URL is the WebSocket or SSE URL, for connecting (without userinfo).
	URL string `json:"url"`
	// Method is the SSE request's method, for connecting.
	Method string `json:"method,omitempty"`
	// Transport is "graphql-transport-ws", "graphql-ws" or "sse", and
	// OperationType the operation's type ("" when unknown), for
	// connecting.
	Transport     string `json:"transport,omitempty"`
	OperationType string `json:"operationType,omitempty"`
	// Subprotocol is the subprotocol selected, for open over WebSocket.
	Subprotocol string `json:"subprotocol"`
	// Status and ContentType are the SSE response's, for open over SSE.
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	// Close describes how the WebSocket connection ended, for closed.
	Close *SubscriptionCloseDTO `json:"close"`
	// Error is why a received message broke the protocol, or why the
	// connection failed (closed); null otherwise.
	Error *apperr.ErrorDTO `json:"error"`
}

// SubscriptionCloseDTO is how the WebSocket connection of a subscription
// ended.
type SubscriptionCloseDTO struct {
	// Code and Reason are the server's close frame; 1005 when it had no
	// code, 1006 when none arrived.
	Code   int    `json:"code"`
	Reason string `json:"reason"`
	// CodeName is what the code means ("unauthorized" for 4401,
	// "normal closure" for 1000), "" when unknown.
	CodeName string `json:"codeName"`
	WasClean bool   `json:"wasClean"`
	// InitiatedBy is "local", "remote" or "none".
	InitiatedBy string `json:"initiatedBy"`
}
