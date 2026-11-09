package listen

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

var (
	v4Loopback = netip.MustParseAddr("127.0.0.1")
	v6Loopback = netip.MustParseAddr("::1")
)

func TestDefaultBindIsLoopback(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"", "localhost", "LocalHost", " localhost ", "localhost."} {
		t.Run(strconv.Quote(host), func(t *testing.T) {
			t.Parallel()
			ln, in := mustListen(t, Spec{Host: host})
			wantLoopback(t, in, v4Loopback)
			if got := addrPort(ln.Addr()); got != in.Addrs[0] {
				t.Errorf("Addr = %v, Info.Addrs[0] = %v", got, in.Addrs[0])
			}
			roundTrip(t, ln, ln.Addr().String())
		})
	}
}

func TestZeroSpecIsLoopbackEphemeral(t *testing.T) {
	t.Parallel()
	ln, in := mustListen(t, Spec{})
	wantLoopback(t, in, v4Loopback)
	if ln.Addr().Network() != "tcp" {
		t.Errorf("network = %q, want tcp", ln.Addr().Network())
	}
}

func TestExplicitLoopback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		host string
		want netip.Addr
		ipv6 bool
	}{
		{host: "127.0.0.1", want: v4Loopback},
		{host: "::ffff:127.0.0.1", want: v4Loopback},
		{host: "[::ffff:127.0.0.1]", want: v4Loopback},
		{host: "::1", want: v6Loopback, ipv6: true},
		{host: "[::1]", want: v6Loopback, ipv6: true},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			if tt.ipv6 {
				skipWithoutIPv6(t)
			}
			ln, in := mustListen(t, Spec{Host: tt.host})
			wantLoopback(t, in, tt.want)
			roundTrip(t, ln, ln.Addr().String())
		})
	}
}

func TestNetworkFamily(t *testing.T) {
	t.Parallel()
	t.Run("tcp6 localhost is ::1", func(t *testing.T) {
		t.Parallel()
		skipWithoutIPv6(t)
		_, in := mustListen(t, Spec{Network: "tcp6", Host: "localhost"})
		wantLoopback(t, in, v6Loopback)
	})
	t.Run("tcp4 localhost is 127.0.0.1", func(t *testing.T) {
		t.Parallel()
		_, in := mustListen(t, Spec{Network: "tcp4", Host: "localhost", DualStack: true})
		wantLoopback(t, in, v4Loopback)
		if len(in.Addrs) != 1 {
			t.Errorf("Addrs = %v, want one: DualStack is ignored for tcp4", in.Addrs)
		}
	})
	t.Run("family mismatch", func(t *testing.T) {
		t.Parallel()
		_, _, err := Listen(t.Context(), Spec{Network: "tcp4", Host: "::1"})
		wantErr(t, err, errs.InvalidArgument, CodeInvalidHost)
	})
}

// TestNonLoopbackNeedsOptIn is the core rule: nothing but loopback binds
// without AllowLAN, for TCP and UDP. No socket is opened for these.
func TestNonLoopbackNeedsOptIn(t *testing.T) {
	t.Parallel()
	hosts := []struct {
		host  string
		scope Scope
	}{
		{"0.0.0.0", ScopeAllInterfaces},
		{"::", ScopeAllInterfaces},
		{"[::]", ScopeAllInterfaces},
		{"::ffff:0.0.0.0", ScopeAllInterfaces},
		{"192.168.1.10", ScopeLAN},
		{"10.0.0.1", ScopeLAN},
		{"169.254.10.20", ScopeLAN},
		{"fe80::1%lo0", ScopeLAN},
		{"fd00::1", ScopeLAN},
		{"8.8.8.8", ScopeLAN},
		{"::ffff:192.168.1.10", ScopeLAN},
	}
	if lan, ok := lanAddr(); ok {
		hosts = append(hosts, struct {
			host  string
			scope Scope
		}{lan.String(), ScopeLAN})
	}
	for _, h := range hosts {
		t.Run(h.host, func(t *testing.T) {
			t.Parallel()
			ln, in, err := Listen(t.Context(), Spec{Host: h.host, Port: 0})
			if ln != nil {
				ln.Close()
				t.Fatalf("Listen bound %v without AllowLAN", in.Addrs)
			}
			e := wantErr(t, err, errs.PermissionDenied, CodeLANNotAllowed)
			if e.Details["scope"] != string(h.scope) || e.Hint != HintLANNotAllowed {
				t.Errorf("details, hint = %v, %q", e.Details, e.Hint)
			}

			pc, in, err := ListenPacket(t.Context(), Spec{Host: h.host})
			if pc != nil {
				pc.Close()
				t.Fatalf("ListenPacket bound %v without AllowLAN", in.Addrs)
			}
			wantErr(t, err, errs.PermissionDenied, CodeLANNotAllowed)
		})
	}
}

