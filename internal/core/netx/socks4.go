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

// SOCKS4Config configures a SOCKS4 or SOCKS4a dialer (SOCKS-06).
type SOCKS4Config struct {
	// Address is the proxy's host:port.
	Address string
	// RemoteDNS makes it SOCKS4a: host names go to the proxy unresolved
	// and the proxy resolves them. When false (SOCKS4), host names are
	// resolved locally with Resolver to an IPv4 address.
	RemoteDNS bool
	// UserID is the USERID field of the request ("" for none); SOCKS4 has
	// no password. At most 255 bytes, no NUL.
	UserID string
	// Timeout bounds the request and reply, after the connection to the
	// proxy is open. Zero means DefaultTimeout; a negative value means no
	// limit besides ctx.
	Timeout time.Duration
	// Resolver resolves host names when RemoteDNS is off; nil means
	// net.DefaultResolver.
	Resolver *net.Resolver
	// Now is the clock for durations; nil means time.Now.
	Now func() time.Time
}

// SOCKS4 dials targets through a SOCKS4 or SOCKS4a proxy (SOCKS-06): the
// CONNECT request with an IPv4 address (SOCKS4) or a host name (SOCKS4a)
// and a user ID. SOCKS4 carries IPv4 only: an IPv6 target fails. Like
// SOCKS5 it reaches the proxy through a forward Dialer and sits below the
// guard and the connection log. It is safe for concurrent use.
type SOCKS4 struct {
	cfg     SOCKS4Config
	forward Dialer
	via     string
	version string
	timeout time.Duration
	now     func() time.Time
}

var _ Dialer = (*SOCKS4)(nil)

// Codes of the SOCKS4 dialer's own errors; the others are the SOCKS5
// dialer's (CodeSOCKSProxyUnreachable, CodeSOCKSTimeout, ...).
const (
	// CodeSOCKS4Rejected: reply 91, request rejected or failed.
	CodeSOCKS4Rejected = "netx.socks4_rejected"
	// CodeSOCKS4IdentdUnreachable: reply 92, the proxy could not reach
	// identd on this computer.
	CodeSOCKS4IdentdUnreachable = "netx.socks4_identd_unreachable"
	// CodeSOCKS4IdentdMismatch: reply 93, identd reported another user ID.
	CodeSOCKS4IdentdMismatch = "netx.socks4_identd_mismatch"
	// CodeSOCKS4IPv6: an IPv6 target, which SOCKS4 cannot carry (kind
	// unsupported).
	CodeSOCKS4IPv6 = "netx.socks4_ipv6_unsupported"
)

// Hints of the SOCKS4 dialer's errors.
const (
	HintSOCKS4Protocol          = "Check that the proxy speaks SOCKS4 on this port (not SOCKS5 or HTTP)."
	HintSOCKS4Timeout           = "Check that the proxy is a working SOCKS4 proxy, or increase the timeout."
	HintSOCKS4Rejected          = "The proxy refused the request, or could not connect to the target. Check that the target is up, the proxy's rules, and the user ID."
	HintSOCKS4IdentdUnreachable = "The proxy checks user IDs with identd, which does not answer on this computer. Use a proxy that does not require identd."
	HintSOCKS4IdentdMismatch    = "The proxy checks user IDs with identd, which reported another user. Check the user ID in the proxy URL (socks4://user@host)."
	HintSOCKS4IPv6              = "SOCKS4 carries IPv4 addresses only. Use a host name with socks4a://, or a SOCKS5 proxy."
)

// SOCKS4 reply codes.
const (
	SOCKS4Granted           byte = 90
	SOCKS4Rejected          byte = 91
	SOCKS4IdentdUnreachable byte = 92
	SOCKS4IdentdMismatch    byte = 93
)

