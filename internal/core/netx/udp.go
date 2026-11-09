package netx

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/listen"
)

// UDP through the dialer (UDP-01, SOCKS-07). A "udp", "udp4" or "udp6"
// dial returns a DatagramConn: each Write sends one datagram to the dialed
// address, each Read returns one datagram, and ReadFrom also says who sent
// it. Direct returns a connected socket (*net.UDPConn), or with
// UnconnectedUDP an unconnected one; SOCKS5 relays through the proxy with
// UDP ASSOCIATE (socks5udp.go). SOCKS4 and HTTP proxies refuse UDP. See
// docs/udp.md.

// Largest UDP payloads: 65,535 bytes minus the IP and UDP headers.
const (
	MaxUDPPayloadIPv4 = 65507
	MaxUDPPayloadIPv6 = 65527
)

// udpSocketBuffer is the send and receive buffer Direct asks for on UDP
// sockets. macOS limits a datagram to the send buffer, 9,216 bytes by
// default; with this, datagrams up to the IP limit can be sent.
const udpSocketBuffer = 256 << 10

// DatagramConn is the connection a UDP dial returns. Write sends one
// datagram to the dialed address (through a proxy, to its relay with the
// SOCKS header). Read and ReadFrom return one datagram, cut to the buffer
// when it is longer, so pass a buffer of MaxDatagramPayload bytes at least;
// ReadFrom returns the datagram's sender: a *net.UDPAddr, or a host:port
// address when a SOCKS proxy reports a host name. *net.UDPConn is a
// DatagramConn.
type DatagramConn interface {
	net.Conn
	ReadFrom(p []byte) (n int, addr net.Addr, err error)
}

var _ DatagramConn = (*net.UDPConn)(nil)

// IsUDP reports whether network is "udp", "udp4" or "udp6".
func IsUDP(network string) bool {
	return network == "udp" || network == "udp4" || network == "udp6"
}

type unconnectedKey struct{}

// UnconnectedUDP marks ctx so that UDP dials return an unconnected socket:
// it sends to the dialed address, and receives datagrams from any address
// (a server that answers from another port or address, as TFTP does). A
// connected socket, the default, receives only from the dialed address and
// reports an ICMP "port unreachable" as netx.connection_refused on the next
// read. Through a SOCKS5 proxy the socket goes to the relay either way;
// connected, datagrams the relay reports from another sender than the
// target are dropped (when the target is an IP address or was resolved
// here).
func UnconnectedUDP(ctx context.Context) context.Context {
	return context.WithValue(ctx, unconnectedKey{}, true)
}

// connectedUDP returns ctx without the UnconnectedUDP mark, for the dial
// to a proxy's relay, which is always connected.
func connectedUDP(ctx context.Context) context.Context {
	if !IsUnconnectedUDP(ctx) {
		return ctx
	}
	return context.WithValue(ctx, unconnectedKey{}, false)
}

// IsUnconnectedUDP reports whether ctx carries UnconnectedUDP.
func IsUnconnectedUDP(ctx context.Context) bool {
	v, _ := ctx.Value(unconnectedKey{}).(bool)
	return v
}

// MaxDatagramPayload returns the largest datagram conn can send in one
// Write: 65,507 bytes to an IPv4 address, 65,527 to an IPv6 one, less the
// SOCKS header through a proxy. It looks through wrappers with an
// Unwrap() net.Conn method. 0 when conn is not a UDP connection.
func MaxDatagramPayload(conn net.Conn) int {
	for range 16 {
		switch c := conn.(type) {
		case interface{ MaxPayload() int }:
			return c.MaxPayload()
		case *net.UDPConn:
			return maxPayloadFor(c.RemoteAddr())
		case interface{ Unwrap() net.Conn }:
			conn = c.Unwrap()
		default:
			return 0
		}
	}
	return 0
}

// maxPayloadFor is the IP limit for a datagram to addr.
func maxPayloadFor(addr net.Addr) int {
	if a, ok := addr.(*net.UDPAddr); ok {
		if ip, ok := netip.AddrFromSlice(a.IP); ok && !ip.Unmap().Is4() {
			return MaxUDPPayloadIPv6
		}
	}
	return MaxUDPPayloadIPv4
}

// IsPortUnreachable reports whether err, from a read or write of a
// connected UDP socket, is the ICMP "port unreachable" that a datagram it
// sent got back: ECONNREFUSED, or WSAECONNRESET on Windows.
func IsPortUnreachable(err error) bool { return isErrno(err, portUnreachableErrnos) }

// IsConnectedUDP reports whether conn receives only from the address it
// was dialed to: false for an UnconnectedUDP dial. It looks through
// wrappers with an Unwrap() net.Conn method.
func IsConnectedUDP(conn net.Conn) bool {
	for range 16 {
		switch c := conn.(type) {
		case interface{ ConnectedUDP() bool }:
			return c.ConnectedUDP()
		case interface{ Unwrap() net.Conn }:
			conn = c.Unwrap()
		default:
			return true
		}
	}
	return true
}

