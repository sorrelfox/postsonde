// Package transportws runs GraphQL operations over WebSocket with the
// graphql-transport-ws protocol (GQL-30), the protocol of the graphql-ws
// project, on top of the WebSocket client in realtime/wsclient.
//
// Dial connects with the "graphql-transport-ws" subprotocol (the server
// must select it), sends connection_init with the connection params and
// waits for connection_ack in the background, closing with 4408 when it
// does not arrive in time. Session.Subscribe waits for the ack, sends
// subscribe with a new id and delivers next, error and complete messages
// on the Subscription's channel; Unsubscribe sends complete. Pings from
// the server are answered with pongs; the client can ping on an interval.
//
// With Options.Protocol ProtocolLegacy the same session speaks Apollo's
// legacy subscriptions-transport-ws protocol (GQL-31): subprotocol
// graphql-ws, start/data/stop instead of subscribe/next/complete, ka
// keep-alives from the server, connection_error, and connection_terminate
// before the client closes.
//
// Messages from the server that break the protocol close the connection
// with 4400. Every protocol message sent and received is reported to
// Options.OnEvent with a timestamp, for the event and message logs. See
// docs/graphql-subscriptions.md.
package transportws
