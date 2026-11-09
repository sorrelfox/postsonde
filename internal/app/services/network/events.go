package network

import "github.com/wailsapp/wails/v3/pkg/application"

// Domain names the network activity events.
const Domain = "network"

// EventActivity carries the connections that changed since the last one
// and the number active now, throttled: at most one event per
// FlushInterval, and none while nothing changes and nothing is open. It is
// a constant so RegisterEvent below is visible to the bindings generator.
const EventActivity = Domain + ":activity"

// ActivityEventDTO is the payload of EventActivity.
type ActivityEventDTO struct {
	// Active is how many connections are connecting or open now.
	Active int `json:"active"`
	// Conns are the connections that changed state since the last event,
	// and every active one (for its byte counts), oldest first. The UI
	// replaces what it has by id.
	Conns []ConnDTO `json:"conns"`
}

// Registering the event types its payload in the generated bindings and
// makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[ActivityEventDTO](EventActivity)
}
