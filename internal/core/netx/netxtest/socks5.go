package netxtest

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx/listen"
)

// SOCKS5Server is an in-process SOCKS5 proxy for tests (RFC 1928 CONNECT
// and UDP ASSOCIATE, RFC 1929 username/password). It records every
// CONNECT and every datagram relayed (UDPDatagrams), and in
// particular whether the client sent a host name (remote DNS, socks5h) or
// an IP address (socks5), so a test can prove that no name was resolved
// on the client's side.
//
// A CONNECT to an IP address is relayed to it. A host name is looked up in
// Hosts only, never in DNS, so tests stay off the network; a name that is
// not there gets reply 4 (host unreachable). Relayed connections are
// loopback in tests.
type SOCKS5Server struct {
	// Username and Password, when Username is set, are the only
	// credentials the proxy accepts, with the username/password method.
	// Without a Username it takes "no authentication".
	Username, Password string
	// Hosts maps the host names the proxy can "resolve" to IP addresses.
	Hosts map[string]string
	// NoUDP refuses UDP ASSOCIATE with reply 7 (command not supported),
	// as a proxy without UDP support (ssh -D) does.
	NoUDP bool
	// UDPBindUnspecified answers UDP ASSOCIATE with BND.ADDR 0.0.0.0 (the
	// relay is on the proxy's own address) instead of 127.0.0.1.
	UDPBindUnspecified bool
	// UDPFragmentFirst makes the relay send a fragment (FRAG 1) to the
	// client before each reply it relays, which the client must drop.
	UDPFragmentFirst bool

	addr     string
	accepted atomic.Int32

	mu       sync.Mutex
	requests []SOCKS5Request
	creds    []string
	// udp are the destinations of the datagrams relayed for clients;
	// assocs the control connections of open UDP associations.
	udp    []SOCKS5Request
	assocs map[net.Conn]bool
}

// SOCKS5Request is one CONNECT the server received.
type SOCKS5Request struct {
	// Name is true when the client sent a domain name (ATYP 3), false for
	// an IPv4 or IPv6 address.
	Name bool
	// Host is the name or the IP address, as sent.
	Host string
	Port int
}

// Start listens on 127.0.0.1 and serves until the test ends; it returns
// the proxy's host:port.
func (s *SOCKS5Server) Start(t testing.TB) string {
	t.Helper()
	ln, _, err := listen.Listen(context.Background(), listen.Spec{Host: "127.0.0.1"})
	if err != nil {
		t.Fatalf("socks5 test server: %v", err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	t.Cleanup(func() {
		close(done)
		ln.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				stop := make(chan struct{})
				defer close(stop)
				go func() {
					select {
					case <-done:
						c.Close()
					case <-stop:
					}
				}()
				s.serve(c, done)
			}()
		}
	}()
	s.addr = ln.Addr().String()
	return s.addr
}

// Addr is the proxy's host:port, once started.
func (s *SOCKS5Server) Addr() string { return s.addr }

// Accepted is the number of connections the proxy has accepted.
func (s *SOCKS5Server) Accepted() int { return int(s.accepted.Load()) }

// Requests returns the CONNECTs received so far, oldest first.
func (s *SOCKS5Server) Requests() []SOCKS5Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SOCKS5Request(nil), s.requests...)
}

// UDPDatagrams returns the destinations of the datagrams the relay got
// from clients, oldest first: Name is set when the client sent a host
// name (socks5h).
func (s *SOCKS5Server) UDPDatagrams() []SOCKS5Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SOCKS5Request(nil), s.udp...)
}

// UDPAssociations is the number of UDP associations open now.
func (s *SOCKS5Server) UDPAssociations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.assocs)
}

// DropUDPAssociations closes the control connection of every open UDP
// association, which ends it, as a proxy that restarts does.
func (s *SOCKS5Server) DropUDPAssociations() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.assocs {
		_ = c.Close()
	}
}

// Credentials returns the "user:password" pairs clients sent, oldest first.
func (s *SOCKS5Server) Credentials() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.creds...)
}

func (s *SOCKS5Server) serve(c net.Conn, stop <-chan struct{}) {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	var head [2]byte
	if _, err := io.ReadFull(c, head[:]); err != nil || head[0] != 5 {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	want := byte(0x00)
	if s.Username != "" {
		want = 0x02
	}
	method := byte(0xff)
	for _, m := range methods {
		if m == want {
			method = want
		}
	}
	if _, err := c.Write([]byte{5, method}); err != nil || method == 0xff {
		return
	}
	if method == 0x02 && !s.authenticate(c) {
		return
	}

	var req [3]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 || (req[1] != 1 && req[1] != 3) {
		return
	}
	r, ok := readSOCKS5Addr(c)
	if !ok {
		return
	}
	if req[1] == 3 {
		s.associate(c, stop)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, r)
	s.mu.Unlock()

	ip := r.Host
	if r.Name {
		ip = s.Hosts[r.Host]
	}
	var upstream net.Conn
	if ip != "" {
		var d net.Dialer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		upstream, _ = d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(r.Port)))
		cancel()
	}
	if upstream == nil {
		reply := byte(4) // host unreachable
		if ip != "" {
			reply = 5 // connection refused
		}
		_, _ = c.Write([]byte{5, reply, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	finished := make(chan struct{}, 2)
	relay := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		finished <- struct{}{}
	}
	go relay(upstream, c)
	go relay(c, upstream)
	for n := 0; n < 2; {
		select {
		case <-finished:
			n++
		case <-stop:
			// The test is over: unblock both copies.
			c.Close()
			upstream.Close()
			stop = nil
		}
	}
}

// readSOCKS5Addr reads ATYP, DST.ADDR and DST.PORT.
func readSOCKS5Addr(c io.Reader) (SOCKS5Request, bool) {
	var atyp [1]byte
	if _, err := io.ReadFull(c, atyp[:]); err != nil {
		return SOCKS5Request{}, false
	}
	r := SOCKS5Request{}
	switch atyp[0] {
	case 1, 4:
		ip := make([]byte, 4)
		if atyp[0] == 4 {
			ip = make([]byte, 16)
		}
		if _, err := io.ReadFull(c, ip); err != nil {
			return r, false
		}
		a, _ := netip.AddrFromSlice(ip)
		r.Host = a.String()
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return r, false
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return r, false
		}
		r.Name, r.Host = true, string(name)
	default:
		return r, false
	}
	var port [2]byte
	if _, err := io.ReadFull(c, port[:]); err != nil {
		return r, false
	}
	r.Port = int(port[0])<<8 | int(port[1])
	return r, true
}

