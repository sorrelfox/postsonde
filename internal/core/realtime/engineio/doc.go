// Package engineio encodes and decodes Engine.IO protocol v4 packets, the
// transport layer under Socket.IO v3 and v4
// (https://socket.io/docs/v4/engine-io-protocol/, repository
// socketio/engine.io-protocol, file README.md for v4).
//
// It is a pure codec: no connections, no timers. The Socket.IO client
// (SIO-01) drives the handshake, heartbeat and upgrade with it.
//
//   - Packet types (spec "Protocol > Packet encoding"): 0 open, 1 close,
//     2 ping, 3 pong, 4 message, 5 upgrade, 6 noop. A text packet is the
//     type digit followed by its data ("4hello", "2probe").
//   - WebSocket (spec "WebSocket > Packet encoding"): one packet per
//     WebSocket message. A text packet goes in a text message; a binary
//     message packet is sent as the raw bytes in a binary message, with no
//     type prefix. EncodePacket and DecodePacket.
//   - HTTP long-polling (spec "HTTP long-polling > Packet encoding"): a
//     request or response body carries one or more packets separated by the
//     record separator "\x1e"; a binary message packet is written as "b"
//     followed by its standard Base64 ("bAQIDBA=="). EncodePayload and
//     DecodePayload.
//   - The open packet's data is the handshake JSON (spec "Handshake"):
//     ParseHandshake and Handshake.Packet.
//
// Decoding is strict: an empty packet, an unknown type, invalid UTF-8 in a
// text packet, invalid Base64, data on a close, upgrade or noop packet, an
// empty record in a payload and input over Options.MaxSize are errors with
// the codes in errors.go, kind Protocol. Encoding rejects packets that cannot
// be written (kind InvalidArgument). See docs/socketio.md.
package engineio
