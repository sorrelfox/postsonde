// Package sioclient is PostSonde's Socket.IO v4 client (SIO-01, SIO-03,
// SIO-04, SIO-05, SIO-06, SIO-07): it runs the Engine.IO v4 handshake over
// WebSocket (wsclient) or HTTP long-polling, with the fallback from one to
// the other or the upgrade from long-polling to WebSocket, answers the
// server's heartbeat, connects to one namespace with an auth payload,
// emits events with JSON and binary arguments, tracks their
// acknowledgements with a timeout and reports everything it sends and
// receives as Traces. Packets are encoded with the engineio and socketio
// codecs.
//
// A Client is one connection attempt: New validates the options, Connect
// connects, Disconnect or the server ends it, and it never reconnects.
// See docs/socketio.md.
package sioclient
