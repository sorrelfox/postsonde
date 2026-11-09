// Package wsframe implements RFC 6455 WebSocket framing for the WebSocket
// client, the WebSocket mock server and GraphQL subscriptions.
//
// It has two layers:
//
//   - Frames: Header, Frame, ReadFrame, AppendFrame and WriteFrame encode and
//     decode single frames and keep every wire detail (FIN, RSV bits,
//     opcode, mask flag and key, the length encoding) for a frame
//     inspector. AppendFrame and WriteFrame send a frame exactly as
//     described, so a test can craft frames a peer should reject.
//   - Messages: Conn runs over an io.ReadWriter (usually a net.Conn after the
//     HTTP upgrade) in the client or server role. It checks masking per
//     role, reassembles fragmented messages with control frames in between,
//     enforces frame and message size limits, validates UTF-8 of text
//     messages incrementally, answers pings and runs the close handshake.
//     Protocol violations fail the connection with the matching close code
//     (1002, 1007, 1009) and an *errs.Error.
//
// The package never dials and does not do the HTTP upgrade handshake beyond
// ComputeAccept. Extensions are not implemented: RSV bits must be zero unless
// Limits.AllowedRSV allows them, which is the hook for permessage-deflate
// (RFC 7692) later. See docs/websocket.md.
package wsframe
