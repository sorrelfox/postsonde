package netx

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx/netxtest"
)

// udpEcho answers every datagram on 127.0.0.1 with "echo:" and the
// datagram, from the same socket, or with otherPort from a second socket
// (as a server that answers from another port). It returns the address
// datagrams go to and the address replies come from.
func udpEcho(t *testing.T, otherPort bool) (addr, from string) {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	reply := pc
	if otherPort {
		if reply, err = net.ListenPacket("udp4", "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reply.Close() })
	}
	setUDPBuffers(pc.(net.Conn))
	setUDPBuffers(reply.(net.Conn))
	go func() {
		buf := make([]byte, 70000)
		for {
			n, peer, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = reply.WriteTo(append([]byte("echo:"), buf[:n]...), peer)
		}
	}()
	return pc.LocalAddr().String(), reply.LocalAddr().String()
}

// readDatagram reads one datagram within d.
func readDatagram(t *testing.T, c net.Conn, d time.Duration) (string, net.Addr, error) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 70000)
	dc, ok := c.(DatagramConn)
	if !ok {
		t.Fatalf("%T is not a DatagramConn", c)
	}
	n, from, err := dc.ReadFrom(buf)
	return string(buf[:n]), from, err
}

func udpRoundTrip(t *testing.T, c net.Conn, msg string) (string, net.Addr) {
	t.Helper()
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	got, from, err := readDatagram(t, c, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return got, from
}

func TestDirectUDP(t *testing.T) {
	t.Parallel()
	addr, _ := udpEcho(t, false)
	for _, unconnected := range []bool{false, true} {
		ctx := t.Context()
		if unconnected {
			ctx = UnconnectedUDP(ctx)
		}
		c, err := NewDirect(Options{}).DialContext(ctx, "udp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if got, from := udpRoundTrip(t, c, "ping"); got != "echo:ping" || from.String() != addr {
			t.Errorf("unconnected=%v: %q from %v", unconnected, got, from)
		}
		// A datagram near the IPv4 limit goes through (macOS's 9 KiB
		// default send buffer is raised).
		big := strings.Repeat("x", 60000)
		if got, _ := udpRoundTrip(t, c, big); got != "echo:"+big {
			t.Errorf("unconnected=%v: big datagram came back as %d bytes", unconnected, len(got))
		}
		if m := MaxDatagramPayload(c); m != MaxUDPPayloadIPv4 {
			t.Errorf("MaxDatagramPayload = %d", m)
		}
		if IsConnectedUDP(c) == unconnected {
			t.Errorf("IsConnectedUDP = %v, unconnected %v", IsConnectedUDP(c), unconnected)
		}
		if !strings.HasPrefix(c.LocalAddr().String(), "127.0.0.1:") || c.RemoteAddr().String() != addr {
			t.Errorf("addresses %v -> %v", c.LocalAddr(), c.RemoteAddr())
		}
	}
}

// TestDirectUDPOtherSender: only an unconnected socket reads a reply from
// another port than the one datagrams go to.
func TestDirectUDPOtherSender(t *testing.T) {
	t.Parallel()
	addr, from := udpEcho(t, true)
	connected, err := NewDirect(Options{}).DialContext(t.Context(), "udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer connected.Close()
	if _, err := connected.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if got, _, err := readDatagram(t, connected, 200*time.Millisecond); err == nil {
		t.Errorf("connected socket read %q from another port", got)
	}

	c, err := NewDirect(Options{}).DialContext(UnconnectedUDP(t.Context()), "udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got, peer := udpRoundTrip(t, c, "b"); got != "echo:b" || peer.String() != from {
		t.Errorf("unconnected: %q from %v, want from %s", got, peer, from)
	}
}

// TestDirectUDPGuard: the guard sees the resolved target of an
// unconnected dial too, and refuses it.
func TestDirectUDPGuard(t *testing.T) {
	t.Parallel()
	addr, _ := udpEcho(t, false)
	d := NewDirect(Options{Guard: func(_ context.Context, c DialCheck) error { return Deny(c, "blocked") }})
	_, err := d.DialContext(UnconnectedUDP(t.Context()), "udp", addr)
	if codeOf(err) != CodeDialDenied {
		t.Errorf("err = %v", err)
	}
}

func socksUDPDialer(t *testing.T, p *netxtest.SOCKS5Server, cfg SOCKS5Config) *SOCKS5 {
	t.Helper()
	cfg.Address = p.Start(t)
	s, err := NewSOCKS5(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSOCKS5UDPAssociate (SOCKS-07): datagrams round-trip through the
// proxy's relay, with the target's name sent unresolved for socks5h and an
// IP address for socks5 (with authentication), and the hop says how.
func TestSOCKS5UDPAssociate(t *testing.T) {
	t.Parallel()
	addr, _ := udpEcho(t, false)
	_, port, _ := net.SplitHostPort(addr)
	tests := []struct {
		name   string
		proxy  *netxtest.SOCKS5Server
		cfg    SOCKS5Config
		target string
		header int // size of the SOCKS UDP header
	}{
		{"socks5h", &netxtest.SOCKS5Server{Hosts: map[string]string{"echo.test": "127.0.0.1"}}, SOCKS5Config{RemoteDNS: true}, "echo.test:" + port, 3 + 1 + 1 + len("echo.test") + 2},
		{"socks5 with auth", &netxtest.SOCKS5Server{Username: "u", Password: "p"}, SOCKS5Config{Username: "u", Password: "p"}, addr, 10},
		{"relay on the proxy's address", &netxtest.SOCKS5Server{UDPBindUnspecified: true}, SOCKS5Config{}, addr, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := socksUDPDialer(t, tt.proxy, tt.cfg)
			rec := &recorder{}
			c, err := s.DialContext(WithTrace(t.Context(), rec.trace()), "udp", tt.target)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			for _, msg := range []string{"one", "two", strings.Repeat("z", 30000)} {
				if got, from := udpRoundTrip(t, c, msg); got != "echo:"+msg || from.String() != addr {
					t.Errorf("%d bytes back from %v", len(got), from)
				}
			}
			if ViaOf(c) != s.Via() {
				t.Errorf("ViaOf = %q", ViaOf(c))
			}
			if got, want := MaxDatagramPayload(c), MaxUDPPayloadIPv4-tt.header; got != want {
				t.Errorf("MaxDatagramPayload = %d, want %d", got, want)
			}
			dgs := tt.proxy.UDPDatagrams()
			if len(dgs) != 3 || dgs[0].Name != tt.cfg.RemoteDNS {
				t.Errorf("relay saw %+v", dgs)
			}
			_, dones := rec.events()
			last := dones[len(dones)-1]
			if last.Proxy == nil || last.Proxy.Command != CommandUDPAssociate || !strings.HasPrefix(last.Proxy.RelayAddr, "127.0.0.1:") ||
				!last.Proxy.Replied || last.Proxy.Reply != 0 {
				t.Fatalf("hop = %+v", last.Proxy)
			}
			if str := last.Proxy.String(); !strings.Contains(str, "UDP ASSOCIATE, relay 127.0.0.1:") {
				t.Errorf("hop = %s", str)
			}
		})
	}
}

// TestSOCKS5UDPConnectedFilter: connected, the reply of another sender
// than the target is dropped; unconnected, it is read with its sender.
func TestSOCKS5UDPConnectedFilter(t *testing.T) {
	t.Parallel()
	addr, from := udpEcho(t, true)
	s := socksUDPDialer(t, &netxtest.SOCKS5Server{}, SOCKS5Config{})
	c, err := s.DialContext(t.Context(), "udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if got, _, err := readDatagram(t, c, 300*time.Millisecond); err == nil {
		t.Errorf("connected read %q from another sender", got)
	}

	u, err := s.DialContext(UnconnectedUDP(t.Context()), "udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	if IsConnectedUDP(u) {
		t.Error("IsConnectedUDP of an unconnected dial")
	}
	if got, peer := udpRoundTrip(t, u, "b"); got != "echo:b" || peer.String() != from {
		t.Errorf("%q from %v, want from %s", got, peer, from)
	}
}

// TestSOCKS5UDPFragmentDropped: a fragment from the relay is dropped with
// an error IsDroppedDatagram knows; the next datagram is read.
func TestSOCKS5UDPFragmentDropped(t *testing.T) {
	t.Parallel()
	addr, _ := udpEcho(t, false)
	s := socksUDPDialer(t, &netxtest.SOCKS5Server{UDPFragmentFirst: true}, SOCKS5Config{})
	c, err := s.DialContext(t.Context(), "udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	_, _, err = readDatagram(t, c, 5*time.Second)
	if !IsDroppedDatagram(err) || codeOf(err) != CodeSOCKSUDPFragment {
		t.Fatalf("first read: %v", err)
	}
	got, _, err := readDatagram(t, c, 5*time.Second)
	if err != nil || got != "echo:x" {
		t.Errorf("second read: %q, %v", got, err)
	}
}

// TestSOCKS5UDPParse: headers the relay may send, and malformed ones.
func TestSOCKS5UDPParse(t *testing.T) {
	t.Parallel()
	c := &socksUDPConn{network: "udp", address: "x:1"}
	tests := []struct {
		in      []byte
		payload string
		from    string
		code    string
	}{
		{[]byte{0, 0, 0, 1, 10, 0, 0, 1, 0, 53, 'h', 'i'}, "hi", "10.0.0.1:53", ""},
		{append([]byte{0, 0, 0, 4}, append(bytes.Repeat([]byte{0}, 15), 1, 0x1f, 0x90, 'v', '6')...), "v6", "[::1]:8080", ""},
		{[]byte{0, 0, 0, 3, 4, 'h', 'o', 's', 't', 0, 7, '!'}, "!", "host:7", ""},
		{[]byte{0, 0, 0, 1, 10, 0, 0, 1, 0, 53}, "", "10.0.0.1:53", ""},
		{[]byte{0, 0, 2, 1, 10, 0, 0, 1, 0, 53, 'x'}, "", "", CodeSOCKSUDPFragment},
		{[]byte{0, 0, 0}, "", "", CodeSOCKSUDPMalformed},
		{[]byte{0, 0, 0, 1, 10, 0}, "", "", CodeSOCKSUDPMalformed},
		{[]byte{0, 0, 0, 3, 9, 'a'}, "", "", CodeSOCKSUDPMalformed},
		{[]byte{0, 0, 0, 7, 1, 2, 3}, "", "", CodeSOCKSUDPMalformed},
	}
	for _, tt := range tests {
		payload, from, err := c.parse(tt.in)
		if tt.code != "" {
			if codeOf(err) != tt.code || !IsDroppedDatagram(err) {
				t.Errorf("%v: err = %v, want %s", tt.in, err, tt.code)
			}
			continue
		}
		if err != nil || string(payload) != tt.payload || from.String() != tt.from {
			t.Errorf("%v: %q from %v, %v", tt.in, payload, from, err)
		}
	}
}

// TestSOCKS5UDPRefused: a proxy without UDP support answers reply 7, which
// reads as UDP ASSOCIATE being unsupported, never going direct.
func TestSOCKS5UDPRefused(t *testing.T) {
	t.Parallel()
	addr, _ := udpEcho(t, false)
	p := &netxtest.SOCKS5Server{NoUDP: true}
	s := socksUDPDialer(t, p, SOCKS5Config{})
	_, err := s.DialContext(t.Context(), "udp", addr)
	e, ok := errors.AsType[*errs.Error](err)
	if !ok || e.Code != CodeSOCKSCommandNotSupported || !strings.Contains(e.Message, "UDP ASSOCIATE") || e.Hint != HintSOCKSUDPNotSupported {
		t.Errorf("err = %#v", err)
	}
	if got := p.UDPDatagrams(); len(got) != 0 {
		t.Errorf("relayed %v", got)
	}
}

// TestSOCKS5UDPAssociationEnds: when the proxy closes the control
// connection, reads and writes fail with the association's end; Close
// ends the association on the proxy's side.
func TestSOCKS5UDPAssociationEnds(t *testing.T) {
	t.Parallel()
	addr, _ := udpEcho(t, false)
	p := &netxtest.SOCKS5Server{}
	s := socksUDPDialer(t, p, SOCKS5Config{})

	c, err := s.DialContext(t.Context(), "udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	udpRoundTrip(t, c, "hello")
	p.DropUDPAssociations()
	_, _, err = readDatagram(t, c, 5*time.Second)
	if codeOf(err) != CodeSOCKSUDPAssociationClosed {
		t.Errorf("read after the association ended: %v", err)
	}
	if _, err := c.Write([]byte("more")); codeOf(err) != CodeSOCKSUDPAssociationClosed {
		t.Errorf("write after the association ended: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	c, err = s.DialContext(t.Context(), "udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	udpRoundTrip(t, c, "again")
	if n := p.UDPAssociations(); n != 1 {
		t.Errorf("%d associations open", n)
	}
	done := make(chan struct{})
	go func() {
		// A blocked read returns when the connection is closed.
		_, _, _ = readDatagram(t, c, time.Minute)
		close(done)
	}()
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a read blocked after Close")
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.UDPAssociations() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the association is still open on the proxy after Close")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestUDPThroughOtherProxies: SOCKS4 and HTTP proxies cannot carry UDP.
func TestUDPThroughOtherProxies(t *testing.T) {
	t.Parallel()
	s4, err := NewSOCKS4(SOCKS4Config{Address: "127.0.0.1:1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s4.DialContext(t.Context(), "udp", "127.0.0.1:53"); codeOf(err) != CodeSOCKSUnsupportedNetwork {
		t.Errorf("socks4: %v", err)
	}
	hp, err := NewHTTPProxy(HTTPProxyConfig{Address: "127.0.0.1:1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hp.DialContext(t.Context(), "udp", "127.0.0.1:53"); codeOf(err) != CodeHTTPProxyUnsupportedNetwork {
		t.Errorf("http proxy: %v", err)
	}
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}
