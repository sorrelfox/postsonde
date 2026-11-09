package netx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// SOCKS5Config configures a SOCKS5 dialer (SOCKS-01, SOCKS-02, SOCKS-03).
type SOCKS5Config struct {
	// Address is the proxy's host:port.
	Address string
	// RemoteDNS sends host names to the proxy unresolved (socks5h), so the
	// proxy resolves them and nothing reaches the local resolver. When
	// false (socks5), host names are resolved locally with Resolver and the
	// proxy is sent an IP address.
	RemoteDNS bool
	// Username and Password are the RFC 1929 credentials. With a Username,
	// the dialer offers both "no authentication" and username/password and
	// the proxy picks; without one it offers "no authentication" only.
	// Each is at most 255 bytes; a Password needs a Username.
	Username string
	Password string
	// Timeout bounds the SOCKS handshake (greeting, authentication and
	// CONNECT), after the connection to the proxy is open; the forward
	// dialer bounds that connection itself. Zero means DefaultTimeout; a
	// negative value means no limit besides ctx.
	Timeout time.Duration
	// Resolver resolves host names when RemoteDNS is off; nil means
	// net.DefaultResolver.
	Resolver *net.Resolver
	// Now is the clock for DialDoneInfo.Duration; nil means time.Now.
	Now func() time.Time
}

// SOCKS5 dials targets through a SOCKS5 proxy: RFC 1928 CONNECT with IPv4,
// IPv6 and domain-name addresses, RFC 1929 username/password or no
// authentication. It reaches the proxy through the forward Dialer it was
// built with (normally Direct), so it sits below the guard and the
// connection log, which see the target as the protocol client asked for it
// (docs/network.md, SOCKS5). It is safe for concurrent use.
type SOCKS5 struct {
	cfg     SOCKS5Config
	forward Dialer
	via     string
	timeout time.Duration
	now     func() time.Time
}

var _ Dialer = (*SOCKS5)(nil)

// Codes of the SOCKS5 dialer's errors. See docs/network.md.
const (
	CodeSOCKSInvalidConfig           = "netx.socks_invalid_config"
	CodeSOCKSInvalidAddress          = "netx.socks_invalid_address"
	CodeSOCKSUnsupportedNetwork      = "netx.socks_unsupported_network"
	CodeSOCKSProxyUnreachable        = "netx.socks_proxy_unreachable"
	CodeSOCKSTimeout                 = "netx.socks_timeout"
	CodeSOCKSProtocolError           = "netx.socks_protocol_error"
	CodeSOCKSAuthRequired            = "netx.socks_auth_required"
	CodeSOCKSAuthUnsupported         = "netx.socks_auth_unsupported"
	CodeSOCKSAuthFailed              = "netx.socks_auth_failed"
	CodeSOCKSGeneralFailure          = "netx.socks_general_failure"
	CodeSOCKSNotAllowed              = "netx.socks_not_allowed"
	CodeSOCKSNetworkUnreachable      = "netx.socks_network_unreachable"
	CodeSOCKSHostUnreachable         = "netx.socks_host_unreachable"
	CodeSOCKSConnectionRefused       = "netx.socks_connection_refused"
	CodeSOCKSTTLExpired              = "netx.socks_ttl_expired"
	CodeSOCKSCommandNotSupported     = "netx.socks_command_not_supported"
	CodeSOCKSAddressTypeNotSupported = "netx.socks_address_type_not_supported"
	CodeSOCKSUnknownReply            = "netx.socks_unknown_reply"
	// UDP ASSOCIATE (SOCKS-07): the relay cannot be reached, or the
	// association ended while open (the control connection closed).
	CodeSOCKSUDPRelayUnreachable  = "netx.socks_udp_relay_unreachable"
	CodeSOCKSUDPAssociationClosed = "netx.socks_udp_association_closed"
)

