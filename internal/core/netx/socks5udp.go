package netx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// UDP through a SOCKS5 proxy (SOCKS-07, RFC 1928 section 7). A UDP dial
// opens the TCP control connection to the proxy, authenticates, and sends
// UDP ASSOCIATE; the proxy answers with the address of its UDP relay. The
// datagrams then go to the relay, each with a header naming the target:
//
//	+-----+------+------+----------+----------+----------+
//	| RSV | FRAG | ATYP | DST.ADDR | DST.PORT |   DATA   |
//	+-----+------+------+----------+----------+----------+
//	|  2  |  1   |  1   | Variable |    2     | Variable |
//
// and the relay sends replies back with the sender in the same header. The
// association lasts as long as the control connection, which is kept open
// until the connection is closed. FRAG is always 0 on what is sent;
// received fragments (FRAG other than 0) are dropped, as the RFC allows
// for clients that do not reassemble.

// Command names of a ProxyHop.
const CommandUDPAssociate = "UDP ASSOCIATE"

// associate runs a UDP dial through the proxy.
func (s *SOCKS5) associate(ctx context.Context, network, address string, hop *ProxyHop) (net.Conn, error) {
	hop.Command = CommandUDPAssociate
	at := s.now()
	dst, err := s.target(ctx, network, address)
	if dst.looked {
		hop.Lookup = s.now().Sub(at)
	}
	if err != nil {
		return nil, err
	}
	at = s.now()
	ctrl, err := s.forward.DialContext(ctx, "tcp", s.cfg.Address)
	hop.Connect = s.now().Sub(at)
	if err != nil {
		return nil, s.proxyDialError(err, network, address)
	}
	hop.ProxyAddr = ctrl.RemoteAddr().String()
	at = s.now()
	var relay string
	err = s.handshake(ctx, ctrl, network, address, hop, func(h *socksHandshake) error {
		var err error
		relay, err = h.udpAssociate()
		return err
	})
	hop.Handshake = s.now().Sub(at)
	if err != nil {
		_ = ctrl.Close()
		return nil, err
	}
	hop.RelayAddr = relay
	// The relay is reached like the proxy, through the forward dialer,
	// always connected: everything comes from the relay.
	udp, err := s.forward.DialContext(connectedUDP(ctx), "udp", relay)
	if err != nil {
		_ = ctrl.Close()
		kind := errs.KindOf(err)
		if kind == errs.Cancelled || kind == errs.PermissionDenied {
			return nil, err
		}
		if kind != errs.Timeout {
			kind = errs.Network
		}
		return nil, withAddr(errs.Wrap(err, kind, "Could not reach the UDP relay "+relay+" of the SOCKS proxy at "+s.cfg.Address+".").
			WithCode(CodeSOCKSUDPRelayUnreachable).
			WithHint(HintSOCKSUDPRelay).
			WithDetail("relay", relay), network, address)
	}
	c := &socksUDPConn{
		ctrl:      ctrl,
		udp:       udp,
		header:    append([]byte{0, 0, 0}, dst.bytes()...),
		via:       s.via,
		proxy:     s.cfg.Address,
		network:   network,
		address:   address,
		connected: !IsUnconnectedUDP(ctx),
		ended:     make(chan struct{}),
	}
	if c.connected && dst.ip.IsValid() {
		c.filter = netip.AddrPortFrom(dst.ip, dst.port)
	}
	go c.watch()
	return c, nil
}