// associate serves UDP ASSOCIATE (RFC 1928 section 7): a relay socket on
// 127.0.0.1 that forwards the client's datagrams to their DST.ADDR (an IP,
// or a name in Hosts) and sends what comes back to the client with the
// sender in the header. The association lasts while c is open.
func (s *SOCKS5Server) associate(c net.Conn, stop <-chan struct{}) {
	if s.NoUDP {
		_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	relay, _, err := listen.ListenPacket(context.Background(), listen.Spec{Network: "udp4", Host: "127.0.0.1"})
	if err != nil {
		_, _ = c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer relay.Close()
	if b, ok := relay.(*net.UDPConn); ok {
		// macOS limits a datagram to the send buffer (9 KiB by default).
		_ = b.SetWriteBuffer(256 << 10)
		_ = b.SetReadBuffer(256 << 10)
	}
	port := relay.LocalAddr().(*net.UDPAddr).Port
	bnd := []byte{127, 0, 0, 1}
	if s.UDPBindUnspecified {
		bnd = []byte{0, 0, 0, 0}
	}
	if _, err := c.Write(append(append([]byte{5, 0, 0, 1}, bnd...), byte(port>>8), byte(port))); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	s.mu.Lock()
	if s.assocs == nil {
		s.assocs = map[net.Conn]bool{}
	}
	s.assocs[c] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.assocs, c)
		s.mu.Unlock()
	}()

	ctrlDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, c)
		close(ctrlDone)
	}()
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		s.relayUDP(relay, c.RemoteAddr().(*net.TCPAddr).AddrPort().Addr())
	}()
	select {
	case <-ctrlDone:
	case <-stop:
	}
	_ = c.Close()
	_ = relay.Close()
	<-relayDone
}

// relayUDP forwards datagrams between the client (the first sender from
// clientIP) and the targets until relay is closed.
func (s *SOCKS5Server) relayUDP(relay net.PacketConn, clientIP netip.Addr) {
	var client netip.AddrPort
	buf := make([]byte, 70000)
	for {
		n, from, err := relay.ReadFrom(buf)
		if err != nil {
			return
		}
		src := from.(*net.UDPAddr).AddrPort()
		src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
		if !client.IsValid() && src.Addr() == clientIP.Unmap() {
			client = src
		}
		if src == client {
			if n < 4 || buf[2] != 0 {
				continue
			}
			r, ok := readSOCKS5Addr(bytes.NewReader(buf[3:n]))
			if !ok {
				continue
			}
			s.mu.Lock()
			s.udp = append(s.udp, r)
			s.mu.Unlock()
			ip := r.Host
			if r.Name {
				ip = s.Hosts[r.Host]
			}
			addr, err := netip.ParseAddr(ip)
			if err != nil {
				continue // unknown name: dropped, as a relay does
			}
			payload := buf[3+addrLen(r) : n]
			_, _ = relay.WriteTo(payload, net.UDPAddrFromAddrPort(netip.AddrPortFrom(addr, uint16(r.Port))))
			continue
		}
		if !client.IsValid() {
			continue
		}
		head := []byte{0, 0, 0}
		if src.Addr().Is4() {
			a := src.Addr().As4()
			head = append(append(head, 1), a[:]...)
		} else {
			a := src.Addr().As16()
			head = append(append(head, 4), a[:]...)
		}
		head = append(head, byte(src.Port()>>8), byte(src.Port()))
		dst := net.UDPAddrFromAddrPort(client)
		if s.UDPFragmentFirst {
			frag := append([]byte(nil), head...)
			frag[2] = 1
			_, _ = relay.WriteTo(append(frag, "fragment"...), dst)
		}
		_, _ = relay.WriteTo(append(head, buf[:n]...), dst)
	}
}

// addrLen is the size of ATYP, DST.ADDR and DST.PORT for r.
func addrLen(r SOCKS5Request) int {
	switch {
	case r.Name:
		return 1 + 1 + len(r.Host) + 2
	case strings.Contains(r.Host, ":"):
		return 1 + 16 + 2
	}
	return 1 + 4 + 2
}

// authenticate runs the RFC 1929 sub-negotiation.
func (s *SOCKS5Server) authenticate(c net.Conn) bool {
	var b [2]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		return false
	}
	user := make([]byte, b[1])
	if _, err := io.ReadFull(c, user); err != nil {
		return false
	}
	if _, err := io.ReadFull(c, b[:1]); err != nil {
		return false
	}
	pass := make([]byte, b[0])
	if _, err := io.ReadFull(c, pass); err != nil {
		return false
	}
	s.mu.Lock()
	s.creds = append(s.creds, string(user)+":"+string(pass))
	s.mu.Unlock()
	ok := string(user) == s.Username && string(pass) == s.Password
	status := byte(1)
	if ok {
		status = 0
	}
	_, err := c.Write([]byte{1, status})
	return ok && err == nil
}