// Hints of the SOCKS5 dialer's errors that do not depend on the dial.
const (
	HintSOCKSProxyUnreachable    = "Check the proxy address and port, and that the proxy is running."
	HintSOCKSTimeout             = "Check that the proxy is a working SOCKS5 proxy, or increase the timeout."
	HintSOCKSProtocol            = "Check that the proxy speaks SOCKS5 on this port (not HTTP or SOCKS4)."
	HintSOCKSAuthRequired        = "Set the proxy username and password."
	HintSOCKSAuthUnsupported     = "PostSonde supports no authentication and username/password. Check the proxy's authentication settings."
	HintSOCKSAuthFailed          = "Check the proxy username and password."
	HintSOCKSGeneralFailure      = "The proxy could not connect for a reason of its own. Check that the target is up, and look at the proxy's logs."
	HintSOCKSNotAllowed          = "The proxy's rules block this target. Check the proxy's configuration, or connect without the proxy."
	HintSOCKSNetworkUnreachable  = "The proxy has no route to that network. Check the address."
	HintSOCKSHostUnreachable     = "Check the host name, and that the host is reachable from the proxy."
	HintSOCKSConnectionRefused   = "Is the server running on that host and port?"
	HintSOCKSTTLExpired          = "The target is too many hops away from the proxy, or there is a routing loop."
	HintSOCKSCommandNotSupported = "Use a proxy that supports the SOCKS5 CONNECT command."
	HintSOCKSDomainNotSupported  = "The proxy does not resolve host names. Turn off remote DNS (socks5 instead of socks5h) so PostSonde resolves them."
	HintSOCKSIPNotSupported      = "The proxy does not accept this kind of IP address. Try a host name or an address of the other IP version."
	HintSOCKSUnknownReply        = "Look at the proxy's logs."
	HintSOCKSUDPNotSupported     = "This proxy does not relay UDP (ssh -D never does). Use a SOCKS5 proxy with UDP ASSOCIATE enabled, or send without the proxy."
	HintSOCKSUDPRelay            = "The proxy named a relay address PostSonde cannot reach. Check the proxy's external address settings."
	HintSOCKSUDPEnded            = "The proxy closed the control connection, which ends the UDP association (it may have restarted or timed it out). Start the UDP request again."
)

// SOCKS5 reply codes (RFC 1928 section 6).
const (
	SOCKSReplySucceeded               byte = 0x00
	SOCKSReplyGeneralFailure          byte = 0x01
	SOCKSReplyNotAllowed              byte = 0x02
	SOCKSReplyNetworkUnreachable      byte = 0x03
	SOCKSReplyHostUnreachable         byte = 0x04
	SOCKSReplyConnectionRefused       byte = 0x05
	SOCKSReplyTTLExpired              byte = 0x06
	SOCKSReplyCommandNotSupported     byte = 0x07
	SOCKSReplyAddressTypeNotSupported byte = 0x08
)

// SOCKSReplyError is the cause of an error for a CONNECT the proxy
// answered with a failure reply (SOCKS5, or SOCKS4's 91 to 93), for
// errors.As.
type SOCKSReplyError struct {
	Reply byte
}

func (e *SOCKSReplyError) Error() string {
	return "socks reply 0x" + strconv.FormatUint(uint64(e.Reply), 16)
}

// Protocol constants of RFC 1928 and RFC 1929.
const (
	socksVersion   = 0x05
	socksCmdConn   = 0x01
	socksCmdUDP    = 0x03
	socksAtypIPv4  = 0x01
	socksAtypName  = 0x03
	socksAtypIPv6  = 0x04
	socksNoAuth    = 0x00
	socksUserPass  = 0x02
	socksNoMethods = 0xff
	userPassVer    = 0x01
)

// socksReply is what a failure reply code maps to.
type socksReply struct {
	code string
	msg  func(target, proxy string) string
	hint string
}

var socksReplies = map[byte]socksReply{
	SOCKSReplyGeneralFailure: {CodeSOCKSGeneralFailure, func(t, p string) string {
		return "The SOCKS proxy at " + p + " could not connect to " + t + " (general failure)."
	}, HintSOCKSGeneralFailure},
	SOCKSReplyNotAllowed: {CodeSOCKSNotAllowed, func(t, p string) string {
		return "The SOCKS proxy at " + p + " does not allow connections to " + t + "."
	}, HintSOCKSNotAllowed},
	SOCKSReplyNetworkUnreachable: {CodeSOCKSNetworkUnreachable, func(t, p string) string {
		return "The SOCKS proxy at " + p + " cannot reach the network of " + t + "."
	}, HintSOCKSNetworkUnreachable},
	SOCKSReplyHostUnreachable: {CodeSOCKSHostUnreachable, func(t, p string) string {
		return "The SOCKS proxy at " + p + " cannot reach the host " + t + "."
	}, HintSOCKSHostUnreachable},
	SOCKSReplyConnectionRefused: {CodeSOCKSConnectionRefused, func(t, p string) string {
		return "The connection to " + t + " was refused by the destination (through the SOCKS proxy at " + p + ")."
	}, HintSOCKSConnectionRefused},
	SOCKSReplyTTLExpired: {CodeSOCKSTTLExpired, func(t, p string) string {
		return "The connection to " + t + " through the SOCKS proxy at " + p + " expired on the way (TTL expired)."
	}, HintSOCKSTTLExpired},
	SOCKSReplyCommandNotSupported: {CodeSOCKSCommandNotSupported, func(_, p string) string {
		return "The SOCKS proxy at " + p + " does not support the CONNECT command."
	}, HintSOCKSCommandNotSupported},
	SOCKSReplyAddressTypeNotSupported: {CodeSOCKSAddressTypeNotSupported, func(_, p string) string {
		return "The SOCKS proxy at " + p + " does not support this kind of address."
	}, ""}, // hint depends on the address type
}