func TestAllowLANAllInterfaces(t *testing.T) {
	t.Parallel()
	tests := []struct {
		host string
		want netip.Addr
		ipv6 bool
	}{
		{host: "0.0.0.0", want: netip.IPv4Unspecified()},
		{host: "::", want: netip.IPv6Unspecified(), ipv6: true},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			if tt.ipv6 {
				skipWithoutIPv6(t)
			}
			ln, in := mustListen(t, Spec{Host: tt.host, AllowLAN: true})
			if in.Scope != ScopeAllInterfaces || in.IsLoopback() || len(in.Addrs) != 1 || in.Addrs[0].Addr() != tt.want || in.Port() == 0 {
				t.Errorf("Info = %+v, want all interfaces on %v with a port", in, tt.want)
			}
			if in.Warning == nil || in.Warning.Code != WarnAllInterfacesExposed || in.Warning.Message == "" {
				t.Errorf("Warning = %+v, want %s", in.Warning, WarnAllInterfacesExposed)
			}
			loop := v4Loopback
			if tt.ipv6 {
				loop = v6Loopback
			}
			roundTrip(t, ln, netip.AddrPortFrom(loop, uint16(in.Port())).String())
		})
	}
}

func TestAllowLANInterfaceAddress(t *testing.T) {
	t.Parallel()
	lan, ok := lanAddr()
	if !ok {
		t.Skip("no non-loopback IPv4 interface address")
	}
	ln, in := mustListen(t, Spec{Host: lan.String(), AllowLAN: true})
	if in.Scope != ScopeLAN || in.IsLoopback() || in.Addrs[0].Addr() != lan {
		t.Errorf("Info = %+v, want LAN scope on %v", in, lan)
	}
	if in.Warning == nil || in.Warning.Code != WarnLANExposed || in.Warning.Message == "" {
		t.Errorf("Warning = %+v, want %s", in.Warning, WarnLANExposed)
	}
	if got := addrPort(ln.Addr()); got != in.Addrs[0] || in.Port() == 0 {
		t.Errorf("Addr = %v, Info.Addrs = %v", got, in.Addrs)
	}
	// No connection test: a firewall or macOS local network privacy may drop
	// connections to an interface address; the bind itself is what matters.
}

func TestAllowLANForeignAddress(t *testing.T) {
	t.Parallel()
	// 192.0.2.1 (TEST-NET-1) is on no interface of a test machine.
	_, _, err := Listen(t.Context(), Spec{Host: "192.0.2.1", AllowLAN: true})
	e := wantErr(t, err, errs.Network, CodeAddressUnavailable)
	if e.Details["address"] != "192.0.2.1:0" || e.Details["network"] != "tcp" {
		t.Errorf("details = %v", e.Details)
	}
}

func TestPorts(t *testing.T) {
	t.Parallel()
	t.Run("ephemeral", func(t *testing.T) {
		t.Parallel()
		ln, in := mustListen(t, Spec{})
		if in.Port() == 0 || int(addrPort(ln.Addr()).Port()) != in.Port() {
			t.Errorf("Port = %d, Addr = %v", in.Port(), ln.Addr())
		}
	})
	t.Run("in use", func(t *testing.T) {
		t.Parallel()
		_, in := mustListen(t, Spec{})
		ln, _, err := Listen(t.Context(), Spec{Port: in.Port()})
		if ln != nil {
			ln.Close()
		}
		e := wantErr(t, err, errs.Network, CodeAddressInUse)
		if want := netip.AddrPortFrom(v4Loopback, uint16(in.Port())).String(); e.Details["address"] != want {
			t.Errorf("address = %q, want %q", e.Details["address"], want)
		}
		if e.Hint != HintAddressInUse {
			t.Errorf("hint = %q", e.Hint)
		}
	})
	t.Run("explicit", func(t *testing.T) {
		t.Parallel()
		// Find a free port, release it and bind it by number.
		ln, in := mustListen(t, Spec{})
		port := in.Port()
		ln.Close()
		ln2, in2, err := Listen(t.Context(), Spec{Port: port})
		if err != nil {
			t.Skipf("port %d was taken again: %v", port, err)
		}
		defer ln2.Close()
		if in2.Port() != port {
			t.Errorf("Port = %d, want %d", in2.Port(), port)
		}
	})
	for _, port := range []int{-1, 65536} {
		t.Run("invalid "+strconv.Itoa(port), func(t *testing.T) {
			t.Parallel()
			_, _, err := Listen(t.Context(), Spec{Port: port})
			wantErr(t, err, errs.InvalidArgument, CodeInvalidPort)
			_, _, err = ListenPacket(t.Context(), Spec{Port: port})
			wantErr(t, err, errs.InvalidArgument, CodeInvalidPort)
		})
	}
}

