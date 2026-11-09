// Package socketio encodes and decodes Socket.IO protocol v5 packets, the
// protocol of Socket.IO v3 and v4 servers
// (https://socket.io/docs/v4/socket-io-protocol/, repository
// socketio/socket.io-protocol, file README.md for v5). Socket.IO packets
// travel as Engine.IO message packets (package engineio).
//
// The text form of a packet (spec "Packet encoding") is
//
//	<type>[<attachments>-][<namespace>,][<ack id>][<JSON payload>]
//
// with the types 0 CONNECT, 1 DISCONNECT, 2 EVENT, 3 ACK, 4 CONNECT_ERROR,
// 5 BINARY_EVENT and 6 BINARY_ACK. The namespace is written only when it is
// not the main namespace "/". A BINARY_EVENT or BINARY_ACK is followed by
// its attachments, each in its own binary Engine.IO message, and its
// payload holds a placeholder {"_placeholder":true,"num":<index>} where each
// binary value was (spec "Binary").
//
// Packet keeps the JSON payload exactly as it was on the wire, so decoding
// and encoding again gives the same bytes. Packet.Value rebuilds the value
// with the attachments in place of the placeholders; NewEvent and NewAck
// build packets from Go values, replacing []byte with placeholders.
//
// Decoder is fed the Engine.IO messages of one direction in order and
// returns each packet once its attachments have arrived. Decoding is
// strict: besides what socket.io-parser checks (types, payload shapes per
// type, reserved event names, placeholder indexes) it rejects leading zeros
// in counts and ack IDs, ack IDs on CONNECT, DISCONNECT and CONNECT_ERROR,
// ACKs without an ID, missing EVENT and ACK payloads, ack IDs above 2^53-1
// and input over the limits in Options. Errors carry the codes in errors.go.
// See docs/socketio.md.
package socketio