// NewSOCKS5 returns a dialer that tunnels through the SOCKS5 proxy in cfg,
// reaching the proxy through forward (normally Direct; nil means
// NewDirect(Options{})). An invalid cfg is an invalid_argument error with
// the code netx.socks_invalid_config.
func NewSOCKS5(cfg SOCKS5Config, forward Dialer) (*SOCKS5, error) {
	invalid := func(msg string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeSOCKSInvalidConfig)
	}
	host, port, err := net.SplitHostPort(cfg.Address)
	if err != nil || host == "" || port == "" {
		return nil, invalid("The SOCKS proxy address must be host:port.")
	}
	switch {
	case len(cfg.Username) > 255:
		return nil, invalid("The SOCKS proxy username is longer than 255 bytes.")
	case len(cfg.Password) > 255:
		return nil, invalid("The SOCKS proxy password is longer than 255 bytes.")
	case cfg.Password != "" && cfg.Username == "":
		return nil, invalid("A SOCKS proxy password needs a username.")
	}
	if forward == nil {
		forward = NewDirect(Options{})
	}
	scheme := "socks5"
	if cfg.RemoteDNS {
		scheme = "socks5h"
	}
	s := &SOCKS5{
		cfg:     cfg,
		forward: forward,
		via:     scheme + "://" + cfg.Address,
		timeout: positiveOr(cfg.Timeout, DefaultTimeout),
		now:     cfg.Now,
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Via names the proxy as "socks5://host:port" or "socks5h://host:port",
// without credentials. Trace events, errors (detail "via") and the
// connections it returns (ViaOf) carry it.
func (s *SOCKS5) Via() string { return s.via }

// DialContext connects to address through the proxy: "tcp", "tcp4" and
// "tcp6" with CONNECT, "udp", "udp4" and "udp6" with UDP ASSOCIATE
// (SOCKS-07; a DatagramConn, socks5udp.go). The returned connection's
// RemoteAddr is the proxy's (for UDP, its relay's); ViaOf reports the
// proxy.
func (s *SOCKS5) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, s.finish(WrapDialError(err, network, address))
	}
	tr := ContextTrace(ctx)
	tr.dialStart(DialStartInfo{Network: network, Address: address, Via: s.via})
	start := s.now()
	hop := &ProxyHop{
		Scheme:    s.via[:strings.Index(s.via, ":")],
		Version:   "SOCKS5",
		Proxy:     s.cfg.Address,
		Target:    address,
		RemoteDNS: s.cfg.RemoteDNS,
	}
	conn, err := s.dial(ctx, network, address, hop)
	info := DialDoneInfo{Network: network, Address: address, Via: s.via, Proxy: hop}
	if err != nil {
		err = s.finish(err)
		info.Err = err
		info.Duration = s.now().Sub(start)
		tr.dialDone(info)
		return nil, err
	}
	info.Duration = s.now().Sub(start)
	info.RemoteAddr = conn.RemoteAddr()
	info.LocalAddr = conn.LocalAddr()
	tr.dialDone(info)
	if IsUDP(network) {
		return conn, nil // a socksUDPConn, which knows its Via
	}
	return proxied(conn, s.via), nil
}

// finish adds the via detail to a dial error.
func (s *SOCKS5) finish(err error) error {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.WithDetail("via", s.via)
	}
	return err
}

