package tcp

import "github.com/wailsapp/wails/v3/pkg/application"

// Domain names the TCP service and prefixes its events.
const Domain = "tcp"

// Event names. They are constants so RegisterEvent below is visible to the
// bindings generator. Both carry connId and requestId; subscribers filter
// by connId. seq counts the events of one connection (state and data
// together) from 1, so the UI can detect a missed one.
const (
	// EventState is sent on every state change of a connection:
	// connecting, open, closing, closed.
	EventState = Domain + ":state"
	// EventData carries the chunks sent and received since the last event,
	// in order, at most every 50 ms per connection.
	EventData = Domain + ":data"
)

// Registering the events types their payloads in the generated bindings
// and makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[StateEventDTO](EventState)
	application.RegisterEvent[DataEventDTO](EventData)
}