// NewSOCKS4 returns a dialer that tunnels through the SOCKS4 (or, with
// RemoteDNS, SOCKS4a) proxy in cfg, reaching the proxy through forward
// (nil means NewDirect(Options{})). An invalid cfg is an invalid_argument
// error with the code netx.socks_invalid_config.
func NewSOCKS4(cfg SOCKS4Config, forward Dialer) (*SOCKS4, error) {
	invalid := func(msg string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeSOCKSInvalidConfig)
	}
	host, port, err := net.SplitHostPort(cfg.Address)
	if err != nil || host == "" || port == "" {
		return nil, invalid("The SOCKS proxy address must be host:port.")
	}
	if len(cfg.UserID) > 255 || strings.ContainsRune(cfg.UserID, 0) {
		return nil, invalid("The SOCKS4 user ID must be at most 255 bytes, without NUL characters.")
	}
	if forward == nil {
		forward = NewDirect(Options{})
	}
	scheme, version := ProxySOCKS4, "SOCKS4"
	if cfg.RemoteDNS {
		scheme, version = ProxySOCKS4A, "SOCKS4a"
	}
	s := &SOCKS4{
		cfg:     cfg,
		forward: forward,
		via:     scheme + "://" + cfg.Address,
		version: version,
		timeout: positiveOr(cfg.Timeout, DefaultTimeout),
		now:     cfg.Now,
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Via names the proxy as "socks4://host:port" or "socks4a://host:port",
// without the user ID.
func (s *SOCKS4) Via() string { return s.via }

// DialContext connects to address through the proxy. Only "tcp" and
// "tcp4" targets reachable over IPv4 (or, with SOCKS4a, host names) are
// supported. The returned connection's RemoteAddr is the proxy's; ViaOf
// reports the proxy.
func (s *SOCKS4) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, s.finish(WrapDialError(err, network, address))
	}
	tr := ContextTrace(ctx)
	tr.dialStart(DialStartInfo{Network: network, Address: address, Via: s.via})
	start := s.now()
	hop := &ProxyHop{
		Scheme:    s.via[:strings.Index(s.via, ":")],
		Version:   s.version,
		Proxy:     s.cfg.Address,
		Target:    address,
		RemoteDNS: s.cfg.RemoteDNS,
	}
	conn, err := s.dial(ctx, network, address, hop)
	info := DialDoneInfo{Network: network, Address: address, Via: s.via, Proxy: hop}
	info.Duration = s.now().Sub(start)
	if err != nil {
		err = s.finish(err)
		info.Err = err
		tr.dialDone(info)
		return nil, err
	}
	info.RemoteAddr = conn.RemoteAddr()
	info.LocalAddr = conn.LocalAddr()
	tr.dialDone(info)
	return proxied(conn, s.via), nil
}

// finish adds the via detail to a dial error.
func (s *SOCKS4) finish(err error) error {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.WithDetail("via", s.via)
	}
	return err
}

