// Package udpclient is PostSonde's UDP client (UDP-01): it opens a UDP
// socket to host:port through a netx.Dialer, directly or through a SOCKS5
// proxy's UDP ASSOCIATE relay (SOCKS-07), sends datagrams and reports the
// datagrams it receives with their sender.
//
// UDP has no connection: "open" means the socket is ready (the name is
// resolved, and through a proxy the association is made), and nothing
// tells whether anyone listens. A Conn goes through the states opening,
// open, closing and closed, reported in order through Options.OnState; the
// peer never closes it. Connected (Options.Connected) receives only from
// the target and reports an ICMP "port unreachable" as a note; unconnected
// (the default) receives from any sender. A datagram the proxy relay sends
// fragmented or malformed is dropped with a note. See docs/udp.md.
package udpclient