func TestRejectsHostNames(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"example.com", "my-laptop.local", "localhost.localdomain", "127.0.0.1:8080", "[::1]:80", "256.0.0.1", "local host", "*"} {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			// AllowLAN does not make a host name acceptable.
			_, _, err := Listen(t.Context(), Spec{Host: host, AllowLAN: true})
			e := wantErr(t, err, errs.InvalidArgument, CodeInvalidHost)
			if e.Details["host"] != host || e.Hint != HintInvalidHost {
				t.Errorf("details, hint = %v, %q", e.Details, e.Hint)
			}
			_, _, err = ListenPacket(t.Context(), Spec{Host: host, AllowLAN: true})
			wantErr(t, err, errs.InvalidArgument, CodeInvalidHost)
		})
	}
}

func TestInvalidNetwork(t *testing.T) {
	t.Parallel()
	for _, nw := range []string{"udp", "unix", "ip", "TCP"} {
		_, _, err := Listen(t.Context(), Spec{Network: nw})
		wantErr(t, err, errs.InvalidArgument, CodeInvalidNetwork)
	}
	for _, nw := range []string{"tcp", "unixgram"} {
		_, _, err := ListenPacket(t.Context(), Spec{Network: nw})
		wantErr(t, err, errs.InvalidArgument, CodeInvalidNetwork)
	}
}

func TestParseHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		network, host string
		want          []netip.Addr
		scope         Scope
		localhost     bool
	}{
		{"tcp", "", []netip.Addr{v4Loopback, v6Loopback}, ScopeLoopback, true},
		{"udp4", "localhost", []netip.Addr{v4Loopback}, ScopeLoopback, true},
		{"tcp6", "localhost", []netip.Addr{v6Loopback}, ScopeLoopback, true},
		{"tcp", "127.1.2.3", []netip.Addr{netip.MustParseAddr("127.1.2.3")}, ScopeLoopback, false},
		{"tcp", "::ffff:127.0.0.1", []netip.Addr{v4Loopback}, ScopeLoopback, false},
		{"tcp4", "::ffff:127.0.0.1", []netip.Addr{v4Loopback}, ScopeLoopback, false},
		{"tcp", "0.0.0.0", []netip.Addr{netip.IPv4Unspecified()}, ScopeAllInterfaces, false},
		{"tcp", "::", []netip.Addr{netip.IPv6Unspecified()}, ScopeAllInterfaces, false},
		{"tcp", "192.168.0.5", []netip.Addr{netip.MustParseAddr("192.168.0.5")}, ScopeLAN, false},
		{"tcp", "fe80::1%en0", []netip.Addr{netip.MustParseAddr("fe80::1%en0")}, ScopeLAN, false},
		{"tcp", "2001:db8::1", []netip.Addr{netip.MustParseAddr("2001:db8::1")}, ScopeLAN, false},
	}
	for _, tt := range tests {
		got, err := ParseHost(tt.network, tt.host)
		if err != nil {
			t.Errorf("ParseHost(%q, %q): %v", tt.network, tt.host, err)
			continue
		}
		if !slices.Equal(got.Addrs, tt.want) || got.Scope != tt.scope || got.Localhost != tt.localhost {
			t.Errorf("ParseHost(%q, %q) = %+v, want %v %s localhost=%v", tt.network, tt.host, got, tt.want, tt.scope, tt.localhost)
		}
	}
}