func (s *SOCKS4) dial(ctx context.Context, network, address string, hop *ProxyHop) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4":
	default:
		return nil, withAddr(errs.New(errs.Unsupported, "A SOCKS4 proxy cannot carry "+network+" connections.").
			WithCode(CodeSOCKSUnsupportedNetwork), network, address)
	}
	at := s.now()
	req, looked, err := s.request(ctx, network, address)
	if looked {
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
	hop.Auth = AuthNone
	if s.cfg.UserID != "" {
		hop.Auth = AuthUserID
	}
	at = s.now()
	err = s.handshake(ctx, conn, req, network, address, hop)
	hop.Handshake = s.now().Sub(at)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// proxyDialError maps a failure to reach the proxy, as SOCKS5 does.
func (s *SOCKS4) proxyDialError(err error, network, address string) error {
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

// request builds the CONNECT request for address: VN 4, CD 1, DSTPORT,
// DSTIP, USERID, NUL, and for a SOCKS4a host name the name and a NUL
// after DSTIP 0.0.0.1. looked is set when a host name was resolved here.
func (s *SOCKS4) request(ctx context.Context, network, address string) (req []byte, looked bool, err error) {
	invalid := func(msg string) error {
		return withAddr(errs.New(errs.InvalidArgument, msg).WithCode(CodeSOCKSInvalidAddress), network, address)
	}
	host, portStr, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return nil, false, invalid(address + " is not a host:port address.")
	}
	resolver := s.cfg.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	port, err := resolver.LookupPort(ctx, "tcp", portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, false, invalid("The port in " + address + " is not valid.")
	}
	var ip netip.Addr
	name := ""
	switch parsed, perr := netip.ParseAddr(host); {
	case perr == nil:
		ip = parsed.WithZone("").Unmap()
	case s.cfg.RemoteDNS:
		if len(host) > 255 || strings.ContainsRune(host, 0) {
			return nil, false, invalid("The host name in " + address + " cannot be sent to a SOCKS4a proxy.")
		}
		name = host
	default:
		ips, lerr := resolver.LookupNetIP(ctx, "ip4", host)
		if lerr != nil {
			return nil, true, WrapDialError(lerr, network, address)
		}
		if len(ips) == 0 {
			return nil, true, WrapDialError(&net.DNSError{Err: "no such host", Name: host, IsNotFound: true}, network, address)
		}
		ip, looked = ips[0].Unmap(), true
	}
	if name == "" && !ip.Is4() {
		return nil, looked, withAddr(errs.New(errs.Unsupported, "A SOCKS4 proxy cannot connect to the IPv6 address in "+address+".").
			WithCode(CodeSOCKS4IPv6).WithHint(HintSOCKS4IPv6), network, address)
	}
	req = []byte{4, 1, byte(port >> 8), byte(port)}
	if name != "" {
		req = append(req, 0, 0, 0, 1) // SOCKS4a: an invalid IP, the name follows
	} else {
		a := ip.As4()
		req = append(req, a[:]...)
	}
	req = append(req, s.cfg.UserID...)
	req = append(req, 0)
	if name != "" {
		req = append(req, name...)
		req = append(req, 0)
	}
	return req, looked, nil
}

// handshake sends req and reads the 8-byte reply, bounded by the timeout
// and ctx.
func (s *SOCKS4) handshake(ctx context.Context, conn net.Conn, req []byte, network, address string, hop *ProxyHop) (err error) {
	if s.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.timeout))
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer func() {
		if !stop() && err == nil {
			err = WrapDialError(ctx.Err(), network, address)
		}
		if err == nil {
			_ = conn.SetDeadline(time.Time{})
		}
	}()
	fail := func(kind errs.Kind, code, msg, hint string, cause error) error {
		e := errs.Wrap(cause, kind, msg).WithCode(code)
		if hint != "" {
			e = e.WithHint(hint)
		}
		return withAddr(e, network, address)
	}
	ioErr := func(err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return WrapDialError(cerr, network, address)
		}
		if errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err) {
			return fail(errs.Timeout, CodeSOCKSTimeout, "The SOCKS4 proxy at "+s.cfg.Address+" did not answer in time.", HintSOCKS4Timeout, err)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || IsReset(err) {
			return fail(errs.Protocol, CodeSOCKSProtocolError, "The SOCKS4 proxy at "+s.cfg.Address+" closed the connection before answering.", HintSOCKS4Protocol, err)
		}
		return fail(errs.Network, CodeSOCKSProtocolError, "The connection to the SOCKS4 proxy at "+s.cfg.Address+" failed.", HintSOCKSProxyUnreachable, err)
	}
	if _, err := conn.Write(req); err != nil {
		return ioErr(err)
	}
	var reply [8]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return ioErr(err)
	}
	// VN is 0; some servers echo 4.
	if reply[0] != 0 && reply[0] != 4 {
		return fail(errs.Protocol, CodeSOCKSProtocolError, "The proxy at "+s.cfg.Address+" did not answer as a SOCKS4 proxy.", HintSOCKS4Protocol, nil)
	}
	rep := reply[1]
	hop.Replied, hop.Reply, hop.ReplyText = true, int(rep), socks4ReplyText(rep)
	p, t := s.cfg.Address, address
	cause := &SOCKSReplyError{Reply: rep}
	var e error
	switch rep {
	case SOCKS4Granted:
		return nil
	case SOCKS4Rejected:
		e = fail(errs.Network, CodeSOCKS4Rejected, "The SOCKS4 proxy at "+p+" rejected the connection to "+t+", or could not make it.", HintSOCKS4Rejected, cause)
	case SOCKS4IdentdUnreachable:
		e = fail(errs.Network, CodeSOCKS4IdentdUnreachable, "The SOCKS4 proxy at "+p+" could not check the user ID: identd on this computer did not answer.", HintSOCKS4IdentdUnreachable, cause)
	case SOCKS4IdentdMismatch:
		e = fail(errs.Network, CodeSOCKS4IdentdMismatch, "The SOCKS4 proxy at "+p+" refused the user ID: identd reported another user.", HintSOCKS4IdentdMismatch, cause)
	default:
		e = fail(errs.Network, CodeSOCKSUnknownReply, "The SOCKS4 proxy at "+p+" did not connect to "+t+" (reply code "+strconv.Itoa(int(rep))+").", HintSOCKSUnknownReply, cause)
	}
	return e.(*errs.Error).WithDetail("socksReply", strconv.Itoa(int(rep)))
}

// socks4ReplyText is the meaning of a SOCKS4 reply code; "" for an
// unknown one.
func socks4ReplyText(rep byte) string {
	switch rep {
	case SOCKS4Granted:
		return "granted"
	case SOCKS4Rejected:
		return "rejected or failed"
	case SOCKS4IdentdUnreachable:
		return "identd unreachable"
	case SOCKS4IdentdMismatch:
		return "user ID mismatch"
	}
	return ""
}
