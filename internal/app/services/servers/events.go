package servers

import "github.com/wailsapp/wails/v3/pkg/application"

// Domain names the servers service and prefixes its events.
const Domain = "servers"

// The service's events. They are constants so RegisterEvent below is
// visible to the bindings generator.
const (
	// EventChanged is sent after every change of a server: created, a
	// state transition (starting, running, stopping, stopped, failed),
	// removed. It carries every server.
	EventChanged = Domain + ":changed"
	// EventTraffic carries traffic events in batches (docs/events.md).
	EventTraffic = Domain + ":traffic"
	// EventClients carries the clients of one WebSocket server after they
	// changed (connected, disconnected, message counts), at most five
	// times a second per server.
	EventClients = Domain + ":clients"
	// EventConnections carries the open connections of one TCP server
	// after they changed (opened, closed, byte counts), at most five times
	// a second per server.
	EventConnections = Domain + ":connections"
	// EventCaptures carries the requests one webhook receiver keeps after
	// they changed (a request arrived, the traffic was cleared), at most
	// five times a second per server.
	EventCaptures = Domain + ":captures"
	// EventSubscribers carries the subscribers of one SSE server after
	// they changed (subscribed, left, event counts), at most five times a
	// second per server.
	EventSubscribers = Domain + ":subscribers"
	// EventPeers carries the recent peers of one UDP server after they
	// changed (a datagram in or out), at most five times a second per
	// server.
	EventPeers = Domain + ":peers"
	// EventSockets carries the sockets of one Socket.IO server after they
	// changed (a client joined a namespace, sent, got an event or left),
	// at most five times a second per server.
	EventSockets = Domain + ":sockets"
)

// Registering the events types their payloads in the generated bindings
// and makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[ChangedEventDTO](EventChanged)
	application.RegisterEvent[TrafficEventDTO](EventTraffic)
	application.RegisterEvent[ClientsEventDTO](EventClients)
	application.RegisterEvent[ConnectionsEventDTO](EventConnections)
	application.RegisterEvent[CapturesEventDTO](EventCaptures)
	application.RegisterEvent[SubscribersEventDTO](EventSubscribers)
	application.RegisterEvent[PeersEventDTO](EventPeers)
	application.RegisterEvent[SocketsEventDTO](EventSockets)
}
