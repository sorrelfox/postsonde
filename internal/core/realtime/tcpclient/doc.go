// Package tcpclient is PostSonde's raw TCP client (TCP-01): it connects to
// host:port through a netx.Dialer, optionally over TLS (TCP-02), sends
// bytes and reports the bytes it receives.
//
// Without a framing, a chunk is whatever one read returned, so one message
// of the peer may arrive as several chunks and several messages as one.
// With a framing (Options.Framing, TCP-03: newline-delimited or
// length-prefixed, package framing) the received bytes are split into
// messages, whole however they were read, and Send frames each message.
//
// A Conn goes through the states connecting, open, closing and closed,
// reported in order through Options.OnState. CloseInfo says who ended the
// connection (this side, the peer, or nobody when it failed), the error if
// it failed, and the bytes and chunks sent and received. See docs/tcp.md.
package tcpclient