func (s *SOCKS5) dial(ctx context.Context, network, address string, hop *ProxyHop) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	case "udp", "udp4", "udp6":
		return s.associate(ctx, network, address, hop)
	default:
		return nil, withAddr(errs.New(errs.Unsupported, "A SOCKS5 proxy cannot carry "+network+" connections.").
			WithCode(CodeSOCKSUnsupportedNetwork), network, address)
	}
	at := s.now()
	dst, err := s.target(ctx, network, address)
	if dst.looked {
		hop.Lookup = s.now().Sub(at)
	}
	if err != nil {
		return nil, err
	}
	at = s.now()
	conn, err := s.forward.DialContext(ctx, "tcp", s.cfg.Address)
	hop.Connect = s.now().Sub(at)
	if err != nil {
		return nil, s.proxyDialError(err, network, address)
	}
	hop.ProxyAddr = conn.RemoteAddr().String()
	at = s.now()
	err = s.handshake(ctx, conn, network, address, hop, func(h *socksHandshake) error { return h.connect(dst) })
	hop.Handshake = s.now().Sub(at)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// proxyDialError maps a failure to reach the proxy. Cancellations and
// refusals (a guard) pass through; anything else names the proxy.
func (s *SOCKS5) proxyDialError(err error, network, address string) error {
	kind := errs.KindOf(err)
	if kind == errs.Cancelled || kind == errs.PermissionDenied {
		return err
	}
	if kind != errs.Timeout {
		kind = errs.Network
	}
	return withAddr(errs.Wrap(err, kind, "Could not connect to the SOCKS proxy at "+s.cfg.Address+".").
		WithCode(CodeSOCKSProxyUnreachable).
		WithHint(HintSOCKSProxyUnreachable), network, address)
}

// socksAddr is a CONNECT destination: an IP address or a host name, and
// a port.
type socksAddr struct {
	ip   netip.Addr
	name string
	port uint16
	// looked is set when the host name was resolved locally.
	looked bool
}

// bytes encodes a as ATYP, DST.ADDR, DST.PORT.
func (a socksAddr) bytes() []byte {
	var b []byte
	switch {
	case a.name != "":
		b = append(b, socksAtypName, byte(len(a.name)))
		b = append(b, a.name...)
	case a.ip.Is4():
		b = append(b, socksAtypIPv4)
		b = append(b, a.ip.AsSlice()...)
	default:
		b = append(b, socksAtypIPv6)
		b = append(b, a.ip.AsSlice()...)
	}
	return append(b, byte(a.port>>8), byte(a.port))
}

// target parses address into the CONNECT destination, resolving a host
// name locally unless RemoteDNS is set.
func (s *SOCKS5) target(ctx context.Context, network, address string) (socksAddr, error) {
	invalid := func(msg string) error {
		return withAddr(errs.New(errs.InvalidArgument, msg).WithCode(CodeSOCKSInvalidAddress), network, address)
	}
	host, portStr, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return socksAddr{}, invalid(address + " is not a host:port address.")
	}
	resolver := s.cfg.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	port, err := resolver.LookupPort(ctx, network, portStr)
	if err != nil {
		return socksAddr{}, invalid("The port in " + address + " is not valid.")
	}
	dst := socksAddr{port: uint16(port)}
	if ip, err := netip.ParseAddr(host); err == nil {
		dst.ip = ip.WithZone("").Unmap()
		return dst, nil
	}
	if s.cfg.RemoteDNS {
		if len(host) > 255 {
			return socksAddr{}, invalid("The host name in " + address + " is longer than 255 bytes.")
		}
		dst.name = host
		return dst, nil
	}
	family := "ip"
	switch network {
	case "tcp4", "udp4":
		family = "ip4"
	case "tcp6", "udp6":
		family = "ip6"
	}
	ips, err := resolver.LookupNetIP(ctx, family, host)
	if err != nil {
		return socksAddr{looked: true}, WrapDialError(err, network, address)
	}
	if len(ips) == 0 {
		return socksAddr{looked: true}, WrapDialError(&net.DNSError{Err: "no such host", Name: host, IsNotFound: true}, network, address)
	}
	dst.looked = true
	dst.ip = ips[0].WithZone("").Unmap()
	return dst, nil
}

// handshake runs the greeting, authentication and then request (CONNECT
// or UDP ASSOCIATE) on conn, bounded by s.timeout and ctx.
func (s *SOCKS5) handshake(ctx context.Context, conn net.Conn, network, address string, hop *ProxyHop, request func(*socksHandshake) error) (err error) {
	if s.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.timeout))
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer func() {
		if !stop() && err == nil {
			// ctx ended just as the handshake finished: the deadline may
			// already be in the past, so the connection is unusable.
			err = WrapDialError(ctx.Err(), network, address)
		}
		if err == nil {
			_ = conn.SetDeadline(time.Time{})
		}
	}()
	h := &socksHandshake{s: s, conn: conn, ctx: ctx, network: network, address: address, hop: hop}
	if err := h.negotiate(); err != nil {
		return err
	}
	return request(h)
}