// setUDPBuffers raises a UDP socket's buffers, best effort.
func setUDPBuffers(conn net.Conn) {
	type buffers interface {
		SetReadBuffer(int) error
		SetWriteBuffer(int) error
	}
	if b, ok := conn.(buffers); ok {
		_ = b.SetWriteBuffer(udpSocketBuffer)
		_ = b.SetReadBuffer(udpSocketBuffer)
	}
}

// unconnect replaces the connected UDP socket conn, which Direct dialed
// (so the target was resolved and the guard checked it), by an
// unconnected one bound to the same local IP address on a port the OS
// picks, that sends to conn's remote address.
func unconnect(ctx context.Context, conn net.Conn, network, address string) (net.Conn, error) {
	local, _ := conn.LocalAddr().(*net.UDPAddr)
	remote, _ := conn.RemoteAddr().(*net.UDPAddr)
	_ = conn.Close()
	if local == nil || remote == nil {
		return nil, withAddr(errs.New(errs.Internal, "The UDP socket has no addresses.").WithCode(CodeDialFailed), network, address)
	}
	family := "udp6"
	if ip, ok := netip.AddrFromSlice(remote.IP); ok && ip.Unmap().Is4() {
		family = "udp4"
	}
	host := local.IP.String()
	if local.Zone != "" {
		host += "%" + local.Zone
	}
	// Bound to one address, so only peers that can reach it can send;
	// loopback stays loopback. It is a client socket, not a listener, so
	// the LAN warning listen gives does not apply.
	pc, _, err := listen.ListenPacket(ctx, listen.Spec{Network: family, Host: host, AllowLAN: true})
	if err != nil {
		return nil, withAddr(errs.Wrap(err, errs.Network, "Could not open a UDP socket to send to "+address+".").
			WithCode(CodeDialFailed), network, address)
	}
	if c, ok := pc.(net.Conn); ok {
		setUDPBuffers(c)
	}
	return &unconnectedUDP{pc: pc, remote: remote}, nil
}

// unconnectedUDP is an unconnected UDP socket that sends to one address.
type unconnectedUDP struct {
	pc     net.PacketConn
	remote *net.UDPAddr
}

var _ DatagramConn = (*unconnectedUDP)(nil)

func (u *unconnectedUDP) Read(p []byte) (int, error) {
	n, _, err := u.pc.ReadFrom(p)
	return n, err
}

func (u *unconnectedUDP) ReadFrom(p []byte) (int, net.Addr, error) { return u.pc.ReadFrom(p) }
func (u *unconnectedUDP) Write(p []byte) (int, error)              { return u.pc.WriteTo(p, u.remote) }
func (u *unconnectedUDP) Close() error                             { return u.pc.Close() }
func (u *unconnectedUDP) LocalAddr() net.Addr                      { return u.pc.LocalAddr() }
func (u *unconnectedUDP) RemoteAddr() net.Addr                     { return u.remote }
func (u *unconnectedUDP) SetDeadline(t time.Time) error            { return u.pc.SetDeadline(t) }
func (u *unconnectedUDP) SetReadDeadline(t time.Time) error        { return u.pc.SetReadDeadline(t) }
func (u *unconnectedUDP) SetWriteDeadline(t time.Time) error       { return u.pc.SetWriteDeadline(t) }

// MaxPayload is the IP limit for the remote address's family.
func (u *unconnectedUDP) MaxPayload() int { return maxPayloadFor(u.remote) }

// ConnectedUDP is false: datagrams from any address are read.
func (u *unconnectedUDP) ConnectedUDP() bool { return false }

// HostPortAddr is a net.Addr of a host name and port, as a SOCKS5 relay
// may report the sender of a datagram.
type HostPortAddr struct {
	Net      string
	HostPort string
}

func (a HostPortAddr) Network() string { return a.Net }
func (a HostPortAddr) String() string  { return a.HostPort }

// Codes of datagrams a UDP connection through a SOCKS5 proxy drops; see
// IsDroppedDatagram.
const (
	// CodeSOCKSUDPFragment: the relay sent a fragment (FRAG other than 0),
	// which PostSonde does not reassemble (RFC 1928 section 7 allows
	// that). Details: size, frag.
	CodeSOCKSUDPFragment = "netx.socks_udp_fragment_dropped"
	// CodeSOCKSUDPMalformed: the relay sent a datagram without a valid
	// SOCKS UDP header. Details: size.
	CodeSOCKSUDPMalformed = "netx.socks_udp_malformed"
)

// IsDroppedDatagram reports whether err, from a read of a UDP connection,
// is a datagram that was dropped (CodeSOCKSUDPFragment,
// CodeSOCKSUDPMalformed) rather than a failure: the connection stays open
// and the next read goes on.
func IsDroppedDatagram(err error) bool {
	e, ok := errors.AsType[*errs.Error](err)
	return ok && (e.Code == CodeSOCKSUDPFragment || e.Code == CodeSOCKSUDPMalformed)
}