func TestDualStack(t *testing.T) {
	t.Parallel()
	ln, in := mustListen(t, Spec{Host: "localhost", DualStack: true})
	if !hasIPv6() {
		if len(in.Addrs) != 1 || in.Addrs[0].Addr() != v4Loopback {
			t.Fatalf("Addrs = %v, want only 127.0.0.1 without IPv6", in.Addrs)
		}
		return
	}
	if len(in.Addrs) != 2 || in.Addrs[0].Addr() != v4Loopback || in.Addrs[1].Addr() != v6Loopback ||
		in.Addrs[0].Port() != in.Addrs[1].Port() || in.Port() == 0 {
		t.Fatalf("Addrs = %v, want 127.0.0.1 and ::1 on one port", in.Addrs)
	}
	wantLoopback(t, in, v4Loopback)
	if got := addrPort(ln.Addr()); got != in.Addrs[0] {
		t.Errorf("Addr = %v, want %v", got, in.Addrs[0])
	}
	for _, a := range in.Addrs {
		roundTrip(t, ln, a.String())
	}

	if err := ln.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if c, err := ln.Accept(); c != nil || !errors.Is(err, net.ErrClosed) {
		t.Errorf("Accept after Close = %v, %v, want net.ErrClosed", c, err)
	}
	for _, a := range in.Addrs {
		if c, err := net.DialTimeout("tcp", a.String(), time.Second); err == nil {
			c.Close()
			t.Errorf("%v still accepts after Close", a)
		}
	}
}

func TestDualStackExplicitPortInUse(t *testing.T) {
	t.Parallel()
	skipWithoutIPv6(t)
	// Take the port on ::1 only; DualStack must fail rather than silently
	// serve IPv4 alone on an explicit port.
	_, in := mustListen(t, Spec{Host: "::1"})
	ln, _, err := Listen(t.Context(), Spec{Port: in.Port(), DualStack: true})
	if err == nil {
		ln.Close()
		t.Skipf("port %d was free on 127.0.0.1 and ::1 at once (IPv6 socket bound dual-stack?)", in.Port())
	}
	wantErr(t, err, errs.Network, CodeAddressInUse)
}

func TestListenPacket(t *testing.T) {
	t.Parallel()
	pc, in, err := ListenPacket(t.Context(), Spec{})
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()
	wantLoopback(t, in, v4Loopback)
	if pc.LocalAddr().Network() != "udp" {
		t.Errorf("network = %q", pc.LocalAddr().Network())
	}

	c, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 16)
	n, from, err := pc.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "ping" || from.String() != c.LocalAddr().String() {
		t.Errorf("ReadFrom = %q from %v, %v", buf[:n], from, err)
	}

	t.Run("ipv6", func(t *testing.T) {
		t.Parallel()
		skipWithoutIPv6(t)
		pc, in, err := ListenPacket(t.Context(), Spec{Network: "udp6"})
		if err != nil {
			t.Fatal(err)
		}
		pc.Close()
		wantLoopback(t, in, v6Loopback)
	})
	t.Run("all interfaces with opt-in", func(t *testing.T) {
		t.Parallel()
		pc, in, err := ListenPacket(t.Context(), Spec{Host: "0.0.0.0", AllowLAN: true})
		if err != nil {
			t.Fatal(err)
		}
		pc.Close()
		if in.Scope != ScopeAllInterfaces || in.Warning == nil || in.Warning.Code != WarnAllInterfacesExposed {
			t.Errorf("Info = %+v", in)
		}
	})
	t.Run("dual stack", func(t *testing.T) {
		t.Parallel()
		_, _, err := ListenPacket(t.Context(), Spec{DualStack: true})
		wantErr(t, err, errs.Unsupported, CodeDualStackUnsupported)
	})
}