// udpAssociate sends UDP ASSOCIATE and returns the relay's host:port. The
// request's address is all zeros: the client does not know yet where it
// will send from (RFC 1928 section 7). An unspecified BND.ADDR (0.0.0.0 or
// ::) means the proxy's own address, as most servers mean it.
func (h *socksHandshake) udpAssociate() (string, error) {
	h.stepDesc = "UDP ASSOCIATE"
	if err := h.write([]byte{socksVersion, socksCmdUDP, 0x00, socksAtypIPv4, 0, 0, 0, 0, 0, 0}); err != nil {
		return "", err
	}
	var head [4]byte
	if err := h.read(head[:]); err != nil {
		return "", err
	}
	if head[0] != socksVersion {
		return "", h.protocolError("The SOCKS proxy at "+h.s.cfg.Address+" sent a malformed UDP ASSOCIATE reply.", nil)
	}
	h.hop.Replied, h.hop.Reply, h.hop.ReplyText = true, int(head[1]), socks5ReplyText(head[1])
	if rep := head[1]; rep != SOCKSReplySucceeded {
		return "", h.replyError(rep, socksAddr{})
	}
	var host string
	switch head[3] {
	case socksAtypIPv4, socksAtypIPv6:
		b := make([]byte, 4)
		if head[3] == socksAtypIPv6 {
			b = make([]byte, 16)
		}
		if err := h.read(b); err != nil {
			return "", err
		}
		ip, _ := netip.AddrFromSlice(b)
		if ip.IsUnspecified() {
			if ap, err := netip.ParseAddrPort(h.conn.RemoteAddr().String()); err == nil {
				ip = ap.Addr()
			}
		}
		host = ip.Unmap().String()
	case socksAtypName:
		var l [1]byte
		if err := h.read(l[:]); err != nil {
			return "", err
		}
		b := make([]byte, l[0])
		if err := h.read(b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		return "", h.protocolError("The SOCKS proxy at "+h.s.cfg.Address+" sent a malformed UDP ASSOCIATE reply.", nil)
	}
	var port [2]byte
	if err := h.read(port[:]); err != nil {
		return "", err
	}
	p := int(port[0])<<8 | int(port[1])
	if p == 0 || host == "" {
		return "", h.protocolError("The SOCKS proxy at "+h.s.cfg.Address+" named no UDP relay address.", nil)
	}
	return net.JoinHostPort(host, strconv.Itoa(p)), nil
}

// socksUDPConn is a UDP connection through a SOCKS5 relay.
type socksUDPConn struct {
	ctrl      net.Conn // the TCP control connection; its end ends the association
	udp       net.Conn // connected to the relay
	header    []byte   // RSV, FRAG 0, ATYP, DST.ADDR, DST.PORT of the target
	via       string
	proxy     string
	network   string
	address   string
	connected bool
	// filter is the target's address in connected mode, when known: a
	// datagram from another sender is dropped.
	filter netip.AddrPort

	writeMu sync.Mutex
	wbuf    []byte
	readMu  sync.Mutex
	rbuf    []byte

	closed  atomic.Bool
	ctrlEnd atomic.Bool // the control connection ended before Close
	ended   chan struct{}
	once    sync.Once
}

var _ DatagramConn = (*socksUDPConn)(nil)

// watch waits for the control connection to end. The proxy sends nothing
// on it after the reply, so anything read is discarded. When it ends
// before Close, the association is over: the relay socket is closed so a
// blocked read returns.
func (c *socksUDPConn) watch() {
	defer close(c.ended)
	_, _ = io.Copy(io.Discard, c.ctrl)
	if !c.closed.Load() {
		c.ctrlEnd.Store(true)
		_ = c.udp.Close()
	}
}

// Write sends p to the target as one datagram, through the relay.
func (c *socksUDPConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.ctrlEnd.Load() {
		return 0, c.endedError()
	}
	c.wbuf = append(append(c.wbuf[:0], c.header...), p...)
	if _, err := c.udp.Write(c.wbuf); err != nil {
		if c.ctrlEnd.Load() {
			return 0, c.endedError()
		}
		return 0, err
	}
	return len(p), nil
}

func (c *socksUDPConn) Read(p []byte) (int, error) {
	n, _, err := c.ReadFrom(p)
	return n, err
}

// ReadFrom returns the next datagram from the relay, without its header,
// and its sender as the relay reports it. A fragment or a datagram without
// a valid header is dropped: ReadFrom returns an error for which
// IsDroppedDatagram is true, and the connection stays open.
func (c *socksUDPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.rbuf == nil {
		c.rbuf = make([]byte, 64<<10+512)
	}
	for {
		n, err := c.udp.Read(c.rbuf)
		if err != nil {
			if c.ctrlEnd.Load() {
				return 0, nil, c.endedError()
			}
			return 0, nil, err
		}
		payload, from, derr := c.parse(c.rbuf[:n])
		if derr != nil {
			return 0, nil, derr
		}
		if c.filter.IsValid() {
			if a, ok := from.(*net.UDPAddr); ok && a.AddrPort() != c.filter {
				continue // connected: only the target's datagrams
			}
		}
		return copy(p, payload), from, nil
	}
}

// parse splits a datagram from the relay into its payload and sender.
func (c *socksUDPConn) parse(b []byte) ([]byte, net.Addr, error) {
	malformed := func() error {
		return withAddr(errs.New(errs.Protocol, "A datagram from the SOCKS relay had no valid SOCKS UDP header and was dropped.").
			WithCode(CodeSOCKSUDPMalformed).
			WithDetail("size", strconv.Itoa(len(b))).
			WithDetail("via", c.via), c.network, c.address)
	}
	if len(b) < 4 {
		return nil, nil, malformed()
	}
	if b[2] != 0 {
		return nil, nil, withAddr(errs.New(errs.Protocol, "A fragmented datagram from the SOCKS relay was dropped: PostSonde does not reassemble SOCKS UDP fragments.").
			WithCode(CodeSOCKSUDPFragment).
			WithDetail("size", strconv.Itoa(len(b))).
			WithDetail("frag", strconv.Itoa(int(b[2]))).
			WithDetail("via", c.via), c.network, c.address)
	}
	var from net.Addr
	rest := b[4:]
	switch b[3] {
	case socksAtypIPv4, socksAtypIPv6:
		size := 4
		if b[3] == socksAtypIPv6 {
			size = 16
		}
		if len(rest) < size+2 {
			return nil, nil, malformed()
		}
		ip, _ := netip.AddrFromSlice(rest[:size])
		port := uint16(rest[size])<<8 | uint16(rest[size+1])
		from = net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip.Unmap(), port))
		rest = rest[size+2:]
	case socksAtypName:
		if len(rest) < 1 || len(rest) < 1+int(rest[0])+2 {
			return nil, nil, malformed()
		}
		l := int(rest[0])
		name := string(rest[1 : 1+l])
		port := int(rest[1+l])<<8 | int(rest[2+l])
		from = HostPortAddr{Net: "udp", HostPort: net.JoinHostPort(name, strconv.Itoa(port))}
		rest = rest[3+l:]
	default:
		return nil, nil, malformed()
	}
	return rest, from, nil
}

