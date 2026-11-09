package listen

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Spec says where to listen.
type Spec struct {
	// Network is "tcp", "tcp4" or "tcp6" for Listen and "udp", "udp4" or
	// "udp6" for ListenPacket. Empty means "tcp" or "udp". The 4 and 6
	// forms restrict "localhost" to that family and reject an IP of the
	// other one.
	Network string
	// Host is an IP literal, "localhost" or empty (the same as
	// "localhost"). "localhost" means 127.0.0.1, falling back to ::1 when
	// IPv4 loopback is unavailable. Brackets around an IPv6 literal and an
	// IPv6 zone are accepted. IPv4-mapped IPv6 addresses (::ffff:127.0.0.1)
	// are bound as the IPv4 address. Any other host name is rejected with
	// CodeInvalidHost: listen addresses never go through DNS.
	Host string
	// Port is the port to bind, 0 for one the OS picks (see Info.Port).
	Port int
	// AllowLAN permits a host that is not loopback: a LAN, link-local or
	// public address, or all interfaces (0.0.0.0, ::). Without it such a
	// host fails with CodeLANNotAllowed. With it, Info.Warning is set.
	AllowLAN bool
	// DualStack binds both 127.0.0.1 and ::1, on the same port, when Host
	// is empty or "localhost", so clients resolving localhost to either
	// family connect. A family that is unavailable on this machine is
	// skipped. It is ignored for IP literals and for tcp4/tcp6, and not
	// supported by ListenPacket (one PacketConn has one address).
	DualStack bool
}

// Scope is how far a listener is reachable.
type Scope string

const (
	// ScopeLoopback is reachable from this computer only.
	ScopeLoopback Scope = "loopback"
	// ScopeLAN is one non-loopback address: a LAN, link-local or public IP.
	ScopeLAN Scope = "lan"
	// ScopeAllInterfaces is every interface of one IP family (0.0.0.0, ::).
	ScopeAllInterfaces Scope = "all_interfaces"
)

// Warning tells the user a listener is reachable from other devices.
// Code is stable for the UI to pick an icon or a help link; Message is
// ready to show.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Warning codes.
const (
	WarnLANExposed           = "listen.lan_exposed"
	WarnAllInterfacesExposed = "listen.all_interfaces_exposed"
)

// Info describes what was bound.
type Info struct {
	// Addrs are the bound addresses, with the actual port; the first is the
	// listener's Addr(). IPv4 addresses are never IPv4-mapped.
	Addrs []netip.AddrPort
	Scope Scope
	// Warning is set for every scope but ScopeLoopback.
	Warning *Warning
}

// IsLoopback reports whether the listener is reachable from this computer
// only.
func (i Info) IsLoopback() bool { return i.Scope == ScopeLoopback }

// Port returns the bound port (the one the OS picked for Spec.Port 0), or 0
// if nothing is bound.
func (i Info) Port() int {
	if len(i.Addrs) == 0 {
		return 0
	}
	return int(i.Addrs[0].Port())
}

// Target is a parsed Spec.Host: the addresses to try and their scope.
type Target struct {
	// Addrs are the candidate addresses in order of preference. For an IP
	// literal there is exactly one.
	Addrs []netip.Addr
	Scope Scope
	// Localhost is true when the host was empty or "localhost".
	Localhost bool
}

// ParseHost parses a listen host for network ("tcp", "udp" or a 4/6 form;
// empty means both families) and classifies it, without binding or checking
// AllowLAN. The UI can use it to validate a host as it is typed. Errors are
// *errs.Error with CodeInvalidHost or CodeInvalidNetwork.
func ParseHost(network, host string) (Target, error) {
	fam, err := family(network)
	if err != nil {
		return Target{}, err
	}
	h := strings.TrimSpace(host)
	if h == "" || strings.EqualFold(h, "localhost") || strings.EqualFold(h, "localhost.") {
		var addrs []netip.Addr
		if fam != 6 {
			addrs = append(addrs, netip.AddrFrom4([4]byte{127, 0, 0, 1}))
		}
		if fam != 4 {
			addrs = append(addrs, netip.IPv6Loopback())
		}
		return Target{Addrs: addrs, Scope: ScopeLoopback, Localhost: true}, nil
	}
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	a, err := netip.ParseAddr(h)
	if err != nil {
		return Target{}, invalidHost(host)
	}
	if a.Is4In6() {
		a = a.Unmap()
	}
	if (fam == 4 && !a.Is4()) || (fam == 6 && !a.Is6()) {
		return Target{}, familyMismatch(network, host)
	}
	scope := ScopeLAN
	switch {
	case a.IsLoopback():
		scope = ScopeLoopback
	case a.IsUnspecified():
		scope = ScopeAllInterfaces
	}
	return Target{Addrs: []netip.Addr{a}, Scope: scope}, nil
}

