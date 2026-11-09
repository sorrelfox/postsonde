// Package wsclient is PostSonde's WebSocket client (WS-01, WS-08): it
// connects to ws:// and wss:// URLs through a netx.Dialer, runs the HTTP
// upgrade handshake itself (headers as typed, subprotocols, cookies from a
// jar), and then exchanges messages with wsframe.
//
// A Client is one connection attempt with a state machine (idle,
// connecting, open, closing, closed) reported through Options.OnState.
// CloseInfo gives the server's close code and reason, the frames sent and
// received, who started closing and whether the close was clean; a
// connection that ends without a close frame reports 1006, which is never
// sent. Handshake holds the raw request and response, timing and TLS
// details, also for a rejected upgrade (status, headers, capped body).
//
// Extensions (permessage-deflate) and WebSocket over HTTP/2 are not
// supported. See docs/websocket.md.
package wsclient