func (c *socksUDPConn) endedError() error {
	return withAddr(errs.New(errs.Network, "The SOCKS proxy at "+c.proxy+" ended the UDP association.").
		WithCode(CodeSOCKSUDPAssociationClosed).
		WithHint(HintSOCKSUDPEnded).
		WithDetail("via", c.via), c.network, c.address)
}

// Close ends the association: it closes the control connection and the
// relay socket, and returns once the watcher has stopped.
func (c *socksUDPConn) Close() error {
	var err error
	c.once.Do(func() {
		c.closed.Store(true)
		err = errors.Join(c.udp.Close(), c.ctrl.Close())
	})
	<-c.ended
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	return err
}

func (c *socksUDPConn) LocalAddr() net.Addr                { return c.udp.LocalAddr() }
func (c *socksUDPConn) RemoteAddr() net.Addr               { return c.udp.RemoteAddr() }
func (c *socksUDPConn) SetDeadline(t time.Time) error      { return c.udp.SetDeadline(t) }
func (c *socksUDPConn) SetReadDeadline(t time.Time) error  { return c.udp.SetReadDeadline(t) }
func (c *socksUDPConn) SetWriteDeadline(t time.Time) error { return c.udp.SetWriteDeadline(t) }

// Via names the proxy, as for a CONNECT tunnel.
func (c *socksUDPConn) Via() string { return c.via }

// RelayAddr is the relay's host:port the datagrams go to.
func (c *socksUDPConn) RelayAddr() string { return c.udp.RemoteAddr().String() }

// MaxPayload is the largest datagram to the relay, less the header.
func (c *socksUDPConn) MaxPayload() int {
	m := MaxDatagramPayload(c.udp)
	if m == 0 {
		m = maxPayloadFor(c.udp.RemoteAddr())
	}
	return m - len(c.header)
}

// ConnectedUDP reports whether only the target's datagrams are read.
func (c *socksUDPConn) ConnectedUDP() bool { return c.connected }