// Listen opens a stream listener (TCP) as described by spec. See Spec for
// the defaults and the LAN opt-in. The listener is a *net.TCPListener, or,
// for a DualStack spec that bound both families, a net.Listener that accepts
// on both; its Addr is the first address in Info.Addrs.
//
// Errors are *errs.Error values: invalid_argument for a bad spec,
// permission_denied (CodeLANNotAllowed) for a non-loopback host without
// AllowLAN or (CodeBindDenied) when the OS refuses the port, network for
// bind failures and cancelled/timeout when ctx ends first.
func Listen(ctx context.Context, spec Spec) (net.Listener, Info, error) {
	network := spec.Network
	if network == "" {
		network = "tcp"
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, Info{}, invalidNetwork(spec.Network, "tcp")
	}
	target, err := prepare(ctx, network, spec)
	if err != nil {
		return nil, Info{}, err
	}
	lc := net.ListenConfig{}
	bind := func(a netip.AddrPort) (net.Listener, error) {
		return lc.Listen(ctx, "tcp"+familyOf(a.Addr()), a.String())
	}

	if !target.Localhost || !spec.DualStack || network != "tcp" {
		ln, err := bindFirst(target.Addrs, spec.Port, network, bind)
		if err != nil {
			return nil, Info{}, err
		}
		return ln, info(target.Scope, ln.Addr()), nil
	}

	// DualStack: the first family that binds picks the port, the others
	// join on the same port. When the OS picked the port and it is taken in
	// the other family, start over with a new one.
	const attempts = 5
	for try := 1; ; try++ {
		first, err := bindFirst(target.Addrs, spec.Port, network, bind)
		if err != nil {
			return nil, Info{}, err
		}
		lns := []net.Listener{first}
		port := addrPort(first.Addr()).Port()
		var retry bool
		for _, a := range target.Addrs {
			if a == addrPort(first.Addr()).Addr() {
				continue
			}
			ln, err := bind(netip.AddrPortFrom(a, port))
			switch {
			case err == nil:
				lns = append(lns, ln)
				continue
			case isErrno(err, unavailableErrnos):
				continue // this family is not available here
			case spec.Port == 0 && isErrno(err, inUseErrnos) && try < attempts:
				retry = true
			}
			closeAll(lns)
			if retry {
				break
			}
			return nil, Info{}, wrapBindError(err, network, netip.AddrPortFrom(a, port).String(), spec.Port)
		}
		if retry {
			continue
		}
		if len(lns) == 1 {
			return first, info(target.Scope, first.Addr()), nil
		}
		m := newMultiListener(lns)
		return m, info(target.Scope, m.Addrs()...), nil
	}
}

// ListenPacket opens a packet listener (UDP) as described by spec. See
// Listen for the rules and errors; Spec.DualStack fails with
// CodeDualStackUnsupported.
func ListenPacket(ctx context.Context, spec Spec) (net.PacketConn, Info, error) {
	network := spec.Network
	if network == "" {
		network = "udp"
	}
	if network != "udp" && network != "udp4" && network != "udp6" {
		return nil, Info{}, invalidNetwork(spec.Network, "udp")
	}
	if spec.DualStack {
		return nil, Info{}, dualStackUnsupported()
	}
	target, err := prepare(ctx, network, spec)
	if err != nil {
		return nil, Info{}, err
	}
	lc := net.ListenConfig{}
	conn, err := bindFirst(target.Addrs, spec.Port, network, func(a netip.AddrPort) (net.PacketConn, error) {
		return lc.ListenPacket(ctx, "udp"+familyOf(a.Addr()), a.String())
	})
	if err != nil {
		return nil, Info{}, err
	}
	return conn, info(target.Scope, conn.LocalAddr()), nil
}