// TestListenPackets: DualStack binds one PacketConn per loopback family on
// the same port, and both receive; an IP literal gets one conn.
func TestListenPackets(t *testing.T) {
	t.Parallel()
	conns, in, err := ListenPackets(t.Context(), Spec{Host: "localhost", DualStack: true})
	if err != nil {
		t.Fatalf("ListenPackets: %v", err)
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	if len(conns) != len(in.Addrs) || in.Scope != ScopeLoopback || in.Addrs[0].Addr() != v4Loopback {
		t.Fatalf("conns %d, Info %+v", len(conns), in)
	}
	for i, c := range conns {
		if in.Addrs[i].Port() != in.Addrs[0].Port() {
			t.Errorf("Addrs = %v: not one port", in.Addrs)
		}
		peer, err := net.Dial("udp", c.LocalAddr().String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 16)
		n, _, err := c.ReadFrom(buf)
		peer.Close()
		if err != nil || string(buf[:n]) != "ping" {
			t.Errorf("conn %d: ReadFrom = %q, %v", i, buf[:n], err)
		}
	}

	one, in, err := ListenPackets(t.Context(), Spec{Host: "127.0.0.1", DualStack: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range one {
		c.Close()
	}
	if len(one) != 1 || len(in.Addrs) != 1 {
		t.Errorf("an IP literal bound %d conns, %v", len(one), in.Addrs)
	}
	_, _, err = ListenPackets(t.Context(), Spec{Network: "tcp"})
	wantErr(t, err, errs.InvalidArgument, CodeInvalidNetwork)
}

func TestCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := Listen(ctx, Spec{})
	wantErr(t, err, errs.Cancelled, CodeCancelled)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false: %v", err)
	}
	_, _, err = ListenPacket(ctx, Spec{})
	wantErr(t, err, errs.Cancelled, CodeCancelled)
}

func TestWrapBindError(t *testing.T) {
	t.Parallel()
	op := func(errno error) error {
		return &net.OpError{Op: "listen", Net: "tcp4", Err: os.NewSyscallError("bind", errno)}
	}
	tests := []struct {
		name     string
		err      error
		port     int
		wantKind errs.Kind
		wantCode string
		wantHint string
	}{
		{"in use", op(inUseErrnos[0]), 8080, errs.Network, CodeAddressInUse, HintAddressInUse},
		{"unavailable", op(unavailableErrnos[0]), 8080, errs.Network, CodeAddressUnavailable, HintAddressUnavailable},
		{"privileged", op(accessErrnos[0]), 80, errs.PermissionDenied, CodeBindDenied, HintPrivilegedPort},
		{"denied", op(accessErrnos[0]), 8080, errs.PermissionDenied, CodeBindDenied, HintBindDenied},
		{"cancelled", context.Canceled, 0, errs.Cancelled, CodeCancelled, ""},
		{"deadline", context.DeadlineExceeded, 0, errs.Timeout, CodeTimeout, ""},
		{"other", errors.New("boom"), 0, errs.Network, CodeFailed, ""},
	}
	for _, tt := range tests {
		e := wrapBindError(tt.err, "tcp", "127.0.0.1:1", tt.port)
		if e.Kind != tt.wantKind || e.Code != tt.wantCode || e.Hint != tt.wantHint {
			t.Errorf("%s: kind, code, hint = %q, %q, %q, want %q, %q, %q", tt.name, e.Kind, e.Code, e.Hint, tt.wantKind, tt.wantCode, tt.wantHint)
		}
		if !errors.Is(e, tt.err) || e.Details["address"] != "127.0.0.1:1" || e.Details["network"] != "tcp" {
			t.Errorf("%s: cause or details lost: %v %v", tt.name, e, e.Details)
		}
	}
}

// helpers

func mustListen(t *testing.T, spec Spec) (net.Listener, Info) {
	t.Helper()
	ln, in, err := Listen(t.Context(), spec)
	if err != nil {
		t.Fatalf("Listen(%+v): %v", spec, err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln, in
}

func wantLoopback(t *testing.T, in Info, want netip.Addr) {
	t.Helper()
	if !in.IsLoopback() || in.Scope != ScopeLoopback || in.Warning != nil {
		t.Errorf("Info = %+v, want loopback without a warning", in)
	}
	if len(in.Addrs) == 0 || in.Addrs[0].Addr() != want || in.Port() == 0 {
		t.Errorf("Addrs = %v, want %v with a port", in.Addrs, want)
	}
}

// roundTrip dials address and checks ln accepts that connection.
func roundTrip(t *testing.T, ln net.Listener, address string) {
	t.Helper()
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		ch <- result{c, err}
	}()
	c, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	defer c.Close()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Accept: %v", r.err)
		}
		defer r.c.Close()
		if r.c.RemoteAddr().String() != c.LocalAddr().String() {
			t.Errorf("accepted %v, dialed from %v", r.c.RemoteAddr(), c.LocalAddr())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no connection accepted from %s", address)
	}
}

func hasIPv6() bool {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func skipWithoutIPv6(t *testing.T) {
	t.Helper()
	if !hasIPv6() {
		t.Skip("IPv6 loopback unavailable")
	}
}

// lanAddr returns an IPv4 address of an up, non-loopback interface.
func lanAddr() (netip.Addr, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return netip.Addr{}, false
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if ip = ip.Unmap(); ok && ip.Is4() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return ip, true
			}
		}
	}
	return netip.Addr{}, false
}

// wantErr checks that err is an *errs.Error itself with kind and code.
func wantErr(t *testing.T, err error, kind errs.Kind, code string) *errs.Error {
	t.Helper()
	e, ok := err.(*errs.Error)
	if !ok {
		t.Fatalf("error = %T %v, want *errs.Error", err, err)
	}
	if e.Kind != kind || e.Code != code {
		t.Fatalf("error kind, code = %q, %q, want %q, %q (%v)", e.Kind, e.Code, kind, code, err)
	}
	if got := errs.KindOf(err); got != kind {
		t.Errorf("errs.KindOf = %q, want %q", got, kind)
	}
	return e
}