// socksHandshake holds one handshake's state for error mapping.
type socksHandshake struct {
	s        *SOCKS5
	conn     net.Conn
	ctx      context.Context
	network  string
	address  string
	stepDesc string
	hop      *ProxyHop
}

func (h *socksHandshake) negotiate() error {
	methods := []byte{socksNoAuth}
	if h.s.cfg.Username != "" {
		methods = append(methods, socksUserPass)
	}
	h.stepDesc = "greeting"
	if err := h.write(append([]byte{socksVersion, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	var resp [2]byte
	if err := h.read(resp[:]); err != nil {
		return err
	}
	if resp[0] != socksVersion {
		return h.protocolError("The proxy at "+h.s.cfg.Address+" did not answer as a SOCKS5 proxy.", nil)
	}
	switch resp[1] {
	case socksNoAuth:
		h.hop.Auth = AuthNone
		return nil
	case socksUserPass:
		if h.s.cfg.Username == "" {
			return h.protocolError("The SOCKS proxy at "+h.s.cfg.Address+" chose an authentication method PostSonde did not offer.", nil)
		}
		h.hop.Auth = AuthUserPass
		return h.userPass()
	case socksNoMethods:
		if h.s.cfg.Username == "" {
			return h.fail(errs.Network, CodeSOCKSAuthRequired,
				"The SOCKS proxy at "+h.s.cfg.Address+" requires a username and password.", HintSOCKSAuthRequired, nil)
		}
		return h.fail(errs.Network, CodeSOCKSAuthUnsupported,
			"The SOCKS proxy at "+h.s.cfg.Address+" accepts none of the authentication methods PostSonde supports.", HintSOCKSAuthUnsupported, nil)
	}
	return h.protocolError("The SOCKS proxy at "+h.s.cfg.Address+" chose an authentication method PostSonde did not offer.", nil)
}

// userPass runs the RFC 1929 sub-negotiation.
func (h *socksHandshake) userPass() error {
	u, p := h.s.cfg.Username, h.s.cfg.Password
	req := make([]byte, 0, 3+len(u)+len(p))
	req = append(req, userPassVer, byte(len(u)))
	req = append(req, u...)
	req = append(req, byte(len(p)))
	req = append(req, p...)
	h.stepDesc = "authentication"
	if err := h.write(req); err != nil {
		return err
	}
	var resp [2]byte
	if err := h.read(resp[:]); err != nil {
		return err
	}
	if resp[0] != userPassVer {
		return h.protocolError("The SOCKS proxy at "+h.s.cfg.Address+" sent a malformed authentication reply.", nil)
	}
	if resp[1] != 0x00 {
		return h.fail(errs.Network, CodeSOCKSAuthFailed,
			"The SOCKS proxy at "+h.s.cfg.Address+" rejected the username or password.", HintSOCKSAuthFailed, nil)
	}
	return nil
}

// connect sends CONNECT and reads the reply, up to and including BND.ADDR
// and BND.PORT, so the tunnel starts clean.
func (h *socksHandshake) connect(dst socksAddr) error {
	h.stepDesc = "connect"
	if err := h.write(append([]byte{socksVersion, socksCmdConn, 0x00}, dst.bytes()...)); err != nil {
		return err
	}
	var head [4]byte
	if err := h.read(head[:]); err != nil {
		return err
	}
	if head[0] != socksVersion {
		return h.protocolError("The SOCKS proxy at "+h.s.cfg.Address+" sent a malformed CONNECT reply.", nil)
	}
	h.hop.Replied, h.hop.Reply, h.hop.ReplyText = true, int(head[1]), socks5ReplyText(head[1])
	if rep := head[1]; rep != SOCKSReplySucceeded {
		return h.replyError(rep, dst)
	}
	var n int
	switch head[3] {
	case socksAtypIPv4:
		n = 4
	case socksAtypIPv6:
		n = 16
	case socksAtypName:
		var l [1]byte
		if err := h.read(l[:]); err != nil {
			return err
		}
		n = int(l[0])
	default:
		return h.protocolError("The SOCKS proxy at "+h.s.cfg.Address+" sent a malformed CONNECT reply.", nil)
	}
	return h.read(make([]byte, n+2))
}

func (h *socksHandshake) replyError(rep byte, dst socksAddr) error {
	cause := &SOCKSReplyError{Reply: rep}
	info, ok := socksReplies[rep]
	if !ok {
		return h.fail(errs.Network, CodeSOCKSUnknownReply,
			"The SOCKS proxy at "+h.s.cfg.Address+" did not connect to "+h.address+" (reply code "+strconv.Itoa(int(rep))+").",
			HintSOCKSUnknownReply, cause)
	}
	hint := info.hint
	if rep == SOCKSReplyCommandNotSupported && IsUDP(h.network) {
		return h.fail(errs.Network, info.code,
			"The SOCKS proxy at "+h.s.cfg.Address+" does not support UDP ASSOCIATE.", HintSOCKSUDPNotSupported, cause)
	}
	if rep == SOCKSReplyAddressTypeNotSupported {
		hint = HintSOCKSIPNotSupported
		if dst.name != "" {
			hint = HintSOCKSDomainNotSupported
		}
	}
	return h.fail(errs.Network, info.code, info.msg(h.address, h.s.cfg.Address), hint, cause)
}

func (h *socksHandshake) write(b []byte) error {
	if _, err := h.conn.Write(b); err != nil {
		return h.ioError(err)
	}
	return nil
}

func (h *socksHandshake) read(b []byte) error {
	if _, err := io.ReadFull(h.conn, b); err != nil {
		return h.ioError(err)
	}
	return nil
}

// ioError maps a read or write failure during the handshake: ctx ended,
// the handshake timed out, or the proxy closed or broke the connection.
func (h *socksHandshake) ioError(err error) error {
	if cerr := h.ctx.Err(); cerr != nil {
		return WrapDialError(cerr, h.network, h.address)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err) {
		return h.fail(errs.Timeout, CodeSOCKSTimeout,
			"The SOCKS proxy at "+h.s.cfg.Address+" did not answer in time ("+h.stepDesc+").", HintSOCKSTimeout, err)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || IsReset(err) {
		return h.protocolError("The SOCKS proxy at "+h.s.cfg.Address+" closed the connection during the "+h.stepDesc+".", err)
	}
	return h.fail(errs.Network, CodeSOCKSProtocolError,
		"The connection to the SOCKS proxy at "+h.s.cfg.Address+" failed during the "+h.stepDesc+".", HintSOCKSProxyUnreachable, err)
}

func (h *socksHandshake) protocolError(msg string, cause error) error {
	return h.fail(errs.Protocol, CodeSOCKSProtocolError, msg, HintSOCKSProtocol, cause)
}

func (h *socksHandshake) fail(kind errs.Kind, code, msg, hint string, cause error) error {
	e := errs.Wrap(cause, kind, msg).WithCode(code)
	if hint != "" {
		e = e.WithHint(hint)
	}
	if r, ok := cause.(*SOCKSReplyError); ok {
		e = e.WithDetail("socksReply", strconv.Itoa(int(r.Reply)))
	}
	return withAddr(e, h.network, h.address)
}

// proxiedConn is a connection tunnelled through a proxy; ViaOf finds it.
type proxiedConn struct {
	net.Conn
	via string
}

// proxiedHalfCloser keeps CloseWrite and CloseRead visible when the
// connection to the proxy has them (TCP).
type proxiedHalfCloser struct {
	*proxiedConn
}

func proxied(conn net.Conn, via string) net.Conn {
	c := &proxiedConn{Conn: conn, via: via}
	if _, ok := conn.(halfCloser); ok {
		return proxiedHalfCloser{c}
	}
	return c
}

type halfCloser interface {
	CloseWrite() error
	CloseRead() error
}

// Via returns the proxy the connection goes through.
func (c *proxiedConn) Via() string { return c.via }

// Unwrap returns the connection to the proxy.
func (c *proxiedConn) Unwrap() net.Conn { return c.Conn }

func (h proxiedHalfCloser) CloseWrite() error { return h.Conn.(halfCloser).CloseWrite() }
func (h proxiedHalfCloser) CloseRead() error  { return h.Conn.(halfCloser).CloseRead() }

// ViaOf returns the proxy conn goes through ("socks5h://proxy:1080"), or ""
// for a direct connection. It looks through wrappers that have an
// Unwrap() net.Conn method and through TLS (*tls.Conn's NetConn), so it
// works on the connection an HTTP client got too (NET-06).
func ViaOf(conn net.Conn) string {
	for range 16 {
		switch c := conn.(type) {
		case interface{ Via() string }:
			return c.Via()
		case interface{ Unwrap() net.Conn }:
			conn = c.Unwrap()
		case interface{ NetConn() net.Conn }:
			conn = c.NetConn()
		default:
			return ""
		}
	}
	return ""
}