// ListenPackets is ListenPacket with Spec.DualStack: for "" and
// "localhost" on "udp" it binds 127.0.0.1 and ::1 on the same port (the
// first family that binds picks the port; a family unavailable here is
// skipped) and returns one PacketConn per family, since one PacketConn has
// one address. Otherwise it returns the one PacketConn ListenPacket would.
// Info.Addrs lists the conns' addresses in the same order.
func ListenPackets(ctx context.Context, spec Spec) ([]net.PacketConn, Info, error) {
	network := spec.Network
	if network == "" {
		network = "udp"
	}
	if network != "udp" && network != "udp4" && network != "udp6" {
		return nil, Info{}, invalidNetwork(spec.Network, "udp")
	}
	target, err := prepare(ctx, network, spec)
	if err != nil {
		return nil, Info{}, err
	}
	lc := net.ListenConfig{}
	bind := func(a netip.AddrPort) (net.PacketConn, error) {
		return lc.ListenPacket(ctx, "udp"+familyOf(a.Addr()), a.String())
	}
	if !target.Localhost || !spec.DualStack || network != "udp" {
		conn, err := bindFirst(target.Addrs, spec.Port, network, bind)
		if err != nil {
			return nil, Info{}, err
		}
		return []net.PacketConn{conn}, info(target.Scope, conn.LocalAddr()), nil
	}
	const attempts = 5
	for try := 1; ; try++ {
		first, err := bindFirst(target.Addrs, spec.Port, network, bind)
		if err != nil {
			return nil, Info{}, err
		}
		conns := []net.PacketConn{first}
		bound := addrPort(first.LocalAddr())
		var retry bool
		var failed error
		for _, a := range target.Addrs {
			if a == bound.Addr() {
				continue
			}
			c, err := bind(netip.AddrPortFrom(a, bound.Port()))
			switch {
			case err == nil:
				conns = append(conns, c)
				continue
			case isErrno(err, unavailableErrnos):
				continue // this family is not available here
			case spec.Port == 0 && isErrno(err, inUseErrnos) && try < attempts:
				retry = true
			default:
				failed = wrapBindError(err, network, netip.AddrPortFrom(a, bound.Port()).String(), spec.Port)
			}
			break
		}
		if retry || failed != nil {
			for _, c := range conns {
				c.Close()
			}
			if failed != nil {
				return nil, Info{}, failed
			}
			continue
		}
		addrs := make([]net.Addr, len(conns))
		for i, c := range conns {
			addrs[i] = c.LocalAddr()
		}
		return conns, info(target.Scope, addrs...), nil
	}
}

// prepare validates spec and enforces the LAN opt-in.
func prepare(ctx context.Context, network string, spec Spec) (Target, error) {
	if err := ctx.Err(); err != nil {
		return Target{}, wrapBindError(err, network, net.JoinHostPort(spec.Host, strconv.Itoa(spec.Port)), spec.Port)
	}
	if spec.Port < 0 || spec.Port > 65535 {
		return Target{}, invalidPort(spec.Port)
	}
	target, err := ParseHost(network, spec.Host)
	if err != nil {
		return Target{}, err
	}
	if target.Scope != ScopeLoopback && !spec.AllowLAN {
		return Target{}, lanNotAllowed(network, netip.AddrPortFrom(target.Addrs[0], uint16(spec.Port)).String(), target.Scope)
	}
	return target, nil
}

// bindFirst binds the candidates in order and returns the first that works.
// A candidate whose address family or address is unavailable is skipped
// while others remain; any other error ends the search.
func bindFirst[T any](addrs []netip.Addr, port int, network string, bind func(netip.AddrPort) (T, error)) (T, error) {
	var zero T
	for i, a := range addrs {
		ap := netip.AddrPortFrom(a, uint16(port))
		ln, err := bind(ap)
		if err == nil {
			return ln, nil
		}
		if i == len(addrs)-1 || !isErrno(err, unavailableErrnos) {
			return zero, wrapBindError(err, network, ap.String(), port)
		}
	}
	return zero, errs.New(errs.Internal, "No address to listen on.") // ParseHost never returns none
}

func info(scope Scope, addrs ...net.Addr) Info {
	in := Info{Scope: scope}
	for _, a := range addrs {
		in.Addrs = append(in.Addrs, addrPort(a))
	}
	switch scope {
	case ScopeLAN:
		in.Warning = &Warning{
			Code: WarnLANExposed,
			Message: fmt.Sprintf("Listening on %s: other devices on the networks this address is on can connect.",
				in.Addrs[0]),
		}
	case ScopeAllInterfaces:
		in.Warning = &Warning{
			Code: WarnAllInterfacesExposed,
			Message: fmt.Sprintf("Listening on all network interfaces (%s): any device that can reach this computer can connect, including on public Wi-Fi.",
				in.Addrs[0]),
		}
	}
	return in
}

func addrPort(a net.Addr) netip.AddrPort {
	var ap netip.AddrPort
	switch a := a.(type) {
	case *net.TCPAddr:
		ap = a.AddrPort()
	case *net.UDPAddr:
		ap = a.AddrPort()
	default:
		ap, _ = netip.ParseAddrPort(a.String())
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// family returns 4, 6 or 0 (both) for a network name.
func family(network string) (int, error) {
	switch network {
	case "", "tcp", "udp":
		return 0, nil
	case "tcp4", "udp4":
		return 4, nil
	case "tcp6", "udp6":
		return 6, nil
	}
	return 0, invalidNetwork(network, "tcp")
}

func familyOf(a netip.Addr) string {
	if a.Is4() {
		return "4"
	}
	return "6"
}

func closeAll(lns []net.Listener) {
	for _, ln := range lns {
		ln.Close()
	}
}
