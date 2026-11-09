package websocket

import "github.com/wailsapp/wails/v3/pkg/application"

// Domain names the WebSocket service and prefixes its events.
const Domain = "ws"

// Event names. They are constants so RegisterEvent below is visible to the
// bindings generator. Both carry connId and requestId; subscribers filter
// by connId. seq counts the events of one connection (state and messages
// together) from 1, so the UI can detect a missed one.
const (
	// EventState is sent on every state change of a connection:
	// connecting, open (with the handshake), closing, closed (with the
	// close info, and the handshake when connecting failed).
	EventState = Domain + ":state"
	// EventMessage carries the messages sent and received since the last
	// event, in order, at most every 50 ms per connection.
	EventMessage = Domain + ":message"
)

// Registering the events types their payloads in the generated bindings
// and makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[StateEventDTO](EventState)
	application.RegisterEvent[MessageEventDTO](EventMessage)
}
